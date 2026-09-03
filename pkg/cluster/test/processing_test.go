package cluster_test

import (
	"testing"
	"time"

	"stream/pkg/cluster"
	"stream/pkg/telemetry"
)

func procNode(id string, cores int, freeRAM uint64, disks []telemetry.DiskStat) *cluster.NodeRecord {
	return &cluster.NodeRecord{
		Status: cluster.StatusOnline,
		Metrics: telemetry.NodeMetrics{
			NodeID:  id,
			CPU:     telemetry.CPUStat{Cores: cores},
			Memory:  telemetry.MemoryStat{AvailableBytes: freeRAM},
			Disks:   disks,
		},
		LastSeen: time.Now(),
	}
}

func TestCheckEligibilityCoversReservation(t *testing.T) {
	node := procNode("vps-02", 2, 1<<30, []telemetry.DiskStat{
		{Path: "/ssd", DiskType: "SSD", FreeBytes: 40 << 30},
	})

	profile := cluster.ProcessingProfile{
		Enabled:              true,
		ReservedCPUCores:     1,
		ReservedRAMBytes:     500 << 20, // 500MB
		PreferredDiskType:    "SSD",
		ReservedStorageBytes: 5 << 30,
	}

	e := cluster.CheckEligibility(node, profile)
	if !e.Eligible {
		t.Fatalf("expected eligible, reasons: %v", e.Reasons)
	}
	if e.FreeCPUCores != 2 || e.FreeRAMBytes != 1<<30 {
		t.Fatalf("unexpected live free resource snapshot: %+v", e)
	}
}

func TestCheckEligibilityRejectsInsufficientRAM(t *testing.T) {
	node := procNode("vps-02", 1, 200<<20, nil)
	profile := cluster.ProcessingProfile{
		Enabled:          true,
		ReservedCPUCores: 1,
		ReservedRAMBytes: 500 << 20,
	}

	e := cluster.CheckEligibility(node, profile)
	if e.Eligible {
		t.Fatal("must be ineligible with only 200MB free vs 500MB requested")
	}
	if len(e.Reasons) == 0 {
		t.Fatal("expected human-readable rejection reason")
	}
}

func TestCheckEligibilityPreferredDiskType(t *testing.T) {
	hddOnly := procNode("vps-hdd", 4, 4<<30, []telemetry.DiskStat{
		{Path: "/hdd", DiskType: "HDD", FreeBytes: 900 << 30},
	})

	profile := cluster.ProcessingProfile{
		Enabled:              true,
		PreferredDiskType:    "SSD",
		ReservedStorageBytes: 5 << 30,
	}

	if e := cluster.CheckEligibility(hddOnly, profile); e.Eligible {
		t.Fatal("SSD-scratch request must fail on HDD-only node")
	}

	hddProfile := profile
	hddProfile.PreferredDiskType = "HDD"
	if e := cluster.CheckEligibility(hddOnly, hddProfile); !e.Eligible {
		t.Fatalf("HDD request on HDD node must pass, reasons: %v", e.Reasons)
	}
}

func TestCheckEligibilityDisabledProfile(t *testing.T) {
	node := procNode("vps-02", 8, 8<<30, nil)
	e := cluster.CheckEligibility(node, cluster.ProcessingProfile{Enabled: false})
	if e.Eligible {
		t.Fatal("disabled profile must never be eligible")
	}
}

func TestCheckEligibilityOfflineNode(t *testing.T) {
	node := procNode("vps-02", 8, 8<<30, nil)
	node.Status = cluster.StatusOffline
	e := cluster.CheckEligibility(node, cluster.ProcessingProfile{Enabled: true})
	if e.Eligible {
		t.Fatal("offline node must never be eligible")
	}
}

func TestSelectProcessingWorkerHonorsReservations(t *testing.T) {
	weak := procNode("vps-weak", 1, 100<<20, nil)
	strong := procNode("vps-strong", 4, 4<<30, nil)

	profiles := map[string]cluster.ProcessingProfile{
		"vps-weak":   {NodeID: "vps-weak", Enabled: true, ReservedCPUCores: 1, ReservedRAMBytes: 500 << 20},
		"vps-strong": {NodeID: "vps-strong", Enabled: true, ReservedCPUCores: 1, ReservedRAMBytes: 500 << 20},
	}

	got, err := cluster.SelectProcessingWorker(
		[]*cluster.NodeRecord{weak, strong},
		profiles,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "vps-strong" {
		t.Fatalf("expected vps-strong (weak fails RAM reservation), got %s", got)
	}
}

func TestSelectProcessingWorkerFallsBackWhenNothingConfigured(t *testing.T) {
	nodes := []*cluster.NodeRecord{
		{
			Status:  cluster.StatusOnline,
			Metrics: telemetry.NodeMetrics{NodeID: "solo", CPU: telemetry.CPUStat{UsedPercent: 5}, Memory: telemetry.MemoryStat{UsedPercent: 5}},
		},
	}

	got, err := cluster.SelectProcessingWorker(nodes, map[string]cluster.ProcessingProfile{})
	if err != nil || got != "solo" {
		t.Fatalf("expected fallback pick 'solo', got %q err=%v", got, err)
	}
}

func TestSelectProcessingWorkerErrorWhenAllReservedNodesStarve(t *testing.T) {
	nodes := []*cluster.NodeRecord{procNode("vps-02", 1, 100<<20, nil)}
	profiles := map[string]cluster.ProcessingProfile{
		"vps-02": {NodeID: "vps-02", Enabled: true, ReservedRAMBytes: 8 << 30},
	}

	if _, err := cluster.SelectProcessingWorker(nodes, profiles); err == nil {
		t.Fatal("expected starvation error when the only reserved node cannot serve")
	}
}

func TestEvaluateProcessingDefaultsUnconfiguredNodes(t *testing.T) {
	nodes := []*cluster.NodeRecord{procNode("fresh", 2, 2<<30, nil)}
	out := cluster.EvaluateProcessing(nodes, map[string]cluster.ProcessingProfile{})
	if out["fresh"].Eligible {
		t.Fatal("unconfigured nodes must default to disabled")
	}
}
