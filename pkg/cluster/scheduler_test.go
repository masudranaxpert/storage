package cluster_test

import (
	"testing"
	"time"

	"stream/pkg/cluster"
	"stream/pkg/telemetry"
)

func TestSelectOptimalWorker(t *testing.T) {
	nodes := []*cluster.NodeRecord{
		{
			Status: cluster.StatusOnline,
			Metrics: telemetry.NodeMetrics{
				NodeID: "local-master",
				CPU:    telemetry.CPUStat{UsedPercent: 10.0},
				Memory: telemetry.MemoryStat{UsedPercent: 20.0},
			},
			LastSeen: time.Now(),
		},
		{
			Status: cluster.StatusOnline,
			Metrics: telemetry.NodeMetrics{
				NodeID: "vps-01",
				CPU:    telemetry.CPUStat{UsedPercent: 5.0},
				Memory: telemetry.MemoryStat{UsedPercent: 15.0},
			},
			LastSeen: time.Now(),
		},
		{
			Status: cluster.StatusOffline,
			Metrics: telemetry.NodeMetrics{
				NodeID: "vps-offline",
				CPU:    telemetry.CPUStat{UsedPercent: 1.0},
				Memory: telemetry.MemoryStat{UsedPercent: 5.0},
			},
			LastSeen: time.Now(),
		},
	}

	bestNode, err := cluster.SelectOptimalWorker(nodes)
	if err != nil {
		t.Fatalf("expected optimal worker selection, got err: %v", err)
	}

	// vps-01 should be chosen because it has lower score and is online
	if bestNode != "vps-01" {
		t.Errorf("expected best node 'vps-01', got '%s'", bestNode)
	}
}
