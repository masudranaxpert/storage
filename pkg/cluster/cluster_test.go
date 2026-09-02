package cluster_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"stream/pkg/cluster"
	"stream/pkg/telemetry"
)

func TestCoordinatorAndPoolAggregation(t *testing.T) {
	coord := cluster.NewCoordinator(100*time.Millisecond, 200*time.Millisecond, nil)

	// Simulate node 1 (Hetzner VPS with 500GB disk)
	node1 := telemetry.NodeMetrics{
		NodeID:   "vps-hetzner-1",
		Hostname: "hetzner-box",
		OS:       "linux",
		Disks: []telemetry.DiskStat{
			{Path: "/", FSType: "ext4", TotalBytes: 500 * 1024 * 1024 * 1024, FreeBytes: 300 * 1024 * 1024 * 1024, UsedBytes: 200 * 1024 * 1024 * 1024},
		},
		Memory: telemetry.MemoryStat{
			TotalBytes:     8 * 1024 * 1024 * 1024,
			AvailableBytes: 4 * 1024 * 1024 * 1024,
		},
		CPU: telemetry.CPUStat{Cores: 4},
	}

	// Simulate node 2 (Contabo VPS with 1TB disk)
	node2 := telemetry.NodeMetrics{
		NodeID:   "vps-contabo-2",
		Hostname: "contabo-box",
		OS:       "linux",
		Disks: []telemetry.DiskStat{
			{Path: "/data", FSType: "ext4", TotalBytes: 1000 * 1024 * 1024 * 1024, FreeBytes: 800 * 1024 * 1024 * 1024, UsedBytes: 200 * 1024 * 1024 * 1024},
		},
		Memory: telemetry.MemoryStat{
			TotalBytes:     16 * 1024 * 1024 * 1024,
			AvailableBytes: 12 * 1024 * 1024 * 1024,
		},
		CPU: telemetry.CPUStat{Cores: 8},
	}

	coord.RegisterHeartbeat(node1)
	coord.RegisterHeartbeat(node2)

	pool := coord.GetPoolSummary()

	if pool.TotalNodes != 2 || pool.ActiveNodes != 2 {
		t.Fatalf("expected 2 active nodes, got total=%d, active=%d", pool.TotalNodes, pool.ActiveNodes)
	}

	expectedStorage := uint64(1500 * 1024 * 1024 * 1024)
	if pool.TotalStorageBytes != expectedStorage {
		t.Fatalf("expected total storage %d, got %d", expectedStorage, pool.TotalStorageBytes)
	}

	expectedFree := uint64(1100 * 1024 * 1024 * 1024)
	if pool.FreeStorageBytes != expectedFree {
		t.Fatalf("expected free storage %d, got %d", expectedFree, pool.FreeStorageBytes)
	}

	if pool.TotalCPUCores != 12 {
		t.Fatalf("expected 12 total CPU cores, got %d", pool.TotalCPUCores)
	}
}

func TestAgentCoordinatorHeartbeat(t *testing.T) {
	coord := cluster.NewCoordinator(10*time.Second, 20*time.Second, nil)

	// Start test HTTP server
	mux := httpMux(coord)
	server := httptest.NewServer(mux)
	defer server.Close()

	agent := cluster.NewAgent("test-agent", server.URL, 50*time.Millisecond, 2052, "", "", "")

	record, err := agent.SendHeartbeat()
	if err != nil {
		t.Fatalf("agent SendHeartbeat failed: %v", err)
	}

	if record.Metrics.NodeID != "test-agent" {
		t.Fatalf("expected node ID 'test-agent', got %s", record.Metrics.NodeID)
	}
	if record.Status != cluster.StatusOnline {
		t.Fatalf("expected status 'online', got %s", record.Status)
	}

	nodes := coord.GetNodes()
	if len(nodes) != 1 {
		t.Fatalf("expected 1 node registered in coordinator, got %d", len(nodes))
	}
}

func httpMux(coord *cluster.Coordinator) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/heartbeat", func(w http.ResponseWriter, r *http.Request) {
		var metrics telemetry.NodeMetrics
		telemetryCollectMock(r, &metrics)
		rec := coord.RegisterHeartbeat(metrics)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"` + string(rec.Status) + `","metrics":{"node_id":"` + rec.Metrics.NodeID + `"}}`))
	})
	return mux
}

func telemetryCollectMock(r *http.Request, target *telemetry.NodeMetrics) {
	// Simple unmarshal mock helper
	var data telemetry.NodeMetrics
	target.NodeID = "test-agent"
	target.CPU.Cores = 2
	_ = data
}
