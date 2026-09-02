package storage_test

import (
	"testing"
	"time"

	"stream/pkg/cluster"
	"stream/pkg/storage"
	"stream/pkg/telemetry"
)

type memStore struct {
	tiers map[int]*storage.Tier
}

func newMemStore() *memStore {
	return &memStore{tiers: make(map[int]*storage.Tier)}
}

func (m *memStore) SaveTier(t *storage.Tier) error {
	cp := *t
	m.tiers[t.ID] = &cp
	return nil
}

func (m *memStore) GetTier(id int) (*storage.Tier, error) {
	if t, ok := m.tiers[id]; ok {
		cp := *t
		return &cp, nil
	}
	return nil, errNotFound
}

func (m *memStore) GetAllTiers() ([]*storage.Tier, error) {
	var out []*storage.Tier
	for _, t := range m.tiers {
		cp := *t
		out = append(out, &cp)
	}
	return out, nil
}

func (m *memStore) DeleteTier(id int) error {
	delete(m.tiers, id)
	return nil
}

type notFoundErr struct{}

func (notFoundErr) Error() string { return "not found" }

var errNotFound = notFoundErr{}

func testNodes() []*cluster.NodeRecord {
	return []*cluster.NodeRecord{
		{
			Status: cluster.StatusOnline,
			Metrics: telemetry.NodeMetrics{
				NodeID: "vps-01",
				Disks: []telemetry.DiskStat{
					{Path: "/mnt/nvme", DiskType: "NVME", TotalBytes: 100 << 30, FreeBytes: 80 << 30, UsedBytes: 20 << 30},
					{Path: "/mnt/hdd", DiskType: "HDD", TotalBytes: 500 << 30, FreeBytes: 400 << 30, UsedBytes: 100 << 30},
				},
			},
		},
	}
}

func TestManagerSeedsDefaultTiers(t *testing.T) {
	m := storage.NewManager(nil)
	tiers := m.List()
	if len(tiers) != 3 {
		t.Fatalf("expected 3 seeded tiers, got %d", len(tiers))
	}
	if tiers[0].ID != 0 || !tiers[0].System {
		t.Fatalf("expected system tier 0 first, got id=%d system=%v", tiers[0].ID, tiers[0].System)
	}
	var cold *storage.Tier
	for i := range tiers {
		if tiers[i].ID == 2 {
			cold = &tiers[i]
		}
	}
	if cold == nil || !cold.Default || !cold.System {
		t.Fatalf("expected tier 2 as default system tier, got %+v", cold)
	}
}

func TestSystemTiersCannotBeDeleted(t *testing.T) {
	m := storage.NewManager(newMemStore())
	for _, id := range []int{0, 1, 2} {
		if err := m.Delete(id); err == nil {
			t.Fatalf("expected delete of system tier %d to be rejected", id)
		}
	}
	tiers := m.List()
	if len(tiers) != 3 {
		t.Fatalf("system tiers must survive delete, got %d", len(tiers))
	}
}

func TestQuotaUnlimitedUsesFullFreeSpace(t *testing.T) {
	m := storage.NewManager(newMemStore())
	_ = m.Upsert(storage.Tier{
		ID:      9,
		Name:    "Quota Test",
		Default: true,
		Blocks: []storage.Block{
			{NodeID: "vps-01", Path: "/mnt/hdd", DiskType: "HDD", QuotaBytes: 0},
		},
	})

	statuses := m.Resolve(testNodes(), nil)
	var tier storage.TierStatus
	for _, ts := range statuses {
		if ts.ID == 9 {
			tier = ts
		}
	}
	if len(tier.Blocks) != 1 {
		t.Fatalf("expected 1 block, got %d", len(tier.Blocks))
	}
	b := tier.Blocks[0]
	if b.UsableBytes != 400<<30 {
		t.Fatalf("quota 0 must expose full free space: expected %d, got %d", 400<<30, b.UsableBytes)
	}
}

func TestQuotaCapsUsableSpace(t *testing.T) {
	m := storage.NewManager(newMemStore())
	_ = m.Upsert(storage.Tier{
		ID:      9,
		Name:    "Quota Test",
		Default: true,
		Blocks: []storage.Block{
			{NodeID: "vps-01", Path: "/mnt/hdd", DiskType: "HDD", QuotaBytes: 50 << 30},
		},
	})

	statuses := m.Resolve(testNodes(), map[string]uint64{"vps-01": 10 << 30})
	for _, ts := range statuses {
		if ts.ID != 9 {
			continue
		}
		b := ts.Blocks[0]
		if b.UsableBytes != 50<<30 {
			t.Fatalf("quota must cap usable bytes: expected %d, got %d", 50<<30, b.UsableBytes)
		}
		if b.UsedBytes != 10<<30 {
			t.Fatalf("expected library usage 10GB, got %d", b.UsedBytes)
		}
	}
}

func TestPickBlockRespectsQuotaHeadroom(t *testing.T) {
	m := storage.NewManager(newMemStore())
	_ = m.Upsert(storage.Tier{
		ID:      9,
		Name:    "Pick Test",
		Default: true,
		Blocks: []storage.Block{
			{NodeID: "vps-01", Path: "/mnt/nvme", DiskType: "NVME", QuotaBytes: 10 << 30},
			{NodeID: "vps-01", Path: "/mnt/hdd", DiskType: "HDD", QuotaBytes: 0},
		},
	})

	// 60GB required: NVMe block has 10GB quota, HDD has 400GB free.
	p, err := m.PickBlock(9, 60<<30, testNodes(), nil)
	if err != nil {
		t.Fatalf("expected HDD pick, got error: %v", err)
	}
	if p.Path != "/mnt/hdd" {
		t.Fatalf("expected /mnt/hdd, got %s", p.Path)
	}

	// 500GB required: no block has that headroom.
	if _, err := m.PickBlock(9, 500<<30, testNodes(), nil); err == nil {
		t.Fatal("expected quota exhaustion error for 500GB request")
	}
}

func TestPickBlockSkipsOfflineNodes(t *testing.T) {
	m := storage.NewManager(newMemStore())
	_ = m.Upsert(storage.Tier{
		ID:      9,
		Name:    "Offline Test",
		Default: true,
		Blocks: []storage.Block{
			{NodeID: "ghost", Path: "/mnt/x", DiskType: "SSD"},
		},
	})

	if _, err := m.PickBlock(9, 0, testNodes(), nil); err == nil {
		t.Fatal("expected error for offline-only tier")
	}
}

func TestUpsertDefaultIsExclusive(t *testing.T) {
	m := storage.NewManager(nil)
	_ = m.Upsert(storage.Tier{ID: 5, Name: "Five", Default: true})
	_ = m.Upsert(storage.Tier{ID: 6, Name: "Six", Default: true})

	for _, tier := range m.List() {
		if tier.ID == 5 && tier.Default {
			t.Fatal("tier 5 must lose default when tier 6 takes it")
		}
	}
	if d := m.DefaultTier(); d == nil || d.ID != 6 {
		t.Fatalf("expected tier 6 default, got %+v", d)
	}
}

func TestDeleteLastDefaultTierRejected(t *testing.T) {
	m := storage.NewManager(newMemStore())
	// Remove seeded tiers 2 and 3, then try to delete the default tier 1.
	_ = m.Delete(2)
	_ = m.Delete(3)
	if err := m.Delete(1); err == nil {
		t.Fatal("deleting the only remaining default tier must fail")
	}
}

func TestCreatedAtPreserved(t *testing.T) {
	m := storage.NewManager(nil)
	stamp := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	_ = m.Upsert(storage.Tier{ID: 8, Name: "Eight", CreatedAt: stamp})
	for _, tier := range m.List() {
		if tier.ID == 8 && !tier.CreatedAt.Equal(stamp) {
			t.Fatalf("created_at must round-trip, got %v want %v", tier.CreatedAt, stamp)
		}
	}
}

func TestBlockExclusivityAcrossTiers(t *testing.T) {
	store := newMemStore()
	m := storage.NewManager(store)

	// Assign block to Tier 1
	_ = m.Upsert(storage.Tier{
		ID:      1,
		Name:    "Tier 1 · Hot",
		Default: true,
		Blocks: []storage.Block{
			{NodeID: "vps-01", Path: "/mnt/nvme", DiskType: "NVME"},
			{NodeID: "vps-01", Path: "/mnt/hdd", DiskType: "HDD"},
		},
	})

	// Assign /mnt/nvme to Tier 2 (should remove /mnt/nvme from Tier 1)
	_ = m.Upsert(storage.Tier{
		ID:      2,
		Name:    "Tier 2 · Warm",
		Default: false,
		Blocks: []storage.Block{
			{NodeID: "vps-01", Path: "/mnt/nvme", DiskType: "NVME"},
		},
	})

	t1, _ := store.GetTier(1)
	if len(t1.Blocks) != 1 || t1.Blocks[0].Path != "/mnt/hdd" {
		t.Fatalf("expected /mnt/nvme to be removed from Tier 1, got blocks: %+v", t1.Blocks)
	}

	t2, _ := store.GetTier(2)
	if len(t2.Blocks) != 1 || t2.Blocks[0].Path != "/mnt/nvme" {
		t.Fatalf("expected Tier 2 to have /mnt/nvme, got blocks: %+v", t2.Blocks)
	}
}

func TestBlockDeduplicationWithinTier(t *testing.T) {
	m := storage.NewManager(newMemStore())
	_ = m.Upsert(storage.Tier{
		ID:      1,
		Name:    "Tier 1",
		Blocks: []storage.Block{
			{NodeID: "vps-01", Path: "/mnt/ssd", DiskType: "SSD"},
			{NodeID: "vps-01", Path: "/mnt/ssd", DiskType: "SSD"},
		},
	})

	for _, tier := range m.List() {
		if tier.ID == 1 {
			if len(tier.Blocks) != 1 {
				t.Fatalf("expected 1 deduplicated block, got %d", len(tier.Blocks))
			}
		}
	}
}
