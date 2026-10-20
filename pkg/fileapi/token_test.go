package fileapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"stream/pkg/cluster"
	"stream/pkg/telemetry"
)

func TestStreamToken(t *testing.T) {
	key := "test-media-123"
	secret := "cluster-secret-key-xyz"
	now := time.Now().Unix()
	expFuture := now + 3600 // 1 hour ahead
	expPast := now - 60     // 1 min ago

	// 1. Generate valid token
	token := GenerateStreamToken(key, expFuture, secret)
	if token == "" {
		t.Fatal("expected non-empty token")
	}

	// 2. Validate valid token
	if !ValidateStreamToken(key, token, fmt.Sprintf("%d", expFuture), secret) {
		t.Fatal("valid token must pass verification")
	}

	// 3. Reject expired token
	pastToken := GenerateStreamToken(key, expPast, secret)
	if ValidateStreamToken(key, pastToken, fmt.Sprintf("%d", expPast), secret) {
		t.Fatal("expired token must fail verification")
	}

	// 4. Reject tampered token or key mismatch
	if ValidateStreamToken("other-media", token, fmt.Sprintf("%d", expFuture), secret) {
		t.Fatal("tampered key must fail verification")
	}
	if ValidateStreamToken(key, "invalid-token", fmt.Sprintf("%d", expFuture), secret) {
		t.Fatal("tampered token must fail verification")
	}

	// 5. Zero-Trust: empty secret must reject access
	if ValidateStreamToken(key, "", fmt.Sprintf("%d", expFuture), "") {
		t.Fatal("empty secret must reject access")
	}
}

func TestHandleTokenMultiTrack(t *testing.T) {
	store := newMemJobStore()
	coord := cluster.NewCoordinator(10*time.Second, 20*time.Second, nil)
	coord.RegisterHeartbeat(telemetry.NodeMetrics{
		NodeID: "vps-01",
		IPs:    []string{"104.244.76.154"},
		Capabilities: telemetry.NodeCapabilities{
			AgentPort: 2052,
		},
	})

	metaJSON := []byte(`{
		"filename": "movie.mp4",
		"video": {"codec": "h264", "width": 1920, "height": 1080},
		"audio_tracks": [
			{"index": 1, "codec": "aac", "language": "ben", "title": "Bengali", "file": "audio_1_ben.m4a"},
			{"index": 2, "codec": "aac", "language": "hin", "title": "Hindi", "file": "audio_2_hin.m4a"}
		],
		"subtitles": [
			{"index": 3, "codec": "vtt", "language": "eng", "title": "English", "file": "subtitle_1_eng.vtt"},
			{"index": 4, "codec": "vtt", "language": "ben", "title": "Bengali", "file": "subtitle_2_ben.vtt"}
		]
	}`)

	job := &FileJob{
		Key:          "test-vid-multi",
		State:        StateCompleted,
		Placement:    Placement{NodeID: "vps-01", Path: "/mnt/hdd"},
		MetadataJSON: metaJSON,
	}
	_ = store.SaveFileJob(job)
	_ = store.SaveFileRecord(&FileRecord{Key: "test-vid-multi", Filename: "movie.mkv"})

	svc := NewService(store, nil, coord, nil, nil)
	svc.SetSecret("cluster-super-secret-xyz")
	svc.SetAuthValidator(func(r *http.Request) bool { return true })

	mux := http.NewServeMux()
	svc.Register(mux)

	// 1. Test GET /api/v1/files/test-vid-multi/token
	req := httptest.NewRequest(http.MethodGet, "/api/v1/files/test-vid-multi/token", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp StreamURLResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if resp.Key != "test-vid-multi" {
		t.Fatalf("expected key 'test-vid-multi', got %s", resp.Key)
	}
	if resp.ExpiresInSec != 14400 {
		t.Fatalf("expected 4 hour default expiry (14400s), got %d", resp.ExpiresInSec)
	}
	if resp.VideoURL == "" || !ValidateStreamToken(resp.Key, resp.Token, fmt.Sprintf("%d", resp.ExpiresAt), "cluster-super-secret-xyz") {
		t.Fatalf("invalid token or video URL: %+v", resp)
	}

	// Verify audio tracks (2 tracks generated with individual signed URLs)
	if len(resp.AudioTracks) != 2 {
		t.Fatalf("expected 2 audio tracks, got %d", len(resp.AudioTracks))
	}
	for _, a := range resp.AudioTracks {
		if a.URL == "" {
			t.Fatalf("missing audio URL for track %d (%s)", a.Index, a.Language)
		}
	}

	// Verify subtitle tracks (2 subtitles generated with individual signed URLs)
	if len(resp.Subtitles) != 2 {
		t.Fatalf("expected 2 subtitles, got %d", len(resp.Subtitles))
	}
	for _, s := range resp.Subtitles {
		if s.URL == "" {
			t.Fatalf("missing subtitle URL for subtitle %d (%s)", s.Index, s.Language)
		}
	}

	// 2. Test GET /api/v1/files/test-vid-multi/streams (alias)
	reqAlias := httptest.NewRequest(http.MethodGet, "/api/v1/files/test-vid-multi/streams", nil)
	recAlias := httptest.NewRecorder()
	mux.ServeHTTP(recAlias, reqAlias)
	if recAlias.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for streams alias, got %d", recAlias.Code)
	}

	// 3. Test Status(): PlayURL and StreamURL must also be masked with ticket
	st, err := svc.Status("test-vid-multi")
	if err != nil {
		t.Fatalf("Status error: %v", err)
	}
	if !strings.HasPrefix(st.PlayURL, "/stream/t_") {
		t.Fatalf("expected PlayURL to use masked ticket /stream/t_..., got %s", st.PlayURL)
	}
	if !strings.Contains(st.StreamURL, "/stream/t_") {
		t.Fatalf("expected StreamURL to use masked ticket, got %s", st.StreamURL)
	}

	// 4. Test default GET /api/v1/files/test-vid-multi: must return the clean player StreamURLResponse
	reqDirect := httptest.NewRequest(http.MethodGet, "/api/v1/files/test-vid-multi", nil)
	recDirect := httptest.NewRecorder()
	mux.ServeHTTP(recDirect, reqDirect)
	if recDirect.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for direct GET, got %d", recDirect.Code)
	}
	var directResp StreamURLResponse
	if err := json.Unmarshal(recDirect.Body.Bytes(), &directResp); err != nil {
		t.Fatalf("failed to decode direct GET response: %v", err)
	}
	if directResp.Key != "test-vid-multi" || directResp.VideoURL == "" || len(directResp.AudioTracks) != 2 {
		t.Fatalf("unexpected direct player response: %+v", directResp)
	}

	// 5. Test GET /api/v1/files/test-vid-multi/info: returns full StatusResponse
	reqInfo := httptest.NewRequest(http.MethodGet, "/api/v1/files/test-vid-multi/info", nil)
	recInfo := httptest.NewRecorder()
	mux.ServeHTTP(recInfo, reqInfo)
	if recInfo.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for /info, got %d", recInfo.Code)
	}
	var infoResp StatusResponse
	if err := json.Unmarshal(recInfo.Body.Bytes(), &infoResp); err != nil {
		t.Fatalf("failed to decode /info response: %v", err)
	}
	if infoResp.Key != "test-vid-multi" || infoResp.State != StateCompleted {
		t.Fatalf("unexpected info response: %+v", infoResp)
	}

	// 6. Test GET /api/v1/files/test-vid-multi/metadata: returns media metadata
	reqMeta := httptest.NewRequest(http.MethodGet, "/api/v1/files/test-vid-multi/metadata", nil)
	recMeta := httptest.NewRecorder()
	mux.ServeHTTP(recMeta, reqMeta)
	if recMeta.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for /metadata, got %d", recMeta.Code)
	}
	var metaMap map[string]interface{}
	if err := json.Unmarshal(recMeta.Body.Bytes(), &metaMap); err != nil {
		t.Fatalf("failed to decode /metadata response: %v", err)
	}
	if _, hasAudio := metaMap["audio_tracks"]; !hasAudio {
		t.Fatalf("expected audio_tracks in /metadata response, got %+v", metaMap)
	}
}
