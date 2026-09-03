package fileapi

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Register mounts the v1 file API on the mux:
//
//	POST   /api/v1/files                 {url}                         -> validate + dispatch download
//	POST   /api/v1/files/upload          {filename, size_bytes}        -> reserve slot, return upload_url
//	POST   /api/v1/files/{key}/upload    raw bytes | multipart file    -> stream to worker, process
//	POST   /api/v1/files/{key}/progress  worker callback (internal)
//	GET    /api/v1/files                 list all files
//	GET    /api/v1/files/{key}           status
//	DELETE /api/v1/files/{key}           remove from worker + table
func (s *Service) Register(mux *http.ServeMux) {
	mux.HandleFunc("/api/v1/files", s.handleFiles)
	mux.HandleFunc("/api/v1/files/", s.handleFileSub)
	mux.HandleFunc("/api/v1/files-upload", s.handleReserveUpload)
}

func (s *Service) handleFiles(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch r.Method {
	case http.MethodPost:
		s.handleCreate(w, r)
	case http.MethodGet:
		list, err := s.List()
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		json.NewEncoder(w).Encode(map[string]interface{}{"files": list})
	case http.MethodOptions:
		w.WriteHeader(http.StatusOK)
	default:
		writeErr(w, http.StatusMethodNotAllowed, fmt.Errorf("method not allowed"))
	}
}

func (s *Service) handleCreate(w http.ResponseWriter, r *http.Request) {
	var req CreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("invalid json body"))
		return
	}
	if strings.TrimSpace(req.URL) == "" {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("'url' is required"))
		return
	}
	resp, err := s.CreateFromURL(req.URL)
	if err != nil {
		writeErr(w, classifyErr(err), err)
		return
	}
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(resp)
}

func (s *Service) handleReserveUpload(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, fmt.Errorf("method not allowed"))
		return
	}
	var req CreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("invalid json body"))
		return
	}
	resp, err := s.ReserveUpload(req.Filename, req.ExpectedSize)
	if err != nil {
		writeErr(w, classifyErr(err), err)
		return
	}
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(resp)
}

func (s *Service) handleFileSub(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/") // api/v1/files/{key}[/{action}]
	if len(parts) < 4 {
		writeErr(w, http.StatusNotFound, fmt.Errorf("not found"))
		return
	}
	key := parts[3]
	action := ""
	if len(parts) >= 5 {
		action = parts[4]
	}

	if action == "progress" {
		s.handleProgress(w, r, key)
		return
	}
	if action == "upload" {
		s.handleUpload(w, r, key)
		return
	}
	if action != "" {
		writeErr(w, http.StatusNotFound, fmt.Errorf("not found"))
		return
	}

	switch r.Method {
	case http.MethodGet:
		st, err := s.Status(key)
		if err != nil {
			writeErr(w, http.StatusNotFound, err)
			return
		}
		json.NewEncoder(w).Encode(st)
	case http.MethodDelete:
		if err := s.Delete(key); err != nil {
			writeErr(w, classifyErr(err), err)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"status": "deleted", "key": key})
	default:
		writeErr(w, http.StatusMethodNotAllowed, fmt.Errorf("method not allowed"))
	}
}

// handleProgress accepts worker callbacks (state/percent/speed/cmaf).
func (s *Service) handleProgress(w http.ResponseWriter, r *http.Request, key string) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, fmt.Errorf("method not allowed"))
		return
	}
	var up ProgressUpdate
	if err := json.NewDecoder(r.Body).Decode(&up); err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("invalid progress payload"))
		return
	}
	if err := s.ApplyProgress(key, &up); err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// handleUpload streams client bytes to the assigned worker, then the worker
// validates (magic bytes) and remuxes asynchronously. Multipart bodies are
// parsed streaming; raw bodies are piped 1:1 — no full buffering either way.
func (s *Service) handleUpload(w http.ResponseWriter, r *http.Request, key string) {
	if r.Method != http.MethodPost && r.Method != http.MethodPut {
		writeErr(w, http.StatusMethodNotAllowed, fmt.Errorf("method not allowed"))
		return
	}
	job, err := s.store.GetFileJob(key)
	if err != nil || job == nil {
		writeErr(w, http.StatusNotFound, fmt.Errorf("file '%s' not found", key))
		return
	}
	if job.State != StateAwaitingUpload {
		writeErr(w, http.StatusConflict, fmt.Errorf("file '%s' is not awaiting upload (state: %s)", key, job.State))
		return
	}
	rec, err := s.store.GetFileRecord(key)
	if err != nil || rec == nil {
		writeErr(w, http.StatusNotFound, fmt.Errorf("file '%s' not found", key))
		return
	}

	workerURL := fmt.Sprintf("%s/api/v1/ingest-upload", s.workerBase(job))
	workerReq, err := http.NewRequest(http.MethodPost, workerURL, r.Body)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	q := workerReq.URL.Query()
	q.Set("key", key)
	if job.NodeID == job.Placement.NodeID {
		// Direct mode: bytes stream straight onto the block this worker owns.
		q.Set("dir", job.Placement.Path)
	} else {
		// Decoupled: land in the worker's scratch; it transfers after remux.
		q.Set("scratch", "1")
		q.Set("final_node_id", job.Placement.NodeID)
		q.Set("final_dir", job.Placement.Path)
		if owner := s.nodeByID(job.Placement.NodeID); owner != nil {
			q.Set("final_addr", AgentBaseURL(owner))
		} else {
			writeErr(w, http.StatusBadGateway, fmt.Errorf("storage node '%s' is offline", job.Placement.NodeID))
			return
		}
	}

	// Multipart: forward only the first file part's stream (no buffering).
	if mediatype := r.Header.Get("Content-Type"); strings.HasPrefix(mediatype, "multipart/form-data") {
		mr, err := r.MultipartReader()
		if err != nil {
			writeErr(w, http.StatusBadRequest, fmt.Errorf("invalid multipart body"))
			return
		}
		var part io.ReadCloser
		for {
			p, err := mr.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				writeErr(w, http.StatusBadRequest, err)
				return
			}
			if p.FileName() != "" {
				part = p
				break
			}
			p.Close()
		}
		if part == nil {
			writeErr(w, http.StatusBadRequest, fmt.Errorf("no file part found in multipart body"))
			return
		}
		defer part.Close()
		workerReq.Body = part
	} else {
		q.Set("size", r.Header.Get("Content-Length"))
	}
	workerReq.URL.RawQuery = q.Encode()
	workerReq.ContentLength = r.ContentLength

	totalSize := r.ContentLength
	if rec.SizeBytes > 0 && totalSize <= 0 {
		totalSize = rec.SizeBytes
	}

	job.State = StateUploading
	job.Stage = "upload"
	job.StageName = "Uploading to Cluster"
	job.StagePercent = 0
	job.TotalBytes = totalSize
	job.UpdatedAt = time.Now().UTC()
	_ = s.store.SaveFileJob(job)

	pr := &uploadProgressReader{
		r:         workerReq.Body,
		total:     totalSize,
		lastTime:  time.Now(),
		lastBytes: 0,
		onUpdate: func(read, total int64, pct float64, speed string) {
			job.Progress = pct
			job.StagePercent = pct
			job.Speed = speed
			job.TransferredBytes = read
			job.UpdatedAt = time.Now().UTC()
			_ = s.store.SaveFileJob(job)
		},
	}
	workerReq.Body = io.NopCloser(pr)

	resp, err := s.client.Do(workerReq)
	if err != nil {
		job.State = StateFailed
		job.Error = fmt.Sprintf("worker unreachable: %v", err)
		_ = s.store.SaveFileJob(job)
		writeErr(w, http.StatusBadGateway, fmt.Errorf("worker unreachable: %w", err))
		return
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode >= 300 {
		job.State = StateFailed
		job.Error = fmt.Sprintf("worker rejected upload (%s)", resp.Status)
		_ = s.store.SaveFileJob(job)
		writeErr(w, http.StatusBadGateway, fmt.Errorf("worker rejected upload (%s)", resp.Status))
		return
	}

	job.State = StateProcessing
	job.Stage = "process"
	job.StageName = "Validating & Remuxing"
	job.StagePercent = 100
	job.UpdatedAt = time.Now().UTC()
	_ = s.store.SaveFileJob(job)

	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":     "accepted",
		"key":        key,
		"status_url": "/api/v1/files/" + key,
	})
}

type uploadProgressReader struct {
	r         io.Reader
	total     int64
	read      int64
	lastTime  time.Time
	lastBytes int64
	onUpdate  func(read, total int64, pct float64, speed string)
}

func (u *uploadProgressReader) Read(p []byte) (int, error) {
	n, err := u.r.Read(p)
	if n > 0 {
		u.read += int64(n)
		now := time.Now()
		if now.Sub(u.lastTime) >= 800*time.Millisecond || (u.total > 0 && u.read >= u.total) {
			sec := now.Sub(u.lastTime).Seconds()
			speedStr := ""
			if sec > 0 {
				bps := float64(u.read-u.lastBytes) / sec
				speedStr = formatSpeedBps(bps)
			}
			pct := float64(0)
			if u.total > 0 {
				pct = (float64(u.read) / float64(u.total)) * 100.0
				if pct > 100 {
					pct = 100
				}
			}
			if u.onUpdate != nil {
				u.onUpdate(u.read, u.total, pct, speedStr)
			}
			u.lastTime = now
			u.lastBytes = u.read
		}
	}
	return n, err
}

func formatSpeedBps(bps float64) string {
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

// Delete removes a file from its worker and both tables.
func (s *Service) Delete(key string) error {
	rec, err := s.store.GetFileRecord(key)
	if err != nil {
		return fmt.Errorf("file '%s' not found", key)
	}
	job, err := s.store.GetFileJob(key)
	if err != nil || job == nil {
		return fmt.Errorf("file '%s' not found", key)
	}
	if job.State == StateCompleted || job.Placement.NodeID != "" {
		statuses := s.tiers.Resolve(s.coord.GetNodes(), s.resolveUsage())
		hdd := s.tierByID(statuses, DefaultPolicy.HDDTierID)
		if err := s.deleteFromWorker(rec, hdd); err != nil {
			return err
		}
	}
	return nil
}

// workerBase resolves the HTTP root of the job's processing worker (which
// may differ from the final placement node in decoupled mode).
func (s *Service) workerBase(job *FileJob) string {
	if node := s.nodeByID(job.NodeID); node != nil {
		return AgentBaseURL(node)
	}
	return ""
}

// classifyErr maps domain failures onto HTTP codes.
func classifyErr(err error) int {
	msg := err.Error()
	switch {
	case err == ErrNoWorker:
		return http.StatusServiceUnavailable
	case strings.Contains(msg, "no storage"):
		return http.StatusInsufficientStorage
	case strings.Contains(msg, "not found"):
		return http.StatusNotFound
	case strings.Contains(msg, "unreachable"), strings.Contains(msg, "rejected"):
		return http.StatusBadGateway
	case strings.Contains(msg, "is not a supported video"), strings.Contains(msg, "is not a video"), strings.Contains(msg, "cannot determine"):
		return http.StatusUnprocessableEntity
	default:
		return http.StatusBadRequest
	}
}

func writeErr(w http.ResponseWriter, code int, err error) {
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}
