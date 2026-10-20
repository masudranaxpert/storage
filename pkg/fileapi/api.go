package fileapi

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"stream/pkg/media"
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
	mux.HandleFunc("/api/v1/files-sync", s.handleSyncFiles)
}

func (s *Service) handleSyncFiles(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.isAuthorized(r) {
		writeErr(w, http.StatusUnauthorized, fmt.Errorf("unauthorized: valid API key required"))
		return
	}
	force := r.URL.Query().Get("force") != "false"
	purged, err := s.ReconcileGhostRecordsOpt(force)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	remaining := 0
	if recs, err := s.store.ListFileRecords(); err == nil {
		remaining = len(recs)
	}
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":    "synced",
		"purged":    purged,
		"remaining": remaining,
	})
}

func (s *Service) isAuthorized(r *http.Request) bool {
	if s.authValidator == nil {
		return false
	}
	return s.authValidator(r)
}

func (s *Service) handleFiles(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodOptions && !s.isAuthorized(r) {
		writeErr(w, http.StatusUnauthorized, fmt.Errorf("unauthorized: valid API key required"))
		return
	}
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
	case http.MethodDelete:
		s.handleBatchDelete(w, r)
	case http.MethodOptions:
		w.WriteHeader(http.StatusOK)
	default:
		writeErr(w, http.StatusMethodNotAllowed, fmt.Errorf("method not allowed"))
	}
}

func (s *Service) handleBatchDelete(w http.ResponseWriter, r *http.Request) {
	var keys []string
	if r.Body != nil {
		var req struct {
			Keys []string `json:"keys"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err == nil && len(req.Keys) > 0 {
			keys = req.Keys
		}
	}
	if len(keys) == 0 {
		if q := r.URL.Query().Get("keys"); q != "" {
			keys = strings.Split(q, ",")
		} else if qList := r.URL.Query()["key"]; len(qList) > 0 {
			keys = qList
		}
	}
	if len(keys) == 0 {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("no keys provided for deletion (provide 'keys' array in JSON body or ?keys=id1,id2)"))
		return
	}
	deleted := make([]string, 0, len(keys))
	failed := make(map[string]string)
	for _, k := range keys {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		if err := s.Delete(k); err != nil {
			failed[k] = err.Error()
		} else {
			deleted = append(deleted, k)
		}
	}
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":  "completed",
		"deleted": deleted,
		"failed":  failed,
		"count":   len(deleted),
	})
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
	creator := ""
	if s.creatorResolver != nil {
		creator = s.creatorResolver(r)
	}
	if creator == "" && req.CreatedBy != "" {
		creator = req.CreatedBy
	}
	resp, err := s.CreateFromURL(req.URL, creator)
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
	if !s.isAuthorized(r) {
		writeErr(w, http.StatusUnauthorized, fmt.Errorf("unauthorized: valid API key required"))
		return
	}
	var req CreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("invalid json body"))
		return
	}
	creator := ""
	if s.creatorResolver != nil {
		creator = s.creatorResolver(r)
	}
	if creator == "" && req.CreatedBy != "" {
		creator = req.CreatedBy
	}
	resp, err := s.ReserveUpload(req.Filename, req.ExpectedSize, creator)
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

	if r.Method != http.MethodOptions && !s.isAuthorized(r) {
		writeErr(w, http.StatusUnauthorized, fmt.Errorf("unauthorized: valid API key required"))
		return
	}

	if action == "upload" {
		s.handleUpload(w, r, key)
		return
	}
	if action == "retry" {
		s.handleRetry(w, r, key)
		return
	}
	if action == "move" && s.moveHandler != nil {
		s.moveHandler(w, r, key)
		return
	}
	if action == "placement" && s.placementHandler != nil {
		s.placementHandler(w, r, key)
		return
	}
	if action == "demand" && s.demandHandler != nil {
		s.demandHandler(w, r, key)
		return
	}
	if (action == "token" || action == "streams") && r.Method == http.MethodGet {
		s.handleToken(w, r, key)
		return
	}
	if (action == "info" || action == "status" || action == "details") && r.Method == http.MethodGet {
		s.handleFileInfo(w, r, key)
		return
	}
	if (action == "metadata" || action == "meta") && r.Method == http.MethodGet {
		s.handleFileMetadata(w, r, key)
		return
	}
	if action != "" {
		writeErr(w, http.StatusNotFound, fmt.Errorf("not found"))
		return
	}

	switch r.Method {
	case http.MethodGet:
		// Default GET /api/v1/files/{key}: returns the direct player stream payload
		s.handleToken(w, r, key)
	case http.MethodDelete:
		keys := strings.Split(key, ",")
		if len(keys) > 1 {
			deleted := make([]string, 0, len(keys))
			failed := make(map[string]string)
			for _, k := range keys {
				k = strings.TrimSpace(k)
				if k == "" {
					continue
				}
				if err := s.Delete(k); err != nil {
					failed[k] = err.Error()
				} else {
					deleted = append(deleted, k)
				}
			}
			json.NewEncoder(w).Encode(map[string]interface{}{
				"status":  "completed",
				"deleted": deleted,
				"failed":  failed,
				"count":   len(deleted),
			})
			return
		}
		if err := s.Delete(key); err != nil {
			writeErr(w, classifyErr(err), err)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"status": "deleted", "key": key})
	default:
		writeErr(w, http.StatusMethodNotAllowed, fmt.Errorf("method not allowed"))
	}
}

// validSecret verifies the cluster secret header or Bearer token for internal callbacks.
func (s *Service) validSecret(r *http.Request) bool {
	if s.secret == "" {
		return false
	}
	provided := r.Header.Get("X-Cluster-Secret")
	if provided == "" {
		if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
			candidate := strings.TrimPrefix(auth, "Bearer ")
			if !strings.HasPrefix(candidate, "sm_live_") {
				provided = candidate
			}
		}
	}
	return provided != "" && subtle.ConstantTimeCompare([]byte(provided), []byte(s.secret)) == 1
}

// handleProgress accepts authenticated worker callbacks (state/percent/speed/cmaf).
func (s *Service) handleProgress(w http.ResponseWriter, r *http.Request, key string) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, fmt.Errorf("method not allowed"))
		return
	}
	if !s.validSecret(r) {
		writeErr(w, http.StatusUnauthorized, fmt.Errorf("unauthorized: valid cluster secret required"))
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

func (s *Service) handleRetry(w http.ResponseWriter, r *http.Request, key string) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, fmt.Errorf("method not allowed"))
		return
	}
	job, err := s.Retry(key)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status": "retrying",
		"key":    job.Key,
		"state":  job.State,
	})
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
	if s.secret != "" {
		workerReq.Header.Set("X-Cluster-Secret", s.secret)
	}
	q := workerReq.URL.Query()
	q.Set("key", key)
	cleanTarget := CleanPlacementPath(job.Placement.Path)
	if job.NodeID == job.Placement.NodeID {
		// Direct mode: bytes stream straight onto the block this worker owns.
		q.Set("dir", cleanTarget)
	} else {
		// Decoupled: land in the worker's scratch; it transfers after remux.
		q.Set("scratch", "1")
		q.Set("final_node_id", job.Placement.NodeID)
		q.Set("final_dir", cleanTarget)
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

// Delete removes a file from its worker, cleans up lifecycle state/tasks, and drops both tables.
func (s *Service) Delete(key string) error {
	rec, err := s.store.GetFileRecord(key)
	if err != nil {
		return fmt.Errorf("file '%s' not found", key)
	}
	job, err := s.store.GetFileJob(key)
	if err != nil || job == nil {
		return fmt.Errorf("file '%s' not found", key)
	}
	if (job.State == StateCompleted || job.Placement.NodeID != "" || job.NodeID != "") && s.coord != nil {
		if err := s.deleteFromWorker(rec); err != nil {
			return err
		}
	}
	if s.deleteHandler != nil {
		s.deleteHandler(key)
	}
	_ = s.store.DeleteFileJob(key)
	_ = s.store.DeleteFileRecord(key)
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

// StreamURLResponse holds the complete multi-track signed playback URLs for modern players.
type StreamURLResponse struct {
	Key          string                `json:"key"`
	Ticket       string                `json:"ticket,omitempty"`
	Filename     string                `json:"filename,omitempty"`
	Token        string                `json:"token"`
	ExpiresAt    int64                 `json:"expires_at"`
	ExpiresInSec int64                 `json:"expires_in_sec"`
	VideoURL     string                `json:"video_url"`
	PlayURL      string                `json:"play_url"`
	AudioTracks  []StreamAudioTrack    `json:"audio_tracks"`
	Subtitles    []StreamSubtitleTrack `json:"subtitles"`
}

type StreamAudioTrack struct {
	Index         int    `json:"index"`
	Language      string `json:"language,omitempty"`
	Title         string `json:"title,omitempty"`
	Codec         string `json:"codec,omitempty"`
	Channels      int    `json:"channels,omitempty"`
	ChannelLayout string `json:"channel_layout,omitempty"`
	BitrateBps    int64  `json:"bitrate_bps,omitempty"`
	URL           string `json:"url"`
	PlayURL       string `json:"play_url"`
	Embedded      bool   `json:"embedded"`
}

type StreamSubtitleTrack struct {
	Index    int    `json:"index"`
	Language string `json:"language,omitempty"`
	Title    string `json:"title,omitempty"`
	Codec    string `json:"codec,omitempty"`
	URL      string `json:"url"`
	PlayURL  string `json:"play_url"`
}

// handleToken generates an HMAC-SHA256 signed playback token and multi-track URLs for anti-leech protection.
func (s *Service) handleToken(w http.ResponseWriter, r *http.Request, key string) {
	duration := 4 * time.Hour
	if d := r.URL.Query().Get("hours"); d != "" {
		if h, err := strconv.Atoi(d); err == nil && h > 0 && h <= 72 {
			duration = time.Duration(h) * time.Hour
		}
	}
	exp := time.Now().Add(duration).Unix()
	token := GenerateStreamToken(key, exp, s.secret)
	ticket, _ := GeneratePlaybackTicket(key, exp, s.secret)

	identifier := key
	if ticket != "" {
		identifier = ticket
	}

	playURL := fmt.Sprintf("/stream/%s", identifier)
	if identifier == key {
		playURL = fmt.Sprintf("/stream/%s?token=%s&exp=%d", key, token, exp)
	}

	resp := StreamURLResponse{
		Key:          key,
		Ticket:       ticket,
		Token:        token,
		ExpiresAt:    exp,
		ExpiresInSec: int64(duration.Seconds()),
		PlayURL:      playURL,
		VideoURL:     playURL,
		AudioTracks:  make([]StreamAudioTrack, 0),
		Subtitles:    make([]StreamSubtitleTrack, 0),
	}

	if s.store != nil {
		job, err := s.store.GetFileJob(key)
		if err != nil || job == nil {
			writeErr(w, http.StatusNotFound, fmt.Errorf("file '%s' not found", key))
			return
		}
		if rec, err := s.store.GetFileRecord(key); err == nil && rec != nil {
			resp.Filename = rec.Filename
		}
		directBase := s.StreamURL(job)
		if directBase != "" {
			if identifier != key {
				directBase = strings.Replace(directBase, "/stream/"+key, "/stream/"+identifier, 1)
				resp.VideoURL = directBase
			} else {
				resp.VideoURL = fmt.Sprintf("%s?token=%s&exp=%d", directBase, token, exp)
			}
		}

			if len(job.MetadataJSON) > 0 {
				var meta media.MediaMetadata
				if json.Unmarshal(job.MetadataJSON, &meta) == nil {
					if meta.Filename != "" {
						resp.Filename = meta.Filename
					} else if meta.OriginalFilename != "" {
						resp.Filename = meta.OriginalFilename
					}

					numAudios := len(meta.AudioTracks)
					for i, aTrack := range meta.AudioTracks {
						trackURL := playURL
						trackPlayURL := playURL
						embedded := true

						if numAudios > 1 || aTrack.File != "" {
							file := aTrack.File
							if file == "" {
								lang := aTrack.Language
								if lang == "" {
									lang = fmt.Sprintf("track_%d", i+1)
								}
								file = fmt.Sprintf("audio_%d_%s.m4a", i+1, lang)
							}
							if identifier != key {
								trackPlayURL = fmt.Sprintf("/stream/%s/%s", identifier, file)
								if directBase != "" {
									trackURL = fmt.Sprintf("%s/%s", directBase, file)
								} else {
									trackURL = trackPlayURL
								}
							} else {
								trackPlayURL = fmt.Sprintf("/stream/%s/%s?token=%s&exp=%d", key, file, token, exp)
								if directBase != "" {
									trackURL = fmt.Sprintf("%s/%s?token=%s&exp=%d", directBase, file, token, exp)
								} else {
									trackURL = trackPlayURL
								}
							}
							embedded = false
						} else if directBase != "" {
							trackURL = resp.VideoURL
						}

						resp.AudioTracks = append(resp.AudioTracks, StreamAudioTrack{
							Index:         aTrack.Index,
							Language:      aTrack.Language,
							Title:         aTrack.Title,
							Codec:         aTrack.Codec,
							Channels:      aTrack.Channels,
							ChannelLayout: aTrack.ChannelLayout,
							BitrateBps:    aTrack.BitrateBps,
							URL:           trackURL,
							PlayURL:       trackPlayURL,
							Embedded:      embedded,
						})
					}

					for i, sTrack := range meta.Subtitles {
						file := sTrack.File
						if file == "" {
							lang := sTrack.Language
							if lang == "" {
								lang = fmt.Sprintf("sub_%d", i+1)
							}
							file = fmt.Sprintf("subtitle_%d_%s.vtt", i+1, lang)
						}
						var subPlayURL, subURL string
						if identifier != key {
							subPlayURL = fmt.Sprintf("/stream/%s/%s", identifier, file)
							if directBase != "" {
								subURL = fmt.Sprintf("%s/%s", directBase, file)
							} else {
								subURL = subPlayURL
							}
						} else {
							subPlayURL = fmt.Sprintf("/stream/%s/%s?token=%s&exp=%d", key, file, token, exp)
							subURL = subPlayURL
							if directBase != "" {
								subURL = fmt.Sprintf("%s/%s?token=%s&exp=%d", directBase, file, token, exp)
							}
						}
						resp.Subtitles = append(resp.Subtitles, StreamSubtitleTrack{
							Index:    sTrack.Index,
							Language: sTrack.Language,
							Title:    sTrack.Title,
							Codec:    sTrack.Codec,
							URL:      subURL,
							PlayURL:  subPlayURL,
						})
					}
				}
			}
		}

	json.NewEncoder(w).Encode(resp)
}

// handleFileInfo returns the full operational lifecycle, stage, and node placement details of a file.
func (s *Service) handleFileInfo(w http.ResponseWriter, r *http.Request, key string) {
	st, err := s.Status(key)
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	json.NewEncoder(w).Encode(st)
}

// handleFileMetadata returns the media tracks and audio/video codec metadata of a file.
func (s *Service) handleFileMetadata(w http.ResponseWriter, r *http.Request, key string) {
	st, err := s.Status(key)
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	if st.Metadata != nil {
		json.NewEncoder(w).Encode(st.Metadata)
		return
	}
	json.NewEncoder(w).Encode(map[string]interface{}{
		"key":      key,
		"filename": st.Filename,
		"state":    st.State,
		"metadata": nil,
	})
}

