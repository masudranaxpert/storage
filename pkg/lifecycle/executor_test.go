package lifecycle

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"stream/pkg/cluster"
	"stream/pkg/fileapi"
	"stream/pkg/storage"
	"stream/pkg/telemetry"
)

type fullMemStore struct {
	mu     sync.Mutex
	policy *FlowPolicy
	states map[string]*FileState
	tasks  map[string]*Task
	active map[string]string
	audits []*AuditEntry
	jobs   map[string]*fileapi.FileJob
	recs   map[string]*fileapi.FileRecord
}

func newFullMemStore() *fullMemStore {
	return &fullMemStore{
		states: make(map[string]*FileState),
		tasks:  make(map[string]*Task),
		active: make(map[string]string),
		jobs:   make(map[string]*fileapi.FileJob),
		recs:   make(map[string]*fileapi.FileRecord),
	}
}

func (m *fullMemStore) GetFlowPolicy() (*FlowPolicy, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.policy == nil {
		return nil, nil
	}
	cp := clonePolicy(*m.policy)
	return &cp, nil
}

func (m *fullMemStore) SaveFlowPolicy(p *FlowPolicy) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := clonePolicy(*p)
	m.policy = &cp
	return nil
}

func (m *fullMemStore) GetLifecycleState(key string) (*FileState, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	st := m.states[key]
	if st == nil {
		return nil, nil
	}
	cp := *st
	cp.History = append([]Move(nil), st.History...)
	return &cp, nil
}

func (m *fullMemStore) SaveLifecycleState(st *FileState) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := *st
	cp.History = append([]Move(nil), st.History...)
	m.states[st.Key] = &cp
	return nil
}

func (m *fullMemStore) DeleteLifecycleState(key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.states, key)
	return nil
}

func (m *fullMemStore) ListLifecycleStates() ([]*FileState, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*FileState, 0, len(m.states))
	for _, st := range m.states {
		cp := *st
		cp.History = append([]Move(nil), st.History...)
		out = append(out, &cp)
	}
	return out, nil
}

func (m *fullMemStore) GetLifecycleTask(id string) (*Task, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t := m.tasks[id]
	if t == nil {
		return nil, nil
	}
	cp := *t
	return &cp, nil
}

func (m *fullMemStore) SaveLifecycleTask(t *Task) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := *t
	m.tasks[t.ID] = &cp
	return nil
}

func (m *fullMemStore) ListLifecycleTasks() ([]*Task, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*Task, 0, len(m.tasks))
	for _, t := range m.tasks {
		cp := *t
		out = append(out, &cp)
	}
	return out, nil
}

func (m *fullMemStore) GetActiveLifecycleTask(key string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.active[key], nil
}

func (m *fullMemStore) SetActiveLifecycleTask(key, taskID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.active[key] = taskID
	return nil
}

func (m *fullMemStore) DeleteActiveLifecycleTask(key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.active, key)
	return nil
}

func (m *fullMemStore) ListActiveLifecycleTasks() (map[string]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string]string, len(m.active))
	for k, v := range m.active {
		out[k] = v
	}
	return out, nil
}

func (m *fullMemStore) SaveLifecycleAudit(a *AuditEntry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := *a
	m.audits = append(m.audits, &cp)
	return nil
}

func (m *fullMemStore) ListLifecycleAudits(limit int) ([]*AuditEntry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*AuditEntry, 0, len(m.audits))
	for _, a := range m.audits {
		cp := *a
		out = append(out, &cp)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (m *fullMemStore) SaveFileRecord(rec *fileapi.FileRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := *rec
	m.recs[rec.Key] = &cp
	return nil
}

func (m *fullMemStore) GetFileRecord(key string) (*fileapi.FileRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r := m.recs[key]
	if r == nil {
		return nil, fmt.Errorf("not found")
	}
	cp := *r
	return &cp, nil
}

func (m *fullMemStore) FileRecordExists(key string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.recs[key]
	return ok, nil
}

func (m *fullMemStore) DeleteFileRecord(key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.recs, key)
	return nil
}

func (m *fullMemStore) ListFileRecords() ([]*fileapi.FileRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*fileapi.FileRecord, 0, len(m.recs))
	for _, r := range m.recs {
		cp := *r
		out = append(out, &cp)
	}
	return out, nil
}

func (m *fullMemStore) SaveFileJob(job *fileapi.FileJob) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := *job
	m.jobs[job.Key] = &cp
	return nil
}

func (m *fullMemStore) GetFileJob(key string) (*fileapi.FileJob, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j := m.jobs[key]
	if j == nil {
		return nil, fmt.Errorf("not found")
	}
	cp := *j
	return &cp, nil
}

func (m *fullMemStore) DeleteFileJob(key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.jobs, key)
	return nil
}

func (m *fullMemStore) ListFileJobs() ([]*fileapi.FileJob, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*fileapi.FileJob, 0, len(m.jobs))
	for _, j := range m.jobs {
		cp := *j
		out = append(out, &cp)
	}
	return out, nil
}

// fakeAgentNode wraps an httptest.Server simulating a worker agent's relocation, media-size, delete, and /media endpoints.
type fakeAgentNode struct {
	nodeID          string
	rootDir         string
	srv             *httptest.Server
	host            string
	port            int
	peers           map[string]*fakeAgentNode
	sizeDelta       int64
	copyBlockCh     chan struct{}
	copyStartedCh   chan struct{}
	onCopyHook      func()
	mu              sync.Mutex
	cancelCalls     int
	deleteCalls     int
	deleteErr       bool
	relocateCalls   int
	activeRelocates map[string]context.CancelFunc
}

func newFakeAgentNode(t *testing.T, nodeID string, peers map[string]*fakeAgentNode) *fakeAgentNode {
	t.Helper()
	rootDir := t.TempDir()
	fa := &fakeAgentNode{
		nodeID:          nodeID,
		rootDir:         rootDir,
		peers:           peers,
		activeRelocates: make(map[string]context.CancelFunc),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/relocate", fa.handleRelocate)
	mux.HandleFunc("/api/v1/relocate/cancel", fa.handleRelocateCancel)
	mux.HandleFunc("/api/v1/media-size", fa.handleMediaSize)
	mux.HandleFunc("/api/v1/ingest-delete", fa.handleIngestDelete)
	mux.HandleFunc("/media/", fa.handleMedia)

	fa.srv = httptest.NewServer(mux)
	t.Cleanup(fa.srv.Close)

	u, err := url.Parse(fa.srv.URL)
	if err != nil {
		t.Fatalf("parse server url: %v", err)
	}
	h, pStr, err := net.SplitHostPort(u.Host)
	if err != nil {
		t.Fatalf("split host port: %v", err)
	}
	p, _ := strconv.Atoi(pStr)
	fa.host = h
	fa.port = p
	peers[nodeID] = fa
	return fa
}

func (fa *fakeAgentNode) blockDir(sub string) string {
	return filepath.Join(fa.rootDir, strings.TrimPrefix(sub, "/"))
}

func (fa *fakeAgentNode) resolveDir(dir string) string {
	clean := filepath.Clean(dir)
	if strings.HasSuffix(filepath.ToSlash(clean), "/stream/media") {
		return clean
	}
	return filepath.Join(clean, "stream", "media")
}

func (fa *fakeAgentNode) seedMedia(t *testing.T, blockPath, key string, videoBytes []byte) {
	t.Helper()
	folder := filepath.Join(fa.resolveDir(blockPath), key)
	if err := os.MkdirAll(folder, 0755); err != nil {
		t.Fatalf("mkdir seed media: %v", err)
	}
	if err := os.WriteFile(filepath.Join(folder, "video.mp4"), videoBytes, 0644); err != nil {
		t.Fatalf("write video.mp4: %v", err)
	}
	if err := os.WriteFile(filepath.Join(folder, "metadata.json"), []byte(`{"status":"ready"}`), 0644); err != nil {
		t.Fatalf("write metadata.json: %v", err)
	}
}

func (fa *fakeAgentNode) hasMedia(blockPath, key string) bool {
	folder := filepath.Join(fa.resolveDir(blockPath), key)
	_, err1 := os.Stat(filepath.Join(folder, "video.mp4"))
	_, err2 := os.Stat(filepath.Join(folder, "metadata.json"))
	return err1 == nil && err2 == nil
}

func (fa *fakeAgentNode) hasAnyDir(blockPath, key string) bool {
	folder := filepath.Join(fa.resolveDir(blockPath), key)
	_, err := os.Stat(folder)
	return err == nil
}

func (fa *fakeAgentNode) handleRelocate(w http.ResponseWriter, r *http.Request) {
	var req cluster.RelocateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	var srcFolder string
	if req.SrcS3 == nil {
		srcFolder = filepath.Join(fa.resolveDir(req.SrcDir), req.Key)
		if st, err := os.Stat(srcFolder); err != nil || !st.IsDir() {
			http.Error(w, "source not found", http.StatusNotFound)
			return
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	fa.mu.Lock()
	fa.relocateCalls++
	fa.activeRelocates[req.TaskID] = cancel
	fa.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"status":  "accepted",
		"task_id": req.TaskID,
		"async":   req.ProgressURL != "",
	})

	runCopy := func() {
		defer func() {
			fa.mu.Lock()
			delete(fa.activeRelocates, req.TaskID)
			fa.mu.Unlock()
		}()

		if req.S3 != nil {
			if err := storage.UploadFolderS3(ctx, *req.S3, req.Key, srcFolder, nil); err != nil {
				fa.postProgress(req.ProgressURL, ProgressPayload{State: "failed", Error: err.Error()})
				return
			}
			fa.postProgress(req.ProgressURL, ProgressPayload{
				State:           "copied",
				ProgressPercent: 100,
				Speed:           "Ready",
			})
			return
		}
		if req.SrcS3 != nil {
			dstFolder := filepath.Join(fa.resolveDir(req.FinalDir), req.Key)
			if err := storage.DownloadFolderS3(ctx, *req.SrcS3, req.Key, dstFolder, nil); err != nil {
				fa.postProgress(req.ProgressURL, ProgressPayload{State: "failed", Error: err.Error()})
				return
			}
			fa.postProgress(req.ProgressURL, ProgressPayload{
				State:           "copied",
				ProgressPercent: 100,
				Speed:           "Ready",
			})
			return
		}

		dstPeer := fa.peers[req.FinalNodeID]
		if dstPeer == nil {
			fa.postProgress(req.ProgressURL, ProgressPayload{State: "failed", Error: "unknown destination peer"})
			return
		}
		dstFolder := filepath.Join(dstPeer.resolveDir(req.FinalDir), req.Key)
		_ = os.MkdirAll(dstFolder, 0755)
		// Write partial file immediately so cancel tests can observe and clean it up.
		_ = os.WriteFile(filepath.Join(dstFolder, "partial.tmp"), []byte("in-flight"), 0644)

		if fa.copyStartedCh != nil {
			select {
			case fa.copyStartedCh <- struct{}{}:
			default:
			}
		}
		if fa.onCopyHook != nil {
			fa.onCopyHook()
		}
		if fa.copyBlockCh != nil {
			select {
			case <-fa.copyBlockCh:
			case <-ctx.Done():
				return
			}
		}
		if ctx.Err() != nil {
			return
		}

		_ = os.Remove(filepath.Join(dstFolder, "partial.tmp"))
		entries, err := os.ReadDir(srcFolder)
		if err != nil {
			fa.postProgress(req.ProgressURL, ProgressPayload{State: "failed", Error: err.Error()})
			return
		}
		var total int64
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			data, err := os.ReadFile(filepath.Join(srcFolder, e.Name()))
			if err != nil {
				fa.postProgress(req.ProgressURL, ProgressPayload{State: "failed", Error: err.Error()})
				return
			}
			if err := os.WriteFile(filepath.Join(dstFolder, e.Name()), data, 0644); err != nil {
				fa.postProgress(req.ProgressURL, ProgressPayload{State: "failed", Error: err.Error()})
				return
			}
			total += int64(len(data))
		}
		fa.postProgress(req.ProgressURL, ProgressPayload{
			State:            "copied",
			ProgressPercent:  100,
			Speed:            "Ready",
			TransferredBytes: total,
			TotalBytes:       total,
		})
	}

	if req.ProgressURL != "" || fa.copyBlockCh != nil {
		go runCopy()
	} else {
		runCopy()
	}
}

func (fa *fakeAgentNode) postProgress(progressURL string, p ProgressPayload) {
	if progressURL == "" {
		return
	}
	body, _ := json.Marshal(p)
	resp, err := http.Post(progressURL, "application/json", bytes.NewReader(body))
	if err == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}
}

func (fa *fakeAgentNode) handleRelocateCancel(w http.ResponseWriter, r *http.Request) {
	taskID := r.URL.Query().Get("task_id")
	fa.mu.Lock()
	fa.cancelCalls++
	cancel := fa.activeRelocates[taskID]
	delete(fa.activeRelocates, taskID)
	fa.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"cancelled"}`))
}

func (fa *fakeAgentNode) handleMediaSize(w http.ResponseWriter, r *http.Request) {
	key := r.URL.Query().Get("key")
	dir := r.URL.Query().Get("dir")
	folder := filepath.Join(fa.resolveDir(dir), key)
	entries, err := os.ReadDir(folder)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	var total int64
	var files int
	for _, e := range entries {
		if !e.IsDir() && !strings.HasSuffix(e.Name(), ".tmp") {
			info, err := e.Info()
			if err == nil {
				total += info.Size()
				files++
			}
		}
	}
	total += fa.sizeDelta
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(cluster.MediaSizeResponse{Bytes: total, Files: files})
}

func (fa *fakeAgentNode) handleIngestDelete(w http.ResponseWriter, r *http.Request) {
	key := r.URL.Query().Get("key")
	dir := r.URL.Query().Get("dir")
	fa.mu.Lock()
	fa.deleteCalls++
	fail := fa.deleteErr
	fa.mu.Unlock()
	if fail {
		http.Error(w, "simulated agent delete failure", http.StatusInternalServerError)
		return
	}
	_ = os.RemoveAll(filepath.Join(fa.resolveDir(dir), key))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"deleted"}`))
}

func (fa *fakeAgentNode) handleMedia(w http.ResponseWriter, r *http.Request) {
	sub := strings.TrimPrefix(r.URL.Path, "/media/")
	var found string
	_ = filepath.WalkDir(fa.rootDir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if strings.HasSuffix(filepath.ToSlash(path), "/"+sub) {
			found = path
			return io.EOF
		}
		return nil
	})
	if found == "" {
		http.NotFound(w, r)
		return
	}
	http.ServeFile(w, r, found)
}

func registerFakeNode(coord *cluster.Coordinator, fa *fakeAgentNode, blockPaths ...string) {
	var disks []telemetry.DiskStat
	for _, bp := range blockPaths {
		disks = append(disks, telemetry.DiskStat{
			Path:       bp,
			DiskType:   "SSD",
			FreeBytes:  100 << 30,
			TotalBytes: 200 << 30,
		})
	}
	coord.RegisterHeartbeat(telemetry.NodeMetrics{
		NodeID:       fa.nodeID,
		IPs:          []string{fa.host},
		Capabilities: telemetry.NodeCapabilities{AgentPort: fa.port, HasFFmpeg: true, HasAria2c: true},
		Disks:        disks,
	})
}

func waitForTaskTerminal(t *testing.T, store *fullMemStore, taskID string, timeout time.Duration) *Task {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		tk, _ := store.GetLifecycleTask(taskID)
		if tk != nil && tk.State.IsTerminal() {
			return tk
		}
		time.Sleep(10 * time.Millisecond)
	}
	tk, _ := store.GetLifecycleTask(taskID)
	t.Fatalf("timed out waiting for task %s to finish; last state: %+v", taskID, tk)
	return nil
}

func TestExecutorCrossNodeHappyPath(t *testing.T) {
	peers := make(map[string]*fakeAgentNode)
	nodeSrc := newFakeAgentNode(t, "node-hdd", peers)
	nodeDst := newFakeAgentNode(t, "node-nvme", peers)

	srcBlock := nodeSrc.blockDir("hdd")
	dstBlock := nodeDst.blockDir("nvme")

	coord := cluster.NewCoordinator(10*time.Second, 20*time.Second, nil)
	registerFakeNode(coord, nodeSrc, srcBlock)
	registerFakeNode(coord, nodeDst, dstBlock)

	tm := storage.NewManager(nil)
	_ = tm.Upsert(storage.Tier{
		ID:     0,
		Name:   "Tier 0 · Hot (NVMe)",
		System: true,
		Blocks: []storage.Block{{NodeID: "node-nvme", Path: dstBlock, DiskType: "NVMe"}},
	})
	_ = tm.Upsert(storage.Tier{
		ID:      2,
		Name:    "Tier 2 · Cold (HDD)",
		System:  true,
		Default: true,
		Blocks:  []storage.Block{{NodeID: "node-hdd", Path: srcBlock, DiskType: "HDD"}},
	})

	store := newFullMemStore()
	key := "fileCrossNode01"
	payload := bytes.Repeat([]byte("stream-video-segment-"), 256)
	nodeSrc.seedMedia(t, srcBlock, key, payload)

	_ = store.SaveFileRecord(&fileapi.FileRecord{Key: key, SizeBytes: int64(len(payload))})
	_ = store.SaveFileJob(&fileapi.FileJob{
		Key:        key,
		State:      fileapi.StateCompleted,
		TotalBytes: int64(len(payload)),
		Placement: fileapi.Placement{
			TierID:    2,
			TierLabel: "tier2",
			NodeID:    "node-hdd",
			Path:      srcBlock,
		},
	})

	eng := New(
		store,
		tm,
		coord,
		nil,
		WithJobStore(store),
		WithExecutorOptions(
			WithExecutorPollInterval(10*time.Millisecond),
			WithExecutorBackoffs([]time.Duration{10 * time.Millisecond}),
		),
	)

	// Wire a progress callback server to exercise async progress reporting end-to-end.
	progSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		if len(parts) == 5 && parts[4] == "progress" {
			var p ProgressPayload
			_ = json.NewDecoder(r.Body).Decode(&p)
			_ = eng.Executor().ApplyProgress(parts[3], p)
			w.WriteHeader(http.StatusAccepted)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer progSrv.Close()
	eng.Executor().SetCoordinatorURL(progSrv.URL)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	task, err := eng.MoveFile(key, 0)
	if err != nil {
		t.Fatalf("MoveFile failed: %v", err)
	}

	// Second concurrent move for the same key must be rejected with ErrTaskAlreadyActive.
	if _, err := eng.MoveFile(key, 0); !errors.Is(err, ErrTaskAlreadyActive) {
		t.Fatalf("expected ErrTaskAlreadyActive on duplicate move, got %v", err)
	}

	doneTask := waitForTaskTerminal(t, store, task.ID, 3*time.Second)
	if doneTask.State != TaskDone {
		t.Fatalf("expected TaskDone, got %s (err=%s)", doneTask.State, doneTask.Error)
	}

	if !nodeDst.hasMedia(dstBlock, key) {
		t.Fatal("expected destination node to have relocated media files")
	}
	if nodeSrc.hasAnyDir(srcBlock, key) {
		t.Fatal("expected source node copy to be cleaned up after switch")
	}

	job, _ := store.GetFileJob(key)
	if job.Placement.TierID != 0 || job.Placement.NodeID != "node-nvme" || job.Placement.Path != dstBlock {
		t.Fatalf("unexpected FileJob.Placement after move: %+v", job.Placement)
	}

	st, _ := store.GetLifecycleState(key)
	if st == nil || st.TierSince.IsZero() || len(st.History) != 1 {
		t.Fatalf("expected lifecycle state TierSince and History to be updated, got %+v", st)
	}
	if st.History[0].From != 2 || st.History[0].To != 0 || string(st.History[0].Kind) != "manual" {
		t.Fatalf("unexpected move history entry: %+v", st.History[0])
	}
}

func TestExecutorSameNodeHappyPathWithRealAgentRelocate(t *testing.T) {
	baseDir := t.TempDir()
	tier2Dir := filepath.Join(baseDir, "tier2")
	tier0Dir := filepath.Join(baseDir, "tier0")

	agent := cluster.NewAgent("node-same", "http://127.0.0.1:0", 5*time.Second, 0, filepath.Join(baseDir, "default-media"), "127.0.0.1:0", "test-secret")
	mux := http.NewServeMux()
	agent.RegisterRelocateTestRoutes(mux)

	srv := httptest.NewServer(mux)
	defer srv.Close()

	u, _ := url.Parse(srv.URL)
	host, portStr, _ := net.SplitHostPort(u.Host)
	port, _ := strconv.Atoi(portStr)

	coord := cluster.NewCoordinator(10*time.Second, 20*time.Second, nil)
	coord.RegisterHeartbeat(telemetry.NodeMetrics{
		NodeID:       "node-same",
		IPs:          []string{host},
		Capabilities: telemetry.NodeCapabilities{AgentPort: port, HasFFmpeg: true, HasAria2c: true},
		Disks: []telemetry.DiskStat{
			{Path: tier2Dir, DiskType: "HDD", FreeBytes: 100 << 30, TotalBytes: 200 << 30},
			{Path: tier0Dir, DiskType: "NVMe", FreeBytes: 100 << 30, TotalBytes: 200 << 30},
		},
	})

	key := "fileSameNode01"
	srcMediaFolder := filepath.Join(agent.ResolveTargetDir(tier2Dir), key)
	if err := os.MkdirAll(srcMediaFolder, 0755); err != nil {
		t.Fatalf("mkdir src: %v", err)
	}
	if err := os.WriteFile(filepath.Join(srcMediaFolder, "video.mp4"), []byte("same-node-video-bytes"), 0644); err != nil {
		t.Fatalf("write video: %v", err)
	}
	if err := os.WriteFile(filepath.Join(srcMediaFolder, "metadata.json"), []byte(`{"ok":true}`), 0644); err != nil {
		t.Fatalf("write metadata: %v", err)
	}

	store := newFullMemStore()
	_ = store.SaveFileJob(&fileapi.FileJob{
		Key:   key,
		State: fileapi.StateCompleted,
		Placement: fileapi.Placement{
			TierID:    2,
			TierLabel: "tier2",
			NodeID:    "node-same",
			Path:      tier2Dir,
		},
	})

	ex := NewExecutor(
		store,
		store,
		storage.NewManager(nil),
		coord,
		WithExecutorSecret("test-secret"),
		WithExecutorPollInterval(10*time.Millisecond),
		WithExecutorBackoffs([]time.Duration{10 * time.Millisecond}),
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go ex.Run(ctx)

	tk, err := ex.Enqueue(&Task{
		Key:    key,
		Kind:   TaskPromote,
		Reason: "same-node promote",
		From:   fileapi.Placement{TierID: 2, TierLabel: "tier2", NodeID: "node-same", Path: tier2Dir},
		To:     fileapi.Placement{TierID: 0, TierLabel: "tier0", NodeID: "node-same", Path: tier0Dir},
	})
	if err != nil {
		t.Fatalf("Enqueue failed: %v", err)
	}

	done := waitForTaskTerminal(t, store, tk.ID, 3*time.Second)
	if done.State != TaskDone {
		t.Fatalf("expected TaskDone, got %s (err=%s)", done.State, done.Error)
	}

	dstMediaFolder := filepath.Join(agent.ResolveTargetDir(tier0Dir), key)
	if _, err := os.Stat(filepath.Join(dstMediaFolder, "video.mp4")); err != nil {
		t.Fatalf("expected destination video.mp4 to exist: %v", err)
	}
	if _, err := os.Stat(srcMediaFolder); !os.IsNotExist(err) {
		t.Fatalf("expected source folder %s to be deleted after cleanup, stat err=%v", srcMediaFolder, err)
	}
}

func TestExecutorVerifySizeMismatchRetriesThenFailsWithSourceIntact(t *testing.T) {
	peers := make(map[string]*fakeAgentNode)
	nodeSrc := newFakeAgentNode(t, "node-src", peers)
	nodeDst := newFakeAgentNode(t, "node-dst", peers)
	nodeDst.sizeDelta = -5 // Corrupt reported destination byte count during verification

	srcBlock := nodeSrc.blockDir("hdd")
	dstBlock := nodeDst.blockDir("nvme")

	coord := cluster.NewCoordinator(10*time.Second, 20*time.Second, nil)
	registerFakeNode(coord, nodeSrc, srcBlock)
	registerFakeNode(coord, nodeDst, dstBlock)

	store := newFullMemStore()
	key := "fileMismatch01"
	nodeSrc.seedMedia(t, srcBlock, key, []byte("original-intact-video-content"))

	_ = store.SaveFileJob(&fileapi.FileJob{
		Key:   key,
		State: fileapi.StateCompleted,
		Placement: fileapi.Placement{
			TierID:    2,
			TierLabel: "tier2",
			NodeID:    "node-src",
			Path:      srcBlock,
		},
	})

	ex := NewExecutor(
		store,
		store,
		storage.NewManager(nil),
		coord,
		WithExecutorPollInterval(10*time.Millisecond),
		WithExecutorBackoffs([]time.Duration{5 * time.Millisecond, 5 * time.Millisecond, 5 * time.Millisecond}),
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go ex.Run(ctx)

	tk, err := ex.Enqueue(&Task{
		Key:  key,
		Kind: TaskPromote,
		From: fileapi.Placement{TierID: 2, TierLabel: "tier2", NodeID: "node-src", Path: srcBlock},
		To:   fileapi.Placement{TierID: 0, TierLabel: "tier0", NodeID: "node-dst", Path: dstBlock},
	})
	if err != nil {
		t.Fatalf("Enqueue failed: %v", err)
	}

	failed := waitForTaskTerminal(t, store, tk.ID, 3*time.Second)
	if failed.State != TaskFailed {
		t.Fatalf("expected TaskFailed, got %s", failed.State)
	}
	if failed.Attempts != 3 {
		t.Fatalf("expected 3 attempts before failure, got %d", failed.Attempts)
	}
	if !strings.Contains(failed.Error, "verify size mismatch") {
		t.Fatalf("expected verify size mismatch error, got %q", failed.Error)
	}
	if !nodeSrc.hasMedia(srcBlock, key) {
		t.Fatal("INVARIANT VIOLATION: source media was deleted after verify failure")
	}
	if nodeDst.hasAnyDir(dstBlock, key) {
		t.Fatal("expected partial/corrupt destination directory to be cleaned up on failure")
	}
	job, _ := store.GetFileJob(key)
	if job.Placement.NodeID != "node-src" || job.Placement.TierID != 2 {
		t.Fatalf("expected FileJob.Placement to remain on node-src, got %+v", job.Placement)
	}
}

func TestExecutorPlacementChangedDuringCopyFailsWithoutDelete(t *testing.T) {
	peers := make(map[string]*fakeAgentNode)
	nodeSrc := newFakeAgentNode(t, "node-src", peers)
	nodeDst := newFakeAgentNode(t, "node-dst", peers)

	srcBlock := nodeSrc.blockDir("hdd")
	dstBlock := nodeDst.blockDir("nvme")

	coord := cluster.NewCoordinator(10*time.Second, 20*time.Second, nil)
	registerFakeNode(coord, nodeSrc, srcBlock)
	registerFakeNode(coord, nodeDst, dstBlock)

	store := newFullMemStore()
	key := "filePlacementChanged01"
	nodeSrc.seedMedia(t, srcBlock, key, []byte("video-data-stays-safe"))

	_ = store.SaveFileJob(&fileapi.FileJob{
		Key:   key,
		State: fileapi.StateCompleted,
		Placement: fileapi.Placement{
			TierID:    2,
			TierLabel: "tier2",
			NodeID:    "node-src",
			Path:      srcBlock,
		},
	})

	// Mutate FileJob.Placement while the copy is in progress on the source agent.
	nodeSrc.onCopyHook = func() {
		job, _ := store.GetFileJob(key)
		job.Placement.TierID = 1
		job.Placement.Path = "/unexpected/other/path"
		_ = store.SaveFileJob(job)
	}

	ex := NewExecutor(
		store,
		store,
		storage.NewManager(nil),
		coord,
		WithExecutorPollInterval(10*time.Millisecond),
		WithExecutorBackoffs([]time.Duration{5 * time.Millisecond}),
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go ex.Run(ctx)

	tk, err := ex.Enqueue(&Task{
		Key:  key,
		Kind: TaskPromote,
		From: fileapi.Placement{TierID: 2, TierLabel: "tier2", NodeID: "node-src", Path: srcBlock},
		To:   fileapi.Placement{TierID: 0, TierLabel: "tier0", NodeID: "node-dst", Path: dstBlock},
	})
	if err != nil {
		t.Fatalf("Enqueue failed: %v", err)
	}

	failed := waitForTaskTerminal(t, store, tk.ID, 3*time.Second)
	if failed.State != TaskFailed || !strings.Contains(failed.Error, "placement changed") {
		t.Fatalf("expected TaskFailed with placement changed, got state=%s err=%q", failed.State, failed.Error)
	}
	if !nodeSrc.hasMedia(srcBlock, key) {
		t.Fatal("INVARIANT VIOLATION: source media was deleted when placement changed during copy")
	}
	if nodeDst.hasAnyDir(dstBlock, key) {
		t.Fatal("expected destination copy to be cleaned up when switch aborts")
	}
}

func TestExecutorSimulatedRestartInEachState(t *testing.T) {
	states := []struct {
		name            string
		persistedState  TaskState
		destAlreadyFull bool
		jobAlreadyMoved bool
	}{
		{name: "restart_in_queued", persistedState: TaskQueued},
		{name: "restart_in_copying", persistedState: TaskCopying},
		{name: "restart_in_verifying", persistedState: TaskVerifying, destAlreadyFull: true},
		{name: "restart_in_switching_before_job_save", persistedState: TaskSwitching, destAlreadyFull: true, jobAlreadyMoved: false},
		{name: "restart_in_switching_after_job_save", persistedState: TaskSwitching, destAlreadyFull: true, jobAlreadyMoved: true},
		{name: "restart_in_cleanup", persistedState: TaskCleanup, destAlreadyFull: true, jobAlreadyMoved: true},
	}

	for _, tc := range states {
		t.Run(tc.name, func(t *testing.T) {
			peers := make(map[string]*fakeAgentNode)
			nodeSrc := newFakeAgentNode(t, "node-src", peers)
			nodeDst := newFakeAgentNode(t, "node-dst", peers)

			srcBlock := nodeSrc.blockDir("hdd")
			dstBlock := nodeDst.blockDir("nvme")

			coord := cluster.NewCoordinator(10*time.Second, 20*time.Second, nil)
			registerFakeNode(coord, nodeSrc, srcBlock)
			registerFakeNode(coord, nodeDst, dstBlock)

			store := newFullMemStore()
			key := "fileRestart_" + tc.name
			videoBytes := []byte("restart-convergence-video-payload")

			nodeSrc.seedMedia(t, srcBlock, key, videoBytes)
			if tc.destAlreadyFull {
				nodeDst.seedMedia(t, dstBlock, key, videoBytes)
			}

			fromPlacement := fileapi.Placement{TierID: 2, TierLabel: "tier2", NodeID: "node-src", Path: srcBlock}
			toPlacement := fileapi.Placement{TierID: 0, TierLabel: "tier0", NodeID: "node-dst", Path: dstBlock}

			currentPlacement := fromPlacement
			if tc.jobAlreadyMoved {
				currentPlacement = toPlacement
			}
			_ = store.SaveFileJob(&fileapi.FileJob{
				Key:       key,
				State:     fileapi.StateCompleted,
				Placement: currentPlacement,
			})

			taskID := "lct_restart_" + tc.name
			_ = store.SaveLifecycleTask(&Task{
				ID:        taskID,
				Key:       key,
				Kind:      TaskPromote,
				Reason:    "simulated crash recovery",
				From:      fromPlacement,
				To:        toPlacement,
				State:     tc.persistedState,
				Attempts:  2,
				CreatedAt: time.Now().UTC().Add(-time.Minute),
				UpdatedAt: time.Now().UTC(),
			})
			_ = store.SetActiveLifecycleTask(key, taskID)

			// Construct a fresh Executor over the same store to simulate a coordinator restart.
			ex := NewExecutor(
				store,
				store,
				storage.NewManager(nil),
				coord,
				WithExecutorPollInterval(10*time.Millisecond),
				WithExecutorBackoffs([]time.Duration{5 * time.Millisecond}),
			)

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			go ex.Run(ctx)

			done := waitForTaskTerminal(t, store, taskID, 3*time.Second)
			if done.State != TaskDone {
				t.Fatalf("expected TaskDone after recovery from %s, got %s (err=%s)", tc.persistedState, done.State, done.Error)
			}

			// Verify convergence: exactly one copy remains on destination and FileJob.Placement matches disk.
			if !nodeDst.hasMedia(dstBlock, key) {
				t.Fatalf("expected destination copy to exist on disk after recovery from %s", tc.persistedState)
			}
			if nodeSrc.hasAnyDir(srcBlock, key) {
				t.Fatalf("expected source copy to be deleted after recovery from %s", tc.persistedState)
			}
			job, _ := store.GetFileJob(key)
			if !samePlacement(job.Placement, toPlacement) {
				t.Fatalf("expected FileJob.Placement == %+v, got %+v", toPlacement, job.Placement)
			}
			if activeID, _ := store.GetActiveLifecycleTask(key); activeID != "" {
				t.Fatalf("expected active task pointer to be cleared, got %q", activeID)
			}
		})
	}
}

func TestExecutorCancelDuringCopyingDeletesPartialDestinationOnly(t *testing.T) {
	peers := make(map[string]*fakeAgentNode)
	nodeSrc := newFakeAgentNode(t, "node-src", peers)
	nodeDst := newFakeAgentNode(t, "node-dst", peers)
	nodeSrc.copyBlockCh = make(chan struct{})
	nodeSrc.copyStartedCh = make(chan struct{}, 1)

	srcBlock := nodeSrc.blockDir("hdd")
	dstBlock := nodeDst.blockDir("nvme")

	coord := cluster.NewCoordinator(10*time.Second, 20*time.Second, nil)
	registerFakeNode(coord, nodeSrc, srcBlock)
	registerFakeNode(coord, nodeDst, dstBlock)

	store := newFullMemStore()
	key := "fileCancel01"
	nodeSrc.seedMedia(t, srcBlock, key, []byte("cancel-safe-video-bytes"))

	_ = store.SaveFileJob(&fileapi.FileJob{
		Key:   key,
		State: fileapi.StateCompleted,
		Placement: fileapi.Placement{
			TierID:    2,
			TierLabel: "tier2",
			NodeID:    "node-src",
			Path:      srcBlock,
		},
	})

	ex := NewExecutor(
		store,
		store,
		storage.NewManager(nil),
		coord,
		WithExecutorPollInterval(10*time.Millisecond),
		WithExecutorBackoffs([]time.Duration{5 * time.Millisecond}),
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go ex.Run(ctx)

	tk, err := ex.Enqueue(&Task{
		Key:  key,
		Kind: TaskPromote,
		From: fileapi.Placement{TierID: 2, TierLabel: "tier2", NodeID: "node-src", Path: srcBlock},
		To:   fileapi.Placement{TierID: 0, TierLabel: "tier0", NodeID: "node-dst", Path: dstBlock},
	})
	if err != nil {
		t.Fatalf("Enqueue failed: %v", err)
	}

	// Wait until the fake agent has begun writing the partial destination folder.
	select {
	case <-nodeSrc.copyStartedCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for copy to start")
	}

	if !nodeDst.hasAnyDir(dstBlock, key) {
		t.Fatal("expected partial destination folder to exist while copying")
	}

	cancelledTask, err := ex.Cancel(tk.ID)
	if err != nil {
		t.Fatalf("Cancel failed: %v", err)
	}
	if cancelledTask.State != TaskCancelled {
		t.Fatalf("expected TaskCancelled, got %s", cancelledTask.State)
	}
	close(nodeSrc.copyBlockCh)

	if !nodeSrc.hasMedia(srcBlock, key) {
		t.Fatal("INVARIANT VIOLATION: source media was deleted on cancel")
	}
	if nodeDst.hasAnyDir(dstBlock, key) {
		t.Fatal("expected partial destination folder to be deleted on cancel")
	}
	if activeID, _ := store.GetActiveLifecycleTask(key); activeID != "" {
		t.Fatalf("expected active task pointer to be cleared after cancel, got %q", activeID)
	}
}

func TestExecutorExpireTaskSuccess(t *testing.T) {
	peers := make(map[string]*fakeAgentNode)
	nodeSrc := newFakeAgentNode(t, "node-src", peers)
	srcBlock := nodeSrc.blockDir("hdd")

	coord := cluster.NewCoordinator(10*time.Second, 20*time.Second, nil)
	registerFakeNode(coord, nodeSrc, srcBlock)

	store := newFullMemStore()
	key := "fileExpireSuccess01"
	videoBytes := []byte("expire-payload-to-be-deleted")
	nodeSrc.seedMedia(t, srcBlock, key, videoBytes)

	fromPlacement := fileapi.Placement{
		TierID:    2,
		TierLabel: "tier2",
		NodeID:    "node-src",
		Path:      srcBlock,
	}

	_ = store.SaveFileJob(&fileapi.FileJob{
		Key:       key,
		State:     fileapi.StateCompleted,
		Placement: fromPlacement,
	})
	_ = store.SaveFileRecord(&fileapi.FileRecord{
		Key:       key,
		Filename:  "movie.mp4",
		SizeBytes: int64(len(videoBytes)),
	})
	_ = store.SaveLifecycleState(&FileState{
		Key:       key,
		TierSince: time.Now().UTC().Add(-40 * 24 * time.Hour),
	})

	ex := NewExecutor(
		store,
		store,
		storage.NewManager(nil),
		coord,
		WithExecutorPollInterval(10*time.Millisecond),
		WithExecutorBackoffs([]time.Duration{5 * time.Millisecond}),
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go ex.Run(ctx)

	tk, err := ex.Enqueue(&Task{
		Key:    key,
		Kind:   TaskExpire,
		From:   fromPlacement,
		To:     fileapi.Placement{TierID: TrashTierID, TierLabel: "trash"},
		EdgeID: "e_expire_age",
		Reason: "age > 30d on tier 2",
		Bytes:  int64(len(videoBytes)),
	})
	if err != nil {
		t.Fatalf("Enqueue failed: %v", err)
	}

	done := waitForTaskTerminal(t, store, tk.ID, 3*time.Second)
	if done.State != TaskDone {
		t.Fatalf("expected TaskDone, got %s (err=%q)", done.State, done.Error)
	}

	// 1. Audit row written FIRST
	audits, err := store.ListLifecycleAudits(10)
	if err != nil || len(audits) != 1 {
		t.Fatalf("expected 1 audit row, got %d (err=%v)", len(audits), err)
	}
	if audits[0].Key != key || audits[0].Filename != "movie.mp4" || audits[0].Size != int64(len(videoBytes)) || audits[0].Tier != 2 || audits[0].Node != "node-src" {
		t.Fatalf("unexpected audit entry: %+v", audits[0])
	}

	// 2. Media folder deleted from agent disk
	if nodeSrc.hasAnyDir(srcBlock, key) {
		t.Fatal("expected media directory to be deleted on disk by agent ingest-delete")
	}

	// 3. DB rows deleted
	if _, err := store.GetFileJob(key); err == nil {
		t.Fatal("expected FileJob to be deleted from store")
	}
	if _, err := store.GetFileRecord(key); err == nil {
		t.Fatal("expected FileRecord to be deleted from store")
	}
	if st, _ := store.GetLifecycleState(key); st != nil {
		t.Fatal("expected FileState to be deleted from store")
	}
	if activeID, _ := store.GetActiveLifecycleTask(key); activeID != "" {
		t.Fatalf("expected active task pointer cleared, got %q", activeID)
	}
}

func TestExecutorExpireTaskAgentFailurePreservesDatabase(t *testing.T) {
	peers := make(map[string]*fakeAgentNode)
	nodeSrc := newFakeAgentNode(t, "node-src", peers)
	srcBlock := nodeSrc.blockDir("hdd")
	nodeSrc.deleteErr = true // Simulate agent delete failure

	coord := cluster.NewCoordinator(10*time.Second, 20*time.Second, nil)
	registerFakeNode(coord, nodeSrc, srcBlock)

	store := newFullMemStore()
	key := "fileExpireFail01"
	videoBytes := []byte("expire-safe-payload")
	nodeSrc.seedMedia(t, srcBlock, key, videoBytes)

	fromPlacement := fileapi.Placement{
		TierID:    2,
		TierLabel: "tier2",
		NodeID:    "node-src",
		Path:      srcBlock,
	}

	_ = store.SaveFileJob(&fileapi.FileJob{
		Key:       key,
		State:     fileapi.StateCompleted,
		Placement: fromPlacement,
	})
	_ = store.SaveFileRecord(&fileapi.FileRecord{
		Key:       key,
		Filename:  "fail.mp4",
		SizeBytes: int64(len(videoBytes)),
	})
	_ = store.SaveLifecycleState(&FileState{
		Key:       key,
		TierSince: time.Now().UTC().Add(-40 * 24 * time.Hour),
	})

	ex := NewExecutor(
		store,
		store,
		storage.NewManager(nil),
		coord,
		WithExecutorPollInterval(10*time.Millisecond),
		WithExecutorBackoffs([]time.Duration{5 * time.Millisecond, 5 * time.Millisecond}),
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go ex.Run(ctx)

	tk, err := ex.Enqueue(&Task{
		Key:    key,
		Kind:   TaskExpire,
		From:   fromPlacement,
		To:     fileapi.Placement{TierID: TrashTierID, TierLabel: "trash"},
		EdgeID: "e_expire_age",
		Reason: "age > 30d on tier 2",
		Bytes:  int64(len(videoBytes)),
	})
	if err != nil {
		t.Fatalf("Enqueue failed: %v", err)
	}

	failed := waitForTaskTerminal(t, store, tk.ID, 3*time.Second)
	if failed.State != TaskFailed {
		t.Fatalf("expected TaskFailed on agent delete failure, got %s", failed.State)
	}
	if failed.Attempts != 3 {
		t.Fatalf("expected 3 attempts before failure, got %d", failed.Attempts)
	}

	// Invariant: Audit row was recorded before calling agent
	audits, _ := store.ListLifecycleAudits(10)
	if len(audits) != 1 || audits[0].Key != key {
		t.Fatalf("expected audit row recorded before delete attempt, got %+v", audits)
	}

	// Invariant: DB rows are NOT deleted when agent fails to delete
	job, err := store.GetFileJob(key)
	if err != nil || job == nil {
		t.Fatal("INVARIANT VIOLATION: FileJob was deleted despite agent delete failure")
	}
	rec, err := store.GetFileRecord(key)
	if err != nil || rec == nil {
		t.Fatal("INVARIANT VIOLATION: FileRecord was deleted despite agent delete failure")
	}
	st, _ := store.GetLifecycleState(key)
	if st == nil {
		t.Fatal("INVARIANT VIOLATION: FileState was deleted despite agent delete failure")
	}
}

func TestLifecycleRelocateToAndFromS3Bucket(t *testing.T) {
	peers := make(map[string]*fakeAgentNode)
	nodeHdd := newFakeAgentNode(t, "node-hdd", peers)

	srcBlock := nodeHdd.blockDir("hdd")
	coord := cluster.NewCoordinator(10*time.Second, 20*time.Second, nil)
	registerFakeNode(coord, nodeHdd, srcBlock)

	// Mock S3/R2 Server
	var s3Mu sync.Mutex
	s3Objects := make(map[string][]byte)

	s3Srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s3Mu.Lock()
		defer s3Mu.Unlock()

		path := strings.TrimPrefix(r.URL.Path, "/archive-bucket")
		path = strings.TrimPrefix(path, "/")

		switch r.Method {
		case http.MethodHead:
			data, ok := s3Objects[path]
			if !ok {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Length", fmt.Sprintf("%d", len(data)))
			w.WriteHeader(http.StatusOK)
		case http.MethodPut:
			data, _ := io.ReadAll(r.Body)
			s3Objects[path] = data
			w.WriteHeader(http.StatusOK)
		case http.MethodGet:
			if r.URL.Query().Get("list-type") == "2" {
				prefix := r.URL.Query().Get("prefix")
				w.Header().Set("Content-Type", "application/xml")
				fmt.Fprint(w, `<ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">`)
				for k, v := range s3Objects {
					if strings.HasPrefix(k, prefix) {
						fmt.Fprintf(w, `<Contents><Key>%s</Key><Size>%d</Size><LastModified>2026-10-19T12:00:00Z</LastModified></Contents>`, k, len(v))
					}
				}
				fmt.Fprint(w, `</ListBucketResult>`)
				return
			}
			data, ok := s3Objects[path]
			if !ok {
				http.NotFound(w, r)
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(data)
		case http.MethodDelete:
			delete(s3Objects, path)
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "not allowed", http.StatusMethodNotAllowed)
		}
	}))
	defer s3Srv.Close()

	s3Cfg := &storage.S3Config{
		Name:      "archive-bucket",
		Provider:  "r2",
		Endpoint:  s3Srv.URL,
		Region:    "auto",
		AccessKey: "test-acc",
		SecretKey: "test-sec",
	}

	tm := storage.NewManager(nil)
	_ = tm.Upsert(storage.Tier{
		ID:     0,
		Name:   "Tier 0 · Hot (HDD)",
		System: true,
		Blocks: []storage.Block{{NodeID: "node-hdd", Path: srcBlock, DiskType: "HDD"}},
	})
	_ = tm.Upsert(storage.Tier{
		ID:     3,
		Name:   "Tier 3 · Cold (Cloudflare R2)",
		System: false,
		Blocks: []storage.Block{{NodeID: "bucket:bkt-r2", Path: ""}},
	})

	store := newFullMemStore()
	key := "fileToR2Archive01"
	payload := []byte("fake-hls-video-data")
	nodeHdd.seedMedia(t, srcBlock, key, payload)

	_ = store.SaveFileRecord(&fileapi.FileRecord{Key: key, SizeBytes: int64(len(payload))})
	_ = store.SaveFileJob(&fileapi.FileJob{
		Key:        key,
		State:      fileapi.StateCompleted,
		TotalBytes: int64(len(payload)),
		Placement: fileapi.Placement{
			TierID:    0,
			TierLabel: "tier0",
			NodeID:    "node-hdd",
			Path:      srcBlock,
		},
	})

	eng := New(
		store,
		tm,
		coord,
		nil,
		WithJobStore(store),
		WithBucketResolver(func(id string) (*storage.S3Config, error) {
			if id == "bkt-r2" {
				return s3Cfg, nil
			}
			return nil, fmt.Errorf("unknown bucket %s", id)
		}),
		WithExecutorOptions(
			WithExecutorPollInterval(10*time.Millisecond),
			WithExecutorBackoffs([]time.Duration{10 * time.Millisecond}),
		),
	)

	progSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		if len(parts) == 5 && parts[4] == "progress" {
			var p ProgressPayload
			_ = json.NewDecoder(r.Body).Decode(&p)
			_ = eng.Executor().ApplyProgress(parts[3], p)
			w.WriteHeader(http.StatusAccepted)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer progSrv.Close()
	eng.Executor().SetCoordinatorURL(progSrv.URL)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	// Step 1: Relocate from VPS HDD -> R2 Bucket
	task, err := eng.Executor().Enqueue(&Task{
		Key:  key,
		Kind: TaskDemote,
		From: fileapi.Placement{TierID: 0, NodeID: "node-hdd", Path: srcBlock},
		To:   fileapi.Placement{TierID: 3, NodeID: "bucket:bkt-r2", Path: ""},
	})
	if err != nil {
		t.Fatalf("Enqueue to S3 failed: %v", err)
	}

	done := waitForTaskTerminal(t, store, task.ID, 5*time.Second)
	if done.State != TaskDone {
		t.Fatalf("expected TaskDone relocating to S3, got %s (err: %s)", done.State, done.Error)
	}

	// Verify object exists in S3
	s3Mu.Lock()
	_, inS3 := s3Objects[key+"/metadata.json"]
	s3Count := len(s3Objects)
	s3Mu.Unlock()
	if !inS3 || s3Count == 0 {
		t.Fatalf("expected media in S3, found %d objects", s3Count)
	}

	// Verify local copy cleaned on VPS
	if nodeHdd.hasAnyDir(srcBlock, key) {
		t.Fatal("expected local copy deleted after moving to S3, but still exists")
	}

	// Step 2: Promote back from R2 Bucket -> VPS HDD
	promoteTask, err := eng.Executor().Enqueue(&Task{
		Key:  key,
		Kind: TaskPromote,
		From: fileapi.Placement{TierID: 3, NodeID: "bucket:bkt-r2", Path: ""},
		To:   fileapi.Placement{TierID: 0, NodeID: "node-hdd", Path: srcBlock},
	})
	if err != nil {
		t.Fatalf("Enqueue from S3 failed: %v", err)
	}

	donePromote := waitForTaskTerminal(t, store, promoteTask.ID, 5*time.Second)
	if donePromote.State != TaskDone {
		t.Fatalf("expected TaskDone promoting from S3, got %s (err: %s)", donePromote.State, donePromote.Error)
	}

	// Verify media restored locally on VPS
	if !nodeHdd.hasMedia(srcBlock, key) {
		t.Fatal("expected local media restored from S3")
	}

	// Verify S3 copy was cleaned up after promotion
	s3Mu.Lock()
	leftoverS3 := len(s3Objects)
	s3Mu.Unlock()
	if leftoverS3 != 0 {
		t.Fatalf("expected S3 objects deleted after promotion, but %d remain", leftoverS3)
	}
}
