package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"stream/internal/model"
	"stream/internal/storage"
)

type rcloneLogLine struct {
	Stats *struct {
		Bytes      int64   `json:"bytes"`
		TotalBytes int64   `json:"totalBytes"`
		Speed      float64 `json:"speed"`
		ETA        *int64  `json:"eta"`
	} `json:"stats"`
}

// transferFolderRcloneS3 uploads a directory to S3/R2 with multi-part chunking,
// concurrent part streaming, and socket-level retries via rclone.
func (a *Agent) transferFolderRcloneS3(ctx context.Context, key, baseDir string, s3Cfg model.S3Config,
	prefix string, totalBytes int64, report ProgressReporterFunc) error {

	// 1. Verify rclone exists (PATH and /opt/stream/bin)
	rcloneBin, err := exec.LookPath("rclone")
	if err != nil {
		if _, statErr := os.Stat("/opt/stream/bin/rclone"); statErr == nil {
			rcloneBin = "/opt/stream/bin/rclone"
		} else {
			return fmt.Errorf("rclone is required for S3/R2 multipart uploads but is not installed on the system (please install rclone): %w", err)
		}
	}

	storage.NormalizeS3Config(&s3Cfg)

	provider := "Other"
	if strings.Contains(s3Cfg.Endpoint, "r2.cloudflarestorage.com") {
		provider = "Cloudflare"
	} else if strings.Contains(s3Cfg.Endpoint, "amazonaws.com") {
		provider = "AWS"
	}

	region := s3Cfg.Region
	if region == "" {
		region = "auto"
	}

	env := append(os.Environ(),
		"RCLONE_CONFIG_STREAM_S3_TYPE=s3",
		"RCLONE_CONFIG_STREAM_S3_PROVIDER="+provider,
		"RCLONE_CONFIG_STREAM_S3_ENDPOINT="+s3Cfg.Endpoint,
		"RCLONE_CONFIG_STREAM_S3_ACCESS_KEY_ID="+s3Cfg.AccessKey,
		"RCLONE_CONFIG_STREAM_S3_SECRET_ACCESS_KEY="+s3Cfg.SecretKey,
		"RCLONE_CONFIG_STREAM_S3_REGION="+region,
	)

	targetFs := fmt.Sprintf("STREAM_S3:%s/%s", s3Cfg.Name, strings.Trim(prefix, "/"))

	cmd := exec.CommandContext(ctx, rcloneBin, "copy",
		baseDir,
		targetFs,
		"--s3-chunk-size=32M",
		"--s3-upload-concurrency=4",
		"--transfers=4",
		"--checkers=8",
		"--retries=5",
		"--low-level-retries=10",
		"--stats=1s",
		"--use-json-log",
	)
	cmd.Env = env

	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("failed to pipe rclone stderr: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to spawn rclone copy: %w", err)
	}

	go func() {
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			line := scanner.Bytes()
			var logEntry rcloneLogLine
			if err := json.Unmarshal(line, &logEntry); err == nil && logEntry.Stats != nil {
				stats := logEntry.Stats
				copied := stats.Bytes
				total := totalBytes
				if stats.TotalBytes > 0 {
					total = stats.TotalBytes
				}
				var pct float64
				if total > 0 {
					pct = (float64(copied) / float64(total)) * 100.0
				}
				if report != nil {
					report("transferring", 90.0+(pct*0.1), "Uploading to S3 (rclone)", formatTransferSpeed(stats.Speed), stats.ETA, map[string]interface{}{
						"stage":             "transfer",
						"stage_name":        "Syncing to S3 (Multipart)",
						"stage_percent":     pct,
						"transferred_bytes": copied,
						"total_bytes":       total,
					})
				}
			}
		}
	}()

	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("rclone copy to S3 failed: %w", err)
	}

	return nil
}
