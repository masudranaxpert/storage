package media_test

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"stream/pkg/media"
)

func TestExtractFilenameFromHeaderAndURL(t *testing.T) {
	// Case 1: RFC 6266 Content-Disposition header
	h1 := http.Header{}
	h1.Set("Content-Disposition", `attachment; filename="avengers_endgame.mp4"`)
	name1 := media.ExtractFilenameFromHeaderAndURL(h1, "http://example.com/download?id=123")
	if name1 != "avengers_endgame.mp4" {
		t.Errorf("expected 'avengers_endgame.mp4', got '%s'", name1)
	}

	// Case 2: URL fallback with query params
	name2 := media.ExtractFilenameFromHeaderAndURL(nil, "https://cdn.example.com/videos/stream_4k_test.mkv?token=abc123xyz")
	if name2 != "stream_4k_test.mkv" {
		t.Errorf("expected 'stream_4k_test.mkv', got '%s'", name2)
	}

	// Case 3: Sanitization of path traversals and dirty names
	dirty := "../../etc/passwd dirty video name!@#.mp4"
	clean := media.SanitizeFilename(dirty)
	if clean != "passwd_dirty_video_name.mp4" {
		t.Errorf("expected sanitized name 'passwd_dirty_video_name.mp4', got '%s'", clean)
	}
}

func TestMIMEDetectionRejectsNonVideo(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "mime_test_*")
	if err != nil {
		t.Fatalf("temp dir error: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create a text file disguised as .mp4
	fakePath := filepath.Join(tmpDir, "fake_video.mp4")
	_ = os.WriteFile(fakePath, []byte("Hello world this is not a video"), 0644)

	err = media.ValidateVideoFile(fakePath)
	if err == nil {
		t.Errorf("expected error rejecting text file, but got nil")
	}
}
