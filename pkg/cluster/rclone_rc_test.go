package cluster

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRcloneRCClient_Parsing(t *testing.T) {
	// Mock server mimicking rclone rcd endpoints
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok || user != "admin" || pass != "testsecret" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/rc/noop":
			w.Write([]byte(`{}`))
		case "/core/obscure":
			w.Write([]byte(`{"obscured":"obscured_token_xyz"}`))
		case "/sync/copy":
			var payload map[string]interface{}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				http.Error(w, err.Error(), 400)
				return
			}
			if payload["srcFs"] == "" || payload["dstFs"] == "" {
				http.Error(w, "missing fs", 400)
				return
			}
			w.Write([]byte(`{"jobid": 42}`))
		case "/job/status":
			w.Write([]byte(`{"id":42,"finished":true,"success":true,"error":"","duration":1.25}`))
		case "/core/stats":
			w.Write([]byte(`{"bytes":1048576,"totalBytes":2097152,"speed":524288,"elapsedTime":2.0,"transfers":8}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer mockServer.Close()

	client := NewRcloneRCClient(mockServer.URL, "admin", "testsecret")
	ctx := context.Background()

	// 1. Noop
	if err := client.Noop(ctx); err != nil {
		t.Fatalf("Noop failed: %v", err)
	}

	// 2. Obscure
	obscured, err := client.Obscure(ctx, "myPassword")
	if err != nil || obscured != "obscured_token_xyz" {
		t.Fatalf("Obscure failed: %v, got: %s", err, obscured)
	}

	// 3. SyncCopy
	jobID, err := client.SyncCopy(ctx, "/src", "/dst", 8)
	if err != nil || jobID != 42 {
		t.Fatalf("SyncCopy failed: %v, got jobID: %d", err, jobID)
	}

	// 4. JobStatus
	status, err := client.GetJobStatus(ctx, 42)
	if err != nil || !status.Finished || !status.Success {
		t.Fatalf("GetJobStatus failed: %v, status: %+v", err, status)
	}

	// 5. GetStats
	stats, err := client.GetStats(ctx)
	if err != nil || stats.Bytes != 1048576 || stats.Speed != 524288 {
		t.Fatalf("GetStats failed: %v, stats: %+v", err, stats)
	}
}

func TestAgent_HandleWebDAV(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "agent_webdav_test_*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempDir)

	mediaDir := filepath.Join(tempDir, "media")
	if err := os.MkdirAll(mediaDir, 0755); err != nil {
		t.Fatal(err)
	}

	agent := &Agent{
		NodeID:   "test-node",
		Secret:   "cluster-pass-123",
		MediaDir: mediaDir,
	}

	key := "testMediaKey123"
	hexDir := hex.EncodeToString([]byte(mediaDir))

	// PUT request to /api/v1/webdav/<hexDir>/<key>/manifest.m3u8
	putURL := "/api/v1/webdav/" + hexDir + "/" + key + "/manifest.m3u8"
	req := httptest.NewRequest("PUT", putURL, strings.NewReader("#EXTM3U\n#EXT-X-VERSION:7\n"))
	req.SetBasicAuth("admin", "cluster-pass-123")
	rec := httptest.NewRecorder()

	agent.handleWebDAV(rec, req)

	if rec.Code != http.StatusCreated && rec.Code != http.StatusOK && rec.Code != http.StatusNoContent {
		t.Fatalf("handleWebDAV PUT expected 201/200/204, got %d (body: %s)", rec.Code, rec.Body.String())
	}

	// Verify file was written to disk at expected location
	writtenPath := filepath.Join(mediaDir, key, "manifest.m3u8")
	content, err := os.ReadFile(writtenPath)
	if err != nil {
		t.Fatalf("Failed to read file from disk: %v", err)
	}
	if !strings.Contains(string(content), "#EXTM3U") {
		t.Fatalf("File content mismatch, got: %s", string(content))
	}
}
