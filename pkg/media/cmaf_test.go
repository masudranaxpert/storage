package media

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Eyevinn/mp4ff/mp4"
)

func TestHLSManifestGeneration(t *testing.T) {
	pkg := &CMAFPackage{
		MediaID:     "test_media_01",
		TotalChunks: 3,
		TotalBytes:  1024 * 1024 * 5,
		DurationSec: 12.0,
		InitChunk: &MediaChunk{
			Index:     0,
			Filename:  "test_media_01_init.mp4",
			SizeBytes: 1024,
			TrackType: "init",
			Tier:      0,
		},
		Chunks: []MediaChunk{
			{Index: 1, Filename: "test_media_01_seg_0001.m4s", DurationSec: 4.0, SizeBytes: 10000, Tier: 0},
			{Index: 2, Filename: "test_media_01_seg_0002.m4s", DurationSec: 4.0, SizeBytes: 12000, Tier: 0},
			{Index: 3, Filename: "test_media_01_seg_0003.m4s", DurationSec: 4.0, SizeBytes: 15000, Tier: 2},
		},
		CreatedAt: time.Now(),
	}

	manifest := GenerateHLSManifest(pkg, "http://10.0.0.1:1212/chunks")

	if !strings.Contains(manifest, "#EXT-X-VERSION:7") {
		t.Errorf("expected HLS v7 header, got:\n%s", manifest)
	}

	if !strings.Contains(manifest, "test_media_01_init.mp4") {
		t.Errorf("expected init chunk mapping in manifest, got:\n%s", manifest)
	}

	if !strings.Contains(manifest, "test_media_01_seg_0001.m4s") {
		t.Errorf("expected segment 1 in manifest, got:\n%s", manifest)
	}
}

func TestCMAFInitSegmentEncoding(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "cmaf_test_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	initSeg := mp4.CreateEmptyInit()
	initSeg.AddEmptyTrack(1000, "video", "und")

	outPath := filepath.Join(tmpDir, "init.mp4")
	f, err := os.Create(outPath)
	if err != nil {
		t.Fatalf("failed to create init file: %v", err)
	}
	defer f.Close()

	if err := initSeg.Encode(f); err != nil {
		t.Fatalf("failed to encode init segment: %v", err)
	}

	stat, err := f.Stat()
	if err != nil || stat.Size() == 0 {
		t.Errorf("init segment file is empty")
	}
}
