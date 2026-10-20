package lifecycle

import (
	"errors"
	"math"
	"reflect"
	"sync"
	"testing"
	"time"

	"stream/internal/cluster"
	"stream/internal/storage"
	"stream/internal/telemetry"
)

func testSystemTiers() []storage.Tier {
	return []storage.Tier{
		{ID: 0, Name: "Tier 0 · Hot (NVMe)", System: true, Lifecycle: storage.TierLifecycle{SpeedRank: 0, HighWatermark: 90, LowWatermark: 75}},
		{ID: 1, Name: "Tier 1 · Warm (SSD)", System: true, Lifecycle: storage.TierLifecycle{SpeedRank: 1, HighWatermark: 90, LowWatermark: 75}},
		{ID: 2, Name: "Tier 2 · Cold (HDD)", System: true, Default: true, Lifecycle: storage.TierLifecycle{SpeedRank: 2, HighWatermark: 90, LowWatermark: 75}},
	}
}

func TestDefaultPolicyReproducesIngestOrder(t *testing.T) {
	tiers := testSystemTiers()
	p := DefaultPolicy(tiers)

	want := []int{2, 1, 0}
	if !reflect.DeepEqual(p.Ingest, want) {
		t.Fatalf("DefaultPolicy system tiers ingest = %v, want %v", p.Ingest, want)
	}
	if len(p.Edges) != 0 {
		t.Fatalf("expected 0 default edges, got %d", len(p.Edges))
	}
	if p.HalfLifeHours != 24 || p.TickSeconds != 60 || p.MaxTasksPerTick != 20 {
		t.Fatalf("unexpected default scalar policy fields: %+v", p)
	}

	// Add custom tiers 3 (rank 13) and 4 (rank 14), with tier 3 marked default.
	customTiers := []storage.Tier{
		tiers[0],
		tiers[1],
		{ID: 2, Name: "Tier 2", System: true, Default: false, Lifecycle: storage.TierLifecycle{SpeedRank: 2, HighWatermark: 90, LowWatermark: 75}},
		{ID: 3, Name: "Tier 3", Default: true, Lifecycle: storage.TierLifecycle{SpeedRank: 13, HighWatermark: 90, LowWatermark: 75}},
		{ID: 4, Name: "Tier 4", Lifecycle: storage.TierLifecycle{SpeedRank: 14, HighWatermark: 90, LowWatermark: 75}},
	}
	p2 := DefaultPolicy(customTiers)
	want2 := []int{3, 1, 0, 2, 4}
	if !reflect.DeepEqual(p2.Ingest, want2) {
		t.Fatalf("DefaultPolicy custom tiers ingest = %v, want %v", p2.Ingest, want2)
	}
}

func TestValidatePolicyTable(t *testing.T) {
	tiers := testSystemTiers()

	cases := []struct {
		name      string
		tiers     []storage.Tier
		policy    FlowPolicy
		wantError bool
		wantField string
	}{
		{
			name:  "valid default policy",
			tiers: tiers,
			policy: FlowPolicy{
				Ingest: []int{2, 1, 0},
			},
			wantError: false,
		},
		{
			name:  "valid promote, demote, and expire edges",
			tiers: tiers,
			policy: FlowPolicy{
				Ingest: []int{2, 1},
				Edges: []Edge{
					{ID: "e_p1", Kind: EdgePromote, From: 2, To: 0, Enabled: true, Trigger: Trigger{HeatAtLeast: 10}},
					{ID: "e_d1", Kind: EdgeDemote, From: 0, To: 2, Enabled: true, Trigger: Trigger{IdleDays: 7, HeatBelow: 2}},
					{ID: "e_x1", Kind: EdgeExpire, From: 2, To: TrashTierID, Enabled: true, Trigger: Trigger{AgeDays: 30}},
				},
			},
			wantError: false,
		},
		{
			name:  "empty ingest rejected",
			tiers: tiers,
			policy: FlowPolicy{
				Ingest: nil,
			},
			wantError: true,
			wantField: "ingest",
		},
		{
			name:  "unknown tier in ingest rejected",
			tiers: tiers,
			policy: FlowPolicy{
				Ingest: []int{2, 99},
			},
			wantError: true,
			wantField: "ingest",
		},
		{
			name:  "unknown source tier on edge rejected",
			tiers: tiers,
			policy: FlowPolicy{
				Ingest: []int{2},
				Edges: []Edge{
					{ID: "e_bad_from", Kind: EdgePromote, From: 42, To: 0, Enabled: true, Trigger: Trigger{HeatAtLeast: 5}},
				},
			},
			wantError: true,
			wantField: "from",
		},
		{
			name:  "unknown target tier on edge rejected",
			tiers: tiers,
			policy: FlowPolicy{
				Ingest: []int{2},
				Edges: []Edge{
					{ID: "e_bad_to", Kind: EdgePromote, From: 2, To: 99, Enabled: true, Trigger: Trigger{HeatAtLeast: 5}},
				},
			},
			wantError: true,
			wantField: "to",
		},
		{
			name:  "promote where To.SpeedRank >= From.SpeedRank rejected",
			tiers: tiers,
			policy: FlowPolicy{
				Ingest: []int{2},
				Edges: []Edge{
					{ID: "e_p_slow", Kind: EdgePromote, From: 0, To: 2, Enabled: true, Trigger: Trigger{HeatAtLeast: 5}},
				},
			},
			wantError: true,
			wantField: "to",
		},
		{
			name:  "demote where To.SpeedRank <= From.SpeedRank rejected",
			tiers: tiers,
			policy: FlowPolicy{
				Ingest: []int{2},
				Edges: []Edge{
					{ID: "e_d_fast", Kind: EdgeDemote, From: 2, To: 0, Enabled: true, Trigger: Trigger{IdleDays: 7}},
				},
			},
			wantError: true,
			wantField: "to",
		},
		{
			name: "cycle within promote edges rejected",
			tiers: []storage.Tier{
				{ID: 0, Name: "T0", Lifecycle: storage.TierLifecycle{SpeedRank: 0, HighWatermark: 90, LowWatermark: 75}},
				{ID: 1, Name: "T1", Lifecycle: storage.TierLifecycle{SpeedRank: 1, HighWatermark: 90, LowWatermark: 75}},
			},
			policy: FlowPolicy{
				Ingest: []int{1},
				Edges: []Edge{
					{ID: "e_c1", Kind: EdgePromote, From: 1, To: 0, Enabled: true, Trigger: Trigger{HeatAtLeast: 10}},
					{ID: "e_c2", Kind: EdgePromote, From: 0, To: 1, Enabled: true, Trigger: Trigger{HeatAtLeast: 10}},
				},
			},
			wantError: true,
			wantField: "to",
		},
		{
			name: "cycle within demote edges rejected",
			tiers: []storage.Tier{
				{ID: 0, Name: "T0", Lifecycle: storage.TierLifecycle{SpeedRank: 0, HighWatermark: 90, LowWatermark: 75}},
				{ID: 1, Name: "T1", Lifecycle: storage.TierLifecycle{SpeedRank: 1, HighWatermark: 90, LowWatermark: 75}},
			},
			policy: FlowPolicy{
				Ingest: []int{0},
				Edges: []Edge{
					{ID: "e_dc1", Kind: EdgeDemote, From: 0, To: 1, Enabled: true, Trigger: Trigger{IdleDays: 3}},
					{ID: "e_dc2", Kind: EdgeDemote, From: 1, To: 0, Enabled: true, Trigger: Trigger{IdleDays: 3}},
				},
			},
			wantError: true,
			wantField: "to",
		},
		{
			name:  "more than one enabled promote edge from same From rejected",
			tiers: tiers,
			policy: FlowPolicy{
				Ingest: []int{2},
				Edges: []Edge{
					{ID: "e_p_a", Kind: EdgePromote, From: 2, To: 1, Enabled: true, Trigger: Trigger{HeatAtLeast: 5}},
					{ID: "e_p_b", Kind: EdgePromote, From: 2, To: 0, Enabled: true, Trigger: Trigger{HeatAtLeast: 15}},
				},
			},
			wantError: true,
			wantField: "from",
		},
		{
			name:  "expire where To != TrashTierID rejected",
			tiers: tiers,
			policy: FlowPolicy{
				Ingest: []int{2},
				Edges: []Edge{
					{ID: "e_x_bad_to", Kind: EdgeExpire, From: 2, To: 1, Enabled: true, Trigger: Trigger{AgeDays: 30}},
				},
			},
			wantError: true,
			wantField: "to",
		},
		{
			name:  "expire with neither AgeDays nor IdleDays rejected",
			tiers: tiers,
			policy: FlowPolicy{
				Ingest: []int{2},
				Edges: []Edge{
					{ID: "e_x_notrig", Kind: EdgeExpire, From: 2, To: TrashTierID, Enabled: true, Trigger: Trigger{OnWatermark: true}},
				},
			},
			wantError: true,
			wantField: "trigger",
		},
		{
			name:  "demote edge with no trigger rejected",
			tiers: tiers,
			policy: FlowPolicy{
				Ingest: []int{2},
				Edges: []Edge{
					{ID: "e_d_notrig", Kind: EdgeDemote, From: 0, To: 2, Enabled: true, Trigger: Trigger{}},
				},
			},
			wantError: true,
			wantField: "trigger",
		},
		{
			name:  "promote HeatAtLeast <= paired demote HeatBelow rejected",
			tiers: tiers,
			policy: FlowPolicy{
				Ingest: []int{2},
				Edges: []Edge{
					{ID: "e_p_hyst", Kind: EdgePromote, From: 2, To: 0, Enabled: true, Trigger: Trigger{HeatAtLeast: 5}},
					{ID: "e_d_hyst", Kind: EdgeDemote, From: 0, To: 2, Enabled: true, Trigger: Trigger{IdleDays: 7, HeatBelow: 5}},
				},
			},
			wantError: true,
			wantField: "trigger.heat_at_least",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			errs := Validate(tc.policy, tc.tiers)
			if !tc.wantError {
				if len(errs) > 0 {
					t.Fatalf("unexpected validation errors: %+v", errs)
				}
				return
			}
			if len(errs) == 0 {
				t.Fatalf("expected validation error on %q, got none", tc.wantField)
			}
			found := false
			for _, fe := range errs {
				if fe.Field == tc.wantField {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("expected error on field %q, got %+v", tc.wantField, errs)
			}
		})
	}
}

func TestFileStateDecayed(t *testing.T) {
	t0 := time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)
	st := FileState{Key: "k1", Score: 16, ScoreAt: t0}

	if got := st.Decayed(t0, 24); math.Abs(got-16) > 1e-9 {
		t.Fatalf("at t0: got %v, want 16", got)
	}
	if got := st.Decayed(t0.Add(24*time.Hour), 24); math.Abs(got-8) > 1e-9 {
		t.Fatalf("after 1 half-life: got %v, want 8", got)
	}
	if got := st.Decayed(t0.Add(48*time.Hour), 24); math.Abs(got-4) > 1e-9 {
		t.Fatalf("after 2 half-lives: got %v, want 4", got)
	}
}

type memLifecycleStore struct {
	mu     sync.Mutex
	policy *FlowPolicy
}

func (m *memLifecycleStore) GetFlowPolicy() (*FlowPolicy, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.policy == nil {
		return nil, nil
	}
	cp := clonePolicy(*m.policy)
	return &cp, nil
}

func (m *memLifecycleStore) SaveFlowPolicy(p *FlowPolicy) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := clonePolicy(*p)
	m.policy = &cp
	return nil
}

func (m *memLifecycleStore) GetLifecycleState(string) (*FileState, error)  { return nil, nil }
func (m *memLifecycleStore) SaveLifecycleState(*FileState) error           { return nil }
func (m *memLifecycleStore) DeleteLifecycleState(string) error             { return nil }
func (m *memLifecycleStore) ListLifecycleStates() ([]*FileState, error)    { return nil, nil }
func (m *memLifecycleStore) GetLifecycleTask(string) (*Task, error)        { return nil, nil }
func (m *memLifecycleStore) SaveLifecycleTask(*Task) error                 { return nil }
func (m *memLifecycleStore) ListLifecycleTasks() ([]*Task, error)          { return nil, nil }
func (m *memLifecycleStore) GetActiveLifecycleTask(string) (string, error) { return "", nil }
func (m *memLifecycleStore) SetActiveLifecycleTask(string, string) error   { return nil }
func (m *memLifecycleStore) DeleteActiveLifecycleTask(string) error        { return nil }
func (m *memLifecycleStore) ListActiveLifecycleTasks() (map[string]string, error) {
	return nil, nil
}
func (m *memLifecycleStore) SaveLifecycleAudit(*AuditEntry) error           { return nil }
func (m *memLifecycleStore) ListLifecycleAudits(int) ([]*AuditEntry, error) { return nil, nil }

func TestSavePolicyVersionConflictAndExpireConfirm(t *testing.T) {
	store := &memLifecycleStore{}
	tm := storage.NewManager(nil)
	eng := New(store, tm, nil)

	initial := eng.GetPolicy()
	if initial.Version != 1 {
		t.Fatalf("expected initial version 1, got %d", initial.Version)
	}
	if store.policy == nil || store.policy.Version != 1 {
		t.Fatal("expected DefaultPolicy to be persisted on first boot")
	}

	// 1. Stale version -> ErrVersionConflict
	if _, err := eng.SavePolicy(initial, 99, ""); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("expected ErrVersionConflict, got %v", err)
	}

	// 2. Enabling an expire edge without confirm phrase -> rejected
	withExpire := initial
	withExpire.Edges = []Edge{
		{ID: "e_exp1", Kind: EdgeExpire, From: 2, To: TrashTierID, Enabled: true, Trigger: Trigger{AgeDays: 30}},
	}
	if _, err := eng.SavePolicy(withExpire, initial.Version, ""); !errors.Is(err, ErrExpireConfirmRequired) {
		t.Fatalf("expected ErrExpireConfirmRequired without phrase, got %v", err)
	}
	if _, err := eng.SavePolicy(withExpire, initial.Version, "wrong phrase"); !errors.Is(err, ErrExpireConfirmRequired) {
		t.Fatalf("expected ErrExpireConfirmRequired with wrong phrase, got %v", err)
	}

	// 3. Enabling expire edge with "DELETE FILES" -> succeeds, bumps version, sets ExpireAcknowledged = true
	saved, err := eng.SavePolicy(withExpire, initial.Version, "DELETE FILES")
	if err != nil {
		t.Fatalf("SavePolicy with confirm failed: %v", err)
	}
	if saved.Version != 2 {
		t.Fatalf("expected version 2, got %d", saved.Version)
	}
	if !saved.ExpireAcknowledged {
		t.Fatal("expected ExpireAcknowledged = true when expire edge is enabled with confirm")
	}

	// 4. Disabling expire edge clears ExpireAcknowledged
	saved.Edges[0].Enabled = false
	saved2, err := eng.SavePolicy(saved, saved.Version, "")
	if err != nil {
		t.Fatalf("SavePolicy disabling expire failed: %v", err)
	}
	if saved2.Version != 3 || saved2.ExpireAcknowledged {
		t.Fatalf("expected version 3 and ExpireAcknowledged = false, got v=%d ack=%v", saved2.Version, saved2.ExpireAcknowledged)
	}

	// 5. Re-enable expire edge with AgeDays: 30
	saved2.Edges[0].Enabled = true
	saved3, err := eng.SavePolicy(saved2, saved2.Version, "DELETE FILES")
	if err != nil {
		t.Fatalf("SavePolicy re-enabling expire failed: %v", err)
	}

	// 6. Tightening AgeDays: 30 -> 15 requires confirm "DELETE FILES"
	saved3.Edges[0].Trigger.AgeDays = 15
	if _, err := eng.SavePolicy(saved3, saved3.Version, ""); !errors.Is(err, ErrExpireConfirmRequired) {
		t.Fatalf("expected ErrExpireConfirmRequired on tightened AgeDays without confirm, got %v", err)
	}
	saved4, err := eng.SavePolicy(saved3, saved3.Version, "DELETE FILES")
	if err != nil {
		t.Fatalf("SavePolicy tightening AgeDays failed: %v", err)
	}

	// 7. Adding OnWatermark: true (loosening) does not require confirm
	saved4.Edges[0].Trigger.OnWatermark = true
	saved5, err := eng.SavePolicy(saved4, saved4.Version, "")
	if err != nil {
		t.Fatalf("SavePolicy adding OnWatermark failed: %v", err)
	}

	// 8. Removing OnWatermark (tightening) requires confirm
	saved5.Edges[0].Trigger.OnWatermark = false
	if _, err := eng.SavePolicy(saved5, saved5.Version, ""); !errors.Is(err, ErrExpireConfirmRequired) {
		t.Fatalf("expected ErrExpireConfirmRequired on removing OnWatermark without confirm, got %v", err)
	}
	if _, err := eng.SavePolicy(saved5, saved5.Version, "DELETE FILES"); err != nil {
		t.Fatalf("SavePolicy removing OnWatermark with confirm failed: %v", err)
	}
}

func TestEngineSimulate(t *testing.T) {
	coord := cluster.NewCoordinator(10*time.Second, 20*time.Second, nil)
	coord.RegisterHeartbeat(telemetry.NodeMetrics{
		NodeID: "vps-hdd",
		IPs:    []string{"10.0.0.1"},
		Disks:  []telemetry.DiskStat{{Path: "/hdd", DiskType: "HDD", FreeBytes: 50 << 30, TotalBytes: 100 << 30}},
	})
	coord.RegisterHeartbeat(telemetry.NodeMetrics{
		NodeID: "vps-ssd",
		IPs:    []string{"10.0.0.2"},
		Disks:  []telemetry.DiskStat{{Path: "/ssd", DiskType: "SSD", FreeBytes: 200 << 30, TotalBytes: 400 << 30}},
	})

	tm := storage.NewManager(nil)
	_ = tm.Upsert(storage.Tier{
		ID:     1,
		Name:   "Tier 1 · Warm (SSD)",
		System: true,
		Blocks: []storage.Block{{NodeID: "vps-ssd", Path: "/ssd", DiskType: "SSD"}},
	})
	_ = tm.Upsert(storage.Tier{
		ID:      2,
		Name:    "Tier 2 · Cold (HDD)",
		System:  true,
		Default: true,
		Blocks:  []storage.Block{{NodeID: "vps-hdd", Path: "/hdd", DiskType: "HDD"}},
	})

	eng := New(&memLifecycleStore{}, tm, coord)

	// 10 GB fits on primary ingest Tier 2 (50 GB free).
	res1, err := eng.Simulate(10 << 30)
	if err != nil || res1.TierID != 2 || res1.NodeID != "vps-hdd" {
		t.Fatalf("Simulate(10GB) = %+v, err=%v; want tier 2 on vps-hdd", res1, err)
	}

	// 100 GB overflows Tier 2 (50 GB) and lands on Tier 1 (200 GB).
	res2, err := eng.Simulate(100 << 30)
	if err != nil || res2.TierID != 1 || res2.NodeID != "vps-ssd" {
		t.Fatalf("Simulate(100GB) = %+v, err=%v; want tier 1 on vps-ssd", res2, err)
	}

	// 500 GB exceeds all tiers -> ErrNoTierFits.
	if _, err := eng.Simulate(500 << 30); !errors.Is(err, ErrNoTierFits) {
		t.Fatalf("Simulate(500GB) err = %v, want ErrNoTierFits", err)
	}
}
