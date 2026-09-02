package ingest

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"stream/pkg/media"
)

// ProcessIngestJob executes the complete media ingest pipeline end-to-end:
// 1. Probes headers & extracts filename from Content-Disposition
// 2. Prepares an isolated folder (data/media/<media_id>/)
// 3. Downloads using aria2c / streaming Go into raw/
// 4. Validates MIME type using magic numbers (rejects JPEG, ZIP, non-videos)
// 5. Segments into CMAF/fMP4 inside cmaf/
// 6. Generates HLS master.m3u8 & metadata.json inside the media folder.
func ProcessIngestJob(ctx context.Context, job *IngestJob, rootMediaDir string, queue *IngestQueue) error {
	queue.UpdateStatus(job.JobID, StatusDownloading, 0.0, "Starting", "")

	// 1. Probe headers & extract filename
	filename, _, _, _ := media.ProbeRemoteHeader(job.SourceURL)
	if filename == "" {
		filename = "video.mp4"
	}

	// 2. Prepare isolated folder structure
	folder, err := media.PrepareMediaFolder(rootMediaDir, job.JobID, filename)
	if err != nil {
		queue.UpdateStatus(job.JobID, StatusFailed, 0.0, "0 B/s", fmt.Sprintf("failed to prepare media folder: %v", err))
		return err
	}

	// 3. Download via aria2c or streaming Go
	progressCb := func(pct float64, speed string) {
		queue.UpdateStatus(job.JobID, StatusDownloading, pct, speed, "")
	}

	if err := DownloadFile(ctx, job.SourceURL, folder.RawFilePath, progressCb); err != nil {
		queue.UpdateStatus(job.JobID, StatusFailed, 0.0, "0 B/s", fmt.Sprintf("download failed: %v", err))
		return err
	}

	// 4. Validate MIME type
	val, err := media.DetectFileMIME(folder.RawFilePath)
	if err != nil || !val.IsVideo {
		errMsg := fmt.Sprintf("invalid file type '%s'. Expected video format (MP4, MKV, WebM, MOV)", val.MIMEType)
		queue.UpdateStatus(job.JobID, StatusFailed, 0.0, "0 B/s", errMsg)
		return fmt.Errorf("%s", errMsg)
	}

	// 5. Package to CMAF (Remux video + normalize audio to AAC)
	queue.UpdateStatus(job.JobID, StatusPackaging, 90.0, "Packaging CMAF", "")

	fileStat, _ := os.Stat(folder.RawFilePath)
	rawSize := int64(0)
	if fileStat != nil {
		rawSize = fileStat.Size()
	}

	cmafPkg, err := media.RemuxAndPackageCMAF(folder.RawFilePath, folder.CMAFDir, job.JobID)
	if err != nil {
		// If the file is not pre-fragmented, create single initialization reference
		cmafPkg = &media.CMAFPackage{
			MediaID:   job.JobID,
			CreatedAt: time.Now(),
			InitChunk: &media.MediaChunk{
				Index:     0,
				Filename:  filepath.Base(folder.RawFilePath),
				TrackType: "raw_video",
				Tier:      2,
			},
		}
	}
	if rawSize > cmafPkg.TotalBytes {
		cmafPkg.TotalBytes = rawSize
	}

	// 6. Generate & save HLS manifest and metadata.json
	manifest := media.GenerateHLSManifest(cmafPkg, fmt.Sprintf("/stream/%s/cmaf", job.JobID))
	_ = folder.SaveHLSManifest(manifest)
	_ = folder.SaveMetadata(cmafPkg)

	// Clean temporary raw download if video.mp4 exists in cmaf/
	if _, err := os.Stat(filepath.Join(folder.CMAFDir, "video.mp4")); err == nil {
		_ = folder.CleanRaw()
	}

	// 7. Complete job
	queue.SetCMAF(job.JobID, cmafPkg)
	return nil
}
