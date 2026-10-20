package cluster

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// RelocateRequest is the JSON payload accepted by POST /api/v1/relocate.
type RelocateRequest struct {
	TaskID      string    `json:"task_id"`
	Key         string    `json:"key"`
	SrcDir      string    `json:"src_dir"`
	FinalNodeID string    `json:"final_node_id"`
	FinalDir    string    `json:"final_dir"`
	FinalAddr   string    `json:"final_addr"`
	ProgressURL string    `json:"progress_url"`
	S3          *S3Config `json:"s3,omitempty"`
	SrcS3       *S3Config `json:"src_s3,omitempty"`
}

// MediaSizeResponse is the JSON payload returned by GET /api/v1/media-size.
type MediaSizeResponse struct {
	Bytes int64 `json:"bytes"`
	Files int   `json:"files"`
}

// RegisterRelocateRoutes attaches the lifecycle relocation and media-size endpoints to mux.
func (a *Agent) RegisterRelocateRoutes(mux *http.ServeMux) {
	a.registerRelocateRoutes(mux)
}

// RegisterRelocateTestRoutes attaches relocation, media-size, ingest-delete, and /media/ handlers to an httptest ServeMux.
func (a *Agent) RegisterRelocateTestRoutes(mux *http.ServeMux) {
	a.registerRelocateRoutes(mux)
	mux.HandleFunc("/api/v1/ingest-delete", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !a.verifySecret(r) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		key := r.URL.Query().Get("key")
		rawDir := filepath.Clean(r.URL.Query().Get("dir"))
		if !validKey(key) || containsDotDot(rawDir) {
			http.Error(w, "invalid key or dir", http.StatusBadRequest)
			return
		}
		dir := a.resolveTargetDir(rawDir)
		_ = os.RemoveAll(filepath.Join(dir, key))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"deleted"}`))
	})
	mux.HandleFunc("/media/", func(w http.ResponseWriter, r *http.Request) {
		subPath := filepath.Clean(strings.TrimPrefix(r.URL.Path, "/media/"))
		if containsDotDot(subPath) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		target := a.resolveMediaPath(subPath)
		if target == "" {
			http.NotFound(w, r)
			return
		}
		http.ServeFile(w, r, target)
	})
}

func (a *Agent) registerRelocateRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/v1/relocate", a.handleRelocate)
	mux.HandleFunc("/api/v1/relocate/cancel", a.handleRelocateCancel)
	mux.HandleFunc("/api/v1/media-size", a.handleMediaSize)
}

func containsDotDot(p string) bool {
	for _, part := range strings.FieldsFunc(filepath.ToSlash(p), func(r rune) bool { return r == '/' }) {
		if part == ".." {
			return true
		}
	}
	return strings.Contains(p, "..")
}

func (a *Agent) locateMediaFolder(rawDir, key string) (string, string, bool) {
	resolved := a.resolveTargetDir(rawDir)
	candidate := filepath.Join(resolved, key)
	if st, err := os.Stat(candidate); err == nil && st.IsDir() {
		return resolved, candidate, true
	}
	if rawDir != "" {
		cleaned := filepath.Clean(rawDir)
		if cleaned != "." && cleaned != "/" && cleaned != resolved {
			alt := filepath.Join(cleaned, key)
			if st, err := os.Stat(alt); err == nil && st.IsDir() {
				return cleaned, alt, true
			}
		}
	}
	return resolved, candidate, false
}

func (a *Agent) handleRelocate(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}
	if !a.verifySecret(r) {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		return
	}

	var req RelocateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
		return
	}
	if req.TaskID == "" || !validKey(req.Key) {
		http.Error(w, `{"error":"invalid task_id or key"}`, http.StatusBadRequest)
		return
	}
	if containsDotDot(req.SrcDir) || containsDotDot(req.FinalDir) {
		http.Error(w, `{"error":"invalid directory path"}`, http.StatusBadRequest)
		return
	}

	var srcFolder string
	if req.SrcS3 == nil {
		var exists bool
		_, srcFolder, exists = a.locateMediaFolder(req.SrcDir, req.Key)
		if !exists {
			http.Error(w, `{"error":"source media folder not found"}`, http.StatusNotFound)
			return
		}
	}

	finalDir := a.resolveTargetDir(req.FinalDir)
	if finalDir == "" || finalDir == "." || containsDotDot(finalDir) {
		http.Error(w, `{"error":"invalid destination directory"}`, http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	a.relocateMu.Lock()
	if a.relocateTasks == nil {
		a.relocateTasks = make(map[string]context.CancelFunc)
	}
	if prev, ok := a.relocateTasks[req.TaskID]; ok {
		prev()
	}
	a.relocateTasks[req.TaskID] = cancel
	a.relocateMu.Unlock()

	go a.runRelocateTask(ctx, req, srcFolder, finalDir)

	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"status":  "accepted",
		"task_id": req.TaskID,
		"async":   req.ProgressURL != "",
	})
}

func (a *Agent) runRelocateTask(ctx context.Context, req RelocateRequest, srcFolder, finalDir string) {
	defer func() {
		a.relocateMu.Lock()
		delete(a.relocateTasks, req.TaskID)
		a.relocateMu.Unlock()
	}()

	totalBytes, _ := measureFolderBytesAndFiles(srcFolder)
	report := a.relocateProgressReporter(req.ProgressURL, totalBytes)

	// Remote S3 download path: pull from S3 into finalDir/<key>
	if req.SrcS3 != nil {
		dstFolder := filepath.Join(finalDir, req.Key)
		report("copying", 10.0, "Downloading from S3", "", nil, map[string]interface{}{
			"transferred_bytes": int64(0),
			"total_bytes":       int64(0),
		})
		prefix := req.Key
		if req.SrcDir != "" && req.SrcDir != "." && req.SrcDir != "/" {
			prefix = strings.Trim(req.SrcDir, "/") + "/" + req.Key
		}
		if err := DownloadFolderS3(ctx, *req.SrcS3, prefix, dstFolder, func(copied int64) {
			report("copying", 50.0, "Downloading from S3", "", nil, map[string]interface{}{
				"transferred_bytes": copied,
			})
		}); err != nil {
			_ = os.RemoveAll(dstFolder)
			report("failed", 0, "0 B/s", err.Error(), nil, map[string]interface{}{
				"transferred_bytes": int64(0),
			})
			return
		}
		a.registerRoot(finalDir)
		dlBytes, _ := measureFolderBytesAndFiles(dstFolder)
		report("copied", 100.0, "Ready", "", nil, map[string]interface{}{
			"transferred_bytes": dlBytes,
			"total_bytes":       dlBytes,
		})
		return
	}

	if req.FinalNodeID == "" || req.FinalNodeID == a.NodeID {
		dstFolder := filepath.Join(finalDir, req.Key)
		report("copying", 10.0, "Local tier copy", "", nil, map[string]interface{}{
			"transferred_bytes": int64(0),
			"total_bytes":       totalBytes,
		})
		if err := copyFolderLocal(ctx, srcFolder, dstFolder, func(copied int64) {
			var pct float64
			if totalBytes > 0 {
				pct = (float64(copied) / float64(totalBytes)) * 100.0
			}
			report("copying", pct, "Local tier copy", "", nil, map[string]interface{}{
				"transferred_bytes": copied,
				"total_bytes":       totalBytes,
			})
		}); err != nil {
			if filepath.Clean(srcFolder) != filepath.Clean(dstFolder) {
				_ = os.RemoveAll(dstFolder)
			}
			report("failed", 0, "0 B/s", err.Error(), nil, map[string]interface{}{
				"transferred_bytes": int64(0),
				"total_bytes":       totalBytes,
			})
			return
		}
		a.registerRoot(finalDir)
		report("copied", 100.0, "Ready", "", nil, map[string]interface{}{
			"transferred_bytes": totalBytes,
			"total_bytes":       totalBytes,
		})
		return
	}

	target := &TransferTarget{
		NodeID: req.FinalNodeID,
		Dir:    finalDir,
		Addr:   req.FinalAddr,
		S3:     req.S3,
	}
	if err := a.transferFolder(ctx, req.Key, srcFolder, target, report); err != nil {
		report("failed", 0, "0 B/s", err.Error(), nil, map[string]interface{}{
			"transferred_bytes": int64(0),
			"total_bytes":       totalBytes,
		})
		return
	}
	report("copied", 100.0, "Ready", "", nil, map[string]interface{}{
		"transferred_bytes": totalBytes,
		"total_bytes":       totalBytes,
	})
}

func (a *Agent) relocateProgressReporter(progressURL string, defaultTotal int64) ProgressReporterFunc {
	return func(state string, pct float64, speed, errMsg string, _ interface{}, extra ...map[string]interface{}) {
		if progressURL == "" {
			return
		}
		var transferred int64
		total := defaultTotal
		if len(extra) > 0 && extra[0] != nil {
			if v, ok := extra[0]["transferred_bytes"].(int64); ok {
				transferred = v
			}
			if v, ok := extra[0]["total_bytes"].(int64); ok && v > 0 {
				total = v
			}
			if sp, ok := extra[0]["stage_percent"].(float64); ok && state == "transferring" {
				pct = sp
			}
		}
		payload := map[string]interface{}{
			"state":             state,
			"progress_percent":  pct,
			"speed":             speed,
			"error":             errMsg,
			"transferred_bytes": transferred,
			"total_bytes":       total,
		}
		body, err := json.Marshal(payload)
		if err != nil {
			return
		}
		req, err := http.NewRequest(http.MethodPost, progressURL, bytes.NewReader(body))
		if err != nil {
			return
		}
		req.Header.Set("Content-Type", "application/json")
		if a.Secret != "" {
			req.Header.Set("X-Cluster-Secret", a.Secret)
		}
		client := a.client
		if client == nil {
			client = http.DefaultClient
		}
		resp, err := client.Do(req)
		if err != nil {
			return
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}
}

func (a *Agent) handleRelocateCancel(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}
	if !a.verifySecret(r) {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		return
	}

	taskID := strings.TrimSpace(r.URL.Query().Get("task_id"))
	if taskID == "" {
		var body struct {
			TaskID string `json:"task_id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		taskID = strings.TrimSpace(body.TaskID)
	}
	if taskID == "" {
		http.Error(w, `{"error":"task_id required"}`, http.StatusBadRequest)
		return
	}

	a.relocateMu.Lock()
	cancel, ok := a.relocateTasks[taskID]
	if ok {
		delete(a.relocateTasks, taskID)
	}
	a.relocateMu.Unlock()

	if ok && cancel != nil {
		cancel()
	}

	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"status":    "cancelled",
		"task_id":   taskID,
		"cancelled": ok,
	})
}

func (a *Agent) handleMediaSize(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodGet {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}
	if !a.verifySecret(r) {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		return
	}

	key := r.URL.Query().Get("key")
	rawDir := r.URL.Query().Get("dir")
	if !validKey(key) || containsDotDot(rawDir) {
		http.Error(w, `{"error":"invalid key or dir"}`, http.StatusBadRequest)
		return
	}

	_, folder, exists := a.locateMediaFolder(rawDir, key)
	if !exists {
		http.Error(w, `{"error":"media folder not found"}`, http.StatusNotFound)
		return
	}

	totalBytes, fileCount := measureFolderBytesAndFiles(folder)
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(MediaSizeResponse{
		Bytes: totalBytes,
		Files: fileCount,
	})
}

func measureFolderBytesAndFiles(folder string) (int64, int) {
	var totalBytes int64
	var files int
	_ = filepath.WalkDir(folder, func(_ string, d os.DirEntry, err error) error {
		if err != nil || d == nil || d.IsDir() {
			return nil
		}
		if strings.HasSuffix(d.Name(), ".tmp") {
			return nil
		}
		if info, err := d.Info(); err == nil {
			totalBytes += info.Size()
			files++
		}
		return nil
	})
	return totalBytes, files
}

// copyFolderLocal recursively copies srcDir to dstDir with fsync and atomic
// per-file os.Rename, leaving srcDir intact until the coordinator's cleanup phase.
func copyFolderLocal(ctx context.Context, srcDir, dstDir string, onProgress func(copiedBytes int64)) error {
	if filepath.Clean(srcDir) == filepath.Clean(dstDir) {
		return nil
	}
	if err := os.RemoveAll(dstDir); err != nil {
		return fmt.Errorf("clean destination: %w", err)
	}
	if err := os.MkdirAll(dstDir, 0755); err != nil {
		return fmt.Errorf("mkdir destination: %w", err)
	}

	var copied int64
	return filepath.WalkDir(srcDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		rel, err := filepath.Rel(srcDir, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		targetPath := filepath.Join(dstDir, rel)
		if d.IsDir() {
			return os.MkdirAll(targetPath, 0755)
		}

		if err := os.MkdirAll(filepath.Dir(targetPath), 0755); err != nil {
			return err
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()

		tmpPath := targetPath + ".reloc.tmp"
		out, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
		if err != nil {
			return err
		}

		buf := make([]byte, 256*1024)
		for {
			if err := ctx.Err(); err != nil {
				out.Close()
				_ = os.Remove(tmpPath)
				return err
			}
			nr, rErr := in.Read(buf)
			if nr > 0 {
				nw, wErr := out.Write(buf[:nr])
				if wErr != nil {
					out.Close()
					_ = os.Remove(tmpPath)
					return wErr
				}
				copied += int64(nw)
				if onProgress != nil {
					onProgress(copied)
				}
			}
			if rErr != nil {
				if rErr == io.EOF {
					break
				}
				out.Close()
				_ = os.Remove(tmpPath)
				return rErr
			}
		}

		if err := out.Sync(); err != nil {
			out.Close()
			_ = os.Remove(tmpPath)
			return err
		}
		if err := out.Close(); err != nil {
			_ = os.Remove(tmpPath)
			return err
		}
		if err := os.Rename(tmpPath, targetPath); err != nil {
			_ = os.Remove(tmpPath)
			return err
		}
		return nil
	})
}
