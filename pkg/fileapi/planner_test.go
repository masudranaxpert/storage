package fileapi

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync"
	"testing"
	"time"

	"stream/pkg/cluster"
	"stream/pkg/storage"
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

func TestPickProcessingWorkerWithLimits(t *testing.T) {
	n1 := node("vps-01", cluster.StatusOnline, 100, true, true)
	n2 := node("vps-02", cluster.StatusOnline, 100, true, true)

	profiles := map[string]cluster.ProcessingProfile{
		"vps-01": {NodeID: "vps-01", Enabled: true, MaxConcurrentIngests: 2},
		"vps-02": {NodeID: "vps-02", Enabled: true, MaxConcurrentIngests: 1},
	}

	// Case 1: vps-01 at capacity (2 active jobs), vps-02 has slot (0 active jobs)
	active := map[string]int{"vps-01": 2, "vps-02": 0}
	got, err := PickProcessingWorkerWithLimits([]*cluster.NodeRecord{n1, n2}, "vps-01", 10<<30, profiles, active)
	if err != nil || got.Metrics.NodeID != "vps-02" {
		t.Fatalf("expected vps-02 (vps-01 is full), got %v (err: %v)", got, err)
	}

	// Case 2: All workers at capacity
	active["vps-02"] = 1
	got, err = PickProcessingWorkerWithLimits([]*cluster.NodeRecord{n1, n2}, "vps-01", 10<<30, profiles, active)
	if err == nil || got != nil {
		t.Fatalf("expected error when all workers are at capacity, got %v", got)
	}
}

func TestPickBlockInTier(t *testing.T) {
	block := storage.BlockStatus{
		Block: storage.Block{
			NodeID: "vps-01",
			Path:   "/data",
		},
		Online:      true,
		UsableBytes: 100 << 30, // 100 GB
		UsedBytes:   10 << 30,  // 10 GB (90 GB free)
	}
	tier := &storage.TierStatus{
		ID:     0,
		Name:   "Tier 0 · Hot (NVMe)",
		Blocks: []storage.BlockStatus{block},
	}

	// 1. Fits within headroom (50 GB < 90 GB free)
	if bs := PickBlockInTier(tier, 50<<30, map[string]bool{"vps-01": true}); bs == nil {
		t.Fatal("expected block to be picked, got nil")
	}

	// 2. Exceeds headroom (95 GB > 90 GB free)
	if bs := PickBlockInTier(tier, 95<<30, map[string]bool{"vps-01": true}); bs != nil {
		t.Fatal("expected nil when request exceeds headroom")
	}

	// 3. Receiver not allowed
	if bs := PickBlockInTier(tier, 50<<30, map[string]bool{"vps-other": true}); bs != nil {
		t.Fatal("expected nil when receiver node not allowed")
	}
}

func TestCleanPlacementPath(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"/", ""},
		{".", ""},
		{"", ""},
		{"/mnt/hdd", "/mnt/hdd"},
		{"/data", "/data"},
	}
	for _, tc := range cases {
		if got := CleanPlacementPath(tc.input); got != tc.want {
			t.Errorf("CleanPlacementPath(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

type memJobStore struct {
	mu   sync.Mutex
	recs map[string]*FileRecord
	jobs map[string]*FileJob
}

func newMemJobStore() *memJobStore {
	return &memJobStore{
		recs: make(map[string]*FileRecord),
		jobs: make(map[string]*FileJob),
	}
}

func (m *memJobStore) SaveFileRecord(rec *FileRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := *rec
	m.recs[rec.Key] = &cp
	return nil
}

func (m *memJobStore) GetFileRecord(key string) (*FileRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.recs[key]
	if !ok {
		return nil, fmt.Errorf("not found")
	}
	cp := *r
	return &cp, nil
}

func (m *memJobStore) FileRecordExists(key string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.recs[key]
	return ok, nil
}

func (m *memJobStore) DeleteFileRecord(key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.recs, key)
	return nil
}

func (m *memJobStore) ListFileRecords() ([]*FileRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*FileRecord
	for _, r := range m.recs {
		cp := *r
		out = append(out, &cp)
	}
	return out, nil
}

func (m *memJobStore) SaveFileJob(job *FileJob) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := *job
	m.jobs[job.Key] = &cp
	return nil
}

func (m *memJobStore) GetFileJob(key string) (*FileJob, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[key]
	if !ok {
		return nil, fmt.Errorf("not found")
	}
	cp := *j
	return &cp, nil
}

func (m *memJobStore) DeleteFileJob(key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.jobs, key)
	return nil
}

func (m *memJobStore) ListFileJobs() ([]*FileJob, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*FileJob
	for _, j := range m.jobs {
		cp := *j
		out = append(out, &cp)
	}
	return out, nil
}

func TestProgressAuth(t *testing.T) {
	store := newMemJobStore()
	_ = store.SaveFileJob(&FileJob{Key: "k1", State: StateDownloading})
	coord := cluster.NewCoordinator(10*time.Second, 20*time.Second, nil)
	svc := NewService(store, nil, coord, nil, nil)
	svc.SetSecret("super-secret")

	mux := http.NewServeMux()
	svc.Register(mux)

	payload := []byte(`{"state":"processing","progress_percent":50}`)

	// 1. Without secret -> 401
	req := httptest.NewRequest(http.MethodPost, "/api/v1/files/k1/progress", bytes.NewReader(payload))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without secret, got %d", rec.Code)
	}

	// 2. Wrong secret -> 401
	req2 := httptest.NewRequest(http.MethodPost, "/api/v1/files/k1/progress", bytes.NewReader(payload))
	req2.Header.Set("X-Cluster-Secret", "wrong")
	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 with wrong secret, got %d", rec2.Code)
	}

	// 3. Valid X-Cluster-Secret -> 202
	req3 := httptest.NewRequest(http.MethodPost, "/api/v1/files/k1/progress", bytes.NewReader(payload))
	req3.Header.Set("X-Cluster-Secret", "super-secret")
	rec3 := httptest.NewRecorder()
	mux.ServeHTTP(rec3, req3)
	if rec3.Code != http.StatusAccepted {
		t.Fatalf("expected 202 with X-Cluster-Secret, got %d", rec3.Code)
	}

	// 4. Valid Bearer token -> 202
	req4 := httptest.NewRequest(http.MethodPost, "/api/v1/files/k1/progress", bytes.NewReader(payload))
	req4.Header.Set("Authorization", "Bearer super-secret")
	rec4 := httptest.NewRecorder()
	mux.ServeHTTP(rec4, req4)
	if rec4.Code != http.StatusAccepted {
		t.Fatalf("expected 202 with Bearer token, got %d", rec4.Code)
	}

	// 5. Dev mode (empty secret) -> 202 without headers
	svc.SetSecret("")
	req5 := httptest.NewRequest(http.MethodPost, "/api/v1/files/k1/progress", bytes.NewReader(payload))
	rec5 := httptest.NewRecorder()
	mux.ServeHTTP(rec5, req5)
	if rec5.Code != http.StatusAccepted {
		t.Fatalf("expected 202 when secret is empty, got %d", rec5.Code)
	}
}

func TestReconcileGhostRecordsThreeStrikes(t *testing.T) {
	var mu sync.Mutex
	calls := make(map[string]int)

	agentSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		calls[r.URL.Path]++
		n := calls[r.URL.Path]
		// transient1 returns 200 on the 3rd probe, resetting its strike count.
		if r.URL.Path == "/media/transient1/metadata.json" && n == 3 {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer agentSrv.Close()

	host, portStr, err := net.SplitHostPort(agentSrv.Listener.Addr().String())
	if err != nil {
		t.Fatalf("split host port: %v", err)
	}
	port, _ := strconv.Atoi(portStr)

	store := newMemJobStore()
	for _, k := range []string{"ghost1", "transient1"} {
		_ = store.SaveFileRecord(&FileRecord{Key: k, Filename: k + ".mp4"})
		_ = store.SaveFileJob(&FileJob{
			Key:       k,
			State:     StateCompleted,
			Placement: Placement{NodeID: "vps-01", Path: "/data"},
		})
	}

	coord := cluster.NewCoordinator(10*time.Second, 20*time.Second, nil)
	coord.RegisterHeartbeat(telemetry.NodeMetrics{
		NodeID: "vps-01",
		IPs:    []string{host},
		Capabilities: telemetry.NodeCapabilities{
			AgentPort: port,
		},
	})

	svc := NewService(store, nil, coord, nil, nil)

	// Strike 1: neither purged.
	if purged, err := svc.ReconcileGhostRecords(); err != nil || purged != 0 {
		t.Fatalf("strike 1: got purged=%d err=%v, want 0", purged, err)
	}
	// Strike 2: neither purged.
	if purged, err := svc.ReconcileGhostRecords(); err != nil || purged != 0 {
		t.Fatalf("strike 2: got purged=%d err=%v, want 0", purged, err)
	}
	// Strike 3: ghost1 hits 3rd 404 and is purged; transient1 gets 200 OK and resets.
	if purged, err := svc.ReconcileGhostRecords(); err != nil || purged != 1 {
		t.Fatalf("strike 3: got purged=%d err=%v, want 1", purged, err)
	}
	if exists, _ := store.FileRecordExists("ghost1"); exists {
		t.Fatal("ghost1 should have been deleted on 3rd consecutive 404")
	}
	if exists, _ := store.FileRecordExists("transient1"); !exists {
		t.Fatal("transient1 should survive because 3rd probe returned 200")
	}

	// Next two 404s for transient1 should only reach 2 strikes, not 3.
	for i := 1; i <= 2; i++ {
		if purged, err := svc.ReconcileGhostRecords(); err != nil || purged != 0 {
			t.Fatalf("post-reset strike %d: got purged=%d err=%v, want 0", i, purged, err)
		}
	}
	if exists, _ := store.FileRecordExists("transient1"); !exists {
		t.Fatal("transient1 should still exist after only 2 consecutive 404s")
	}

	// Force reconcile: transient1 should be purged immediately on strike 1.
	if purged, err := svc.ReconcileGhostRecordsOpt(true); err != nil || purged != 1 {
		t.Fatalf("force reconcile: got purged=%d err=%v, want 1", purged, err)
	}
	if exists, _ := store.FileRecordExists("transient1"); exists {
		t.Fatal("transient1 should have been purged immediately under force reconcile")
	}
}

func TestStreamURLOfflineAndRunContext(t *testing.T) {
	store := newMemJobStore()
	coord := cluster.NewCoordinator(1*time.Millisecond, 2*time.Millisecond, nil)
	rec := coord.RegisterHeartbeat(telemetry.NodeMetrics{
		NodeID: "vps-01",
		IPs:    []string{"203.0.113.10"},
		Capabilities: telemetry.NodeCapabilities{
			AgentPort: 2052,
		},
	})
	svc := NewService(store, nil, coord, nil, nil)

	job := &FileJob{
		Key:       "vid1",
		State:     StateCompleted,
		Placement: Placement{NodeID: "vps-01", Path: "/data"},
	}
	if got := svc.StreamURL(job); got != "http://203.0.113.10:2052/stream/vid1" {
		t.Fatalf("online StreamURL = %q", got)
	}

	rec.Status = cluster.StatusOffline
	if got := svc.StreamURL(job); got != "" {
		t.Fatalf("offline StreamURL without PublicHost must be empty, got %q", got)
	}

	job.Placement.PublicHost = "cdn.example.com"
	if got := svc.StreamURL(job); got != "https://cdn.example.com/stream/vid1" {
		t.Fatalf("offline StreamURL with PublicHost = %q", got)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		svc.Run(ctx)
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Service.Run did not stop after context cancel")
	}
}

func TestPlanPlacementCustomIngestOrder(t *testing.T) {
	coord := cluster.NewCoordinator(10*time.Second, 20*time.Second, nil)
	coord.RegisterHeartbeat(telemetry.NodeMetrics{
		NodeID: "vps-nvme",
		IPs:    []string{"10.0.0.1"},
		Disks:  []telemetry.DiskStat{{Path: "/nvme", DiskType: "NVME", FreeBytes: 50 << 30, TotalBytes: 100 << 30}},
	})
	coord.RegisterHeartbeat(telemetry.NodeMetrics{
		NodeID: "vps-ssd",
		IPs:    []string{"10.0.0.2"},
		Disks:  []telemetry.DiskStat{{Path: "/ssd", DiskType: "SSD", FreeBytes: 200 << 30, TotalBytes: 400 << 30}},
	})
	coord.RegisterHeartbeat(telemetry.NodeMetrics{
		NodeID: "vps-hdd",
		IPs:    []string{"10.0.0.3"},
		Disks:  []telemetry.DiskStat{{Path: "/hdd", DiskType: "HDD", FreeBytes: 500 << 30, TotalBytes: 1000 << 30}},
	})

	tm := storage.NewManager(nil)
	_ = tm.Upsert(storage.Tier{
		ID:     0,
		Name:   "Tier 0",
		System: true,
		Blocks: []storage.Block{{NodeID: "vps-nvme", Path: "/nvme", DiskType: "NVME"}},
	})
	_ = tm.Upsert(storage.Tier{
		ID:     1,
		Name:   "Tier 1",
		System: true,
		Blocks: []storage.Block{{NodeID: "vps-ssd", Path: "/ssd", DiskType: "SSD"}},
	})
	_ = tm.Upsert(storage.Tier{
		ID:      2,
		Name:    "Tier 2",
		System:  true,
		Default: true,
		Blocks:  []storage.Block{{NodeID: "vps-hdd", Path: "/hdd", DiskType: "HDD"}},
	})

	svc := NewService(newMemJobStore(), tm, coord, nil, func() map[string]uint64 { return nil })
	receivers := map[string]bool{"vps-nvme": true, "vps-ssd": true, "vps-hdd": true}

	// Default (nil ingestOrder) places on Tier 2 first.
	p0, err := svc.PlanPlacement(10<<30, receivers)
	if err != nil || p0.TierID != 2 {
		t.Fatalf("default order: got tier %d err=%v, want tier 2", p0.TierID, err)
	}

	// Custom ingest order [0, 1, 2]: small file (20 GB) lands on Tier 0 (50 GB free).
	svc.SetIngestOrder(func() []int { return []int{0, 1, 2} })
	p1, err := svc.PlanPlacement(20<<30, receivers)
	if err != nil || p1.TierID != 0 || p1.NodeID != "vps-nvme" {
		t.Fatalf("custom order [0,1,2] 20GB: got %+v err=%v, want tier 0 on vps-nvme", p1, err)
	}

	// Medium file (100 GB) exceeds Tier 0 (50 GB) and falls through to Tier 1 (200 GB).
	p2, err := svc.PlanPlacement(100<<30, receivers)
	if err != nil || p2.TierID != 1 || p2.NodeID != "vps-ssd" {
		t.Fatalf("custom order [0,1,2] 100GB fallthrough: got %+v err=%v, want tier 1 on vps-ssd", p2, err)
	}

	// Large file (300 GB) exceeds Tier 0 and Tier 1, falling through to Tier 2 (500 GB).
	p3, err := svc.PlanPlacement(300<<30, receivers)
	if err != nil || p3.TierID != 2 || p3.NodeID != "vps-hdd" {
		t.Fatalf("custom order [0,1,2] 300GB fallthrough: got %+v err=%v, want tier 2 on vps-hdd", p3, err)
	}

	// Huge file (900 GB) exceeds all tiers -> error.
	if _, err := svc.PlanPlacement(900<<30, receivers); err == nil {
		t.Fatal("expected error when all ingest tiers are full")
	}
}

func TestBatchDelete(t *testing.T) {
	store := newMemJobStore()
	store.SaveFileRecord(&FileRecord{Key: "k1", Filename: "v1.mp4"})
	store.SaveFileJob(&FileJob{Key: "k1", State: StateAwaitingUpload})
	store.SaveFileRecord(&FileRecord{Key: "k2", Filename: "v2.mp4"})
	store.SaveFileJob(&FileJob{Key: "k2", State: StateAwaitingUpload})
	store.SaveFileRecord(&FileRecord{Key: "k3", Filename: "v3.mp4"})
	store.SaveFileJob(&FileJob{Key: "k3", State: StateAwaitingUpload})

	coord := cluster.NewCoordinator(10*time.Second, 20*time.Second, nil)
	svc := NewService(store, nil, coord, nil, nil)

	mux := http.NewServeMux()
	svc.Register(mux)

	// 1. Comma-separated path delete: DELETE /api/v1/files/k1,k2
	req1 := httptest.NewRequest(http.MethodDelete, "/api/v1/files/k1,k2", nil)
	rec1 := httptest.NewRecorder()
	mux.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusOK {
		t.Fatalf("expected 200 on comma-separated delete, got %d: %s", rec1.Code, rec1.Body.String())
	}
	if exists, _ := store.FileRecordExists("k1"); exists {
		t.Errorf("expected k1 to be deleted")
	}
	if exists, _ := store.FileRecordExists("k2"); exists {
		t.Errorf("expected k2 to be deleted")
	}
	if exists, _ := store.FileRecordExists("k3"); !exists {
		t.Errorf("expected k3 to still exist")
	}

	// 2. Batch delete with JSON body: DELETE /api/v1/files {"keys": ["k3"]}
	var deletedViaHook []string
	svc.SetDeleteHandler(func(k string) {
		deletedViaHook = append(deletedViaHook, k)
	})

	req2 := httptest.NewRequest(http.MethodDelete, "/api/v1/files", bytes.NewBufferString(`{"keys":["k3"]}`))
	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("expected 200 on batch delete body, got %d: %s", rec2.Code, rec2.Body.String())
	}
	if exists, _ := store.FileRecordExists("k3"); exists {
		t.Errorf("expected k3 to be deleted")
	}
	if len(deletedViaHook) != 1 || deletedViaHook[0] != "k3" {
		t.Errorf("expected hook to receive ['k3'], got %v", deletedViaHook)
	}
}

func TestDelete_BucketAndWorkerPhysicalDeletes(t *testing.T) {
	store := newMemJobStore()
	store.SaveFileRecord(&FileRecord{Key: "k-bkt", Filename: "bkt-vid.mp4"})
	store.SaveFileJob(&FileJob{
		Key:   "k-bkt",
		State: StateCompleted,
		Placement: Placement{
			NodeID:    "bucket:test-bkt-01",
			Path:      "gc-bucket",
			TierID:    0,
			TierLabel: "tier0",
		},
	})

	coord := cluster.NewCoordinator(10*time.Second, 20*time.Second, nil)
	svc := NewService(store, nil, coord, nil, nil)

	bucketResolved := false
	svc.SetBucketResolver(func(id string) (*storage.S3Config, error) {
		if id == "test-bkt-01" {
			bucketResolved = true
			return &storage.S3Config{
				Name:      "gc-bucket",
				Endpoint:  "https://test.r2.cloudflarestorage.com",
				AccessKey: "test-acc",
				SecretKey: "test-sec",
			}, nil
		}
		return nil, fmt.Errorf("unknown bucket")
	})

	// 1. Delete bucket-placed file
	if err := svc.Delete("k-bkt"); err != nil {
		t.Fatalf("Delete on bucket-placed file failed: %v", err)
	}
	if !bucketResolved {
		t.Errorf("expected bucketResolver to be invoked for cloud deletion")
	}
	if exists, _ := store.FileRecordExists("k-bkt"); exists {
		t.Errorf("expected k-bkt to be deleted from store")
	}

	// 2. Delete worker-placed file with live agent
	var receivedKey, receivedDir string
	mockAgent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/ingest-delete" && r.Method == http.MethodPost {
			receivedKey = r.URL.Query().Get("key")
			receivedDir = r.URL.Query().Get("dir")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"status":"deleted"}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer mockAgent.Close()

	u, _ := url.Parse(mockAgent.URL)
	port, _ := strconv.Atoi(u.Port())
	coord.RegisterHeartbeat(telemetry.NodeMetrics{
		NodeID: "vps-test-01",
		IPs:    []string{u.Hostname()},
		Capabilities: telemetry.NodeCapabilities{
			AgentPort: port,
		},
	})

	store.SaveFileRecord(&FileRecord{Key: "k-vps", Filename: "vps-vid.mp4"})
	store.SaveFileJob(&FileJob{
		Key:   "k-vps",
		State: StateCompleted,
		Placement: Placement{
			NodeID:    "vps-test-01",
			Path:      "/mnt/storage",
			TierID:    1,
			TierLabel: "tier1",
		},
	})

	if err := svc.Delete("k-vps"); err != nil {
		t.Fatalf("Delete on worker-placed file failed: %v", err)
	}
	if receivedKey != "k-vps" {
		t.Errorf("expected agent to receive key 'k-vps', got '%s'", receivedKey)
	}
	if receivedDir != "/mnt/storage" {
		t.Errorf("expected agent to receive dir '/mnt/storage', got '%s'", receivedDir)
	}
	if exists, _ := store.FileRecordExists("k-vps"); exists {
		t.Errorf("expected k-vps to be deleted from store")
	}
}

