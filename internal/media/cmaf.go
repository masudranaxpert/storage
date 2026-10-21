package media

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// MediaChunk represents a single sliced CMAF chunk or single-file fMP4.
type MediaChunk struct {
	Index       int     `json:"index"`
	Filename    string  `json:"filename"`
	SizeBytes   int64   `json:"size_bytes"`
	DurationSec float64 `json:"duration_sec"`
	SHA256      string  `json:"sha256"`
	TrackType   string  `json:"track_type"`
	Tier        int     `json:"tier"`
}

// CMAFPackage represents the packaged CMAF media manifest and metadata.
type CMAFPackage struct {
	MediaID     string       `json:"media_id"`
	TotalChunks int          `json:"total_chunks"`
	TotalBytes  int64        `json:"total_bytes"`
	DurationSec float64      `json:"duration_sec"`
	InitChunk   *MediaChunk  `json:"init_chunk"`
	Chunks      []MediaChunk `json:"chunks"`
	CreatedAt   time.Time    `json:"created_at"`
}

// RemuxAndPackageCMAF prepares a single-file, fast-start MP4 for byte-range
// streaming. Video is copied when browser-decodable and re-encoded to H.264
// otherwise (PlanVideo); audio is copied when AAC/MP3, else encoded to AAC.
func RemuxAndPackageCMAF(srcPath, outputDir, mediaID string) (*CMAFPackage, error) {
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return nil, fmt.Errorf("create cmaf dir: %w", err)
	}

	targetMP4Path := filepath.Join(outputDir, "video.mp4")
	ffmpegPath, err := exec.LookPath("ffmpeg")
	if err != nil || ffmpegPath == "" {
		return nil, fmt.Errorf("ffmpeg not found on node: required for CMAF packaging")
	}

	ffprobePath, _ := exec.LookPath("ffprobe")
	srcMeta := &MediaMetadata{}
	probed := false
	if ffprobePath != "" {
		probed = enrichViaFFprobe(ffprobePath, srcPath, srcMeta) == nil
	}
	// Only the primary audio track is kept in this single-file package.
	tracks := srcMeta.AudioTracks
	if len(tracks) > 1 {
		tracks = tracks[:1]
	}

	plan := PlanVideo(srcMeta.Video, probed)
	out, runErr := runRemux(ffmpegPath, srcPath, outputDir, plan, tracks, probed, false)
	if runErr != nil && plan.Mode == VideoCopy {
		_ = os.Remove(targetMP4Path)
		plan = TranscodePlan("copy pass failed: " + lastLine(out))
		out, runErr = runRemux(ffmpegPath, srcPath, outputDir, plan, tracks, probed, true)
	}
	if runErr != nil {
		_ = os.Remove(targetMP4Path)
		return nil, fmt.Errorf("ffmpeg %s failed: %v: %s", plan.Mode, runErr, lastLines(out, 6))
	}

	stat, err := os.Stat(targetMP4Path)
	if err != nil {
		return nil, fmt.Errorf("stat target video: %w", err)
	}

	totalSize := stat.Size()
	return &CMAFPackage{
		MediaID:    mediaID,
		TotalBytes: totalSize,
		CreatedAt:  time.Now(),
		InitChunk: &MediaChunk{
			Index:     0,
			Filename:  "video.mp4",
			SizeBytes: totalSize,
			TrackType: "cmaf_fmp4",
			Tier:      2,
		},
	}, nil
}
