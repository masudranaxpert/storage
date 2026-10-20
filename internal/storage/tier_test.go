package storage

import "testing"

func TestTierLifecycleDefaultsAndValidation(t *testing.T) {
	m := NewManager(nil)
	tiers := m.List()
	if len(tiers) != 3 {
		t.Fatalf("expected 3 system tiers, got %d", len(tiers))
	}

	wantRanks := map[int]int{0: 0, 1: 1, 2: 2}
	for _, tr := range tiers {
		if tr.Lifecycle.SpeedRank != wantRanks[tr.ID] {
			t.Fatalf("tier %d SpeedRank = %d, want %d", tr.ID, tr.Lifecycle.SpeedRank, wantRanks[tr.ID])
		}
		if tr.Lifecycle.HighWatermark != 90 || tr.Lifecycle.LowWatermark != 75 {
			t.Fatalf("tier %d watermarks = %+v, want 90/75", tr.ID, tr.Lifecycle)
		}
	}

	// Custom tier (-1 -> ID 3) defaults SpeedRank to 10+ID = 13.
	if err := m.Upsert(Tier{ID: -1, Name: "Archive"}); err != nil {
		t.Fatalf("Upsert custom tier failed: %v", err)
	}
	var custom *Tier
	for _, tr := range m.List() {
		if tr.ID == 3 {
			cp := tr
			custom = &cp
		}
	}
	if custom == nil || custom.Lifecycle.SpeedRank != 13 || custom.Lifecycle.HighWatermark != 90 || custom.Lifecycle.LowWatermark != 75 {
		t.Fatalf("custom tier lifecycle = %+v, want SpeedRank=13 High=90 Low=75", custom)
	}

	// Reject Low >= High and High > 100 in Upsert and UpdateLifecycle.
	if err := m.Upsert(Tier{ID: 3, Name: "Archive", Lifecycle: TierLifecycle{SpeedRank: 13, HighWatermark: 70, LowWatermark: 80}}); err == nil {
		t.Fatal("expected error when LowWatermark >= HighWatermark")
	}
	if err := m.Upsert(Tier{ID: 3, Name: "Archive", Lifecycle: TierLifecycle{SpeedRank: 13, HighWatermark: 105, LowWatermark: 80}}); err == nil {
		t.Fatal("expected error when HighWatermark > 100")
	}
	if _, err := m.UpdateLifecycle(1, TierLifecycle{SpeedRank: 1, HighWatermark: 85, LowWatermark: 85}); err == nil {
		t.Fatal("expected error when LowWatermark == HighWatermark")
	}

	updated, err := m.UpdateLifecycle(1, TierLifecycle{SpeedRank: 1, HighWatermark: 88, LowWatermark: 65})
	if err != nil || updated.Lifecycle.HighWatermark != 88 || updated.Lifecycle.LowWatermark != 65 {
		t.Fatalf("UpdateLifecycle failed: %+v, err=%v", updated, err)
	}
}
