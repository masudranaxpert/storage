package cluster

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"stream/pkg/download"
	"stream/pkg/media"
)

func newTransferHTTPClient() *http.Client {
	dialer := &net.Dialer{
		Timeout:   30 * time.Second,
		KeepAlive: 30 * time.Second,
	}
	return &http.Client{
		Timeout: 2 * time.Hour,
		Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			DialContext:           dialer.DialContext,
			ForceAttemptHTTP2:     false, // one long HTTP/1.1 upload stream
			MaxIdleConns:          64,
			MaxIdleConnsPerHost:   32,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   20 * time.Second,
			ExpectContinueTimeout: 0,
			DisableCompression:    true,
			WriteBufferSize:       1 << 20, // 1 MiB
			ReadBufferSize:        1 << 20,
			ResponseHeaderTimeout: 30 * time.Minute,
		},
	}
}

// RunIngestJob downloads a source media file and packages it into CMAF on this
// node, streaming live progress back to the coordinator through the given
// reporter (persistent gRPC mesh stream, or legacy HTTP POST fallback).
func (a *Agent) RunIngestJob(jobID, srcURL string, report ProgressReporter) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
	defer cancel()

	report("downloading", "Starting", 0.0, "", nil)

	filename, _, _, _ := media.ProbeRemoteHeader(srcURL)
	if filename == "" {
		filename = "video.mp4"
	}

	folder, err := media.PrepareMediaFolder(a.MediaDir, jobID, filename)
	if err != nil {
		report("failed", "0 B/s", 0.0, fmt.Sprintf("folder prep failed: %v", err), nil)
		return
	}

	dlProgress := func(pct float64, speed string) {
		report("downloading", speed, pct, "", nil)
	}

	if err := download.DownloadFile(ctx, srcURL, folder.RawFilePath, dlProgress); err != nil {
		report("failed", "0 B/s", 0.0, fmt.Sprintf("download failed: %v", err), nil)
		return
	}

	report("packaging", "Processing streamable media", 90.0, "", nil)
	meta, err := media.RemuxToStreamableMP4(folder.RawFilePath, folder.VideoFilePath, jobID, filename)
	if err != nil {
		report("failed", "0 B/s", 0.0, fmt.Sprintf("remux failed: %v", err), nil)
		return
	}
	_ = folder.SaveMetadata(meta)

	if meta != nil {
		_ = folder.CleanRaw()
	}

	cmafPkg := meta.ToCMAFPackage()
	report("completed", "Ready", 100.0, "", cmafPkg)
}

// fileProgressReporter posts file-job state transitions to the master's
// v1 progress callback over the same coordinator URL used for heartbeats.
// ProgressReporterFunc represents the standard progress callback signature with optional stage metadata.
type ProgressReporterFunc func(state string, pct float64, speed, errMsg string, meta interface{}, extra ...map[string]interface{})

// State strings mirror fileapi.FileState values without importing it (the
// fileapi package imports cluster for node records).
func (a *Agent) fileProgressReporter(key string) ProgressReporterFunc {
	report := func(state string, pct float64, speed, errMsg string, meta interface{}, extra ...map[string]interface{}) {
		payload := map[string]interface{}{
			"state":            state,
			"progress_percent": pct,
			"speed":            speed,
			"error":            errMsg,
		}
		if len(extra) > 0 && extra[0] != nil {
			for k, v := range extra[0] {
				payload[k] = v
			}
		}
		if meta != nil {
			if data, err := json.Marshal(meta); err == nil {
				payload["cmaf_json"] = data
			}
		}
		body, _ := json.Marshal(payload)
		url := fmt.Sprintf("%s/api/v1/files/%s/progress", strings.TrimRight(a.CoordinatorURL, "/"), key)
		req, err := http.NewRequest(http.MethodPost, url, bytes.NewBuffer(body))
		if err != nil {
			fmt.Printf("[Agent %s] ⚠️ file progress request build failed for %s: %v\n", a.NodeID, key, err)
			return
		}
		req.Header.Set("Content-Type", "application/json")
		if a.Secret != "" {
			req.Header.Set("X-Cluster-Secret", a.Secret)
		}
		resp, err := a.client.Do(req)
		if err != nil {
			fmt.Printf("[Agent %s] ⚠️ file progress post failed for %s: %v\n", a.NodeID, key, err)
			return
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
	return report
}

// RunFileJob is the worker half of the v1 file API: optionally download the
// source (URL mode), validate magic bytes, package to CMAF, and report every
// transition to the master. Direct mode (final == nil): everything happens
// in targetDir, a tier block this node owns. Decoupled mode (final != nil):
// the job runs in this node's scratch, the finished folder is streamed to
// the block owner (final), and the scratch copy is deleted.
func (a *Agent) RunFileJob(key, srcURL, filename, targetDir string, final *TransferTarget) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
	defer cancel()

	report := a.fileProgressReporter(key)
	if final == nil {
		a.registerRoot(targetDir)
	}

	folder, err := media.PrepareMediaFolder(targetDir, key, filename)
	if err != nil {
		report("failed", 0, "0 B/s", fmt.Sprintf("folder prep failed: %v", err), nil)
		return
	}

	if final != nil {
		defer func() {
			_ = os.RemoveAll(folder.BaseDir)
		}()
	}

	if srcURL != "" {
		report("downloading", 0, "Starting download...", "", nil, map[string]interface{}{
			"stage":         "download",
			"stage_name":    "Downloading Source (16-thread)",
			"stage_percent": 0.0,
		})
		dlProgress := func(pct float64, speed string) {
			report("downloading", pct, speed, "", nil, map[string]interface{}{
				"stage":         "download",
				"stage_name":    "Downloading Source (16-thread)",
				"stage_percent": pct,
			})
		}
		if err := download.DownloadFile(ctx, srcURL, folder.RawFilePath, dlProgress); err != nil {
			report("failed", 0, "0 B/s", fmt.Sprintf("download failed: %v", err), nil, map[string]interface{}{
				"stage": "failed",
			})
			if final != nil {
				_ = os.RemoveAll(folder.BaseDir) // reclaim scratch
			}
			return
		}
	} else if _, err := os.Stat(folder.RawFilePath); err != nil {
		report("failed", 0, "0 B/s", "neither source url nor uploaded bytes present", nil, map[string]interface{}{
			"stage": "failed",
		})
		if final != nil {
			_ = os.RemoveAll(folder.BaseDir)
		}
		return
	}

	report("processing", 100, "Validating video stream...", "", nil, map[string]interface{}{
		"stage":         "process",
		"stage_name":    "Validating Video Stream",
		"stage_percent": 25.0,
	})

	// Authoritative magic-byte check: a mislabeled source never enters the library.
	if err := media.ValidateVideoFile(folder.RawFilePath); err != nil {
		_ = os.RemoveAll(folder.BaseDir)
		report("failed", 0, "0 B/s", err.Error(), nil, map[string]interface{}{
			"stage": "failed",
		})
		return
	}

	report("processing", 100, "Fast-Start CMAF & AAC Audio Normalization...", "", nil, map[string]interface{}{
		"stage":         "process",
		"stage_name":    "CMAF Remuxing & Audio Normalization",
		"stage_percent": 65.0,
	})
	meta, err := media.RemuxToStreamableMP4(folder.RawFilePath, folder.VideoFilePath, key, filename)
	if err != nil {
		report("failed", 0, "0 B/s", fmt.Sprintf("remux failed: %v", err), nil, map[string]interface{}{
			"stage": "failed",
		})
		if final != nil {
			_ = os.RemoveAll(folder.BaseDir)
		}
		return
	}
	_ = folder.SaveMetadata(meta)

	if meta != nil {
		_ = folder.CleanRaw()
	}

	if final != nil {
		if err := a.transferFolder(ctx, key, folder.BaseDir, final, report); err != nil {
			_ = os.RemoveAll(folder.BaseDir)
			report("failed", 95, "0 B/s", fmt.Sprintf("transfer to %s failed: %v", final.NodeID, err), nil, map[string]interface{}{
				"stage": "failed",
			})
			return
		}
		_ = os.RemoveAll(folder.BaseDir) // scratch is reclaimed once stored
		report("completed", 100, "Ready", "", meta, map[string]interface{}{
			"stage":         "ready",
			"stage_name":    "Stream Ready",
			"stage_percent": 100.0,
		})
		fmt.Printf("[Agent %s] ✅ File job '%s' processed in scratch, stored on %s:%s (local copy cleaned)\n",
			a.NodeID, key, final.NodeID, final.Dir)
		return
	}

	report("completed", 100, "Ready", "", meta, map[string]interface{}{
		"stage":         "ready",
		"stage_name":    "Stream Ready",
		"stage_percent": 100.0,
	})
	fmt.Printf("[Agent %s] ✅ File job '%s' completed on tier block %s\n", a.NodeID, key, targetDir)
}

// countingReader wraps an io.Reader to track bytes read and invoke a progress callback periodically.
type countingReader struct {
	r         io.Reader
	total     int64
	read      int64
	startTime time.Time
	lastT     time.Time
	lastBytes int64
	onUpdate  func(transferred, total int64, speed string, pct float64)
}

func (cr *countingReader) Read(p []byte) (int, error) {
	n, err := cr.r.Read(p)
	if n > 0 {
		cr.read += int64(n)
		now := time.Now()
		if now.Sub(cr.lastT) >= 1200*time.Millisecond {
			elapsedSec := now.Sub(cr.lastT).Seconds()
			if elapsedSec > 0 {
				bytesInInterval := cr.read - cr.lastBytes
				speedBps := float64(bytesInInterval) / elapsedSec
				var pct float64
				if cr.total > 0 {
					pct = 90.0 + (float64(cr.read)/float64(cr.total))*9.0 // 90.0% to 99.0%
				} else {
					pct = 95.0
				}
				speedStr := formatTransferSpeed(speedBps)
				if cr.onUpdate != nil {
					cr.onUpdate(cr.read, cr.total, speedStr, pct)
				}
			}
			cr.lastT = now
			cr.lastBytes = cr.read
		}
	}
	return n, err
}

func formatTransferSpeed(bps float64) string {
	if bps <= 0 {
		return "0 B/s"
	}
	const unit = 1024
	if bps < unit {
		return fmt.Sprintf("%.0f B/s", bps)
	}
	div, exp := int64(unit), 0
	for n := bps / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB/s", bps/float64(div), "KMGTPE"[exp])
}

func (a *Agent) transferFolder(ctx context.Context, key, baseDir string, final *TransferTarget,
	report ProgressReporterFunc) error {
	totalBytes, _ := media.CalculateFolderSize(baseDir)
	if report != nil {
		report("transferring", 90.0, "Starting transfer to "+final.NodeID, "", nil, map[string]interface{}{
			"stage":             "transfer",
			"stage_name":        fmt.Sprintf("Syncing to %s", final.NodeID),
			"stage_percent":     0.0,
			"transferred_bytes": int64(0),
			"total_bytes":       totalBytes,
		})
	}

	// S3 / R2 Bucket transfer path
	if final.S3 != nil || strings.HasPrefix(final.NodeID, "bucket:") {
		if final.S3 == nil {
			return fmt.Errorf("transfer to bucket %s requested but S3 configuration missing", final.NodeID)
		}
		prefix := key
		if final.Dir != "" && final.Dir != "." && final.Dir != "/" && (final.S3 == nil || final.Dir != final.S3.Name) {
			prefix = strings.Trim(final.Dir, "/") + "/" + key
		}
		// Strict Resumable Multipart: S3/R2 uploads strictly require rclone; no fallback permitted.
		return a.transferFolderRcloneS3(ctx, key, baseDir, *final.S3, prefix, totalBytes, report)
	}

	// Primary Engine: Rclone Remote Control Daemon (rcd) with multi-connection transfers
	rcloneErr := a.transferFolderRclone(ctx, key, baseDir, final, totalBytes, report)
	if rcloneErr == nil {
		return nil
	}

	fmt.Printf("[Agent %s] ⚠️ Rclone RCD transfer failed (%v), falling back to parallel chunk HTTP...\n",
		a.NodeID, rcloneErr)

	// Fallback Engine 1: Go Native parallel chunk streaming
	initURL := fmt.Sprintf("%s/api/v1/ingest-init?key=%s&dir=%s",
		strings.TrimRight(final.Addr, "/"), url.QueryEscape(key), url.QueryEscape(final.Dir))
	initReq, err := http.NewRequestWithContext(ctx, http.MethodPost, initURL, nil)
	if err == nil {
		if a.Secret != "" {
			initReq.Header.Set("X-Cluster-Secret", a.Secret)
		}
		initResp, initErr := a.transferClient.Do(initReq)
		if initErr == nil {
			if initResp.StatusCode == http.StatusOK {
				initResp.Body.Close()
				return a.transferFolderParallel(ctx, key, baseDir, final, totalBytes, report)
			}
			body, _ := io.ReadAll(io.LimitReader(initResp.Body, 1024))
			initResp.Body.Close()
			fmt.Printf("[Agent %s] ⚠️ Ingest-init rejected (%s: %s), falling back to classic transfer...\n",
				a.NodeID, initResp.Status, strings.TrimSpace(string(body)))
		}
	}

	// Fallback Engine 2: Legacy single-stream tar transfer for older nodes
	return a.transferFolderClassic(ctx, key, baseDir, final, totalBytes, report)
}

type progressCountingReader struct {
	r      io.Reader
	onRead func(n int)
}

func (pcr *progressCountingReader) Read(p []byte) (int, error) {
	n, err := pcr.r.Read(p)
	if n > 0 && pcr.onRead != nil {
		pcr.onRead(n)
	}
	return n, err
}

func (a *Agent) transferFolderParallel(ctx context.Context, key, baseDir string, final *TransferTarget,
	totalBytes int64, report ProgressReporterFunc) error {

	startTime := time.Now()
	fmt.Printf("[Agent %s] 🚀 Starting 8-stream parallel transfer for '%s' -> %s (%s, Total: %s)\n",
		a.NodeID, key, final.NodeID, final.Addr, formatTransferSpeed(float64(totalBytes)))

	type fileEntry struct {
		relPath string
		absPath string
		size    int64
	}
	var files []fileEntry
	_ = filepath.Walk(baseDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(baseDir, path)
		if err != nil {
			return nil
		}
		files = append(files, fileEntry{
			relPath: filepath.ToSlash(rel),
			absPath: path,
			size:    info.Size(),
		})
		return nil
	})

	// Initialize all target files on the remote node with preallocated disk space
	for _, f := range files {
		initFileURL := fmt.Sprintf("%s/api/v1/ingest-file-init?key=%s&dir=%s&file=%s&size=%d",
			strings.TrimRight(final.Addr, "/"), url.QueryEscape(key), url.QueryEscape(final.Dir),
			url.QueryEscape(f.relPath), f.size)
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, initFileURL, nil)
		if a.Secret != "" {
			req.Header.Set("X-Cluster-Secret", a.Secret)
		}
		resp, err := a.transferClient.Do(req)
		if err != nil {
			return fmt.Errorf("failed to init remote file %s: %w", f.relPath, err)
		}
		if resp.StatusCode >= 300 {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
			resp.Body.Close()
			msg := strings.TrimSpace(string(body))
			if msg != "" {
				return fmt.Errorf("receiver refused file %s: %s (%s)", f.relPath, resp.Status, msg)
			}
			return fmt.Errorf("receiver refused file %s: %s", f.relPath, resp.Status)
		}
		resp.Body.Close()
	}

	type chunkTask struct {
		file   fileEntry
		offset int64
		length int64
	}

	chunkSize := int64(16 * 1024 * 1024) // 16 MB chunks for optimal TCP window ramp-up
	var tasks []chunkTask
	for _, f := range files {
		if f.size == 0 {
			continue
		}
		for off := int64(0); off < f.size; off += chunkSize {
			l := chunkSize
			if off+l > f.size {
				l = f.size - off
			}
			tasks = append(tasks, chunkTask{
				file:   f,
				offset: off,
				length: l,
			})
		}
	}

	var transferred atomic.Int64
	taskChan := make(chan chunkTask, len(tasks))
	for _, t := range tasks {
		taskChan <- t
	}
	close(taskChan)

	tickerDone := make(chan struct{})
	var lastLoggedTime time.Time
	go func() {
		ticker := time.NewTicker(1 * time.Second)
		defer ticker.Stop()
		lastBytes := int64(0)
		lastTick := time.Now()
		var smoothSpeedBps float64

		for {
			select {
			case <-tickerDone:
				return
			case now := <-ticker.C:
				cur := transferred.Load()
				elapsed := now.Sub(lastTick).Seconds()
				if elapsed > 0 {
					instantSpeed := float64(cur-lastBytes) / elapsed
					if smoothSpeedBps == 0 {
						smoothSpeedBps = instantSpeed
					} else {
						smoothSpeedBps = 0.35*instantSpeed + 0.65*smoothSpeedBps
					}
					speedStr := formatTransferSpeed(smoothSpeedBps)
					var transferPct float64
					if totalBytes > 0 {
						transferPct = (float64(cur) / float64(totalBytes)) * 100.0
					}
					overallPct := 90.0 + (transferPct * 0.09) // 90.0% to 99.0%

					detailedSpeed := fmt.Sprintf("%s • %.0f/%.0f MB (%.0f%%) [12 streams]",
						speedStr,
						float64(cur)/(1024*1024),
						float64(totalBytes)/(1024*1024),
						transferPct)

					report("transferring", overallPct, detailedSpeed, "", nil, map[string]interface{}{
						"stage":             "transfer",
						"stage_name":        fmt.Sprintf("Syncing to %s", final.NodeID),
						"stage_percent":     transferPct,
						"transferred_bytes": cur,
						"total_bytes":       totalBytes,
						"details":           fmt.Sprintf("12 parallel streams to %s (%s)", final.NodeID, final.Dir),
					})

					if time.Since(lastLoggedTime) >= 3*time.Second {
						lastLoggedTime = time.Now()
						fmt.Printf("[Agent %s] 🔀 Transferring '%s' -> %s: %.1f%% | %s\n",
							a.NodeID, key, final.NodeID, overallPct, detailedSpeed)
					}
					lastBytes = cur
					lastTick = now
				}
			}
		}
	}()

	const numWorkers = 12
	var wg sync.WaitGroup
	var workerErr error
	var errMu sync.Mutex

	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()

			for task := range taskChan {
				if ctx.Err() != nil {
					return
				}
				errMu.Lock()
				if workerErr != nil {
					errMu.Unlock()
					return
				}
				errMu.Unlock()

				f, err := os.Open(task.file.absPath)
				if err != nil {
					errMu.Lock()
					workerErr = err
					errMu.Unlock()
					return
				}
				sr := io.NewSectionReader(f, task.offset, task.length)

				var uploadSuccess bool
				for attempt := 1; attempt <= 3; attempt++ {
					_, _ = sr.Seek(0, io.SeekStart)
					var attemptBytes int64
					pr := &progressCountingReader{
						r: sr,
						onRead: func(n int) {
							attemptBytes += int64(n)
							transferred.Add(int64(n))
						},
					}

					chunkURL := fmt.Sprintf("%s/api/v1/ingest-chunk?key=%s&dir=%s&file=%s&offset=%d",
						strings.TrimRight(final.Addr, "/"), url.QueryEscape(key), url.QueryEscape(final.Dir),
						url.QueryEscape(task.file.relPath), task.offset)
					req, err := http.NewRequestWithContext(ctx, http.MethodPost, chunkURL, pr)
					if err != nil {
						transferred.Add(-attemptBytes)
						break
					}
					req.ContentLength = task.length
					req.Header.Set("Content-Type", "application/octet-stream")
					req.Header.Set("Content-Length", strconv.FormatInt(task.length, 10))
					if a.Secret != "" {
						req.Header.Set("X-Cluster-Secret", a.Secret)
					}
					resp, err := a.transferClient.Do(req)
					if err == nil {
						resp.Body.Close()
						if resp.StatusCode == http.StatusOK {
							uploadSuccess = true
							break
						}
					}
					// If attempt failed or rejected, rollback byte progress before retry
					transferred.Add(-attemptBytes)
					time.Sleep(time.Duration(attempt*200) * time.Millisecond)
				}
				f.Close()

				if !uploadSuccess {
					errMu.Lock()
					workerErr = fmt.Errorf("failed chunk %s at offset %d after 3 retries", task.file.relPath, task.offset)
					errMu.Unlock()
					return
				}
			}
		}(i)
	}

	wg.Wait()
	close(tickerDone)

	if workerErr != nil {
		return fmt.Errorf("parallel transfer error: %w", workerErr)
	}

	completeURL := fmt.Sprintf("%s/api/v1/ingest-complete?key=%s&dir=%s",
		strings.TrimRight(final.Addr, "/"), url.QueryEscape(key), url.QueryEscape(final.Dir))
	compReq, _ := http.NewRequestWithContext(ctx, http.MethodPost, completeURL, nil)
	if a.Secret != "" {
		compReq.Header.Set("X-Cluster-Secret", a.Secret)
	}
	compResp, err := a.transferClient.Do(compReq)
	if err != nil {
		return fmt.Errorf("complete request failed: %w", err)
	}
	compResp.Body.Close()
	if compResp.StatusCode >= 300 {
		return fmt.Errorf("remote node rejected complete with %s", compResp.Status)
	}

	elapsed := time.Since(startTime)
	avgSpeed := float64(totalBytes) / elapsed.Seconds()
	fmt.Printf("[Agent %s] ✅ Parallel transfer complete: '%s' -> %s in %s (Avg: %s) [12 parallel streams]\n",
		a.NodeID, key, final.NodeID, elapsed.Round(time.Millisecond), formatTransferSpeed(avgSpeed))
	return nil
}

func (a *Agent) transferFolderClassic(ctx context.Context, key, baseDir string, final *TransferTarget,
	totalBytes int64, report ProgressReporterFunc) error {

	fmt.Printf("[Agent %s] 🚀 Starting classic single-stream transfer for '%s' -> %s (%s, Total: %s)\n",
		a.NodeID, key, final.NodeID, final.Addr, formatTransferSpeed(float64(totalBytes)))

	pipeR, pipeW := io.Pipe()
	defer pipeR.Close()
	go func() {
		err := media.PackFolder(pipeW, baseDir)
		pipeW.CloseWithError(err)
	}()

	bufferedPipeR := bufio.NewReaderSize(pipeR, 1024*1024)
	var lastLog time.Time
	cr := &countingReader{
		r:         bufferedPipeR,
		total:     totalBytes,
		startTime: time.Now(),
		lastT:     time.Now(),
		onUpdate: func(transferred, total int64, speed string, pct float64) {
			var transferPct float64
			if total > 0 {
				transferPct = (float64(transferred) / float64(total)) * 100.0
			}
			speedWithProgress := fmt.Sprintf("%s • %.0f/%.0f MB (%.0f%%)",
				speed,
				float64(transferred)/(1024*1024),
				float64(total)/(1024*1024),
				transferPct,
			)
			report("transferring", pct, speedWithProgress, "", nil, map[string]interface{}{
				"stage":             "transfer",
				"stage_name":        fmt.Sprintf("Syncing to %s", final.NodeID),
				"stage_percent":     transferPct,
				"transferred_bytes": transferred,
				"total_bytes":       total,
			})
			if time.Since(lastLog) >= 2*time.Second {
				lastLog = time.Now()
				fmt.Printf("[Agent %s] 🔀 Transferring '%s' -> %s: %.1f%% | %s\n",
					a.NodeID, key, final.NodeID, pct, speedWithProgress)
			}
		},
	}

	endpoint := fmt.Sprintf("%s/api/v1/ingest-receive?key=%s&dir=%s",
		strings.TrimRight(final.Addr, "/"), url.QueryEscape(key), url.QueryEscape(final.Dir))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, cr)
	if err != nil {
		pipeR.Close()
		return err
	}
	req.Header.Set("Content-Type", "application/x-tar")
	if a.Secret != "" {
		req.Header.Set("X-Cluster-Secret", a.Secret)
	}

	resp, err := a.transferClient.Do(req)
	if err != nil {
		fmt.Printf("[Agent %s] ❌ Node transfer failed for '%s' -> %s: %v\n", a.NodeID, key, final.NodeID, err)
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		msg := strings.TrimSpace(string(body))
		var err error
		if msg != "" {
			err = fmt.Errorf("receiver answered %s: %s", resp.Status, msg)
		} else {
			err = fmt.Errorf("receiver answered %s", resp.Status)
		}
		fmt.Printf("[Agent %s] ❌ Node transfer rejected for '%s' -> %s: %v\n", a.NodeID, key, final.NodeID, err)
		return err
	}
	io.Copy(io.Discard, resp.Body)
	elapsed := time.Since(cr.startTime)
	avgSpeed := float64(totalBytes) / elapsed.Seconds()
	fmt.Printf("[Agent %s] ✅ Node transfer complete: '%s' -> %s in %s (Avg: %s)\n",
		a.NodeID, key, final.NodeID, elapsed.Round(time.Millisecond), formatTransferSpeed(avgSpeed))
	return nil
}

func measureDir(dir string) (uint64, int) {
	if dir == "" || dir == "/" || dir == "." || dir == "\\" {
		return 0, 0
	}
	var size uint64
	var count int
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d == nil {
			return nil
		}
		if !d.IsDir() {
			if info, err := d.Info(); err == nil {
				size += uint64(info.Size())
			}
		} else if path != dir {
			if filepath.Dir(path) == dir {
				count++
			}
		}
		return nil
	})
	return size, count
}

func cleanDirContents(dir string) (uint64, int, error) {
	if dir == "" || dir == "/" || dir == "." || dir == "\\" || !strings.Contains(dir, "stream") {
		return 0, 0, fmt.Errorf("invalid or protected directory: %s", dir)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, 0, err
	}
	var freedBytes uint64
	var freedCount int
	for _, e := range entries {
		p := filepath.Join(dir, e.Name())
		if !e.IsDir() {
			if info, err := e.Info(); err == nil {
				freedBytes += uint64(info.Size())
				freedCount++
			}
		} else {
			s, _ := measureDir(p)
			freedBytes += s
			freedCount++
		}
		if err := os.RemoveAll(p); err != nil {
			fmt.Printf("[CleanDir] Warning: failed to remove %s: %v\n", p, err)
		}
	}
	return freedBytes, freedCount, nil
}
