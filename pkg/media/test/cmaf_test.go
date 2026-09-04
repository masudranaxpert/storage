package media_test

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"stream/pkg/media"
)

func TestHLSManifestGeneration(t *testing.T) {
	pkg := &media.CMAFPackage{
		MediaID:     "test_media_01",
		TotalChunks: 3,
		TotalBytes:  1024 * 1024 * 5,
		DurationSec: 12.0,
		InitChunk: &media.MediaChunk{
			Index:     0,
			Filename:  "test_media_01_init.mp4",
			SizeBytes: 1024,
			TrackType: "init",
			Tier:      0,
		},
		Chunks: []media.MediaChunk{
			{Index: 1, Filename: "test_media_01_seg_0001.m4s", DurationSec: 4.0, SizeBytes: 10000, Tier: 0},
			{Index: 2, Filename: "test_media_01_seg_0002.m4s", DurationSec: 4.0, SizeBytes: 12000, Tier: 0},
			{Index: 3, Filename: "test_media_01_seg_0003.m4s", DurationSec: 4.0, SizeBytes: 15000, Tier: 2},
		},
		CreatedAt: time.Now(),
	}

	manifest := media.GenerateHLSManifest(pkg, "http://10.0.0.1:1212/chunks")

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

func TestMediaMetadataMasterM3U8JSON(t *testing.T) {
	meta := &media.MediaMetadata{
		Key:        "test_m3u8",
		MasterM3U8: "master.m3u8",
	}
	data, err := json.Marshal(meta)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}
	if !strings.Contains(string(data), `"master_m3u8":"master.m3u8"`) {
		t.Errorf("expected master_m3u8 in json, got: %s", string(data))
	}
}

