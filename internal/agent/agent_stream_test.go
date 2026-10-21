package agent

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"stream/internal/cluster"
)

func newMediaTestAgent(t *testing.T, secret string) *Agent {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "abc123"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"master.m3u8":  "#EXTM3U\n",
		"stream_0.m4s": "segment-bytes",
	} {
		if err := os.WriteFile(filepath.Join(dir, "abc123", name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return &Agent{MediaDir: dir, Secret: secret, extraRoots: make(map[string]bool)}
}

func serveMedia(a *Agent, target string, hdr map[string]string) int {
	return serveMediaFrom(a, target, "198.51.100.1:5000", hdr)
}

func serveMediaFrom(a *Agent, target, remoteAddr string, hdr map[string]string) int {
	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.RemoteAddr = remoteAddr
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	a.handleMedia(rec, req)
	return rec.Code
}

func TestHandleMediaFailsClosedWithoutSecret(t *testing.T) {
	a := newMediaTestAgent(t, "")
	if got := serveMedia(a, "/stream/abc123/master.m3u8", nil); got != http.StatusForbidden {
		t.Fatalf("empty secret, no token: want 403, got %d", got)
	}
	// A token signed with an empty secret must not unlock anything.
	exp := time.Now().Add(time.Hour).Unix()
	target := fmt.Sprintf("/stream/abc123/master.m3u8?token=%s&exp=%d", cluster.GenerateStreamToken("abc123", exp, ""), exp)
	if got := serveMedia(a, target, nil); got != http.StatusForbidden {
		t.Fatalf("empty secret, forged token: want 403, got %d", got)
	}
}

func TestHandleMediaTokenAndSecret(t *testing.T) {
	const secret = "test-secret"
	a := newMediaTestAgent(t, secret)

	if got := serveMedia(a, "/stream/abc123/master.m3u8", nil); got != http.StatusForbidden {
		t.Fatalf("no token: want 403, got %d", got)
	}

	exp := time.Now().Add(time.Hour).Unix()
	good := fmt.Sprintf("/stream/abc123/master.m3u8?token=%s&exp=%d", cluster.GenerateStreamToken("abc123", exp, secret), exp)
	if got := serveMedia(a, good, nil); got != http.StatusOK {
		t.Fatalf("valid token: want 200, got %d", got)
	}

	past := time.Now().Add(-time.Hour).Unix()
	expired := fmt.Sprintf("/stream/abc123/master.m3u8?token=%s&exp=%d", cluster.GenerateStreamToken("abc123", past, secret), past)
	if got := serveMedia(a, expired, nil); got != http.StatusForbidden {
		t.Fatalf("expired token: want 403, got %d", got)
	}

	other := fmt.Sprintf("/stream/abc123/master.m3u8?token=%s&exp=%d", cluster.GenerateStreamToken("other", exp, secret), exp)
	if got := serveMedia(a, other, nil); got != http.StatusForbidden {
		t.Fatalf("token for another key: want 403, got %d", got)
	}

	if got := serveMedia(a, "/stream/abc123/master.m3u8", map[string]string{"X-Cluster-Secret": secret}); got != http.StatusOK {
		t.Fatalf("cluster secret header: want 200, got %d", got)
	}
}

func TestHandleMediaTicket(t *testing.T) {
	const secret = "test-secret"
	a := newMediaTestAgent(t, secret)

	exp := time.Now().Add(time.Hour).Unix()
	ticket, err := cluster.GeneratePlaybackTicket("abc123", exp, secret)
	if err != nil {
		t.Fatal(err)
	}
	if got := serveMedia(a, "/stream/"+ticket+"/master.m3u8", nil); got != http.StatusOK {
		t.Fatalf("valid ticket: want 200, got %d", got)
	}
	if got := serveMedia(a, "/stream/"+ticket+"/stream_0.m4s", nil); got != http.StatusOK {
		t.Fatalf("valid ticket, segment: want 200, got %d", got)
	}

	past := time.Now().Add(-time.Hour).Unix()
	old, err := cluster.GeneratePlaybackTicket("abc123", past, secret)
	if err != nil {
		t.Fatal(err)
	}
	if got := serveMedia(a, "/stream/"+old+"/master.m3u8", nil); got != http.StatusForbidden {
		t.Fatalf("expired ticket: want 403, got %d", got)
	}

	wrong, err := cluster.GeneratePlaybackTicket("abc123", exp, "another-secret")
	if err != nil {
		t.Fatal(err)
	}
	if got := serveMedia(a, "/stream/"+wrong+"/master.m3u8", nil); got != http.StatusForbidden {
		t.Fatalf("ticket from another secret: want 403, got %d", got)
	}
}

func TestHandleMediaRequiresAFile(t *testing.T) {
	const secret = "test-secret"
	a := newMediaTestAgent(t, secret)
	hdr := map[string]string{"X-Cluster-Secret": secret}

	if got := serveMedia(a, "/stream/abc123", hdr); got != http.StatusNotFound {
		t.Fatalf("bare key: want 404, got %d", got)
	}
	if got := serveMedia(a, "/stream/abc123/missing.m4s", hdr); got != http.StatusNotFound {
		t.Fatalf("unknown file: want 404, got %d", got)
	}
	if got := serveMedia(a, "/stream/abc123/../abc123/master.m3u8", hdr); got == http.StatusOK {
		t.Fatalf("dot-dot path must not be served, got %d", got)
	}
	if got := serveMedia(a, "/stream/abc123/%2e%2e/secret", hdr); got == http.StatusOK {
		t.Fatalf("encoded traversal must not be served, got %d", got)
	}
}

// coordinatorSpy counts the hit and heartbeat callbacks the agent sends to the coordinator.
type coordinatorSpy struct {
	hits, heartbeats atomic.Int32
}

func (c *coordinatorSpy) server(t *testing.T) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/lifecycle/hit":
			c.hits.Add(1)
		case "/api/stream/heartbeat":
			c.heartbeats.Add(1)
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(ts.Close)
	return ts
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestHandleMediaCountsPlaysOnMasterOnly(t *testing.T) {
	const secret = "test-secret"
	spy := &coordinatorSpy{}
	a := newMediaTestAgent(t, secret)
	a.CoordinatorURL = spy.server(t).URL
	hdr := map[string]string{"X-Cluster-Secret": secret}

	// A viewer that only fetches a segment keeps the session alive but is not a new play.
	if got := serveMediaFrom(a, "/stream/abc123/stream_0.m4s", "203.0.113.10:1111", hdr); got != http.StatusOK {
		t.Fatalf("segment: want 200, got %d", got)
	}
	waitFor(t, "segment heartbeat", func() bool { return spy.heartbeats.Load() == 1 })
	if spy.hits.Load() != 0 {
		t.Fatalf("segment request counted as a play: %d hits", spy.hits.Load())
	}

	// Fetching the master playlist from a new client starts a play.
	if got := serveMediaFrom(a, "/stream/abc123/master.m3u8", "203.0.113.11:2222", hdr); got != http.StatusOK {
		t.Fatalf("master: want 200, got %d", got)
	}
	waitFor(t, "play hit", func() bool { return spy.hits.Load() == 1 })
	waitFor(t, "master heartbeat", func() bool { return spy.heartbeats.Load() == 2 })

	// The same client re-fetching the playlist is deduplicated.
	serveMediaFrom(a, "/stream/abc123/master.m3u8", "203.0.113.11:2223", hdr)
	time.Sleep(100 * time.Millisecond)
	if spy.hits.Load() != 1 {
		t.Fatalf("repeat play not deduplicated: %d hits", spy.hits.Load())
	}
}

func TestHandleMediaIgnoresSidecarsForPlayCounting(t *testing.T) {
	const secret = "test-secret"
	spy := &coordinatorSpy{}
	a := newMediaTestAgent(t, secret)
	a.CoordinatorURL = spy.server(t).URL
	if err := os.WriteFile(filepath.Join(a.MediaDir, "abc123", "subtitle_1_eng.vtt"), []byte("WEBVTT"), 0o644); err != nil {
		t.Fatal(err)
	}

	if got := serveMediaFrom(a, "/stream/abc123/subtitle_1_eng.vtt", "203.0.113.20:3333",
		map[string]string{"X-Cluster-Secret": secret}); got != http.StatusOK {
		t.Fatalf("subtitle: want 200, got %d", got)
	}
	time.Sleep(100 * time.Millisecond)
	if spy.hits.Load() != 0 || spy.heartbeats.Load() != 0 {
		t.Fatalf("subtitle fetch recorded playback: hits=%d heartbeats=%d", spy.hits.Load(), spy.heartbeats.Load())
	}
}
