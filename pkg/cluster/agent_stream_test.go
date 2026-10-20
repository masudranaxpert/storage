package cluster

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func newMediaTestAgent(t *testing.T, secret string) *Agent {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "abc123"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "abc123", "movie.mp4"), []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	return &Agent{MediaDir: dir, Secret: secret, extraRoots: make(map[string]bool)}
}

func serveMedia(a *Agent, target string, hdr map[string]string) int {
	req := httptest.NewRequest(http.MethodGet, target, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	a.handleMedia(rec, req)
	return rec.Code
}

func TestHandleMediaFailsClosedWithoutSecret(t *testing.T) {
	a := newMediaTestAgent(t, "")
	if got := serveMedia(a, "/stream/abc123/movie.mp4", nil); got != http.StatusForbidden {
		t.Fatalf("empty secret, no token: want 403, got %d", got)
	}
	// A token signed with an empty secret must not unlock anything.
	exp := time.Now().Add(time.Hour).Unix()
	target := fmt.Sprintf("/stream/abc123/movie.mp4?token=%s&exp=%d", GenerateStreamToken("abc123", exp, ""), exp)
	if got := serveMedia(a, target, nil); got != http.StatusForbidden {
		t.Fatalf("empty secret, forged token: want 403, got %d", got)
	}
}

func TestHandleMediaTokenAndSecret(t *testing.T) {
	const secret = "test-secret"
	a := newMediaTestAgent(t, secret)

	if got := serveMedia(a, "/stream/abc123/movie.mp4", nil); got != http.StatusForbidden {
		t.Fatalf("no token: want 403, got %d", got)
	}

	exp := time.Now().Add(time.Hour).Unix()
	good := fmt.Sprintf("/stream/abc123/movie.mp4?token=%s&exp=%d", GenerateStreamToken("abc123", exp, secret), exp)
	if got := serveMedia(a, good, nil); got != http.StatusOK {
		t.Fatalf("valid token: want 200, got %d", got)
	}

	past := time.Now().Add(-time.Hour).Unix()
	expired := fmt.Sprintf("/stream/abc123/movie.mp4?token=%s&exp=%d", GenerateStreamToken("abc123", past, secret), past)
	if got := serveMedia(a, expired, nil); got != http.StatusForbidden {
		t.Fatalf("expired token: want 403, got %d", got)
	}

	other := fmt.Sprintf("/stream/abc123/movie.mp4?token=%s&exp=%d", GenerateStreamToken("other", exp, secret), exp)
	if got := serveMedia(a, other, nil); got != http.StatusForbidden {
		t.Fatalf("token for another key: want 403, got %d", got)
	}

	if got := serveMedia(a, "/stream/abc123/movie.mp4", map[string]string{"X-Cluster-Secret": secret}); got != http.StatusOK {
		t.Fatalf("cluster secret header: want 200, got %d", got)
	}
}

func TestHandleMediaTicket(t *testing.T) {
	const secret = "test-secret"
	a := newMediaTestAgent(t, secret)

	exp := time.Now().Add(time.Hour).Unix()
	ticket, err := GeneratePlaybackTicket("abc123", exp, secret)
	if err != nil {
		t.Fatal(err)
	}
	if got := serveMedia(a, "/stream/"+ticket+"/movie.mp4", nil); got != http.StatusOK {
		t.Fatalf("valid ticket: want 200, got %d", got)
	}

	past := time.Now().Add(-time.Hour).Unix()
	old, err := GeneratePlaybackTicket("abc123", past, secret)
	if err != nil {
		t.Fatal(err)
	}
	if got := serveMedia(a, "/stream/"+old+"/movie.mp4", nil); got != http.StatusForbidden {
		t.Fatalf("expired ticket: want 403, got %d", got)
	}

	wrong, err := GeneratePlaybackTicket("abc123", exp, "another-secret")
	if err != nil {
		t.Fatal(err)
	}
	if got := serveMedia(a, "/stream/"+wrong+"/movie.mp4", nil); got != http.StatusForbidden {
		t.Fatalf("ticket from another secret: want 403, got %d", got)
	}
}
