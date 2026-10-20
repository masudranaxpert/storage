package media_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
	"time"

	"stream/pkg/media"
)

func TestPackUnpackRoundTrip(t *testing.T) {
	src := t.TempDir()
	key := "abc123DEF456gh7"
	folder, err := media.PrepareMediaFolder(src, key, "movie.mkv")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(folder.VideoFilePath, []byte("fmp4-bytes-here"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := folder.SaveMetadata(&media.MediaMetadata{Key: key, Filename: folder.TargetFilename}); err != nil {
		t.Fatal(err)
	}
	// A nested deeper file plus an empty dir must survive the trip.
	if err := os.MkdirAll(filepath.Join(folder.BaseDir, "audio", "en"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(folder.BaseDir, "audio", "en", "a.m4s"), []byte("seg"), 0644); err != nil {
		t.Fatal(err)
	}

	size, err := media.CalculateFolderSize(folder.BaseDir)
	if err != nil || size <= 0 {
		t.Fatalf("expected folder size > 0, got %d, err: %v", size, err)
	}

	var buf bytes.Buffer
	if err := media.PackFolder(&buf, folder.BaseDir); err != nil {
		t.Fatalf("pack: %v", err)
	}

	dstRoot := t.TempDir()
	dst := filepath.Join(dstRoot, key)
	n, err := media.UnpackFolder(bytes.NewReader(buf.Bytes()), dst)
	if err != nil {
		t.Fatalf("unpack: %v", err)
	}
	if n != 3 {
		t.Fatalf("expected 3 files restored, got %d", n)
	}

	for _, rel := range []string{
		folder.TargetFilename,
		"metadata.json",
		filepath.Join("audio", "en", "a.m4s"),
	} {
		if _, err := os.Stat(filepath.Join(dst, rel)); err != nil {
			t.Errorf("restored file missing: %s (%v)", rel, err)
		}
	}
}

func TestUnpackFolderRejectsPathTraversal(t *testing.T) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	hdr := &tar.Header{
		Name: "../evil.txt",
		Mode: 0644,
		Size: 4,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte("evil")); err != nil {
		t.Fatal(err)
	}
	tw.Close()

	dst := t.TempDir()
	if _, err := media.UnpackFolder(bytes.NewReader(buf.Bytes()), dst); err == nil {
		t.Fatal("expected traversal reject, got nil")
	}
}

func TestUnpackGzipStream(t *testing.T) {
	var tarBuf bytes.Buffer
	tw := tar.NewWriter(&tarBuf)
	hdr := &tar.Header{
		Name: "test.txt",
		Mode: 0644,
		Size: 5,
	}
	_ = tw.WriteHeader(hdr)
	_, _ = tw.Write([]byte("hello"))
	tw.Close()

	var gzBuf bytes.Buffer
	gw := gzip.NewWriter(&gzBuf)
	_, _ = gw.Write(tarBuf.Bytes())
	gw.Close()

	dst := t.TempDir()
	n, err := media.UnpackFolder(bytes.NewReader(gzBuf.Bytes()), dst)
	if err != nil {
		t.Fatalf("gzip unpack failed: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 file, got %d", n)
	}
}

func TestSweepStaleScratch(t *testing.T) {
	root := t.TempDir()
	stale := filepath.Join(root, "stale_job")
	fresh := filepath.Join(root, "fresh_job")
	_ = os.MkdirAll(stale, 0755)
	_ = os.MkdirAll(fresh, 0755)

	oldTime := time.Now().Add(-2 * time.Hour)
	_ = os.Chtimes(stale, oldTime, oldTime)

	removed := media.SweepStaleScratch(root, 1*time.Hour)
	if len(removed) != 1 || filepath.Base(removed[0]) != "stale_job" {
		t.Fatalf("expected stale_job removed, got %v", removed)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatalf("fresh_job should remain, got %v", err)
	}
}
