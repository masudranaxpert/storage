package cluster

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"stream/pkg/media"
)

// =============================================================================
// BACKUP REFERENCE FILE: Native Go Parallel HTTP Cluster Transfer
// =============================================================================
// This file preserves the complete implementation of the native Go parallel
// HTTP chunk streaming transfer engine between cluster nodes.
// Saved per user request for future reference.
// =============================================================================

/*
SUMMARY OF ARCHITECTURE:

1. Sender Workflow (transferFolderParallel):
   - Scans the base directory for all media files (manifests, m4s segments, vtt).
   - Probes the receiver via POST /api/v1/ingest-init.
   - For every file, calls POST /api/v1/ingest-file-init to preallocate target file size.
   - Divides large files into 16 MB chunks.
   - Launches 12 parallel worker goroutines.
   - Each worker streams chunks via POST /api/v1/ingest-chunk (writing at exact offsets via WriteAt).
   - Measures instant and smooth speed, reports progress percentages to coordinator.
   - Finalizes via POST /api/v1/ingest-complete to flush and close file handles.

2. Receiver Workflow (Mux endpoints on target agent :2052):
   - POST /api/v1/ingest-init: Cleans and prepares destination media directory.
   - POST /api/v1/ingest-file-init: Creates and truncates target file. Keeps file handle cached in activeTransferFiles map.
   - POST /api/v1/ingest-chunk: Reads raw chunk body using sync.Pool buffers (1MB) and writes directly to target file at offset via f.WriteAt().
   - POST /api/v1/ingest-complete: Closes all file handles, removes from activeTransferFiles map.
   - POST /api/v1/ingest-receive: Fallback classic tar stream receiver.
*/

// BackupCopy_transferFolder is a reference backup of Agent.transferFolder
func (a *Agent) BackupCopy_transferFolder(ctx context.Context, key, baseDir string, final *TransferTarget,
	report ProgressReporterFunc) error {
	totalBytes, _ := media.CalculateFolderSize(baseDir)
	report("transferring", 90.0, "Starting parallel transfer to "+final.NodeID, "", nil, map[string]interface{}{
		"stage":             "transfer",
		"stage_name":        fmt.Sprintf("Syncing to %s", final.NodeID),
		"stage_percent":     0.0,
		"transferred_bytes": int64(0),
		"total_bytes":       totalBytes,
	})

	initURL := fmt.Sprintf("%s/api/v1/ingest-init?key=%s&dir=%s",
		strings.TrimRight(final.Addr, "/"), url.QueryEscape(key), url.QueryEscape(final.Dir))
	initReq, err := http.NewRequestWithContext(ctx, http.MethodPost, initURL, nil)
	if err == nil {
		if a.Secret != "" {
			initReq.Header.Set("X-Cluster-Secret", a.Secret)
		}
		initResp, initErr := a.transferClient.Do(initReq)
		if initErr == nil {
			initResp.Body.Close()
			if initResp.StatusCode == http.StatusOK {
				return a.BackupCopy_transferFolderParallel(ctx, key, baseDir, final, totalBytes, report)
			}
		}
	}

	return a.BackupCopy_transferFolderClassic(ctx, key, baseDir, final, totalBytes, report)
}

// BackupCopy_transferFolderParallel is a reference backup of Agent.transferFolderParallel
func (a *Agent) BackupCopy_transferFolderParallel(ctx context.Context, key, baseDir string, final *TransferTarget,
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
		resp.Body.Close()
		if resp.StatusCode >= 300 {
			return fmt.Errorf("receiver refused file %s: %s", f.relPath, resp.Status)
		}
	}

	type chunkTask struct {
		file   fileEntry
		offset int64
		length int64
	}

	chunkSize := int64(16 * 1024 * 1024)
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
					overallPct := 90.0 + (transferPct * 0.09)

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

// BackupCopy_transferFolderClassic is a reference backup of Agent.transferFolderClassic
func (a *Agent) BackupCopy_transferFolderClassic(ctx context.Context, key, baseDir string, final *TransferTarget,
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
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode >= 300 {
		err := fmt.Errorf("receiver answered %s", resp.Status)
		fmt.Printf("[Agent %s] ❌ Node transfer rejected for '%s' -> %s: %v\n", a.NodeID, key, final.NodeID, err)
		return err
	}
	elapsed := time.Since(cr.startTime)
	avgSpeed := float64(totalBytes) / elapsed.Seconds()
	fmt.Printf("[Agent %s] ✅ Node transfer complete: '%s' -> %s in %s (Avg: %s)\n",
		a.NodeID, key, final.NodeID, elapsed.Round(time.Millisecond), formatTransferSpeed(avgSpeed))
	return nil
}
