package db_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"stream/pkg/cluster"
	"stream/pkg/db"
	"stream/pkg/lifecycle"
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

	// Verify registered admin tables include flow, lcstate, lctask, lcactive, lcaudit even when empty
	tables, err := store.AdminTables()
	if err != nil {
		t.Fatalf("AdminTables failed: %v", err)
	}
	tableNames := make(map[string]bool, len(tables))
	for _, tbl := range tables {
		tableNames[tbl.Name] = true
	}
	for _, expected := range []string{"flow", "lcstate", "lctask", "lcactive", "lcaudit"} {
		if !tableNames[expected] {
			t.Fatalf("expected %q in AdminTables, got %v", expected, tables)
		}
	}

	// Flow policy persistence
	pol := &lifecycle.FlowPolicy{Version: 1, Ingest: []int{2, 1, 0}, HalfLifeHours: 24, TickSeconds: 60, MaxTasksPerTick: 20}
	if err := store.SaveFlowPolicy(pol); err != nil {
		t.Fatalf("SaveFlowPolicy failed: %v", err)
	}
	gotPol, err := store.GetFlowPolicy()
	if err != nil || gotPol == nil || gotPol.Version != 1 {
		t.Fatalf("GetFlowPolicy = %+v, err=%v", gotPol, err)
	}

	// lcstate, lctask, lcactive, lcaudit round-trips
	st := &lifecycle.FileState{Key: "k1", Score: 12.5}
	if err := store.SaveLifecycleState(st); err != nil {
		t.Fatalf("SaveLifecycleState: %v", err)
	}
	if gotSt, err := store.GetLifecycleState("k1"); err != nil || gotSt == nil || gotSt.Score != 12.5 {
		t.Fatalf("GetLifecycleState = %+v, err=%v", gotSt, err)
	}

	task := &lifecycle.Task{ID: "t1", Key: "k1", Kind: lifecycle.TaskPromote, State: lifecycle.TaskQueued, CreatedAt: time.Now().UTC()}
	if err := store.SaveLifecycleTask(task); err != nil {
		t.Fatalf("SaveLifecycleTask: %v", err)
	}
	if gotTask, err := store.GetLifecycleTask("t1"); err != nil || gotTask == nil || gotTask.Key != "k1" {
		t.Fatalf("GetLifecycleTask = %+v, err=%v", gotTask, err)
	}

	if err := store.SetActiveLifecycleTask("k1", "t1"); err != nil {
		t.Fatalf("SetActiveLifecycleTask: %v", err)
	}
	if activeID, err := store.GetActiveLifecycleTask("k1"); err != nil || activeID != "t1" {
		t.Fatalf("GetActiveLifecycleTask = %q, err=%v", activeID, err)
	}
	if err := store.DeleteActiveLifecycleTask("k1"); err != nil {
		t.Fatalf("DeleteActiveLifecycleTask: %v", err)
	}

	if err := store.SaveLifecycleAudit(&lifecycle.AuditEntry{Key: "k1", Filename: "a.mp4", Reason: "expired", At: time.Now().UTC()}); err != nil {
		t.Fatalf("SaveLifecycleAudit: %v", err)
	}
	if audits, err := store.ListLifecycleAudits(10); err != nil || len(audits) != 1 || audits[0].Key != "k1" {
		t.Fatalf("ListLifecycleAudits = %+v, err=%v", audits, err)
	}

	// Test Bucket Persistence
	bucket := &db.BucketRecord{
		Name:      "test-archive",
		Provider:  "r2",
		Endpoint:  "https://test.r2.cloudflarestorage.com",
		Region:    "auto",
		AccessKey: "key123",
		SecretKey: "secret456",
	}
	if err := store.SaveBucket(bucket); err != nil {
		t.Fatalf("SaveBucket: %v", err)
	}
	if bucket.ID == "" {
		t.Fatalf("expected bucket ID to be generated")
	}

	gotBucket, err := store.GetBucket(bucket.ID)
	if err != nil || gotBucket == nil || gotBucket.Name != "test-archive" {
		t.Fatalf("GetBucket failed: got %+v, err=%v", gotBucket, err)
	}

	allBuckets, err := store.ListBuckets()
	if err != nil || len(allBuckets) != 1 {
		t.Fatalf("ListBuckets failed: got %d buckets, err=%v", len(allBuckets), err)
	}

	if err := store.DeleteBucket(bucket.ID); err != nil {
		t.Fatalf("DeleteBucket: %v", err)
	}

	remaining, err := store.ListBuckets()
	if err != nil || len(remaining) != 0 {
		t.Fatalf("expected 0 buckets after delete, got %d", len(remaining))
	}
}
