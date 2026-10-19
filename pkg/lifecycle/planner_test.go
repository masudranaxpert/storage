package lifecycle

import (
	"fmt"
	"math"
	"testing"
	"time"

	"stream/pkg/fileapi"
	"stream/pkg/storage"
)

func TestDecayMath(t *testing.T) {
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	halfLifeHours := 24.0

	tests := []struct {
		name      string
		score     float64
		scoreAt   time.Time
		at        time.Time
		wantScore float64
	}{
		{
			name:      "Zero time difference maintains score",
			score:     100.0,
			scoreAt:   now,
			at:        now,
			wantScore: 100.0,
		},
		{
			name:      "Score halves after exactly one half-life",
			score:     100.0,
			scoreAt:   now.Add(-24 * time.Hour),
			at:        now,
			wantScore: 50.0,
		},
		{
			name:      "Score quarters after two half-lives",
			score:     100.0,
			scoreAt:   now.Add(-48 * time.Hour),
			at:        now,
			wantScore: 25.0,
		},
		{
			name:      "Score decays exponentially after half a half-life",
			score:     100.0,
			scoreAt:   now.Add(-12 * time.Hour),
			at:        now,
			wantScore: 100.0 * math.Pow(2, -0.5), // ~70.71
		},
		{
			name:      "Zero initial score remains zero",
			score:     0.0,
			scoreAt:   now.Add(-24 * time.Hour),
			at:        now,
			wantScore: 0.0,
		},
		{
			name:      "Future scoreAt is clamped to now",
			score:     100.0,
			scoreAt:   now.Add(10 * time.Hour),
			at:        now,
			wantScore: 100.0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fs := FileState{
				Score:   tc.score,
				ScoreAt: tc.scoreAt,
			}
			got := fs.Decayed(tc.at, halfLifeHours)
			if math.Abs(got-tc.wantScore) > 0.001 {
				t.Fatalf("Decayed() = %v, want %v", got, tc.wantScore)
			}
		})
	}
}

func helperMakeTier(id int, name string, speedRank int, highW, lowW float64, totalBytes, usedBytes uint64) storage.TierStatus {
	freeBytes := uint64(0)
	if totalBytes > usedBytes {
		freeBytes = totalBytes - usedBytes
	}
	usedPercent := float64(0)
	if totalBytes > 0 {
		usedPercent = (float64(usedBytes) / float64(totalBytes)) * 100.0
	}
	return storage.TierStatus{
		ID:          id,
		Name:        name,
		UsableBytes: totalBytes,
		UsedBytes:   usedBytes,
		FreeBytes:   freeBytes,
		Lifecycle: storage.TierLifecycle{
			SpeedRank:     speedRank,
			HighWatermark: highW,
			LowWatermark:  lowW,
		},
		Blocks: []storage.BlockStatus{
			{
				Block: storage.Block{
					NodeID:   fmt.Sprintf("node_%d", id),
					Path:     fmt.Sprintf("/data/tier%d", id),
					DiskType: "SSD",
				},
				Online:      true,
				TotalBytes:  totalBytes,
				UsedBytes:   usedBytes,
				FreeBytes:   freeBytes,
				UsableBytes: totalBytes,
				UsedPercent: usedPercent,
			},
		},
	}
}

func TestPromoteThreshold(t *testing.T) {
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	policy := FlowPolicy{
		Version:         1,
		TickSeconds:     60,
		HalfLifeHours:   24,
		MaxTasksPerTick: 10,
		Ingest:          []int{2, 1, 0},
		Edges: []Edge{
			{
				ID:      "e_promote_2_0",
				Kind:    EdgePromote,
				From:    2,
				To:      0,
				Enabled: true,
				Trigger: Trigger{
					HeatAtLeast: 5.0,
				},
			},
		},
	}

	tier0 := helperMakeTier(0, "Tier 0 · Hot (NVMe)", 0, 90, 75, 1000*1024*1024, 100*1024*1024)
	tier2 := helperMakeTier(2, "Tier 2 · Cold (HDD)", 2, 90, 75, 1000*1024*1024, 500*1024*1024)

	snap := Snapshot{
		Policy: policy,
		Tiers:  []storage.TierStatus{tier0, tier2},
		Now:    now,
		Files: []FileView{
			{
				Key:       "hot_file",
				SizeBytes: 50 * 1024 * 1024,
				State:     fileapi.StateCompleted,
				Placement: fileapi.Placement{TierID: 2, NodeID: "node_2", Path: "/data/tier2"},
				CreatedAt: now.Add(-48 * time.Hour),
			},
			{
				Key:       "cold_file",
				SizeBytes: 50 * 1024 * 1024,
				State:     fileapi.StateCompleted,
				Placement: fileapi.Placement{TierID: 2, NodeID: "node_2", Path: "/data/tier2"},
				CreatedAt: now.Add(-48 * time.Hour),
			},
		},
		States: map[string]FileState{
			"hot_file": {
				Key:       "hot_file",
				Score:     6.0,
				ScoreAt:   now,
				TierSince: now.Add(-48 * time.Hour),
			},
			"cold_file": {
				Key:       "cold_file",
				Score:     4.5,
				ScoreAt:   now,
				TierSince: now.Add(-48 * time.Hour),
			},
		},
	}

	tasks, skipped := Evaluate(snap)
	if len(tasks) != 1 {
		t.Fatalf("expected 1 promote task, got %d (skipped: %+v)", len(tasks), skipped)
	}

	task := tasks[0]
	if task.Key != "hot_file" {
		t.Errorf("task.Key = %s, want hot_file", task.Key)
	}
	if task.Kind != TaskPromote {
		t.Errorf("task.Kind = %s, want %s", task.Kind, TaskPromote)
	}
	if task.To.TierID != 0 {
		t.Errorf("task.To.TierID = %d, want 0", task.To.TierID)
	}
}

func TestHysteresisFlapping(t *testing.T) {
	startTime := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	policy := FlowPolicy{
		Version:         1,
		TickSeconds:     60,
		HalfLifeHours:   24,
		MaxTasksPerTick: 10,
		Ingest:          []int{2, 1, 0},
		Edges: []Edge{
			{
				ID:      "e_promote",
				Kind:    EdgePromote,
				From:    2,
				To:      0,
				Enabled: true,
				Trigger: Trigger{
					HeatAtLeast: 5.0,
				},
			},
			{
				ID:      "e_demote",
				Kind:    EdgeDemote,
				From:    0,
				To:      2,
				Enabled: true,
				Trigger: Trigger{
					IdleDays:  7,
					HeatBelow: 2.0, // Hysteresis gap: heat must drop below 2.0
				},
			},
		},
	}

	tier0 := helperMakeTier(0, "Tier 0 · Hot (NVMe)", 0, 90, 75, 1000*1024*1024, 200*1024*1024)
	tier2 := helperMakeTier(2, "Tier 2 · Cold (HDD)", 2, 90, 75, 1000*1024*1024, 200*1024*1024)

	// File is on Tier 0. Idle for 10 days, but score starts at 5.5.
	st := FileState{
		Key:       "media_hys",
		Score:     5.5,
		ScoreAt:   startTime,
		LastHitAt: startTime.Add(-10 * 24 * time.Hour),
		TierSince: startTime.Add(-10 * 24 * time.Hour),
	}

	// Over 10 consecutive ticks (e.g. simulating 1 tick per hour), score decays:
	// From 5.5, after 10 hours score is ~4.12 > 2.0 -> NO demote!
	for tick := 0; tick < 10; tick++ {
		now := startTime.Add(time.Duration(tick) * time.Hour)
		snap := Snapshot{
			Policy: policy,
			Tiers:  []storage.TierStatus{tier0, tier2},
			Now:    now,
			Files: []FileView{
				{
					Key:       "media_hys",
					SizeBytes: 50 * 1024 * 1024,
					State:     fileapi.StateCompleted,
					Placement: fileapi.Placement{TierID: 0, NodeID: "node_0", Path: "/data/tier0"},
					CreatedAt: startTime.Add(-10 * 24 * time.Hour),
				},
			},
			States: map[string]FileState{"media_hys": st},
		}

		tasks, _ := Evaluate(snap)
		if len(tasks) != 0 {
			t.Fatalf("tick %d: expected 0 tasks due to hysteresis heat >= 2.0, got %d", tick, len(tasks))
		}
	}

	// After 40 hours, score has decayed below 2.0 (~1.7) -> demote must trigger!
	demoteTime := startTime.Add(40 * time.Hour)
	snapDemote := Snapshot{
		Policy: policy,
		Tiers:  []storage.TierStatus{tier0, tier2},
		Now:    demoteTime,
		Files: []FileView{
			{
				Key:       "media_hys",
				SizeBytes: 50 * 1024 * 1024,
				State:     fileapi.StateCompleted,
				Placement: fileapi.Placement{TierID: 0, NodeID: "node_0", Path: "/data/tier0"},
				CreatedAt: startTime.Add(-10 * 24 * time.Hour),
			},
		},
		States: map[string]FileState{"media_hys": st},
	}

	tasks, _ := Evaluate(snapDemote)
	if len(tasks) != 1 {
		t.Fatalf("expected 1 demote task once score decayed below 2.0, got %d", len(tasks))
	}
	if tasks[0].Kind != TaskDemote || tasks[0].To.TierID != 2 {
		t.Errorf("task = %+v, expected Demote to tier 2", tasks[0])
	}
}

func TestMakeRoomDemoteOrderAndPinProtection(t *testing.T) {
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	policy := FlowPolicy{
		Version:         1,
		TickSeconds:     60,
		HalfLifeHours:   24,
		MaxTasksPerTick: 10,
		Ingest:          []int{2, 1, 0},
		Edges: []Edge{
			{
				ID:      "e_promote_2_0",
				Kind:    EdgePromote,
				From:    2,
				To:      0,
				Enabled: true,
				Trigger: Trigger{HeatAtLeast: 5.0},
			},
			{
				ID:      "e_demote_0_2",
				Kind:    EdgeDemote,
				From:    0,
				To:      2,
				Enabled: true,
				Trigger: Trigger{IdleDays: 30},
			},
		},
	}

	// Tier 0 has 200MB total, exactly 200MB used (100% full).
	tier0 := helperMakeTier(0, "Tier 0 · Hot (NVMe)", 0, 90, 75, 200*1024*1024, 200*1024*1024)
	tier2 := helperMakeTier(2, "Tier 2 · Cold (HDD)", 2, 90, 75, 1000*1024*1024, 200*1024*1024)

	pinnedTier := 0
	snap := Snapshot{
		Policy: policy,
		Tiers:  []storage.TierStatus{tier0, tier2},
		Now:    now,
		Files: []FileView{
			// Residents on Tier 0:
			{Key: "resident_a", SizeBytes: 50 * 1024 * 1024, State: fileapi.StateCompleted, Placement: fileapi.Placement{TierID: 0, NodeID: "node_0", Path: "/data/tier0"}},
			{Key: "resident_b", SizeBytes: 50 * 1024 * 1024, State: fileapi.StateCompleted, Placement: fileapi.Placement{TierID: 0, NodeID: "node_0", Path: "/data/tier0"}},
			{Key: "resident_pinned", SizeBytes: 100 * 1024 * 1024, State: fileapi.StateCompleted, Placement: fileapi.Placement{TierID: 0, NodeID: "node_0", Path: "/data/tier0"}},
			// Candidate on Tier 2 needing 60MB:
			{Key: "candidate_hot", SizeBytes: 60 * 1024 * 1024, State: fileapi.StateCompleted, Placement: fileapi.Placement{TierID: 2, NodeID: "node_2", Path: "/data/tier2"}},
		},
		States: map[string]FileState{
			"resident_a": {
				Key:       "resident_a",
				Score:     1.0,
				ScoreAt:   now,
				TierSince: now.Add(-10 * 24 * time.Hour),
			},
			"resident_b": {
				Key:       "resident_b",
				Score:     2.0,
				ScoreAt:   now,
				TierSince: now.Add(-10 * 24 * time.Hour),
			},
			"resident_pinned": {
				Key:       "resident_pinned",
				Score:     0.1, // lowest score, but pinned!
				ScoreAt:   now,
				PinTierID: &pinnedTier,
				PinUntil:  now.Add(24 * time.Hour),
				TierSince: now.Add(-10 * 24 * time.Hour),
			},
			"candidate_hot": {
				Key:       "candidate_hot",
				Score:     10.0,
				ScoreAt:   now,
				TierSince: now.Add(-10 * 24 * time.Hour),
			},
		},
	}

	tasks, skipped := Evaluate(snap)
	if len(tasks) != 3 {
		t.Fatalf("expected 3 tasks (2 make-room demotes + 1 promote), got %d (skipped: %+v)", len(tasks), skipped)
	}

	// Verify pinned file was NOT demoted
	for _, tk := range tasks {
		if tk.Key == "resident_pinned" {
			t.Fatalf("pinned file 'resident_pinned' was selected for demotion!")
		}
	}

	// Verify make-room demote tasks
	demotedKeys := make(map[string]bool)
	for _, tk := range tasks {
		if tk.Kind == TaskDemote {
			demotedKeys[tk.Key] = true
			if tk.To.TierID != 2 {
				t.Errorf("demote task target tier = %d, want 2", tk.To.TierID)
			}
		}
	}

	if !demotedKeys["resident_a"] || !demotedKeys["resident_b"] {
		t.Fatalf("expected resident_a and resident_b demoted, got %+v", demotedKeys)
	}

	// Verify candidate_hot was promoted
	lastTask := tasks[len(tasks)-1]
	if lastTask.Key != "candidate_hot" || lastTask.Kind != TaskPromote || lastTask.To.TierID != 0 {
		t.Fatalf("expected candidate_hot promoted to tier 0, got %+v", lastTask)
	}
}

func TestWatermarkDemote(t *testing.T) {
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	policy := FlowPolicy{
		Version:         1,
		TickSeconds:     60,
		HalfLifeHours:   24,
		MaxTasksPerTick: 10,
		Ingest:          []int{2, 1, 0},
		Edges: []Edge{
			{
				ID:      "e_watermark_0_2",
				Kind:    EdgeDemote,
				From:    0,
				To:      2,
				Enabled: true,
				Trigger: Trigger{OnWatermark: true},
			},
		},
	}

	// Tier 0: Total 1000MB, High 90% (900MB), Low 75% (750MB).
	// Currently used: 920MB (92% > 90% HighWatermark).
	tier0 := helperMakeTier(0, "Tier 0 · Hot (NVMe)", 0, 90, 75, 1000*1024*1024, 920*1024*1024)
	tier2 := helperMakeTier(2, "Tier 2 · Cold (HDD)", 2, 90, 75, 2000*1024*1024, 200*1024*1024)

	snap := Snapshot{
		Policy: policy,
		Tiers:  []storage.TierStatus{tier0, tier2},
		Now:    now,
		Files: []FileView{
			{Key: "cold_f1", SizeBytes: 100 * 1024 * 1024, State: fileapi.StateCompleted, Placement: fileapi.Placement{TierID: 0, NodeID: "node_0", Path: "/data/tier0"}},
			{Key: "cold_f2", SizeBytes: 100 * 1024 * 1024, State: fileapi.StateCompleted, Placement: fileapi.Placement{TierID: 0, NodeID: "node_0", Path: "/data/tier0"}},
			{Key: "heavy_f3", SizeBytes: 720 * 1024 * 1024, State: fileapi.StateCompleted, Placement: fileapi.Placement{TierID: 0, NodeID: "node_0", Path: "/data/tier0"}},
		},
		States: map[string]FileState{
			"cold_f1":  {Key: "cold_f1", Score: 1.0, ScoreAt: now},
			"cold_f2":  {Key: "cold_f2", Score: 2.0, ScoreAt: now},
			"heavy_f3": {Key: "heavy_f3", Score: 5.0, ScoreAt: now},
		},
	}

	tasks, skipped := Evaluate(snap)
	// Initial used: 920MB.
	// Low watermark: 750MB. Need to demote >= 170MB.
	// Demote cold_f1 (100MB) -> 820MB used (> 750MB).
	// Demote cold_f2 (100MB) -> 720MB used (<= 750MB -> STOP!).
	// heavy_f3 must NOT be demoted!
	if len(tasks) != 2 {
		t.Fatalf("expected exactly 2 watermark demote tasks, got %d (skipped: %+v)", len(tasks), skipped)
	}

	if tasks[0].Key != "cold_f1" || tasks[1].Key != "cold_f2" {
		t.Fatalf("expected cold_f1 and cold_f2 in coldest-first order, got %s and %s", tasks[0].Key, tasks[1].Key)
	}
}

func TestIdleCalculation(t *testing.T) {
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	policy := FlowPolicy{
		Version:         1,
		TickSeconds:     60,
		HalfLifeHours:   24,
		MaxTasksPerTick: 10,
		Ingest:          []int{2, 1, 0},
		Edges: []Edge{
			{
				ID:      "e_idle_0_2",
				Kind:    EdgeDemote,
				From:    0,
				To:      2,
				Enabled: true,
				Trigger: Trigger{IdleDays: 7},
			},
		},
	}

	tier0 := helperMakeTier(0, "Tier 0 · Hot (NVMe)", 0, 90, 75, 1000*1024*1024, 200*1024*1024)
	tier2 := helperMakeTier(2, "Tier 2 · Cold (HDD)", 2, 90, 75, 1000*1024*1024, 200*1024*1024)

	snap := Snapshot{
		Policy: policy,
		Tiers:  []storage.TierStatus{tier0, tier2},
		Now:    now,
		Files: []FileView{
			{
				Key:       "recent_hit",
				SizeBytes: 50 * 1024 * 1024,
				State:     fileapi.StateCompleted,
				Placement: fileapi.Placement{TierID: 0, NodeID: "node_0", Path: "/data/tier0"},
				CreatedAt: now.Add(-30 * 24 * time.Hour),
			},
			{
				Key:       "truly_idle",
				SizeBytes: 50 * 1024 * 1024,
				State:     fileapi.StateCompleted,
				Placement: fileapi.Placement{TierID: 0, NodeID: "node_0", Path: "/data/tier0"},
				CreatedAt: now.Add(-30 * 24 * time.Hour),
			},
		},
		States: map[string]FileState{
			"recent_hit": {
				Key:       "recent_hit",
				Score:     0.1,
				ScoreAt:   now,
				TierSince: now.Add(-30 * 24 * time.Hour),
				LastHitAt: now.Add(-2 * 24 * time.Hour), // hit 2 days ago (< 7d)
			},
			"truly_idle": {
				Key:       "truly_idle",
				Score:     0.1,
				ScoreAt:   now,
				TierSince: now.Add(-30 * 24 * time.Hour),
				LastHitAt: now.Add(-15 * 24 * time.Hour), // hit 15 days ago (> 7d)
			},
		},
	}

	tasks, _ := Evaluate(snap)
	if len(tasks) != 1 {
		t.Fatalf("expected 1 idle task, got %d", len(tasks))
	}
	if tasks[0].Key != "truly_idle" {
		t.Errorf("expected truly_idle task, got %s", tasks[0].Key)
	}
}

func TestMaxTasksPerTickPriority(t *testing.T) {
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	policy := FlowPolicy{
		Version:         1,
		TickSeconds:     60,
		HalfLifeHours:   24,
		MaxTasksPerTick: 2, // strictly cap at 2 tasks
		Ingest:          []int{2, 1, 0},
		Edges: []Edge{
			{
				ID:      "e_promote",
				Kind:    EdgePromote,
				From:    2,
				To:      0,
				Enabled: true,
				Trigger: Trigger{HeatAtLeast: 5.0},
			},
			{
				ID:      "e_idle",
				Kind:    EdgeDemote,
				From:    0,
				To:      2,
				Enabled: true,
				Trigger: Trigger{IdleDays: 7},
			},
		},
	}

	tier0 := helperMakeTier(0, "Tier 0 · Hot (NVMe)", 0, 90, 75, 1000*1024*1024, 200*1024*1024)
	tier2 := helperMakeTier(2, "Tier 2 · Cold (HDD)", 2, 90, 75, 1000*1024*1024, 200*1024*1024)

	pinTarget := 0
	snap := Snapshot{
		Policy: policy,
		Tiers:  []storage.TierStatus{tier0, tier2},
		Now:    now,
		Files: []FileView{
			{Key: "pin_file", SizeBytes: 10 * 1024 * 1024, State: fileapi.StateCompleted, Placement: fileapi.Placement{TierID: 2, NodeID: "node_2", Path: "/data/tier2"}},
			{Key: "promote_hot", SizeBytes: 10 * 1024 * 1024, State: fileapi.StateCompleted, Placement: fileapi.Placement{TierID: 2, NodeID: "node_2", Path: "/data/tier2"}},
			{Key: "idle_file", SizeBytes: 10 * 1024 * 1024, State: fileapi.StateCompleted, Placement: fileapi.Placement{TierID: 0, NodeID: "node_0", Path: "/data/tier0"}},
		},
		States: map[string]FileState{
			"pin_file": {
				Key:       "pin_file",
				Score:     1.0,
				ScoreAt:   now,
				PinTierID: &pinTarget,
				PinUntil:  now.Add(24 * time.Hour),
			},
			"promote_hot": {
				Key:     "promote_hot",
				Score:   10.0,
				ScoreAt: now,
			},
			"idle_file": {
				Key:       "idle_file",
				Score:     0.1,
				ScoreAt:   now,
				TierSince: now.Add(-20 * 24 * time.Hour),
				LastHitAt: now.Add(-20 * 24 * time.Hour),
			},
		},
	}

	tasks, _ := Evaluate(snap)
	if len(tasks) != 2 {
		t.Fatalf("expected exactly 2 tasks (due to MaxTasksPerTick=2), got %d", len(tasks))
	}

	// Priority order: Pins > Promote > Watermark > Idle > Expire
	if tasks[0].Key != "pin_file" {
		t.Errorf("task[0] = %s, want pin_file (pins have highest priority)", tasks[0].Key)
	}
	if tasks[1].Key != "promote_hot" {
		t.Errorf("task[1] = %s, want promote_hot (promote before idle)", tasks[1].Key)
	}
}

func TestSkippedReasons(t *testing.T) {
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	policy := FlowPolicy{
		Version:         1,
		TickSeconds:     60,
		HalfLifeHours:   24,
		MaxTasksPerTick: 10,
		Ingest:          []int{2, 1, 0},
		Edges: []Edge{
			{
				ID:      "e_promote_2_0",
				Kind:    EdgePromote,
				From:    2,
				To:      0,
				Enabled: true,
				Trigger: Trigger{HeatAtLeast: 5.0},
			},
			// Notice: NO demote edge from tier 0, so make-room is impossible!
		},
	}

	// Tier 0 has 50MB total, completely full (50MB used)
	tier0 := helperMakeTier(0, "Tier 0 · Hot (NVMe)", 0, 90, 75, 50*1024*1024, 50*1024*1024)
	tier2 := helperMakeTier(2, "Tier 2 · Cold (HDD)", 2, 90, 75, 1000*1024*1024, 200*1024*1024)

	snap := Snapshot{
		Policy: policy,
		Tiers:  []storage.TierStatus{tier0, tier2},
		Now:    now,
		Files: []FileView{
			{
				Key:       "hot_too_big",
				SizeBytes: 60 * 1024 * 1024, // larger than whole tier
				State:     fileapi.StateCompleted,
				Placement: fileapi.Placement{TierID: 2, NodeID: "node_2", Path: "/data/tier2"},
			},
		},
		States: map[string]FileState{
			"hot_too_big": {
				Key:     "hot_too_big",
				Score:   10.0,
				ScoreAt: now,
			},
		},
	}

	tasks, skipped := Evaluate(snap)
	if len(tasks) != 0 {
		t.Fatalf("expected 0 tasks because destination is full, got %d", len(tasks))
	}
	if len(skipped) != 1 {
		t.Fatalf("expected 1 skipped candidate, got %d", len(skipped))
	}
	if skipped[0].Key != "hot_too_big" || skipped[0].Reason != "destination full" {
		t.Errorf("skipped[0] = %+v, want destination full", skipped[0])
	}
}

func TestPlayDeduper(t *testing.T) {
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	ttl := 30 * time.Minute
	deduper := NewPlayDeduper(3, ttl) // capacity 3 for testing LRU

	// 1. First hit for IP1 + Key1 -> counts
	if !deduper.ShouldCount("192.168.1.1", "key1", now) {
		t.Errorf("first hit for key1 should count")
	}

	// 2. Second hit from same IP within 30m -> rejected
	if deduper.ShouldCount("192.168.1.1", "key1", now.Add(5*time.Minute)) {
		t.Errorf("second hit within 30m should not count")
	}

	// 3. Different key from same IP -> counts
	if !deduper.ShouldCount("192.168.1.1", "key2", now.Add(5*time.Minute)) {
		t.Errorf("hit for different key should count")
	}

	// 4. Same key from different IP -> counts
	if !deduper.ShouldCount("10.0.0.1", "key1", now.Add(5*time.Minute)) {
		t.Errorf("hit from different IP should count")
	}

	// 5. Same IP + Key1 after 30m -> counts
	if !deduper.ShouldCount("192.168.1.1", "key1", now.Add(31*time.Minute)) {
		t.Errorf("hit after 31m should count")
	}

	// 6. Test LRU capacity eviction with capacity 3:
	dCap := NewPlayDeduper(3, 1*time.Hour)
	dCap.ShouldCount("ipA", "k1", now)
	dCap.ShouldCount("ipB", "k2", now)
	dCap.ShouldCount("ipC", "k3", now)

	// Fourth entry pushes out oldest (ipA:k1)
	dCap.ShouldCount("ipD", "k4", now)

	// ipA:k1 was evicted, so it counts again immediately!
	if !dCap.ShouldCount("ipA", "k1", now) {
		t.Errorf("evicted entry should count again")
	}
}

func TestPlayDeduperByteBudget(t *testing.T) {
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	const entryBytes = int64(11 + 128) // len("ip1|key-one") + overhead

	// Part 1: at capacity, an insert sheds exactly the oldest entry.
	d := NewPlayDeduper(1000, time.Hour, WithBudget(300)) // fits two 139B entries
	d.ShouldCount("ip1", "key-one", now)
	d.ShouldCount("ip2", "key-two", now)
	if snap := d.Snapshot(); snap.Bytes != 2*entryBytes || snap.Evicted != 0 {
		t.Fatalf("snapshot after two inserts = %+v", snap)
	}

	// Third insert exceeds the budget and sheds the oldest entry (key-one).
	if !d.ShouldCount("ip9", "key-nix", now) {
		t.Fatalf("third hit should count")
	}
	snap := d.Snapshot()
	if snap.Bytes > 300 {
		t.Fatalf("bytes = %d, want <= 300 after budget enforcement", snap.Bytes)
	}
	if snap.Evicted != 1 {
		t.Fatalf("evicted = %d, want 1 (oldest entry shed)", snap.Evicted)
	}

	// The evicted entry counts again when replayed.
	if !d.ShouldCount("ip1", "key-one", now) {
		t.Errorf("budget-evicted entry should count again")
	}

	// Part 2: within budget, entries dedupe and nothing is evicted.
	e := NewPlayDeduper(1000, time.Hour, WithBudget(450)) // fits three 139B entries
	e.ShouldCount("ip1", "key-one", now)
	e.ShouldCount("ip2", "key-two", now)
	e.ShouldCount("ip9", "key-nix", now)
	if e.ShouldCount("ip2", "key-two", now.Add(time.Minute)) {
		t.Errorf("recent surviving entry should dedupe within TTL")
	}
	if snap := e.Snapshot(); snap.Evicted != 0 || snap.Entries != 3 {
		t.Fatalf("within-budget snapshot = %+v, want 3 entries, 0 evicted", snap)
	}
}

func TestPlayDeduperDefaultBudget(t *testing.T) {
	d := NewPlayDeduper(1000, time.Hour)
	if snap := d.Snapshot(); snap.Name != "play_deduper" || snap.Bytes != 0 {
		t.Fatalf("fresh deduper snapshot = %+v", snap)
	}
}

func TestHitRecorder(t *testing.T) {
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	store := newFullMemStore()

	recorder := NewHitRecorder(store, 24.0)
	recorder.RecordHit("key_a")
	recorder.RecordHit("key_a")
	recorder.RecordHit("key_b")

	if recorder.Pending("key_a") != 2 {
		t.Errorf("pending key_a = %d, want 2", recorder.Pending("key_a"))
	}
	if recorder.Pending("key_b") != 1 {
		t.Errorf("pending key_b = %d, want 1", recorder.Pending("key_b"))
	}

	if err := recorder.Flush(now, 24.0); err != nil {
		t.Fatalf("Flush error: %v", err)
	}

	stA, err := store.GetLifecycleState("key_a")
	if err != nil || stA == nil {
		t.Fatalf("state key_a not saved")
	}
	if stA.Score != 2.0 {
		t.Errorf("stA.Score = %v, want 2.0", stA.Score)
	}
	if !stA.LastHitAt.Equal(now) {
		t.Errorf("stA.LastHitAt = %v, want %v", stA.LastHitAt, now)
	}

	// Flush again with 1 more hit 24h later: 2.0 decays to 1.0 + 1 hit = 2.0
	recorder.RecordHit("key_a")
	if err := recorder.Flush(now.Add(24*time.Hour), 24.0); err != nil {
		t.Fatalf("second Flush error: %v", err)
	}
	stA2, _ := store.GetLifecycleState("key_a")
	if math.Abs(stA2.Score-2.0) > 0.001 {
		t.Errorf("stA2.Score = %v, want 2.0 (1.0 decayed + 1 new hit)", stA2.Score)
	}
}

func TestEvaluateExpireRules(t *testing.T) {
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	pinTier := 2

	policy := FlowPolicy{
		Version:            1,
		TickSeconds:        60,
		HalfLifeHours:      24,
		MaxTasksPerTick:    10,
		Ingest:             []int{2, 1, 0},
		ExpireAcknowledged: false, // Expire unacknowledged
		Edges: []Edge{
			{
				ID:      "e_exp",
				Kind:    EdgeExpire,
				From:    2,
				To:      TrashTierID,
				Enabled: true,
				Trigger: Trigger{AgeDays: 30},
			},
		},
	}

	tier2 := helperMakeTier(2, "Tier 2 · Cold (HDD)", 2, 90, 75, 1000*1024*1024, 200*1024*1024)

	snap := Snapshot{
		Policy: policy,
		Tiers:  []storage.TierStatus{tier2},
		Now:    now,
		Active: map[string]bool{
			"file_active": true,
		},
		Files: []FileView{
			{
				Key:       "file_old",
				SizeBytes: 10 * 1024 * 1024,
				State:     fileapi.StateCompleted,
				Placement: fileapi.Placement{TierID: 2, NodeID: "node_2", Path: "/data/tier2"},
				CreatedAt: now.Add(-40 * 24 * time.Hour),
			},
			{
				Key:       "file_pinned",
				SizeBytes: 10 * 1024 * 1024,
				State:     fileapi.StateCompleted,
				Placement: fileapi.Placement{TierID: 2, NodeID: "node_2", Path: "/data/tier2"},
				CreatedAt: now.Add(-40 * 24 * time.Hour),
			},
			{
				Key:       "file_active",
				SizeBytes: 10 * 1024 * 1024,
				State:     fileapi.StateCompleted,
				Placement: fileapi.Placement{TierID: 2, NodeID: "node_2", Path: "/data/tier2"},
				CreatedAt: now.Add(-40 * 24 * time.Hour),
			},
		},
		States: map[string]FileState{
			"file_old": {
				Key:       "file_old",
				TierSince: now.Add(-40 * 24 * time.Hour),
			},
			"file_pinned": {
				Key:       "file_pinned",
				TierSince: now.Add(-40 * 24 * time.Hour),
				PinTierID: &pinTier,
				PinUntil:  now.Add(24 * time.Hour),
			},
			"file_active": {
				Key:       "file_active",
				TierSince: now.Add(-40 * 24 * time.Hour),
			},
		},
	}

	// 1. Unacknowledged: 0 expire tasks
	tasks, _ := Evaluate(snap)
	if len(tasks) != 0 {
		t.Fatalf("expected 0 tasks when ExpireAcknowledged=false, got %d", len(tasks))
	}

	// 2. Acknowledged: only unpinned, inactive file_old expires
	snap.Policy.ExpireAcknowledged = true
	tasks, _ = Evaluate(snap)
	if len(tasks) != 1 {
		t.Fatalf("expected exactly 1 task when ExpireAcknowledged=true, got %d", len(tasks))
	}
	if tasks[0].Key != "file_old" || tasks[0].Kind != TaskExpire {
		t.Fatalf("expected expire task for file_old, got %+v", tasks[0])
	}

	// 3. Oldest-first ordering with MaxTasksPerTick=1
	snap.Files = append(snap.Files, FileView{
		Key:       "file_older",
		SizeBytes: 10 * 1024 * 1024,
		State:     fileapi.StateCompleted,
		Placement: fileapi.Placement{TierID: 2, NodeID: "node_2", Path: "/data/tier2"},
		CreatedAt: now.Add(-60 * 24 * time.Hour),
	})
	snap.States["file_older"] = FileState{
		Key:       "file_older",
		TierSince: now.Add(-60 * 24 * time.Hour),
	}
	snap.Policy.MaxTasksPerTick = 1

	tasks, _ = Evaluate(snap)
	if len(tasks) != 1 {
		t.Fatalf("expected 1 task with MaxTasksPerTick=1, got %d", len(tasks))
	}
	if tasks[0].Key != "file_older" {
		t.Fatalf("expected oldest file (file_older) to be expired first, got %s", tasks[0].Key)
	}
}

func TestEvaluateExpireOnWatermarkPreference(t *testing.T) {
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)

	// High watermark = 90%, Low watermark = 75%. Total 1000MB, used 950MB (95% > 90%).
	tier0 := helperMakeTier(0, "Tier 0 · Hot (NVMe)", 0, 90, 75, 1000*1024*1024, 950*1024*1024)
	tier1 := helperMakeTier(1, "Tier 1 · Warm (SSD)", 1, 90, 75, 1000*1024*1024, 200*1024*1024)

	basePolicy := FlowPolicy{
		Version:            1,
		TickSeconds:        60,
		HalfLifeHours:      24,
		MaxTasksPerTick:    10,
		Ingest:             []int{1, 0},
		ExpireAcknowledged: true,
	}

	snap := Snapshot{
		Policy: basePolicy,
		Tiers:  []storage.TierStatus{tier0, tier1},
		Now:    now,
		Files: []FileView{
			{
				Key:       "file_c",
				SizeBytes: 100 * 1024 * 1024,
				State:     fileapi.StateCompleted,
				Placement: fileapi.Placement{TierID: 0, NodeID: "node_0", Path: "/data/tier0"},
				CreatedAt: now.Add(-10 * 24 * time.Hour),
			},
		},
		States: map[string]FileState{
			"file_c": {
				Key:       "file_c",
				Score:     0.1,
				TierSince: now.Add(-10 * 24 * time.Hour),
				LastHitAt: now.Add(-10 * 24 * time.Hour),
			},
		},
	}

	// 1. Both demote and expire edges exist. Demote edge has room on tier 1. Demote should be preferred!
	snap.Policy.Edges = []Edge{
		{
			ID:      "e_demote",
			Kind:    EdgeDemote,
			From:    0,
			To:      1,
			Enabled: true,
			Trigger: Trigger{OnWatermark: true},
		},
		{
			ID:      "e_expire_wm",
			Kind:    EdgeExpire,
			From:    0,
			To:      TrashTierID,
			Enabled: true,
			Trigger: Trigger{OnWatermark: true, AgeDays: 7},
		},
	}

	tasks, _ := Evaluate(snap)
	if len(tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(tasks))
	}
	if tasks[0].Kind != TaskDemote {
		t.Fatalf("expected demote to be preferred over expire when demote target has room, got %s", tasks[0].Kind)
	}

	// 2. Disable demote edge: now watermark expire fires
	snap.Policy.Edges[0].Enabled = false
	tasks, _ = Evaluate(snap)
	if len(tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(tasks))
	}
	if tasks[0].Kind != TaskExpire {
		t.Fatalf("expected watermark expire task when no demote edge is available, got %s", tasks[0].Kind)
	}

	// 3. When usage is below HighWatermark (e.g. 800MB used / 1000MB = 80% < 90%), watermark expire does not fire
	snapUnderPressure := snap
	tier0Normal := helperMakeTier(0, "Tier 0 · Hot (NVMe)", 0, 90, 75, 1000*1024*1024, 800*1024*1024)
	snapUnderPressure.Tiers = []storage.TierStatus{tier0Normal, tier1}

	tasks, _ = Evaluate(snapUnderPressure)
	if len(tasks) != 0 {
		t.Fatalf("expected 0 tasks when usage is below HighWatermark, got %d", len(tasks))
	}
}
