package cache

import "testing"

func TestRegistryRegisterAndSnapshot(t *testing.T) {
	var reg Registry
	reg.Register("b_cache", func() Snapshot { return Snapshot{Name: "b_cache", Entries: 2, Bytes: 20} })
	reg.Register("a_cache", func() Snapshot { return Snapshot{Name: "a_cache", Entries: 1, Bytes: 10} })

	snaps, total := reg.Snapshot()
	if len(snaps) != 2 {
		t.Fatalf("len(snaps) = %d, want 2", len(snaps))
	}
	if snaps[0].Name != "a_cache" || snaps[1].Name != "b_cache" {
		t.Fatalf("snapshots not sorted by name: %v, %v", snaps[0].Name, snaps[1].Name)
	}
	if total != 30 {
		t.Fatalf("total = %d, want 30", total)
	}

	// Re-registering a name must replace, not duplicate.
	reg.Register("a_cache", func() Snapshot { return Snapshot{Name: "a_cache", Entries: 5, Bytes: 50} })
	snaps, total = reg.Snapshot()
	if len(snaps) != 2 {
		t.Fatalf("len(snaps) after re-register = %d, want 2", len(snaps))
	}
	if snaps[0].Entries != 5 || total != 70 {
		t.Fatalf("re-registered reporter not applied: %+v total=%d", snaps[0], total)
	}
}

func TestRegistryNilReporterDefaultsName(t *testing.T) {
	var reg Registry
	reg.Register("named", nil)
	snaps, total := reg.Snapshot()
	if len(snaps) != 1 || snaps[0].Name != "named" || total != 0 {
		t.Fatalf("unexpected snapshot: %+v total=%d", snaps, total)
	}
}
