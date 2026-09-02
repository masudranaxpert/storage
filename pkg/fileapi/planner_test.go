package fileapi

import (
	"testing"

	"stream/pkg/cluster"
	"stream/pkg/telemetry"
)

func node(id string, status cluster.NodeStatus, freeGB uint64, aria, ffmpeg bool) *cluster.NodeRecord {
	return &cluster.NodeRecord{
		Status: status,
		Metrics: telemetry.NodeMetrics{
			NodeID: id,
			IPs:    []string{"10.0.0.1"},
			CPU:    telemetry.CPUStat{Cores: 4},
			Disks:  []telemetry.DiskStat{{Path: "/data", DiskType: "HDD", FreeBytes: freeGB << 30, TotalBytes: 2 * freeGB << 30}},
			Capabilities: telemetry.NodeCapabilities{
				HasAria2c: aria,
				HasFFmpeg: ffmpeg,
			},
		},
	}
}

func TestReceiverCandidatesExcludesMasterAndOffline(t *testing.T) {
	nodes := []*cluster.NodeRecord{
		node("vps-01", cluster.StatusOnline, 100, true, true),
		node("vps-storage", cluster.StatusOnline, 400, false, false), // no tools: still a receiver
		node("vps-off", cluster.StatusOffline, 900, true, true),
		node("local-master", cluster.StatusOnline, 900, true, true),
		node("pc (master)", cluster.StatusOnline, 900, true, true),
	}

	got := ReceiverCandidates(nodes)
	want := map[string]bool{"vps-01": true, "vps-storage": true}
	if len(got) != len(want) {
		t.Fatalf("receiver set = %v, want %v", got, want)
	}
	for id := range want {
		if !got[id] {
			t.Errorf("node %s must be a receiver", id)
		}
	}
}

func TestPickProcessingWorkerPrefersBlockOwner(t *testing.T) {
	owner := node("vps-01", cluster.StatusOnline, 50, true, true)
	roomier := node("vps-02", cluster.StatusOnline, 500, true, true)

	if got := PickProcessingWorker([]*cluster.NodeRecord{roomier, owner}, "vps-01", 10<<30); got.Metrics.NodeID != "vps-01" {
		t.Fatalf("block owner must win the fast path, got %s", got.Metrics.NodeID)
	}
}

func TestPickProcessingWorkerSkipsTightScratch(t *testing.T) {
	// 10GB file needs ~20GB scratch; vps-tight has only 12GB free.
	tight := node("vps-tight", cluster.StatusOnline, 12, true, true)
	spacy := node("vps-spacy", cluster.StatusOnline, 100, true, true)

	got := PickProcessingWorker([]*cluster.NodeRecord{tight, spacy}, "vps-absent", 10<<30)
	if got == nil || got.Metrics.NodeID != "vps-spacy" {
		t.Fatalf("expected the worker with roomy scratch, got %+v", got)
	}

	if got := PickProcessingWorker([]*cluster.NodeRecord{tight}, "vps-absent", 10<<30); got != nil {
		t.Fatal("no worker fits → nil expected")
	}
}
