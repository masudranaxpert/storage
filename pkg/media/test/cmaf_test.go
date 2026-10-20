package media_test

import (
	"os"
	"testing"

	"stream/pkg/media"
)

func TestRemuxAndPackageCMAFFailOnNonexistentFile(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "cmaf_fail_test_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	_, err = media.RemuxAndPackageCMAF("nonexistent_video.mp4", tmpDir, "media_nonexistent")
	if err == nil {
		t.Errorf("expected remux to fail on nonexistent input file")
	}
}
