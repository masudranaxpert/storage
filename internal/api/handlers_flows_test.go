package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"stream/internal/cluster"
	"stream/internal/database"
	"stream/internal/lifecycle"
	"stream/internal/model"
	"stream/internal/storage"
	"stream/internal/telemetry"
)

func TestFlowAndTierLifecycleEndpoints(t *testing.T) {
	coord := cluster.NewCoordinator(10*time.Second, 20*time.Second, nil)
	coord.RegisterHeartbeat(telemetry.NodeMetrics{
		NodeID: "vps-hdd",
		IPs:    []string{"10.0.0.1"},
		Disks:  []telemetry.DiskStat{{Path: "/hdd", DiskType: "HDD", FreeBytes: 50 << 30, TotalBytes: 100 << 30}},
	})

	tm := storage.NewManager(nil)
	_ = tm.Upsert(storage.Tier{
		ID:      2,
		Name:    "Tier 2 · Cold (HDD)",
		System:  true,
		Default: true,
		Blocks:  []storage.Block{{NodeID: "vps-hdd", Path: "/hdd", DiskType: "HDD"}},
	})

	hub := cluster.NewGRPCHub(coord, nil, "test-secret")
	srv := NewServer("127.0.0.1:0", coord, hub, nil, tm, "", "")
	handler := srv.Handler()

	// 1. GET /api/flows returns default policy and tiers with lifecycle
	reqGet := httptest.NewRequest(http.MethodGet, "/api/flows", nil)
	reqGet.Header.Set("X-Cluster-Secret", "test-secret")
	recGet := httptest.NewRecorder()
	handler.ServeHTTP(recGet, reqGet)
	if recGet.Code != http.StatusOK {
		t.Fatalf("GET /api/flows = %d: %s", recGet.Code, recGet.Body.String())
	}
	var flowResp struct {
		Policy lifecycle.FlowPolicy `json:"policy"`
		Tiers  []storage.TierStatus `json:"tiers"`
	}
	if err := json.Unmarshal(recGet.Body.Bytes(), &flowResp); err != nil {
		t.Fatalf("decode GET /api/flows: %v", err)
	}
	if flowResp.Policy.Version != 1 || len(flowResp.Tiers) != 3 {
		t.Fatalf("unexpected GET /api/flows payload: %+v", flowResp)
	}

	// 2. PUT /api/flows from non-loopback without secret -> 401
	putBody, _ := json.Marshal(map[string]interface{}{
		"version": 1,
		"ingest":  []int{2, 1},
		"edges":   []lifecycle.Edge{},
	})
	reqUnauth := httptest.NewRequest(http.MethodPut, "/api/flows", bytes.NewReader(putBody))
	reqUnauth.RemoteAddr = "198.51.100.25:44123"
	recUnauth := httptest.NewRecorder()
	handler.ServeHTTP(recUnauth, reqUnauth)
	if recUnauth.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without secret from non-loopback, got %d", recUnauth.Code)
	}

	// 3. PUT /api/flows with invalid edge -> 422 with {"errors":[...]}
	badBody, _ := json.Marshal(map[string]interface{}{
		"version": 1,
		"ingest":  []int{2},
		"edges": []lifecycle.Edge{
			{ID: "e_bad", Kind: lifecycle.EdgePromote, From: 0, To: 2, Enabled: true},
		},
	})
	reqBad := httptest.NewRequest(http.MethodPut, "/api/flows", bytes.NewReader(badBody))
	reqBad.Header.Set("X-Cluster-Secret", "test-secret")
	recBad := httptest.NewRecorder()
	handler.ServeHTTP(recBad, reqBad)
	if recBad.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 on invalid edge, got %d: %s", recBad.Code, recBad.Body.String())
	}
	var errResp struct {
		Errors []lifecycle.FieldError `json:"errors"`
	}
	if err := json.Unmarshal(recBad.Body.Bytes(), &errResp); err != nil || len(errResp.Errors) == 0 {
		t.Fatalf("expected structured errors in 422 response, got %s", recBad.Body.String())
	}

	// 4. PUT /api/flows valid update -> 200 and version=2
	reqValid := httptest.NewRequest(http.MethodPut, "/api/flows", bytes.NewReader(putBody))
	reqValid.Header.Set("X-Cluster-Secret", "test-secret")
	recValid := httptest.NewRecorder()
	handler.ServeHTTP(recValid, reqValid)
	if recValid.Code != http.StatusOK {
		t.Fatalf("expected 200 on valid PUT /api/flows, got %d: %s", recValid.Code, recValid.Body.String())
	}

	// 5. PUT /api/flows with stale version 1 -> 409 Conflict
	reqConflict := httptest.NewRequest(http.MethodPut, "/api/flows", bytes.NewReader(putBody))
	reqConflict.Header.Set("X-Cluster-Secret", "test-secret")
	recConflict := httptest.NewRecorder()
	handler.ServeHTTP(recConflict, reqConflict)
	if recConflict.Code != http.StatusConflict {
		t.Fatalf("expected 409 on version conflict, got %d", recConflict.Code)
	}

	// 6. PUT /api/tiers/1/lifecycle valid and invalid watermarks
	lcValid, _ := json.Marshal(storage.TierLifecycle{SpeedRank: 1, HighWatermark: 85, LowWatermark: 70})
	reqLC := httptest.NewRequest(http.MethodPut, "/api/tiers/1/lifecycle", bytes.NewReader(lcValid))
	reqLC.Header.Set("X-Cluster-Secret", "test-secret")
	recLC := httptest.NewRecorder()
	handler.ServeHTTP(recLC, reqLC)
	if recLC.Code != http.StatusOK {
		t.Fatalf("PUT /api/tiers/1/lifecycle = %d: %s", recLC.Code, recLC.Body.String())
	}

	lcBad, _ := json.Marshal(storage.TierLifecycle{SpeedRank: 1, HighWatermark: 60, LowWatermark: 80})
	reqLCBad := httptest.NewRequest(http.MethodPut, "/api/tiers/1/lifecycle", bytes.NewReader(lcBad))
	reqLCBad.Header.Set("X-Cluster-Secret", "test-secret")
	recLCBad := httptest.NewRecorder()
	handler.ServeHTTP(recLCBad, reqLCBad)
	if recLCBad.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 on invalid tier watermarks, got %d", recLCBad.Code)
	}

	// 7. GET /api/flows/simulate fits (200) and exceeds capacity (507)
	reqSimOK := httptest.NewRequest(http.MethodGet, "/api/flows/simulate?size_bytes=1073741824", nil)
	reqSimOK.Header.Set("X-Cluster-Secret", "test-secret")
	recSimOK := httptest.NewRecorder()
	handler.ServeHTTP(recSimOK, reqSimOK)
	if recSimOK.Code != http.StatusOK {
		t.Fatalf("expected 200 on simulate 1GB, got %d: %s", recSimOK.Code, recSimOK.Body.String())
	}

	reqSimFull := httptest.NewRequest(http.MethodGet, "/api/flows/simulate?size_bytes=107374182400", nil)
	reqSimFull.Header.Set("X-Cluster-Secret", "test-secret")
	recSimFull := httptest.NewRecorder()
	handler.ServeHTTP(recSimFull, reqSimFull)
	if recSimFull.Code != http.StatusInsufficientStorage {
		t.Fatalf("expected 507 on simulate 100GB, got %d: %s", recSimFull.Code, recSimFull.Body.String())
	}
}

func TestLifecycleTasksAndFileMoveEndpoints(t *testing.T) {
	store, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer store.Close()

	coord := cluster.NewCoordinator(10*time.Second, 20*time.Second, store)
	coord.RegisterHeartbeat(telemetry.NodeMetrics{
		NodeID:       "vps-hdd",
		IPs:          []string{"10.0.0.1"},
		Capabilities: telemetry.NodeCapabilities{AgentPort: 2052, HasFFmpeg: true, HasAria2c: true},
		Disks:        []telemetry.DiskStat{{Path: "/hdd", DiskType: "HDD", FreeBytes: 50 << 30, TotalBytes: 100 << 30}},
	})
	coord.RegisterHeartbeat(telemetry.NodeMetrics{
		NodeID:       "vps-nvme",
		IPs:          []string{"10.0.0.2"},
		Capabilities: telemetry.NodeCapabilities{AgentPort: 2052, HasFFmpeg: true, HasAria2c: true},
		Disks:        []telemetry.DiskStat{{Path: "/nvme", DiskType: "NVMe", FreeBytes: 50 << 30, TotalBytes: 100 << 30}},
	})

	tm := storage.NewManager(store)
	_ = tm.Upsert(storage.Tier{
		ID:     0,
		Name:   "Tier 0 · Hot (NVMe)",
		System: true,
		Blocks: []storage.Block{{NodeID: "vps-nvme", Path: "/nvme", DiskType: "NVMe"}},
	})
	_ = tm.Upsert(storage.Tier{
		ID:      2,
		Name:    "Tier 2 · Cold (HDD)",
		System:  true,
		Default: true,
		Blocks:  []storage.Block{{NodeID: "vps-hdd", Path: "/hdd", DiskType: "HDD"}},
	})

	_ = store.SaveFileRecord(&model.FileRecord{Key: "moveKey01", SizeBytes: 1024 * 1024})
	_ = store.SaveFileJob(&model.FileJob{
		Key:        "moveKey01",
		State:      model.StateCompleted,
		TotalBytes: 1024 * 1024,
		Placement:  model.Placement{TierID: 2, TierLabel: "tier2", NodeID: "vps-hdd", Path: "/hdd"},
	})

	hub := cluster.NewGRPCHub(coord, store, "test-secret")
	srv := NewServer("127.0.0.1:0", coord, hub, store, tm, "", "")
	handler := srv.Handler()

	// 1. POST /api/v1/files/{key}/move on same tier -> 409
	sameTierReq := httptest.NewRequest(http.MethodPost, "/api/v1/files/moveKey01/move", bytes.NewReader([]byte(`{"tier_id":2}`)))
	sameTierReq.Header.Set("X-Cluster-Secret", "test-secret")
	sameTierRec := httptest.NewRecorder()
	handler.ServeHTTP(sameTierRec, sameTierReq)
	if sameTierRec.Code != http.StatusConflict {
		t.Fatalf("expected 409 on same tier move, got %d: %s", sameTierRec.Code, sameTierRec.Body.String())
	}

	// 2. POST /api/v1/files/{key}/move to tier 0 -> 202 Accepted with manual task
	moveReq := httptest.NewRequest(http.MethodPost, "/api/v1/files/moveKey01/move", bytes.NewReader([]byte(`{"tier_id":0}`)))
	moveReq.Header.Set("X-Cluster-Secret", "test-secret")
	moveRec := httptest.NewRecorder()
	handler.ServeHTTP(moveRec, moveReq)
	if moveRec.Code != http.StatusAccepted {
		t.Fatalf("expected 202 on valid move, got %d: %s", moveRec.Code, moveRec.Body.String())
	}
	var createdTask lifecycle.Task
	if err := json.Unmarshal(moveRec.Body.Bytes(), &createdTask); err != nil {
		t.Fatalf("decode move response: %v", err)
	}
	if createdTask.ID == "" || createdTask.Kind != lifecycle.TaskManual || createdTask.To.TierID != 0 {
		t.Fatalf("unexpected task returned by move endpoint: %+v", createdTask)
	}

	// 3. GET /api/lifecycle/tasks?state=active returns the queued task
	listReq := httptest.NewRequest(http.MethodGet, "/api/lifecycle/tasks?state=active", nil)
	listReq.Header.Set("X-Cluster-Secret", "test-secret")
	listRec := httptest.NewRecorder()
	handler.ServeHTTP(listRec, listReq)
	if listRec.Code != http.StatusOK {
		t.Fatalf("GET /api/lifecycle/tasks = %d: %s", listRec.Code, listRec.Body.String())
	}
	var listResp struct {
		Tasks []lifecycle.Task `json:"tasks"`
	}
	if err := json.Unmarshal(listRec.Body.Bytes(), &listResp); err != nil || len(listResp.Tasks) != 1 {
		t.Fatalf("expected 1 active task, got %s", listRec.Body.String())
	}

	// 4. POST /api/lifecycle/tasks/{id}/progress requires cluster secret
	progUnauth := httptest.NewRequest(http.MethodPost, "/api/lifecycle/tasks/"+createdTask.ID+"/progress", bytes.NewReader([]byte(`{"state":"copying","progress_percent":50}`)))
	progUnauthRec := httptest.NewRecorder()
	handler.ServeHTTP(progUnauthRec, progUnauth)
	if progUnauthRec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 on unauthenticated progress callback, got %d", progUnauthRec.Code)
	}

	progAuth := httptest.NewRequest(http.MethodPost, "/api/lifecycle/tasks/"+createdTask.ID+"/progress", bytes.NewReader([]byte(`{"state":"copying","progress_percent":50,"speed":"100 MB/s"}`)))
	progAuth.Header.Set("X-Cluster-Secret", "test-secret")
	progAuthRec := httptest.NewRecorder()
	handler.ServeHTTP(progAuthRec, progAuth)
	if progAuthRec.Code != http.StatusAccepted {
		t.Fatalf("expected 202 on authenticated progress callback, got %d: %s", progAuthRec.Code, progAuthRec.Body.String())
	}

	// 5. POST /api/lifecycle/tasks/{id}/cancel cancels the queued task
	cancelReq := httptest.NewRequest(http.MethodPost, "/api/lifecycle/tasks/"+createdTask.ID+"/cancel", nil)
	cancelReq.Header.Set("X-Cluster-Secret", "test-secret")
	cancelRec := httptest.NewRecorder()
	handler.ServeHTTP(cancelRec, cancelReq)
	if cancelRec.Code != http.StatusOK {
		t.Fatalf("expected 200 on task cancel, got %d: %s", cancelRec.Code, cancelRec.Body.String())
	}
}

func TestDemandPlacementAndPreviewEndpoints(t *testing.T) {
	coord := cluster.NewCoordinator(10*time.Second, 20*time.Second, nil)
	coord.RegisterHeartbeat(telemetry.NodeMetrics{
		NodeID: "worker-node-1",
		IPs:    []string{"10.0.0.1"},
		Disks:  []telemetry.DiskStat{{Path: "/storage/hdd", DiskType: "HDD", FreeBytes: 50 << 30, TotalBytes: 100 << 30}},
	})

	store, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer store.Close()
	tm := storage.NewManager(store)
	_ = tm.Upsert(storage.Tier{
		ID:      0,
		Name:    "Tier 0 · Hot (NVMe)",
		System:  true,
		Default: false,
		Blocks:  []storage.Block{{NodeID: "worker-node-1", Path: "/storage/nvme", DiskType: "NVME"}},
	})
	_ = tm.Upsert(storage.Tier{
		ID:      2,
		Name:    "Tier 2 · Cold (HDD)",
		System:  true,
		Default: true,
		Blocks:  []storage.Block{{NodeID: "worker-node-1", Path: "/storage/hdd", DiskType: "HDD"}},
	})

	hub := cluster.NewGRPCHub(coord, store, "test-secret")
	srv := NewServer("127.0.0.1:0", coord, hub, store, tm, "", "")
	handler := srv.Handler()

	now := time.Now().UTC()
	testKey := "testDemandKey01"
	rec := &model.FileRecord{
		Key:       testKey,
		Filename:  "sample.mp4",
		SizeBytes: 10 * 1024 * 1024,
		CreatedAt: now,
	}
	job := &model.FileJob{
		Key:   testKey,
		State: model.StateCompleted,
		Placement: model.Placement{
			TierID:     2,
			TierLabel:  "tier2",
			NodeID:     "worker-node-1",
			Path:       "/storage/hdd",
			PublicHost: "10.0.0.1:2052",
		},
		CreatedAt: now,
		UpdatedAt: now,
	}
	_ = store.SaveFileRecord(rec)
	_ = store.SaveFileJob(job)

	// 1. GET /api/flows/preview returns 200 OK
	previewReq := httptest.NewRequest(http.MethodGet, "/api/flows/preview", nil)
	previewReq.Header.Set("X-Cluster-Secret", "test-secret")
	previewRec := httptest.NewRecorder()
	handler.ServeHTTP(previewRec, previewReq)
	if previewRec.Code != http.StatusOK {
		t.Fatalf("GET /api/flows/preview = %d: %s", previewRec.Code, previewRec.Body.String())
	}
	var previewResp struct {
		Tasks   []lifecycle.Task             `json:"tasks"`
		Skipped []lifecycle.SkippedCandidate `json:"skipped"`
	}
	if err := json.Unmarshal(previewRec.Body.Bytes(), &previewResp); err != nil {
		t.Fatalf("decode preview response: %v", err)
	}

	// 2. GET /api/v1/files/{key}/placement returns placement view
	placeReq := httptest.NewRequest(http.MethodGet, "/api/v1/files/"+testKey+"/placement", nil)
	placeReq.Header.Set("X-Cluster-Secret", "test-secret")
	placeRec := httptest.NewRecorder()
	handler.ServeHTTP(placeRec, placeReq)
	if placeRec.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/files/%s/placement = %d: %s", testKey, placeRec.Code, placeRec.Body.String())
	}
	var placeResp lifecycle.PlacementView
	if err := json.Unmarshal(placeRec.Body.Bytes(), &placeResp); err != nil {
		t.Fatalf("decode placement response: %v", err)
	}
	if placeResp.Placement.TierID != 2 || placeResp.State.Key != testKey {
		t.Fatalf("unexpected placement view: %+v", placeResp)
	}

	// 3. POST /api/v1/files/{key}/demand without auth -> 401
	demandUnauth := httptest.NewRequest(http.MethodPost, "/api/v1/files/"+testKey+"/demand", bytes.NewReader([]byte(`{"level":"high"}`)))
	demandUnauthRec := httptest.NewRecorder()
	handler.ServeHTTP(demandUnauthRec, demandUnauth)
	if demandUnauthRec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 on unauthenticated demand, got %d", demandUnauthRec.Code)
	}

	// 4. POST /api/v1/files/{key}/demand with level=high -> 200 with pin
	demandReq := httptest.NewRequest(http.MethodPost, "/api/v1/files/"+testKey+"/demand", bytes.NewReader([]byte(`{"level":"high","ttl_hours":48}`)))
	demandReq.Header.Set("X-Cluster-Secret", "test-secret")
	demandRec := httptest.NewRecorder()
	handler.ServeHTTP(demandRec, demandReq)
	if demandRec.Code != http.StatusOK {
		t.Fatalf("expected 200 on valid demand, got %d: %s", demandRec.Code, demandRec.Body.String())
	}
	var demandResp lifecycle.DemandResponse
	if err := json.Unmarshal(demandRec.Body.Bytes(), &demandResp); err != nil {
		t.Fatalf("decode demand response: %v", err)
	}
	if demandResp.PinnedTier == nil || *demandResp.PinnedTier != 0 {
		t.Fatalf("expected pinned tier 0, got %+v", demandResp.PinnedTier)
	}
	if demandResp.PinUntil == nil || demandResp.PinUntil.Before(now) {
		t.Fatalf("expected pin_until set in future, got %+v", demandResp.PinUntil)
	}

	// 5. POST /api/v1/files/{key}/demand with level=normal -> clears pin
	normalReq := httptest.NewRequest(http.MethodPost, "/api/v1/files/"+testKey+"/demand", bytes.NewReader([]byte(`{"level":"normal"}`)))
	normalReq.Header.Set("X-Cluster-Secret", "test-secret")
	normalRec := httptest.NewRecorder()
	handler.ServeHTTP(normalRec, normalReq)
	if normalRec.Code != http.StatusOK {
		t.Fatalf("expected 200 on unpin, got %d: %s", normalRec.Code, normalRec.Body.String())
	}
	var normalResp lifecycle.DemandResponse
	if err := json.Unmarshal(normalRec.Body.Bytes(), &normalResp); err != nil {
		t.Fatalf("decode normal demand response: %v", err)
	}
	if normalResp.PinnedTier != nil {
		t.Fatalf("expected pinned_tier cleared, got %+v", normalResp.PinnedTier)
	}

	// 6. Streaming redirect: unauthenticated request without token must return 403 Forbidden
	unauthReq := httptest.NewRequest(http.MethodGet, "/stream/"+testKey, nil)
	unauthReq.RemoteAddr = "203.0.113.50:54321"
	unauthRec := httptest.NewRecorder()
	handler.ServeHTTP(unauthRec, unauthReq)
	if unauthRec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 forbidden without stream token, got %d", unauthRec.Code)
	}

	// Signed stream playback with valid 4h HMAC token -> 307 redirect + hit counting
	exp := time.Now().Add(4 * time.Hour).Unix()
	tok := cluster.GenerateStreamToken(testKey, exp, "test-secret")
	tokenQuery := fmt.Sprintf("?token=%s&exp=%d", tok, exp)

	playReq1 := httptest.NewRequest(http.MethodGet, "/stream/"+testKey+tokenQuery, nil)
	playReq1.RemoteAddr = "203.0.113.50:54321"
	playRec1 := httptest.NewRecorder()
	handler.ServeHTTP(playRec1, playReq1)
	if playRec1.Code != http.StatusTemporaryRedirect {
		t.Fatalf("expected 307 redirect on stream play, got %d", playRec1.Code)
	}
	if hits := srv.Engine().Hits().Pending(testKey); hits != 1 {
		t.Fatalf("expected 1 pending hit after first play, got %d", hits)
	}

	// Second play from same client IP within 30m -> deduped, pending hits stays 1
	playReq2 := httptest.NewRequest(http.MethodGet, "/stream/"+testKey+tokenQuery, nil)
	playReq2.RemoteAddr = "203.0.113.50:54322" // same IP host
	playRec2 := httptest.NewRecorder()
	handler.ServeHTTP(playRec2, playReq2)
	if playRec2.Code != http.StatusTemporaryRedirect {
		t.Fatalf("expected 307 redirect on second play, got %d", playRec2.Code)
	}
	if hits := srv.Engine().Hits().Pending(testKey); hits != 1 {
		t.Fatalf("expected hits to remain 1 due to 30m IP deduplication, got %d", hits)
	}

	// Sub-track request (.vtt) -> must NOT count as a primary play
	subReq := httptest.NewRequest(http.MethodGet, "/stream/"+testKey+"/subtitle_1_eng.vtt"+tokenQuery, nil)
	subReq.RemoteAddr = "203.0.113.99:12345" // new IP
	subRec := httptest.NewRecorder()
	handler.ServeHTTP(subRec, subReq)
	if subRec.Code != http.StatusTemporaryRedirect {
		t.Fatalf("expected 307 on subtitle redirect, got %d", subRec.Code)
	}
	if hits := srv.Engine().Hits().Pending(testKey); hits != 1 {
		t.Fatalf("sub-track .vtt should not be counted as a hit, got pending %d", hits)
	}

	// 7. Test ArtPlayer embed: browser requesting stream with Accept: text/html must receive 200 OK HTML
	ticket, err := cluster.GeneratePlaybackTicket(testKey, exp, "test-secret")
	if err != nil {
		t.Fatalf("generate ticket: %v", err)
	}
	embedReq := httptest.NewRequest(http.MethodGet, "/stream/"+ticket, nil)
	embedReq.Header.Set("Accept", "text/html,application/xhtml+xml")
	embedRec := httptest.NewRecorder()
	handler.ServeHTTP(embedRec, embedReq)
	if embedRec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK on embed player HTML, got %d", embedRec.Code)
	}
	if !strings.Contains(embedRec.Body.String(), "artplayer") {
		t.Fatalf("expected embed HTML to contain artplayer, got: %s", embedRec.Body.String()[:200])
	}

	// 8. Test ticket stream URL retention: non-HTML stream request with ticket must preserve ticket
	rawReq := httptest.NewRequest(http.MethodGet, "/stream/"+ticket, nil)
	rawRec := httptest.NewRecorder()
	handler.ServeHTTP(rawRec, rawReq)
	if rawRec.Code != http.StatusTemporaryRedirect {
		t.Fatalf("expected 307 redirect on raw ticket request, got %d", rawRec.Code)
	}
	loc := rawRec.Header().Get("Location")
	if !strings.Contains(loc, ticket) {
		t.Fatalf("expected redirect to preserve ticket %s, got: %s", ticket, loc)
	}
	if strings.Contains(loc, testKey) {
		t.Fatalf("redirect leaked real DB key %s: %s", testKey, loc)
	}
}

func TestLifecycleAuditEndpoint(t *testing.T) {
	store, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer store.Close()

	hub := cluster.NewGRPCHub(nil, nil, "audit-secret")
	srv := NewServer("127.0.0.1:0", nil, hub, store, nil, "", "")
	handler := srv.Handler()

	// 0. Unauthenticated request must return 401
	reqUnauth := httptest.NewRequest(http.MethodGet, "/api/lifecycle/audit", nil)
	recUnauth := httptest.NewRecorder()
	handler.ServeHTTP(recUnauth, reqUnauth)
	if recUnauth.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 on unauthenticated audit access, got %d", recUnauth.Code)
	}

	// 1. Initially empty audit list
	req1 := httptest.NewRequest(http.MethodGet, "/api/lifecycle/audit", nil)
	req1.Header.Set("X-Cluster-Secret", "audit-secret")
	rec1 := httptest.NewRecorder()
	handler.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusOK {
		t.Fatalf("GET /api/lifecycle/audit = %d: %s", rec1.Code, rec1.Body.String())
	}
	var resp1 struct {
		Audits []*lifecycle.AuditEntry `json:"audits"`
	}
	if err := json.Unmarshal(rec1.Body.Bytes(), &resp1); err != nil {
		t.Fatalf("decode audit response: %v", err)
	}
	if len(resp1.Audits) != 0 {
		t.Fatalf("expected 0 audits initially, got %d", len(resp1.Audits))
	}

	// 2. Insert audit entries
	_ = store.SaveLifecycleAudit(&lifecycle.AuditEntry{
		Key:      "audit_file_1",
		Filename: "movie1.mp4",
		Size:     1000,
		Tier:     2,
		Node:     "node_1",
		Path:     "/hdd",
		EdgeID:   "e_exp1",
		Reason:   "age > 30d",
		At:       time.Now().UTC().Add(-time.Hour),
	})
	_ = store.SaveLifecycleAudit(&lifecycle.AuditEntry{
		Key:      "audit_file_2",
		Filename: "movie2.mp4",
		Size:     2000,
		Tier:     2,
		Node:     "node_1",
		Path:     "/hdd",
		EdgeID:   "e_exp1",
		Reason:   "age > 30d",
		At:       time.Now().UTC(),
	})

	// 3. Query audits with limit=1
	req2 := httptest.NewRequest(http.MethodGet, "/api/lifecycle/audit?limit=1", nil)
	req2.Header.Set("X-Cluster-Secret", "audit-secret")
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("GET /api/lifecycle/audit?limit=1 = %d: %s", rec2.Code, rec2.Body.String())
	}
	var resp2 struct {
		Audits []*lifecycle.AuditEntry `json:"audits"`
	}
	if err := json.Unmarshal(rec2.Body.Bytes(), &resp2); err != nil {
		t.Fatalf("decode audit response: %v", err)
	}
	if len(resp2.Audits) != 1 {
		t.Fatalf("expected 1 audit with limit=1, got %d", len(resp2.Audits))
	}
	// Newest first
	if resp2.Audits[0].Key != "audit_file_2" {
		t.Fatalf("expected newest audit entry audit_file_2, got %s", resp2.Audits[0].Key)
	}

	// 4. Non-GET method returns 405 Method Not Allowed
	reqPost := httptest.NewRequest(http.MethodPost, "/api/lifecycle/audit", nil)
	reqPost.Header.Set("X-Cluster-Secret", "audit-secret")
	recPost := httptest.NewRecorder()
	handler.ServeHTTP(recPost, reqPost)
	if recPost.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405 on POST /api/lifecycle/audit, got %d", recPost.Code)
	}
}
