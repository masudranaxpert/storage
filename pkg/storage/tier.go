// Package storage implements tiered storage pool management: admin-defined
// tiers (e.g. Tier 1 hot NVMe, Tier 2 warm SSD, Tier 3 cold HDD) that group
// individual storage blocks (a node disk) with optional per-block quotas.
package storage

import (
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"stream/pkg/cluster"
)

// IngestTierID is the tier where new files are placed first (HDD cold tier).
// fileapi placement uses the same constant for its primary target.
const IngestTierID = 2

// SystemTierIDs are the three permanent placement classes seeded on first
// boot. They cannot be deleted: 0 is the reserved hot tier (never receives
// new ingest), 1 and 2 are the overflow placement targets.
var SystemTierIDs = []int{0, 1, 2}

// DefaultTiers are the system tiers. Placement policy (fileapi): new files
// land on Tier 2 (HDD) first, overflow to Tier 1 (SSD), never Tier 0.
var DefaultTiers = []Tier{
	{ID: 0, Name: "Tier 0 · Hot (NVMe)", Mediums: []string{"NVME"}, System: true, Lifecycle: TierLifecycle{SpeedRank: 0, HighWatermark: 90, LowWatermark: 75}},
	{ID: 1, Name: "Tier 1 · Warm (SSD)", Mediums: []string{"SSD"}, System: true, Lifecycle: TierLifecycle{SpeedRank: 1, HighWatermark: 90, LowWatermark: 75}},
	{ID: 2, Name: "Tier 2 · Cold (HDD)", Mediums: []string{"HDD"}, System: true, Default: true, Lifecycle: TierLifecycle{SpeedRank: 2, HighWatermark: 90, LowWatermark: 75}},
}

// legacySeedTiers describes the pre-system default seeds (IDs 1..3). On load
// they are migrated into the new 0/1/2 scheme, keeping assigned blocks.
var legacySeedTiers = map[int]string{
	1: "Tier 1 · Hot (NVMe)",
	2: "Tier 2 · Warm (SSD)",
	3: "Tier 3 · Cold (HDD)",
}

// TierLifecycle configures automatic placement rank and watermark thresholds for a tier.
type TierLifecycle struct {
	SpeedRank     int     `json:"speed_rank"`     // 0 = fastest; defaults: tier0=0, tier1=1, tier2=2, custom=10+ID
	HighWatermark float64 `json:"high_watermark"` // % of usable; 0 = disabled; default 90
	LowWatermark  float64 `json:"low_watermark"`  // % of usable; default 75; must be < High
}

// Tier groups storage blocks under one placement class. Exactly one tier may
// be the default target for new media.
type Tier struct {
	ID        int           `json:"id"`
	Name      string        `json:"name"`
	Mediums   []string      `json:"mediums"`   // preferred disk types for auto-assignment
	Default   bool          `json:"default"`   // new media lands here when no tier is chosen
	System     bool          `json:"system"`               // system tier: cannot be deleted
	PublicHost string        `json:"public_host,omitempty"` // tier-wide custom streaming domain/CDN (e.g. "cdn-hot.example.com")
	Blocks     []Block       `json:"blocks"`               // explicitly assigned storage blocks
	Lifecycle  TierLifecycle `json:"lifecycle"`            // speed rank and capacity watermarks
	CreatedAt  time.Time     `json:"created_at"`
}

// Label renders the short public tier identifier, e.g. "tier0".
func (t Tier) Label() string {
	return fmt.Sprintf("tier%d", t.ID)
}

// Block identifies one physical storage unit: a disk mount on a node.
type Block struct {
	NodeID     string `json:"node_id"`
	Path       string `json:"path"`
	DiskType   string `json:"disk_type"`
	QuotaBytes uint64 `json:"quota_bytes"`           // 0 = unlimited, use full free capacity
	PublicHost string `json:"public_host,omitempty"` // custom streaming domain/host (e.g. "cdn1.streammesh.com" or "https://cdn1.streammesh.com:2053")
	TotalBytes uint64 `json:"total_bytes,omitempty"` // populated for unassigned block inspection
	FreeBytes  uint64 `json:"free_bytes,omitempty"`  // populated for unassigned block inspection
}

// BlockStatus joins a block assignment with live telemetry and library usage.
type BlockStatus struct {
	Block       Block   `json:"block"`
	Online      bool    `json:"online"`
	TotalBytes  uint64  `json:"total_bytes"`
	FreeBytes   uint64  `json:"free_bytes"`
	UsedBytes   uint64  `json:"used_bytes"`   // media library bytes on this node
	UsableBytes uint64  `json:"usable_bytes"` // min(free, quota) — quota 0 = full free
	UsedPercent float64 `json:"used_percent"` // of usable
}

// TierStatus is a tier resolved against live cluster telemetry.
type TierStatus struct {
	ID          int           `json:"id"`
	Name        string        `json:"name"`
	Mediums     []string      `json:"mediums"`
	Default     bool          `json:"default"`
	System      bool          `json:"system"`
	PublicHost  string        `json:"public_host,omitempty"`
	Blocks      []BlockStatus `json:"blocks"`
	Lifecycle   TierLifecycle `json:"lifecycle"`
	UsableBytes uint64        `json:"usable_bytes"`
	UsedBytes   uint64        `json:"used_bytes"`
	FreeBytes   uint64        `json:"free_bytes"`
	CreatedAt   time.Time     `json:"created_at"`
}

// Manager owns tier definitions. Persistence and telemetry are injected so
// the manager stays testable without BadgerDB or a live cluster.
type Manager struct {
	mu    sync.RWMutex
	tiers map[int]*Tier
	store TierStore
}

// TierStore persists tier definitions (implemented by db.Store).
type TierStore interface {
	SaveTier(t *Tier) error
	GetTier(id int) (*Tier, error)
	GetAllTiers() ([]*Tier, error)
	DeleteTier(id int) error
}

// NewManager loads persisted tiers, migrating legacy seeds and ensuring the
// three system tiers always exist.
func NewManager(store TierStore) *Manager {
	m := &Manager{
		tiers: make(map[int]*Tier),
		store: store,
	}

	if store != nil {
		if persisted, err := store.GetAllTiers(); err == nil && persisted != nil {
			assigned := make(map[string]bool)
			for _, t := range persisted {
				if t != nil {
					tier := *t
					var validBlocks []Block
					for _, b := range tier.Blocks {
						key := b.NodeID + "|" + b.Path
						if !assigned[key] && b.NodeID != "" && b.Path != "" {
							assigned[key] = true
							validBlocks = append(validBlocks, b)
						}
					}
					tier.Blocks = validBlocks
					m.tiers[tier.ID] = &tier
				}
			}
		}
	}

	m.ensureSystemTiers(store)
	return m
}

func defaultSpeedRank(id int) int {
	switch id {
	case 0:
		return 0
	case 1:
		return 1
	case 2:
		return 2
	default:
		return 10 + id
	}
}

func applyLifecycleDefaults(t *Tier) {
	if t.ID != 0 && t.Lifecycle.SpeedRank == 0 {
		t.Lifecycle.SpeedRank = defaultSpeedRank(t.ID)
	}
	if t.Lifecycle.HighWatermark == 0 {
		t.Lifecycle.HighWatermark = 90
	}
	if t.Lifecycle.LowWatermark == 0 {
		t.Lifecycle.LowWatermark = 75
	}
}

func validateLifecycle(lc TierLifecycle) error {
	if lc.SpeedRank < 0 {
		return fmt.Errorf("invalid speed_rank %d: must be >= 0", lc.SpeedRank)
	}
	if lc.LowWatermark <= 0 || lc.LowWatermark >= lc.HighWatermark || lc.HighWatermark > 100 {
		return fmt.Errorf("invalid watermarks: require 0 < low_watermark (%.1f) < high_watermark (%.1f) <= 100", lc.LowWatermark, lc.HighWatermark)
	}
	return nil
}

// ensureSystemTiers guarantees tiers 0/1/2 exist. Legacy deployments seeded
// defaults at IDs 1/2/3 with different medium meanings; their blocks are
// migrated onto the matching new system tier and the legacy seeds removed.
func (m *Manager) ensureSystemTiers(store TierStore) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Collect blocks from legacy seed tiers (matched by ID + seed name).
	legacyBlocks := make(map[int][]Block) // legacyID -> blocks
	for id, seedName := range legacySeedTiers {
		t, ok := m.tiers[id]
		if !ok || t.Name != seedName {
			continue
		}
		legacyBlocks[id] = t.Blocks
		delete(m.tiers, id)
		if store != nil {
			if err := store.DeleteTier(id); err != nil {
				log.Printf("[Storage] delete legacy tier %d failed: %v", id, err)
			}
		}
	}

	now := time.Now().UTC()
	for i := range DefaultTiers {
		seed := DefaultTiers[i]
		seed.CreatedAt = now
		applyLifecycleDefaults(&seed)

		if existing, ok := m.tiers[seed.ID]; ok {
			// Preserve user blocks and name; only enforce system invariants.
			existing.System = true
			applyLifecycleDefaults(existing)
			if store != nil {
				if err := store.SaveTier(existing); err != nil {
					log.Printf("[Storage] save system tier %d failed: %v", existing.ID, err)
				}
			}
			continue
		}

		// Pull in blocks from the corresponding legacy tier:
		// old NVMe(1) -> 0, old SSD(2) -> 1, old HDD(3) -> 2.
		switch seed.ID {
		case 0:
			seed.Blocks = append(seed.Blocks, legacyBlocks[1]...)
		case 1:
			seed.Blocks = append(seed.Blocks, legacyBlocks[2]...)
		case 2:
			seed.Blocks = append(seed.Blocks, legacyBlocks[3]...)
		}
		m.tiers[seed.ID] = &seed
		if store != nil {
			if err := store.SaveTier(&seed); err != nil {
				log.Printf("[Storage] save seeded tier %d failed: %v", seed.ID, err)
			}
		}
	}

	// Backfill lifecycle defaults on any persisted custom tiers.
	for _, t := range m.tiers {
		if t.System {
			continue
		}
		before := t.Lifecycle
		applyLifecycleDefaults(t)
		if t.Lifecycle != before && store != nil {
			if err := store.SaveTier(t); err != nil {
				log.Printf("[Storage] save custom tier %d lifecycle defaults failed: %v", t.ID, err)
			}
		}
	}

	// Enforce exactly one default: the ingest target is Tier 2 (HDD).
	defaults := 0
	for _, t := range m.tiers {
		if t.Default {
			defaults++
		}
	}
	if defaults > 1 {
		for id, t := range m.tiers {
			if !t.Default || id == IngestTierID {
				continue
			}
			t.Default = false
			if store != nil {
				if err := store.SaveTier(t); err != nil {
					log.Printf("[Storage] save tier %d default reset failed: %v", t.ID, err)
				}
			}
		}
	}
}

// List returns all tiers ordered by ID.
func (m *Manager) List() []Tier {
	m.mu.RLock()
	defer m.mu.RUnlock()

	out := make([]Tier, 0, len(m.tiers))
	for _, t := range m.tiers {
		out = append(out, *t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Upsert creates or updates a tier. Setting Default clears the flag on all
// other tiers so exactly one default exists. Storage blocks are exclusive:
// assigning a block to this tier removes it from any other tier.
func (m *Manager) Upsert(t Tier) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if t.ID < 0 {
		t.ID = m.nextID() // negative ID = create new tier
	}
	if t.CreatedAt.IsZero() {
		t.CreatedAt = time.Now().UTC()
	}

	// System tier invariants: system flag and mediums are fixed. Admins may
	// still rename system tiers and manage their blocks.
	if existing, ok := m.tiers[t.ID]; ok {
		if existing.System {
			t.System = true
			t.Mediums = existing.Mediums
		}
		if t.Lifecycle == (TierLifecycle{}) {
			t.Lifecycle = existing.Lifecycle
		}
	}

	applyLifecycleDefaults(&t)
	if err := validateLifecycle(t.Lifecycle); err != nil {
		return err
	}

	t.PublicHost = strings.TrimSpace(t.PublicHost)

	if t.Name == "" {
		t.Name = fmt.Sprintf("Tier %d", t.ID)
	}

	if t.Default {
		for id, other := range m.tiers {
			if id != t.ID && other.Default {
				other.Default = false
				if m.store != nil {
					if err := m.store.SaveTier(other); err != nil {
						log.Printf("[Storage] save tier %d default clear failed: %v", other.ID, err)
					}
				}
			}
		}
	}

	// 1. Deduplicate blocks within incoming tier
	seen := make(map[string]bool)
	var dedupedBlocks []Block
	for _, b := range t.Blocks {
		if b.NodeID == "" || b.Path == "" {
			continue
		}
		key := b.NodeID + "|" + b.Path
		if !seen[key] {
			seen[key] = true
			dedupedBlocks = append(dedupedBlocks, b)
		}
	}
	t.Blocks = dedupedBlocks

	// 2. Storage block exclusivity: remove these blocks from all other tiers
	for id, other := range m.tiers {
		if id == t.ID {
			continue
		}
		var kept []Block
		modified := false
		for _, ob := range other.Blocks {
			key := ob.NodeID + "|" + ob.Path
			if seen[key] {
				modified = true
			} else {
				kept = append(kept, ob)
			}
		}
		if modified {
			other.Blocks = kept
			if m.store != nil {
				if err := m.store.SaveTier(other); err != nil {
					log.Printf("[Storage] save tier %d block exclusivity update failed: %v", other.ID, err)
				}
			}
		}
	}

	stored := t
	m.tiers[t.ID] = &stored
	if m.store != nil {
		return m.store.SaveTier(&stored)
	}
	return nil
}

// UpdateLifecycle updates only the lifecycle settings of an existing tier.
func (m *Manager) UpdateLifecycle(id int, lc TierLifecycle) (*Tier, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	existing, ok := m.tiers[id]
	if !ok {
		return nil, fmt.Errorf("tier %d not found", id)
	}
	if lc.HighWatermark == 0 {
		lc.HighWatermark = 90
	}
	if lc.LowWatermark == 0 {
		lc.LowWatermark = 75
	}
	if err := validateLifecycle(lc); err != nil {
		return nil, err
	}
	existing.Lifecycle = lc
	if m.store != nil {
		if err := m.store.SaveTier(existing); err != nil {
			return nil, err
		}
	}
	cp := *existing
	return &cp, nil
}

// Delete removes a user-created tier. The three system tiers (0/1/2) are
// permanent; the last remaining default tier also cannot be deleted so
// ingest always has a placement target.
func (m *Manager) Delete(id int) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	t, exists := m.tiers[id]
	if !exists {
		return fmt.Errorf("tier %d not found", id)
	}
	if t.System {
		return fmt.Errorf("tier%d is a permanent system tier and cannot be deleted", id)
	}
	if t.Default && len(m.tiers) == 1 {
		return fmt.Errorf("cannot delete the only default tier")
	}

	wasDefault := t.Default
	delete(m.tiers, id)

	if wasDefault {
		for _, other := range m.tiers {
			other.Default = true
			if m.store != nil {
				_ = m.store.SaveTier(other)
			}
			break
		}
	}

	if m.store != nil {
		return m.store.DeleteTier(id)
	}
	return nil
}

// DefaultTier returns the tier that receives new media by default.
func (m *Manager) DefaultTier() *Tier {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var fallback *Tier
	for _, t := range m.tiers {
		if t.Default {
			cp := *t
			return &cp
		}
		if fallback == nil || t.ID < fallback.ID {
			cp := *t
			fallback = &cp
		}
	}
	return fallback
}

func (m *Manager) nextID() int {
	max := 0
	for id := range m.tiers {
		if id > max {
			max = id
		}
	}
	return max + 1
}

// Resolve evaluates every tier block against live node telemetry and the
// media library footprint (usedBytes per node ID).
func (m *Manager) Resolve(nodes []*cluster.NodeRecord, usedPerNode map[string]uint64) []TierStatus {
	tiers := m.List()
	out := make([]TierStatus, 0, len(tiers))

	for _, t := range tiers {
		status := TierStatus{
			ID:         t.ID,
			Name:       t.Name,
			Mediums:    t.Mediums,
			Default:    t.Default,
			System:     t.System,
			PublicHost: t.PublicHost,
			Lifecycle:  t.Lifecycle,
			CreatedAt:  t.CreatedAt,
		}

		for _, b := range t.Blocks {
			bs := resolveBlock(b, nodes, usedPerNode)
			status.Blocks = append(status.Blocks, bs)
			status.UsableBytes += bs.UsableBytes
			status.UsedBytes += bs.UsedBytes
			status.FreeBytes += usableFree(bs)
		}
		out = append(out, status)
	}
	return out
}

func resolveBlock(b Block, nodes []*cluster.NodeRecord, usedPerNode map[string]uint64) BlockStatus {
	var used uint64
	blockKey := b.NodeID + "|" + b.Path
	if u, ok := usedPerNode[blockKey]; ok {
		used = u
	} else {
		used = usedPerNode[b.NodeID]
	}
	bs := BlockStatus{Block: b, UsedBytes: used}

	for _, n := range nodes {
		if n.Metrics.NodeID != b.NodeID {
			continue
		}
		for _, d := range n.Metrics.Disks {
			if d.Path != b.Path {
				continue
			}
			bs.Online = true
			bs.TotalBytes = d.TotalBytes
			bs.FreeBytes = d.FreeBytes
			break
		}
	}

	bs.UsableBytes = bs.FreeBytes
	if b.QuotaBytes > 0 && b.QuotaBytes < bs.FreeBytes {
		bs.UsableBytes = b.QuotaBytes
	}
	if bs.UsableBytes > 0 {
		bs.UsedPercent = float64(bs.UsedBytes) / float64(bs.UsableBytes) * 100.0
	}
	return bs
}

// usableFree is the remaining headroom for new writes on a block.
func usableFree(bs BlockStatus) uint64 {
	if bs.UsedBytes >= bs.UsableBytes {
		return 0
	}
	return bs.UsableBytes - bs.UsedBytes
}

// Placement names a chosen write target.
type Placement struct {
	TierID int    `json:"tier_id"`
	NodeID string `json:"node_id"`
	Path   string `json:"path"`
}

// PickBlock selects the write target for an incoming asset of requiredBytes
// (use 0 when the size is unknown, e.g. URL ingest). Candidates must be
// online and have headroom; the block with the most headroom wins so writes
// spread evenly across the tier.
func (m *Manager) PickBlock(tierID int, requiredBytes uint64, nodes []*cluster.NodeRecord, usedPerNode map[string]uint64) (*Placement, error) {
	tiers := m.List()

	var tier *Tier
	for i := range tiers {
		if tiers[i].ID == tierID {
			tier = &tiers[i]
			break
		}
	}
	if tier == nil {
		return nil, fmt.Errorf("tier %d not found", tierID)
	}

	var best *BlockStatus
	for _, b := range tier.Blocks {
		bs := resolveBlock(b, nodes, usedPerNode)
		if !bs.Online || usableFree(bs) < requiredBytes {
			continue
		}
		if best == nil || usableFree(bs) > usableFree(*best) {
			cp := bs
			best = &cp
		}
	}
	if best == nil {
		return nil, fmt.Errorf("no eligible block in tier %d (%s): quota or capacity exhausted", tier.ID, tier.Name)
	}

	return &Placement{
		TierID: tier.ID,
		NodeID: best.Block.NodeID,
		Path:   best.Block.Path,
	}, nil
}
