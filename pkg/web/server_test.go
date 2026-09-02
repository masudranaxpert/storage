package web_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"stream/pkg/cluster"
	"stream/pkg/db"
	"stream/pkg/storage"
	"stream/pkg/telemetry"
	"stream/pkg/web"
)

func TestWebServerAPIs(t *testing.T) {
	testDir := filepath.Join(os.TempDir(), "stream_web_test_db")
	os.RemoveAll(testDir)
	defer os.RemoveAll(testDir)

	store, err := db.Open(testDir)
	if err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}
	defer store.Close()

	coord := cluster.NewCoordinator(10*time.Second, 20*time.Second, store)
	hub := cluster.NewGRPCHub(coord, store, "")
	tiers := storage.NewManager(store)
	srv := web.NewServer(":0", coord, hub, store, tiers, "../../web/static", "../../web/templates")
	handler := srv.Handler()

	// 1. Test POST /api/heartbeat
	metrics := telemetry.NodeMetrics{
		NodeID:   "web-test-vps",
		Hostname: "web-host",
		OS:       "linux",
		CPU:      telemetry.CPUStat{Cores: 4},
	}
	body, _ := json.Marshal(metrics)
	req := httptest.NewRequest(http.MethodPost, "/api/heartbeat", bytes.NewBuffer(body))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK from heartbeat, got %d", rec.Code)
	}

	// 2. Test GET /api/pool
	reqPool := httptest.NewRequest(http.MethodGet, "/api/pool", nil)
	recPool := httptest.NewRecorder()
	handler.ServeHTTP(recPool, reqPool)

	if recPool.Code != http.StatusOK {
		t.Fatalf("expected 200 OK from /api/pool, got %d", recPool.Code)
	}

	var pool cluster.ClusterPoolSummary
	if err := json.NewDecoder(recPool.Body).Decode(&pool); err != nil {
		t.Fatalf("failed to decode pool response: %v", err)
	}
	if pool.TotalNodes != 1 {
		t.Fatalf("expected 1 node in pool, got %d", pool.TotalNodes)
	}

	// 3. Test POST /api/nodes/web-test-vps/allocate
	allocPayload := []byte(`{"allocated_max_bytes": 107374182400}`)
	reqAlloc := httptest.NewRequest(http.MethodPost, "/api/nodes/web-test-vps/allocate", bytes.NewBuffer(allocPayload))
	recAlloc := httptest.NewRecorder()
	handler.ServeHTTP(recAlloc, reqAlloc)

	if recAlloc.Code != http.StatusOK {
		t.Fatalf("expected 200 OK from allocate, got %d", recAlloc.Code)
	}

	// 4. GET /api/tiers — seeded defaults, unassigned block discovery
	reqTiers := httptest.NewRequest(http.MethodGet, "/api/tiers", nil)
	recTiers := httptest.NewRecorder()
	handler.ServeHTTP(recTiers, reqTiers)

	if recTiers.Code != http.StatusOK {
		t.Fatalf("expected 200 OK from /api/tiers, got %d", recTiers.Code)
	}

	var tiersResp struct {
		Tiers            []storage.TierStatus `json:"tiers"`
		UnassignedBlocks []storage.Block      `json:"unassigned_blocks"`
	}
	if err := json.NewDecoder(recTiers.Body).Decode(&tiersResp); err != nil {
		t.Fatalf("failed to decode tiers response: %v", err)
	}
	if len(tiersResp.Tiers) < 3 {
		t.Fatalf("expected 3 seeded tiers, got %d", len(tiersResp.Tiers))
	}

	// 5. POST /api/tiers — bag the test node disk into tier 1 with a quota
	nodeRecord := coord.RegisterHeartbeat(telemetry.NodeMetrics{
		NodeID:  "web-test-vps",
		CPU:     telemetry.CPUStat{Cores: 4},
		Memory:  telemetry.MemoryStat{TotalBytes: 4 << 30, AvailableBytes: 2 << 30},
		Disks:   []telemetry.DiskStat{{Path: "/mnt/ssd", DiskType: "SSD", TotalBytes: 100 << 30, FreeBytes: 90 << 30}},
	})
	_ = nodeRecord

	tierPayload, _ := json.Marshal(storage.Tier{
		ID:      1,
		Name:    "Tier 1 · Hot",
		Default: true,
		Blocks: []storage.Block{
			{NodeID: "web-test-vps", Path: "/mnt/ssd", DiskType: "SSD", QuotaBytes: 10 << 30},
		},
	})
	reqTierSave := httptest.NewRequest(http.MethodPost, "/api/tiers", bytes.NewBuffer(tierPayload))
	recTierSave := httptest.NewRecorder()
	handler.ServeHTTP(recTierSave, reqTierSave)

	if recTierSave.Code != http.StatusOK {
		t.Fatalf("expected 200 OK from tier upsert, got %d: %s", recTierSave.Code, recTierSave.Body.String())
	}

	// 6. POST /api/nodes/web-test-vps/processing — reserve 1 core / 500MB / SSD
	procPayload := []byte(`{"processing_enabled":true,"reserved_cpu_cores":1,"reserved_ram_mb":500,"preferred_disk_type":"ssd","reserved_storage_gb":5}`)
	reqProc := httptest.NewRequest(http.MethodPost, "/api/nodes/web-test-vps/processing", bytes.NewBuffer(procPayload))
	recProc := httptest.NewRecorder()
	handler.ServeHTTP(recProc, reqProc)

	if recProc.Code != http.StatusOK {
		t.Fatalf("expected 200 OK from processing profile save, got %d: %s", recProc.Code, recProc.Body.String())
	}

	var savedCfg db.NodeConfig
	if err := json.NewDecoder(recProc.Body).Decode(&savedCfg); err != nil {
		t.Fatalf("failed to decode processing save response: %v", err)
	}
	if !savedCfg.ProcessingEnabled || savedCfg.ReservedCPUCores != 1 ||
		savedCfg.ReservedRAMBytes != 500*1024*1024 ||
		savedCfg.PreferredDiskType != "SSD" || savedCfg.ReservedStorageBytes != 5<<30 {
		t.Fatalf("processing profile not persisted correctly: %+v", savedCfg)
	}

	// 7. GET /api/processing — the node must be eligible for processing
	reqProcList := httptest.NewRequest(http.MethodGet, "/api/processing", nil)
	recProcList := httptest.NewRecorder()
	handler.ServeHTTP(recProcList, reqProcList)

	if recProcList.Code != http.StatusOK {
		t.Fatalf("expected 200 OK from /api/processing, got %d", recProcList.Code)
	}
	var procResp struct {
		Nodes []cluster.Eligibility `json:"nodes"`
	}
	if err := json.NewDecoder(recProcList.Body).Decode(&procResp); err != nil {
		t.Fatalf("failed to decode processing response: %v", err)
	}
	found := false
	for _, e := range procResp.Nodes {
		if e.NodeID == "web-test-vps" {
			found = true
			if !e.Eligible {
				t.Fatalf("expected web-test-vps eligible, reasons: %v", e.Reasons)
			}
		}
	}
	if !found {
		t.Fatal("web-test-vps missing from /api/processing response")
	}

	// 8. Quota merge: allocation save must not wipe the processing profile
	reqAlloc2 := httptest.NewRequest(http.MethodPost, "/api/nodes/web-test-vps/allocate", bytes.NewBuffer([]byte(`{"allocated_max_bytes": 1000000}`)))
	recAlloc2 := httptest.NewRecorder()
	handler.ServeHTTP(recAlloc2, reqAlloc2)

	if recAlloc2.Code != http.StatusOK {
		t.Fatalf("expected 200 OK from second allocate, got %d", recAlloc2.Code)
	}
	var mergedCfg db.NodeConfig
	_ = json.NewDecoder(recAlloc2.Body).Decode(&mergedCfg)
	if !mergedCfg.ProcessingEnabled || mergedCfg.ReservedCPUCores != 1 {
		t.Fatalf("allocate must preserve processing profile, got %+v", mergedCfg)
	}

	// 9. GET /api/tiers — verify assigned block is excluded from unassigned_blocks
	reqTiers2 := httptest.NewRequest(http.MethodGet, "/api/tiers", nil)
	recTiers2 := httptest.NewRecorder()
	handler.ServeHTTP(recTiers2, reqTiers2)

	var tiersResp2 struct {
		Tiers            []storage.TierStatus `json:"tiers"`
		UnassignedBlocks []storage.Block      `json:"unassigned_blocks"`
	}
	_ = json.NewDecoder(recTiers2.Body).Decode(&tiersResp2)
	for _, ub := range tiersResp2.UnassignedBlocks {
		if ub.NodeID == "web-test-vps" && ub.Path == "/mnt/ssd" {
			t.Fatalf("assigned block /mnt/ssd must not appear in unassigned_blocks: %+v", ub)
		}
	}

	// 10. POST /api/tiers — moving block to tier 2 enforces exclusivity (removed from tier 1)
	tier2Payload, _ := json.Marshal(storage.Tier{
		ID:   2,
		Name: "Tier 2 · Warm",
		Blocks: []storage.Block{
			{NodeID: "web-test-vps", Path: "/mnt/ssd", DiskType: "SSD"},
		},
	})
	reqTier2Save := httptest.NewRequest(http.MethodPost, "/api/tiers", bytes.NewBuffer(tier2Payload))
	recTier2Save := httptest.NewRecorder()
	handler.ServeHTTP(recTier2Save, reqTier2Save)
	if recTier2Save.Code != http.StatusOK {
		t.Fatalf("expected 200 OK moving block to tier 2: %d", recTier2Save.Code)
	}

	reqTiers3 := httptest.NewRequest(http.MethodGet, "/api/tiers", nil)
	recTiers3 := httptest.NewRecorder()
	handler.ServeHTTP(recTiers3, reqTiers3)
	var tiersResp3 struct {
		Tiers []storage.TierStatus `json:"tiers"`
	}
	_ = json.NewDecoder(recTiers3.Body).Decode(&tiersResp3)
	for _, ts := range tiersResp3.Tiers {
		if ts.ID == 1 && len(ts.Blocks) != 0 {
			t.Fatalf("tier 1 must have 0 blocks after move, got %d", len(ts.Blocks))
		}
		if ts.ID == 2 && len(ts.Blocks) != 1 {
			t.Fatalf("tier 2 must have 1 block after move, got %d", len(ts.Blocks))
		}
	}
}
