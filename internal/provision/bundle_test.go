package provision

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"testing"
)

func TestIsStaticELF(t *testing.T) {
	// Host dynamic binary should not be recognized as static
	if isStaticELF("/usr/bin/ffmpeg") {
		t.Errorf("expected /usr/bin/ffmpeg to be dynamic, got static")
	}

	// Statically linked binaries in bin/ should be recognized
	for _, tool := range []string{"ffmpeg", "ffprobe", "aria2c", "rclone"} {
		if path, found := FindStaticBinary(tool); found {
			if !isStaticELF(path) {
				t.Errorf("expected %s at %s to be static", tool, path)
			}
		}
	}
}

func TestBuildWorkerBundleContents(t *testing.T) {
	bundleBytes, err := BuildOrGetWorkerBundle()
	if err != nil {
		t.Fatalf("BuildOrGetWorkerBundle failed: %v", err)
	}

	gr, err := gzip.NewReader(bytes.NewReader(bundleBytes))
	if err != nil {
		t.Fatalf("gzip.NewReader failed: %v", err)
	}
	defer gr.Close()

	tr := tar.NewReader(gr)
	foundFiles := make(map[string]bool)

	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("tar read error: %v", err)
		}
		foundFiles[hdr.Name] = true
	}

	expected := []string{
		"stream/bin/stream",
		"stream/bin/ffmpeg",
		"stream/bin/ffprobe",
		"stream/bin/aria2c",
	}

	for _, exp := range expected {
		if !foundFiles[exp] {
			t.Errorf("expected bundle to contain %s, but missing", exp)
		}
	}
}
