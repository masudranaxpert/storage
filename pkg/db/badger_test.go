package db_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"stream/pkg/cluster"
	"stream/pkg/db"
	"stream/pkg/telemetry"
)

func TestBadgerStoreSaveAndRetrieve(t *testing.T) {
	testDir := filepath.Join(os.TempDir(), "stream_badger_test")
	os.RemoveAll(testDir)
	defer os.RemoveAll(testDir)

	store, err := db.Open(testDir)
	if err != nil {
		t.Fatalf("failed to open test badger db: %v", err)
	}
	defer store.Close()

	// Save test node
	record := &cluster.NodeRecord{
		Metrics: telemetry.NodeMetrics{
			NodeID:   "vps-test-db",
			Hostname: "db-host",
			OS:       "linux",
			CPU:      telemetry.CPUStat{Cores: 8},
		},
		Status:   cluster.StatusOnline,
		LastSeen: time.Now().UTC(),
	}

	if err := store.SaveNode(record); err != nil {
		t.Fatalf("failed to save node into badger: %v", err)
	}

	// Retrieve node
	fetched, err := store.GetNode("vps-test-db")
	if err != nil {
		t.Fatalf("failed to get node from badger: %v", err)
	}
	if fetched.Metrics.NodeID != "vps-test-db" {
		t.Fatalf("expected node_id 'vps-test-db', got %s", fetched.Metrics.NodeID)
	}

	// Set & Get allocation
	cfg := &db.NodeConfig{
		NodeID:            "vps-test-db",
		AllocatedMaxBytes: 500 * 1024 * 1024 * 1024,
		Enabled:           true,
	}
	if err := store.SaveNodeConfig(cfg); err != nil {
		t.Fatalf("failed to save node config: %v", err)
	}

	fetchedCfg, err := store.GetNodeConfig("vps-test-db")
	if err != nil {
		t.Fatalf("failed to get node config: %v", err)
	}
	if fetchedCfg.AllocatedMaxBytes != 500*1024*1024*1024 {
		t.Fatalf("expected 500GB allocation, got %d", fetchedCfg.AllocatedMaxBytes)
	}
}
