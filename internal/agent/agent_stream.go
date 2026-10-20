package agent

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v3/disk"
	"golang.org/x/net/webdav"

	"stream/internal/cluster"
	"stream/internal/media"
	"stream/internal/model"
	"stream/internal/provision"
	"stream/internal/storage"
)

var (
	activeTransferFilesMu sync.Mutex
	activeTransferFiles   = make(map[string]*activeTransferEntry)
)

type activeTransferEntry struct {
	file       *os.File
	lastAccess time.Time
}

// startStreamingServer initializes the local Byte-Range HTTP streaming server on port 2052
func (a *Agent) startStreamingServer(ctx context.Context) {
	tuneLinuxTCPBuffers()

	// Clean up stale in-flight file descriptors periodically to prevent leaks
	go func() {
		ticker := time.NewTicker(1 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				activeTransferFilesMu.Lock()
				for p, entry := range activeTransferFiles {
					_ = entry.file.Sync()
					_ = entry.file.Close()
					delete(activeTransferFiles, p)
				}
				activeTransferFilesMu.Unlock()
				return
			case now := <-ticker.C:
				activeTransferFilesMu.Lock()
				for p, entry := range activeTransferFiles {
					if now.Sub(entry.lastAccess) > 10*time.Minute {
						_ = entry.file.Sync()
						_ = entry.file.Close()
						delete(activeTransferFiles, p)
					}
				}
				activeTransferFilesMu.Unlock()
			}
		}
	}()

	mux := http.NewServeMux()

	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(fmt.Sprintf(`{"status":"ok","node_id":"%s","port":%d}`, a.NodeID, a.ListenPort)))
	})

	mux.HandleFunc("/api/install-tools", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Content-Type", "application/json")
		if !a.verifySecret(r) {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		tool := r.URL.Query().Get("tool")
		go func() {
			cmd := exec.Command("bash", "-c", provision.InstallShell(tool))
			_ = cmd.Run()
			_, _ = a.SendHeartbeat()
		}()
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"installing","message":"Installing tools on host in background"}`))
	})

	mux.HandleFunc("/api/uninstall-tools", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Content-Type", "application/json")
		if !a.verifySecret(r) {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		tool := r.URL.Query().Get("tool")
		go func() {
			cmd := exec.Command("bash", "-c", provision.UninstallShell(tool))
			_ = cmd.Run()
			_, _ = a.SendHeartbeat()
		}()
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"uninstalling","message":"Uninstalling tools on host in background"}`))
	})

	// File API worker endpoints dispatched by coordinator.

	// POST /api/v1/ingest {key, source_url, filename, target_dir,
	// final_node_id, final_dir, final_addr}: download from the source, then
	// validate + remux, reporting progress back to the master. Direct mode
	// (target_dir, no final_*): everything happens on the tier block this
	// node owns. Decoupled mode (final_*): the job runs in this node's
	// scratch folder and the finished package is transferred to the block
	// owner, then the scratch copy is deleted.
	mux.HandleFunc("/api/v1/ingest", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !a.verifySecret(r) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		var req struct {
			Key         string          `json:"key"`
			SourceURL   string          `json:"source_url"`
			Filename    string          `json:"filename"`
			TargetDir   string          `json:"target_dir"`
			FinalNodeID string          `json:"final_node_id"`
			FinalDir    string          `json:"final_dir"`
			FinalAddr   string          `json:"final_addr"`
			S3          *model.S3Config `json:"s3,omitempty"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Key == "" {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		if a.Quota != nil && a.Quota.AllocatedMaxBytes() > 0 && a.Quota.AvailableBytes() == 0 {
			http.Error(w, "insufficient storage: node storage quota exhausted", http.StatusInsufficientStorage)
			return
		}
		filename := media.SanitizeFilename(req.Filename)
		if filename == "" {
			filename = "video.mp4"
		}
		targetDir := a.resolveTargetDir(req.TargetDir)

		var final *TransferTarget
		if req.FinalNodeID != "" && req.FinalNodeID != a.NodeID {
			if req.S3 != nil || strings.HasPrefix(req.FinalNodeID, "bucket:") {
				final = &TransferTarget{NodeID: req.FinalNodeID, Dir: req.FinalDir, S3: req.S3}
			} else if req.FinalAddr != "" {
				if dir := a.resolveTargetDir(req.FinalDir); dir != "" && dir != "." && !strings.Contains(dir, "..") {
					final = &TransferTarget{NodeID: req.FinalNodeID, Dir: dir, Addr: req.FinalAddr}
				}
			}
		}
		if final != nil {
			targetDir = a.ScratchDir // process locally, transfer afterwards
		}

		go a.RunFileJob(req.Key, req.SourceURL, filename, targetDir, final)
		w.WriteHeader(http.StatusAccepted)
		w.Write([]byte(`{"status":"accepted"}`))
	})

	// POST /api/v1/ingest-upload?key=&dir=&filename=[&scratch=1&final_node_id=
	// &final_dir=&final_addr=]: raw streamed bytes from the master (proxied
	// client upload) land either in the block folder (direct) or in scratch
	// (decoupled: remux here, transfer to the block owner afterwards).
	mux.HandleFunc("/api/v1/ingest-upload", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodPost && r.Method != http.MethodPut {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !a.verifySecret(r) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		key := r.URL.Query().Get("key")
		if !validKey(key) {
			http.Error(w, "invalid key", http.StatusBadRequest)
			return
		}
		filename := media.SanitizeFilename(r.URL.Query().Get("filename"))
		if filename == "" {
			filename = "video.mp4"
		}
		targetDir := a.resolveTargetDir(r.URL.Query().Get("dir"))

		var final *TransferTarget
		if fin := r.URL.Query().Get("final_node_id"); fin != "" && fin != a.NodeID {
			addr := r.URL.Query().Get("final_addr")
			dir := a.resolveTargetDir(r.URL.Query().Get("final_dir"))
			if addr != "" && dir != "" && dir != "." && !strings.Contains(dir, "..") {
				final = &TransferTarget{NodeID: fin, Dir: dir, Addr: addr}
			}
		}
		if final != nil {
			targetDir = a.ScratchDir
		}

		if a.Quota != nil && r.ContentLength > 0 {
			if err := a.Quota.Reserve(uint64(r.ContentLength)); err != nil {
				http.Error(w, fmt.Sprintf("insufficient storage quota: %v", err), http.StatusInsufficientStorage)
				return
			}
		}

		folder, err := media.PrepareMediaFolder(targetDir, key, filename)
		if err != nil {
			if a.Quota != nil && r.ContentLength > 0 {
				a.Quota.Release(uint64(r.ContentLength))
			}
			http.Error(w, "folder prep failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		out, err := os.Create(folder.RawFilePath)
		if err != nil {
			if a.Quota != nil && r.ContentLength > 0 {
				a.Quota.Release(uint64(r.ContentLength))
			}
			http.Error(w, "create file failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		written, err := io.Copy(out, r.Body)
		if err != nil {
			out.Close()
			if a.Quota != nil && r.ContentLength > 0 {
				a.Quota.Release(uint64(r.ContentLength))
			}
			http.Error(w, "stream interrupted: "+err.Error(), http.StatusBadRequest)
			return
		}
		out.Close()
		if a.Quota != nil {
			if r.ContentLength > 0 {
				a.Quota.Commit(uint64(r.ContentLength))
			} else if written > 0 {
				a.Quota.Commit(uint64(written))
			}
		}
		if final == nil {
			a.registerRoot(targetDir)
		}

		go a.RunFileJob(key, "", filename, targetDir, final)
		w.WriteHeader(http.StatusAccepted)
		w.Write([]byte(`{"status":"stored"}`))
	})

	// POST /api/v1/ingest-receive?key=&dir= (body: tar.gz folder stream):
	// storage-side half of a decoupled placement. A processing worker
	// streams the finished media folder; it is unpacked into <dir>/<key>/,
	// the block root is registered for streaming, and the manifest is
	// verified. Guarded by the shared cluster secret.
	mux.HandleFunc("/api/v1/ingest-receive", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !a.verifySecret(r) {
			http.Error(w, "invalid cluster secret", http.StatusUnauthorized)
			return
		}
		key := r.URL.Query().Get("key")
		if !validKey(key) {
			http.Error(w, "invalid key", http.StatusBadRequest)
			return
		}
		rawDir := filepath.Clean(r.URL.Query().Get("dir"))
		dir := a.resolveTargetDir(rawDir)
		if dir == "" || dir == "." || strings.HasPrefix(dir, "..") || strings.Contains(dir, "..") {
			http.Error(w, "dir required", http.StatusBadRequest)
			return
		}

		dest := filepath.Join(dir, key)
		if err := os.RemoveAll(dest); err != nil { // stale partial from a retry
			http.Error(w, "cleanup failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		if rawDir != dir {
			_ = os.RemoveAll(filepath.Join(rawDir, key))
		}
		// Slow relay links: give this request its own long read deadline,
		// independent of the server-wide streaming timeout.
		if rc, ok := w.(interface {
			SetReadDeadline(time.Time) error
		}); ok {
			_ = rc.SetReadDeadline(time.Now().Add(2 * time.Hour))
		}
		files, err := media.UnpackFolder(r.Body, dest)
		if err != nil {
			_ = os.RemoveAll(dest)
			http.Error(w, "unpack failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		if _, err := os.Stat(filepath.Join(dest, "metadata.json")); err != nil {
			_ = os.RemoveAll(dest)
			http.Error(w, "archive incomplete: metadata.json missing", http.StatusUnprocessableEntity)
			return
		}
		if a.Quota != nil {
			if sz, err := storage.ScanDirSize(dest); err == nil && sz > 0 {
				if err := a.Quota.Reserve(sz); err != nil {
					_ = os.RemoveAll(dest)
					http.Error(w, fmt.Sprintf("insufficient storage quota: %v", err), http.StatusInsufficientStorage)
					return
				}
				a.Quota.Commit(sz)
			}
		}
		a.registerRoot(dir)

		fmt.Printf("[Agent %s] 📥 received file job '%s' (%d files) into block %s\n", a.NodeID, key, files, dir)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(fmt.Sprintf(`{"status":"stored","files":%d}`, files)))
	})

	// WebDAV endpoint for high-speed rclone RCD node-to-node transfers
	mux.HandleFunc("/api/v1/webdav/", a.handleWebDAV)

	// Parallel Chunk Ingest Engine (SeaweedFS/MinIO multi-stream high throughput)
	mux.HandleFunc("/api/v1/ingest-init", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !a.verifySecret(r) {
			http.Error(w, "invalid cluster secret", http.StatusUnauthorized)
			return
		}
		key := r.URL.Query().Get("key")
		if !validKey(key) {
			http.Error(w, "invalid key", http.StatusBadRequest)
			return
		}
		rawDir := filepath.Clean(r.URL.Query().Get("dir"))
		dir := a.resolveTargetDir(rawDir)
		if dir == "" || dir == "." || strings.HasPrefix(dir, "..") || strings.Contains(dir, "..") {
			http.Error(w, "dir required", http.StatusBadRequest)
			return
		}
		dest := filepath.Join(dir, key)
		_ = os.RemoveAll(dest)
		if err := os.MkdirAll(dest, 0755); err != nil {
			http.Error(w, "mkdir failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"initialized"}`))
	})

	mux.HandleFunc("/api/v1/ingest-file-init", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !a.verifySecret(r) {
			http.Error(w, "invalid cluster secret", http.StatusUnauthorized)
			return
		}
		key := r.URL.Query().Get("key")
		filename := filepath.Base(r.URL.Query().Get("file"))
		if !validKey(key) || filename == "" || filename == "." {
			http.Error(w, "invalid key or filename", http.StatusBadRequest)
			return
		}
		dir := a.resolveTargetDir(filepath.Clean(r.URL.Query().Get("dir")))
		dest := filepath.Join(dir, key, filename)
		size, _ := strconv.ParseInt(r.URL.Query().Get("size"), 10, 64)

		activeTransferFilesMu.Lock()
		if old, exists := activeTransferFiles[dest]; exists {
			_ = old.file.Close()
		}
		f, err := os.OpenFile(dest, os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0644)
		if err != nil {
			activeTransferFilesMu.Unlock()
			http.Error(w, "create file failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		if size > 0 {
			_ = f.Truncate(size)
		}
		activeTransferFiles[dest] = &activeTransferEntry{file: f, lastAccess: time.Now()}
		activeTransferFilesMu.Unlock()

		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"file_ready"}`))
	})

	var chunkBufPool = sync.Pool{
		New: func() interface{} {
			b := make([]byte, 1024*1024)
			return &b
		},
	}

	mux.HandleFunc("/api/v1/ingest-chunk", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !a.verifySecret(r) {
			http.Error(w, "invalid cluster secret", http.StatusUnauthorized)
			return
		}
		key := r.URL.Query().Get("key")
		filename := filepath.Base(r.URL.Query().Get("file"))
		offset, _ := strconv.ParseInt(r.URL.Query().Get("offset"), 10, 64)
		if !validKey(key) || filename == "" || filename == "." {
			http.Error(w, "invalid key or filename", http.StatusBadRequest)
			return
		}
		dir := a.resolveTargetDir(filepath.Clean(r.URL.Query().Get("dir")))
		dest := filepath.Join(dir, key, filename)

		activeTransferFilesMu.Lock()
		entry := activeTransferFiles[dest]
		var f *os.File
		if entry != nil {
			entry.lastAccess = time.Now()
			f = entry.file
		}
		activeTransferFilesMu.Unlock()

		if f == nil {
			var err error
			f, err = os.OpenFile(dest, os.O_WRONLY, 0644)
			if err != nil {
				http.Error(w, "open file failed: "+err.Error(), http.StatusInternalServerError)
				return
			}
			defer f.Close()
		}

		bufPtr := chunkBufPool.Get().(*[]byte)
		defer chunkBufPool.Put(bufPtr)
		buf := *bufPtr

		var written int64
		curOffset := offset
		for {
			nr, readErr := r.Body.Read(buf)
			if nr > 0 {
				nw, writeErr := f.WriteAt(buf[:nr], curOffset)
				if writeErr != nil {
					http.Error(w, "write error: "+writeErr.Error(), http.StatusInternalServerError)
					return
				}
				written += int64(nw)
				curOffset += int64(nw)
			}
			if readErr != nil {
				if readErr == io.EOF {
					break
				}
				http.Error(w, "read stream error: "+readErr.Error(), http.StatusInternalServerError)
				return
			}
		}

		w.WriteHeader(http.StatusOK)
		w.Write([]byte(fmt.Sprintf(`{"status":"chunk_written","written":%d}`, written)))
	})

	mux.HandleFunc("/api/v1/ingest-complete", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !a.verifySecret(r) {
			http.Error(w, "invalid cluster secret", http.StatusUnauthorized)
			return
		}
		key := r.URL.Query().Get("key")
		if !validKey(key) {
			http.Error(w, "invalid key", http.StatusBadRequest)
			return
		}
		rawDir := filepath.Clean(r.URL.Query().Get("dir"))
		dir := a.resolveTargetDir(rawDir)
		dest := filepath.Join(dir, key)

		activeTransferFilesMu.Lock()
		prefix := dest + string(os.PathSeparator)
		for p, entry := range activeTransferFiles {
			if strings.HasPrefix(p, prefix) || p == dest {
				_ = entry.file.Sync()
				_ = entry.file.Close()
				delete(activeTransferFiles, p)
			}
		}
		activeTransferFilesMu.Unlock()

		if _, err := os.Stat(filepath.Join(dest, "metadata.json")); err != nil {
			_ = os.RemoveAll(dest)
			http.Error(w, "metadata.json missing", http.StatusUnprocessableEntity)
			return
		}
		if a.Quota != nil {
			if sz, err := storage.ScanDirSize(dest); err == nil && sz > 0 {
				if err := a.Quota.Reserve(sz); err != nil {
					_ = os.RemoveAll(dest)
					http.Error(w, fmt.Sprintf("insufficient storage quota: %v", err), http.StatusInsufficientStorage)
					return
				}
				a.Quota.Commit(sz)
			}
		}
		a.registerRoot(dir)
		fmt.Printf("[Agent %s] 📥 Parallel ingest complete for key '%s' into block %s\n", a.NodeID, key, dir)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"stored"}`))
	})

	// POST /api/v1/ingest-delete?key=&dir=: removes a placed file folder.
	mux.HandleFunc("/api/v1/ingest-delete", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !a.verifySecret(r) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		key := r.URL.Query().Get("key")
		if !validKey(key) {
			http.Error(w, "invalid key", http.StatusBadRequest)
			return
		}
		rawDir := filepath.Clean(r.URL.Query().Get("dir"))
		dir := a.resolveTargetDir(rawDir)
		if dir == "" || dir == "." || strings.Contains(dir, "..") {
			http.Error(w, "invalid dir", http.StatusBadRequest)
			return
		}
		target := filepath.Join(dir, key)
		if a.Quota != nil {
			if freed, err := storage.ScanDirSize(target); err == nil && freed > 0 {
				a.Quota.Reclaim(freed)
			}
		}
		_ = os.RemoveAll(target)
		if rawDir != dir && !strings.Contains(rawDir, "..") {
			_ = os.RemoveAll(filepath.Join(rawDir, key))
		}
		if a.ScratchDir != "" {
			_ = os.RemoveAll(filepath.Join(a.ScratchDir, key))
		}
		w.Write([]byte(`{"status":"deleted"}`))
	})

	// GET /api/v1/storage-folders: inspect all stream drives and their media/processing directories.
	mux.HandleFunc("/api/v1/storage-folders", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if !a.verifySecret(r) {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		drives := a.getStreamDrives()

		var totalMediaBytes, totalProcBytes uint64
		var totalMediaFiles, totalProcFiles int
		for _, d := range drives {
			totalMediaBytes += d.MediaSizeBytes
			totalMediaFiles += d.MediaFileCount
			totalProcBytes += d.ProcessingSizeBytes
			totalProcFiles += d.ProcessingFileCount
		}

		resp := map[string]interface{}{
			"node_id":               a.NodeID,
			"drives":                drives,
			"media_size_bytes":      totalMediaBytes,
			"media_file_count":      totalMediaFiles,
			"processing_size_bytes": totalProcBytes,
			"processing_file_count": totalProcFiles,
			"total_stream_bytes":    totalMediaBytes + totalProcBytes,
		}
		json.NewEncoder(w).Encode(resp)
	})

	// POST /api/v1/storage-clean?target=processing|media[&dir=...]: cleans files inside the requested directories.
	mux.HandleFunc("/api/v1/storage-clean", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !a.verifySecret(r) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		target := r.URL.Query().Get("target")
		requestedDir := r.URL.Query().Get("dir")

		var dirsToClean []string
		seen := make(map[string]bool)
		addCleanDir := func(d string) {
			if d != "" && !seen[d] && strings.Contains(d, "stream") {
				seen[d] = true
				dirsToClean = append(dirsToClean, d)
			}
		}

		if requestedDir != "" {
			addCleanDir(requestedDir)
		} else {
			drives := a.getStreamDrives()
			for _, d := range drives {
				if target == "processing" || target == "scratch" {
					addCleanDir(d.ProcessingDir)
				} else if target == "media" {
					addCleanDir(d.MediaDir)
				}
			}
			if target == "media" {
				addCleanDir(a.MediaDir)
				addCleanDir("/stream/media")
				addCleanDir("/var/lib/stream/media")
			}
			if target == "processing" || target == "scratch" {
				addCleanDir(a.ScratchDir)
				addCleanDir("/stream/processing")
				addCleanDir("/var/lib/stream/processing")
			}
		}

		var totalFreedBytes uint64
		var totalFreedItems int
		for _, dir := range dirsToClean {
			if stat, err := os.Stat(dir); err == nil && stat.IsDir() {
				fb, fi, _ := cleanDirContents(dir)
				totalFreedBytes += fb
				totalFreedItems += fi
				_ = os.MkdirAll(dir, 0755)
			}
		}

		json.NewEncoder(w).Encode(map[string]interface{}{
			"status":      "cleaned",
			"target":      target,
			"freed_bytes": totalFreedBytes,
			"freed_items": totalFreedItems,
		})
	})

	a.registerRelocateRoutes(mux)

	mux.HandleFunc("/media/", a.handleMedia)
	mux.HandleFunc("/stream/", a.handleMedia)

	addr := fmt.Sprintf(":%d", a.ListenPort)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		fmt.Printf("[Agent %s] Port %d busy (%v), trying fallback port :8880...\n", a.NodeID, a.ListenPort, err)
		addr = ":8880"
		listener, err = net.Listen("tcp", addr)
		if err != nil {
			fmt.Printf("[Agent %s] Warning: failed to bind streaming server: %v\n", a.NodeID, err)
			return
		}
		a.ListenPort = 8880
	}

	a.httpServer = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 15 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}

	fmt.Printf("[Agent %s] Direct Byte-Range Media Server active on %s (Media Root: %s)\n",
		a.NodeID, addr, a.MediaDir)

	if err := a.httpServer.Serve(listener); err != nil && err != http.ErrServerClosed {
		fmt.Printf("[Agent %s] Streaming server error: %v\n", a.NodeID, err)
	}
}

type StreamDriveStat = model.StreamDriveStat

func (a *Agent) getStreamDrives() []StreamDriveStat {
	var drives []StreamDriveStat
	seenRoots := make(map[string]bool)

	addRoot := func(mountPoint, driveName string) {
		mountPoint = filepath.Clean(mountPoint)
		var streamRoot string
		if mountPoint == "/" || mountPoint == "." || mountPoint == "\\" {
			streamRoot = "/stream"
		} else {
			streamRoot = filepath.Join(mountPoint, "stream")
		}

		cleanRoot := filepath.Clean(streamRoot)
		if seenRoots[cleanRoot] {
			return
		}
		seenRoots[cleanRoot] = true

		mediaDir := filepath.Join(cleanRoot, "media")
		procDir := filepath.Join(cleanRoot, "processing")

		mediaSize, mediaCount := measureDir(mediaDir)
		procSize, procCount := measureDir(procDir)

		// Check if data is inside /var/lib/stream/media and this is the root drive
		if (mountPoint == "/" || mountPoint == "/stream") && mediaSize == 0 {
			varLibMedia := "/var/lib/stream/media"
			if stat, err := os.Lstat(varLibMedia); err == nil && stat.Mode()&os.ModeSymlink == 0 {
				s, c := measureDir(varLibMedia)
				if s > 0 {
					mediaSize = s
					mediaCount = c
				}
			}
		}

		drives = append(drives, StreamDriveStat{
			DriveName:           driveName,
			MountPoint:          mountPoint,
			StreamRoot:          cleanRoot,
			MediaDir:            mediaDir,
			MediaSizeBytes:      mediaSize,
			MediaFileCount:      mediaCount,
			ProcessingDir:       procDir,
			ProcessingSizeBytes: procSize,
			ProcessingFileCount: procCount,
			TotalStreamBytes:    mediaSize + procSize,
		})
	}

	// 1. Discover active mounts on Linux (matches telemetry.collectDisks)
	if runtime.GOOS == "linux" {
		partitions, err := disk.Partitions(false)
		if err == nil {
			for _, p := range partitions {
				if strings.HasPrefix(p.Mountpoint, "/proc") ||
					strings.HasPrefix(p.Mountpoint, "/sys") ||
					strings.HasPrefix(p.Mountpoint, "/dev") ||
					strings.HasPrefix(p.Mountpoint, "/run") ||
					strings.HasPrefix(p.Mountpoint, "/boot") ||
					strings.HasPrefix(p.Mountpoint, "/snap") ||
					strings.Contains(p.Fstype, "squashfs") ||
					strings.Contains(p.Fstype, "tmpfs") ||
					strings.Contains(p.Fstype, "overlay") {
					continue
				}

				name := p.Mountpoint
				if p.Mountpoint == "/" {
					name = "System SSD (/)"
				} else {
					name = fmt.Sprintf("%s (%s)", filepath.Base(p.Mountpoint), p.Mountpoint)
				}
				addRoot(p.Mountpoint, name)
			}
		}
	}

	// 2. Always ensure system root "/" is present
	if !seenRoots["/stream"] {
		addRoot("/", "System SSD (/)")
	}

	return drives
}

// handleWebDAV provides high-speed WebDAV file ingest for rclone multi-stream transfers.
func (a *Agent) handleWebDAV(w http.ResponseWriter, r *http.Request) {
	authed := false
	if a.verifySecret(r) {
		authed = true
	} else if user, pass, ok := r.BasicAuth(); ok && a.Secret != "" {
		if subtle.ConstantTimeCompare([]byte(pass), []byte(a.Secret)) == 1 && (user == "admin" || user == "stream") {
			authed = true
		}
	}

	if !authed {
		w.Header().Set("WWW-Authenticate", `Basic realm="Cluster WebDAV"`)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	subPath := strings.TrimPrefix(r.URL.Path, "/api/v1/webdav/")
	parts := strings.SplitN(subPath, "/", 3)
	if len(parts) < 2 {
		http.Error(w, "invalid webdav path", http.StatusBadRequest)
		return
	}
	hexDir := parts[0]
	key := parts[1]
	if !validKey(key) {
		http.Error(w, "invalid key", http.StatusBadRequest)
		return
	}

	rawDirBytes, err := hex.DecodeString(hexDir)
	if err != nil {
		http.Error(w, "invalid hex directory", http.StatusBadRequest)
		return
	}
	rawDir := string(rawDirBytes)
	dir := a.resolveTargetDir(filepath.Clean(rawDir))
	dest := filepath.Join(dir, key)

	if r.Method == http.MethodPut && a.Quota != nil && r.ContentLength > 0 {
		if err := a.Quota.Reserve(uint64(r.ContentLength)); err != nil {
			http.Error(w, fmt.Sprintf("insufficient storage quota: %v", err), http.StatusInsufficientStorage)
			return
		}
		defer a.Quota.Commit(uint64(r.ContentLength))
	}

	_ = os.MkdirAll(dest, 0755)
	a.registerRoot(dir)

	prefix := "/api/v1/webdav/" + hexDir + "/" + key
	handler := &webdav.Handler{
		Prefix:     prefix,
		FileSystem: webdav.Dir(dest),
		LockSystem: webdav.NewMemLS(),
	}
	handler.ServeHTTP(w, r)
}

// transferFolder streams the finished media folder to the block owner. It prioritizes
// rclone Remote Control Daemon (rcd) multi-stream transfer, seamlessly falling back
// to native parallel HTTP chunking or single-stream tar if rclone is unavailable.

// validKey enforces the file-key charset so a hostile transfer request can
// never craft a path outside its block directory.
func validKey(key string) bool {
	if len(key) < 6 || len(key) > 64 {
		return false
	}
	for _, c := range key {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_':
		default:
			return false
		}
	}
	return true
}

// handleMedia serves media files over HTTP; callers need the cluster secret, a signed token or a playback ticket.
func (a *Agent) handleMedia(w http.ResponseWriter, r *http.Request) {
	subPath := strings.TrimPrefix(r.URL.Path, "/media/")
	subPath = strings.TrimPrefix(subPath, "/stream/")
	cleanSubPath := filepath.Clean(subPath)

	if strings.Contains(cleanSubPath, "..") {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	allowedOrigin := cluster.MatchCORSOrigin(a.GetCORSOrigins(), r.Header.Get("Origin"))
	if allowedOrigin != "" {
		w.Header().Set("Access-Control-Allow-Origin", allowedOrigin)
		w.Header().Set("Access-Control-Allow-Credentials", "true")
		w.Header().Set("Vary", "Origin")
	}
	w.Header().Set("Access-Control-Allow-Methods", "GET, HEAD, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Range, Origin, Accept, Content-Type, Authorization")
	w.Header().Set("Access-Control-Expose-Headers", "Content-Length, Content-Range, Accept-Ranges")
	w.Header().Set("Accept-Ranges", "bytes")

	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}

	parts := strings.Split(cleanSubPath, string(filepath.Separator))
	mediaID := parts[0]
	hasMP4Suffix := strings.HasSuffix(mediaID, ".mp4")
	baseID := strings.TrimSuffix(mediaID, ".mp4")

	if !a.verifySecret(r) {
		if strings.HasPrefix(baseID, "t_") {
			resolvedKey, _, err := cluster.ResolvePlaybackTicket(baseID, a.Secret)
			if err != nil {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusForbidden)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": "forbidden: invalid or expired stream ticket"})
				return
			}
			mediaID = resolvedKey
			parts[0] = resolvedKey
			cleanSubPath = filepath.Join(parts...)
			if hasMP4Suffix && len(parts) == 1 {
				cleanSubPath += ".mp4"
			}
		} else {
			if a.Secret == "" {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusForbidden)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": "forbidden: authentication required"})
				return
			}
			token := r.URL.Query().Get("token")
			expStr := r.URL.Query().Get("exp")
			if expStr == "" {
				expStr = r.URL.Query().Get("expires")
			}
			if !cluster.ValidateStreamToken(baseID, token, expStr, a.Secret) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusForbidden)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": "forbidden: invalid or expired stream token"})
				return
			}
			mediaID = baseID
			parts[0] = baseID
			cleanSubPath = filepath.Join(parts...)
			if hasMP4Suffix && len(parts) == 1 {
				cleanSubPath += ".mp4"
			}
		}
	} else if strings.HasPrefix(baseID, "t_") {
		if resolvedKey, _, err := cluster.ResolvePlaybackTicket(baseID, a.Secret); err == nil {
			mediaID = resolvedKey
			parts[0] = resolvedKey
			cleanSubPath = filepath.Join(parts...)
			if hasMP4Suffix && len(parts) == 1 {
				cleanSubPath += ".mp4"
			}
		}
	} else if hasMP4Suffix {
		mediaID = baseID
		parts[0] = baseID
		cleanSubPath = filepath.Join(parts...)
		if len(parts) == 1 {
			cleanSubPath += ".mp4"
		}
	}

	targetFilePath := a.resolveMediaPath(cleanSubPath)
	if targetFilePath == "" {
		http.NotFound(w, r)
		return
	}

	isPlay := (len(parts) == 1 || hasMP4Suffix) &&
		!strings.Contains(cleanSubPath, "audio_") &&
		!strings.Contains(cleanSubPath, "sub_") &&
		!strings.HasSuffix(cleanSubPath, ".vtt") &&
		!strings.HasSuffix(cleanSubPath, ".m4a")

	if isPlay && mediaID != "" {
		clientIP := r.Header.Get("CF-Connecting-IP")
		if clientIP == "" {
			clientIP = r.Header.Get("X-Forwarded-For")
		}
		if clientIP == "" {
			clientIP = r.RemoteAddr
		}
		if idx := strings.Index(clientIP, ","); idx != -1 {
			clientIP = strings.TrimSpace(clientIP[:idx])
		}
		if host, _, err := net.SplitHostPort(clientIP); err == nil {
			clientIP = host
		}
		a.recordPlayHit(mediaID, clientIP)
		a.recordViewerHeartbeat(mediaID, clientIP)
	}

	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")

	if strings.HasSuffix(targetFilePath, ".mp4") {
		w.Header().Set("Content-Type", "video/mp4")
	} else if strings.HasSuffix(targetFilePath, ".json") {
		w.Header().Set("Content-Type", "application/json")
	} else if strings.HasSuffix(targetFilePath, ".vtt") {
		w.Header().Set("Content-Type", "text/vtt")
	} else if strings.HasSuffix(targetFilePath, ".m4a") {
		w.Header().Set("Content-Type", "audio/mp4")
	} else if strings.HasSuffix(targetFilePath, ".m4s") {
		w.Header().Set("Content-Type", "video/iso.segment")
	}

	http.ServeFile(w, r, targetFilePath)
}

var (
	agentDedupeMu      sync.Mutex
	agentDedupe        = make(map[string]time.Time)
	agentHeartbeatMu   sync.Mutex
	agentHeartbeats    = make(map[string]time.Time)
)

func (a *Agent) recordViewerHeartbeat(key, clientIP string) {
	if a.CoordinatorURL == "" || key == "" {
		return
	}
	dedupeKey := clientIP + ":" + key
	agentHeartbeatMu.Lock()
	last, seen := agentHeartbeats[dedupeKey]
	now := time.Now()
	if seen && now.Sub(last) < 25*time.Second {
		agentHeartbeatMu.Unlock()
		return
	}
	agentHeartbeats[dedupeKey] = now
	if len(agentHeartbeats) > 10000 {
		for k, t := range agentHeartbeats {
			if now.Sub(t) > 60*time.Second {
				delete(agentHeartbeats, k)
			}
		}
	}
	agentHeartbeatMu.Unlock()

	go func() {
		payload, err := json.Marshal(map[string]string{
			"key":        key,
			"session_id": clientIP,
		})
		if err != nil {
			return
		}
		coord := strings.TrimRight(a.CoordinatorURL, "/")
		req, err := http.NewRequest(http.MethodPost, coord+"/api/stream/heartbeat", bytes.NewReader(payload))
		if err != nil {
			return
		}
		req.Header.Set("Content-Type", "application/json")
		if a.Secret != "" {
			req.Header.Set("X-Cluster-Secret", a.Secret)
		}
		client := &http.Client{Timeout: 3 * time.Second}
		resp, err := client.Do(req)
		if err == nil && resp != nil {
			_ = resp.Body.Close()
		}
	}()
}

func (a *Agent) recordPlayHit(key, clientIP string) {
	if a.CoordinatorURL == "" || key == "" {
		return
	}
	dedupeKey := clientIP + ":" + key
	agentDedupeMu.Lock()
	last, seen := agentDedupe[dedupeKey]
	now := time.Now()
	if seen && now.Sub(last) < 30*time.Minute {
		agentDedupeMu.Unlock()
		return
	}
	agentDedupe[dedupeKey] = now
	if len(agentDedupe) > 10000 {
		for k, t := range agentDedupe {
			if now.Sub(t) > 30*time.Minute {
				delete(agentDedupe, k)
			}
		}
	}
	agentDedupeMu.Unlock()

	go func() {
		payload, err := json.Marshal(map[string]string{
			"key":       key,
			"client_ip": clientIP,
		})
		if err != nil {
			return
		}
		coord := strings.TrimRight(a.CoordinatorURL, "/")
		req, err := http.NewRequest(http.MethodPost, coord+"/api/lifecycle/hit", bytes.NewReader(payload))
		if err != nil {
			return
		}
		req.Header.Set("Content-Type", "application/json")
		if a.Secret != "" {
			req.Header.Set("Authorization", "Bearer "+a.Secret)
		}
		client := &http.Client{Timeout: 3 * time.Second}
		resp, err := client.Do(req)
		if err == nil {
			_ = resp.Body.Close()
		}
	}()
}
