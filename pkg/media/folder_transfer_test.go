package media

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPackUnpackRoundTrip(t *testing.T) {
	src := t.TempDir()
	key := "abc123DEF456gh7"
	folder, err := PrepareMediaFolder(src, key, "movie.mkv")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(folder.CMAFDir, "video.mp4"), []byte("fmp4-bytes-here"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := folder.SaveHLSManifest("#EXTM3U\n"); err != nil {
		t.Fatal(err)
	}
	if err := folder.SaveMetadata(&CMAFPackage{MediaID: key}); err != nil {
		t.Fatal(err)
	}
	// A nested deeper file plus an empty dir must survive the trip.
	if err := os.MkdirAll(filepath.Join(folder.CMAFDir, "audio", "en"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(folder.CMAFDir, "audio", "en", "a.m4s"), []byte("seg"), 0644); err != nil {
		t.Fatal(err)
	}

	size, err := CalculateFolderSize(folder.BaseDir)
	if err != nil || size <= 0 {
		t.Fatalf("expected folder size > 0, got %d, err: %v", size, err)
	}

	var buf bytes.Buffer
	if err := PackFolder(&buf, folder.BaseDir); err != nil {
		t.Fatalf("pack: %v", err)
	}

	dstRoot := t.TempDir()
	dst := filepath.Join(dstRoot, key)
	n, err := UnpackFolder(bytes.NewReader(buf.Bytes()), dst)
	if err != nil {
		t.Fatalf("unpack: %v", err)
	}
	if n != 4 {
		t.Fatalf("expected 4 files restored, got %d", n)
	}

	for _, rel := range []string{
		filepath.Join("cmaf", "video.mp4"),
		"master.m3u8",
		"metadata.json",
		filepath.Join("cmaf", "audio", "en", "a.m4s"),
	} {
		if _, err := os.Stat(filepath.Join(dst, rel)); err != nil {
			t.Errorf("restored file missing: %s (%v)", rel, err)
		}
	}
}

func TestUnpackLegacyGzip(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)

	hdr := &tar.Header{
		Name: "./master.m3u8",
		Mode: 0644,
		Size: int64(len("#EXTM3U\n")),
	}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte("#EXTM3U\n")); err != nil {
		t.Fatal(err)
	}
	tw.Close()
	gz.Close()

	dst := t.TempDir()
	n, err := UnpackFolder(bytes.NewReader(buf.Bytes()), dst)
	if err != nil {
		t.Fatalf("unpack legacy gzip: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 file restored, got %d", n)
	}
	if _, err := os.Stat(filepath.Join(dst, "master.m3u8")); err != nil {
		t.Fatal("master.m3u8 not restored from legacy gzip stream")
	}
}

func TestUnpackRejectsTraversal(t *testing.T) {
	// Direct path-validation checks for the sanitiser.
	root := t.TempDir()
	if err := safeMkdirAll(root, "../escape"); err == nil {
		t.Fatal("relative traversal must be rejected")
	}
	if err := safeMkdirAll(root, `C:\absolute`); err == nil {
		t.Fatal("absolute path must be rejected")
	}
}

func TestSweepStaleScratch(t *testing.T) {
	dir := t.TempDir()
	stale := filepath.Join(dir, "oldjob")
	fresh := filepath.Join(dir, "newjob")
	if err := os.MkdirAll(stale, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(fresh, 0755); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}

	removed := SweepStaleScratch(dir, 24*time.Hour)
	if len(removed) != 1 || removed[0] != stale {
		t.Fatalf("expected only stale dir removed, got %v", removed)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatal("fresh scratch must survive the sweep")
	}
}
