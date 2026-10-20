package api

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"stream/internal/media"
	"stream/internal/storage"
)

func (s *Service) CreateFromURL(rawURL string, createdBy ...string) (*CreateResponse, error) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return nil, fmt.Errorf("url is required")
	}
	if !strings.HasPrefix(rawURL, "http://") && !strings.HasPrefix(rawURL, "https://") {
		return nil, fmt.Errorf("url must start with http:// or https://")
	}

	creator := ""
	if len(createdBy) > 0 {
		creator = strings.TrimSpace(createdBy[0])
	}
	if creator == "" {
		creator = "Dashboard"
	}

	// Requirement: at least one processing VPS with aria2c + ffmpeg.
	workers, err := s.workerPool()
	if err != nil {
		return nil, err
	}

	// Metadata detection before any byte is downloaded.
	probe, err := ProbeRemote(rawURL)
	if err != nil {
		return nil, fmt.Errorf("probe failed: %w", err)
	}
	if probe.SizeBytes <= 0 {
		return nil, fmt.Errorf("cannot determine file size (source does not advertise Content-Length); size is required to reserve working space")
	}
	if err := ValidateVideo(probe.Filename, probe.ContentType); err != nil {
		return nil, err
	}

	key, err := s.uniqueKey()
	if err != nil {
		return nil, err
	}

	required := RequiredHeadroom(probe.SizeBytes)
	receivers := ReceiverCandidates(s.coord.GetNodes())
	placement, err := s.PlanPlacement(required, receivers)
	if err != nil {
		return nil, err
	}

	// Pick the processing worker independently: prefer the block owner
	// (direct write, no transfer), else the worker with the roomiest scratch and concurrency headroom.
	worker, err := PickProcessingWorkerWithLimits(workers, placement.NodeID, uint64(probe.SizeBytes), s.profiles(), s.activeIngestsPerWorker())
	if err != nil {
		return nil, err
	}
	if worker.Metrics.NodeID != placement.NodeID {
		log.Printf("[FileAPI] 🔀 decoupled placement for %s: process on %s, store on %s:%s",
			key, worker.Metrics.NodeID, placement.NodeID, placement.Path)
	}

	now := time.Now().UTC()
	rec := &FileRecord{
		Key:        key,
		Filename:   probe.Filename,
		SizeBytes:  probe.SizeBytes,
		MimeType:   probe.ContentType,
		SourceType: SourceURL,
		CreatedBy:  creator,
		CreatedAt:  now,
	}
	job := &FileJob{
		Key:       key,
		State:     StateDetected,
		SourceURL: rawURL,
		NodeID:    worker.Metrics.NodeID,
		Placement: placement,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.store.SaveFileRecord(rec); err != nil {
		return nil, err
	}
	if err := s.store.SaveFileJob(job); err != nil {
		return nil, err
	}

	if err := s.Dispatch(job, rec); err != nil {
		job.State = StateFailed
		job.Error = err.Error()
		_ = s.store.SaveFileJob(job)
		return nil, err
	}

	return &CreateResponse{
		Key:       key,
		State:     StateDownloading,
		Filename:  rec.Filename,
		SizeBytes: rec.SizeBytes,
		MimeType:  rec.MimeType,
		CreatedBy: creator,
		Placement: placement,
		StatusURL: "/api/v1/files/" + key,
		StreamURL: s.StreamURL(job),
		PlayURL:   "/stream/" + key,
		CreatedAt: now,
	}, nil
}

// ReserveUpload creates an upload slot: the client receives the key plus a
// one-shot upload URL. Placement is fixed now so bytes stream straight to
// the owning worker.
func (s *Service) ReserveUpload(rawFilename string, expectedSize int64, createdBy ...string) (*CreateResponse, error) {
	if strings.TrimSpace(rawFilename) == "" {
		return nil, fmt.Errorf("filename is required")
	}
	filename := media.SanitizeFilename(rawFilename)
	if err := ValidateVideo(filename, ""); err != nil {
		return nil, err
	}

	creator := ""
	if len(createdBy) > 0 {
		creator = strings.TrimSpace(createdBy[0])
	}
	if creator == "" {
		creator = "Dashboard"
	}

	workers, err := s.workerPool()
	if err != nil {
		return nil, err
	}

	key, err := s.uniqueKey()
	if err != nil {
		return nil, err
	}

	required := RequiredHeadroom(expectedSize)
	receivers := ReceiverCandidates(s.coord.GetNodes())
	placement, err := s.PlanPlacement(required, receivers)
	if err != nil {
		return nil, err
	}

	worker, err := PickProcessingWorkerWithLimits(workers, placement.NodeID, uint64(expectedSize), s.profiles(), s.activeIngestsPerWorker())
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	rec := &FileRecord{
		Key:        key,
		Filename:   filename,
		SizeBytes:  expectedSize,
		SourceType: SourceUpload,
		CreatedBy:  creator,
		CreatedAt:  now,
	}
	job := &FileJob{
		Key:       key,
		State:     StateAwaitingUpload,
		NodeID:    worker.Metrics.NodeID,
		Placement: placement,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.store.SaveFileRecord(rec); err != nil {
		return nil, err
	}
	if err := s.store.SaveFileJob(job); err != nil {
		return nil, err
	}

	return &CreateResponse{
		Key:       key,
		State:     StateAwaitingUpload,
		Filename:  rec.Filename,
		SizeBytes: rec.SizeBytes,
		CreatedBy: creator,
		UploadURL: "/api/v1/files/" + key + "/upload",
		Placement: placement,
		StatusURL: "/api/v1/files/" + key,
		PlayURL:   "/stream/" + key,
		CreatedAt: now,
	}, nil
}

// Dispatch streams the job to the assigned processing worker over HTTP.
// Direct mode (worker owns the block): bytes land straight on the block.
// Decoupled mode: the worker runs the job in its scratch folder and, on
// completion, streams the finished package to the block owner.
func (s *Service) Dispatch(job *FileJob, rec *FileRecord) error {
	worker := s.nodeByID(job.NodeID)
	if worker == nil {
		return fmt.Errorf("assigned worker '%s' is offline", job.NodeID)
	}

	payload := map[string]interface{}{
		"key":        job.Key,
		"source_url": job.SourceURL,
		"filename":   rec.Filename,
	}

	cleanTarget := CleanPlacementPath(job.Placement.Path)
	if job.NodeID == job.Placement.NodeID {
		// Direct: write onto the block this worker owns.
		payload["target_dir"] = cleanTarget
	} else {
		// Decoupled: process in scratch, then transfer to the block owner or S3.
		if strings.HasPrefix(job.Placement.NodeID, "bucket:") {
			bucketID := strings.TrimPrefix(job.Placement.NodeID, "bucket:")
			var s3Cfg *storage.S3Config
			if s.bucketResolver != nil {
				cfg, err := s.bucketResolver(bucketID)
				if err != nil || cfg == nil {
					return fmt.Errorf("storage bucket '%s' not found: %v", bucketID, err)
				}
				s3Cfg = cfg
			}
			payload["scratch"] = "true"
			payload["final_node_id"] = job.Placement.NodeID
			payload["final_dir"] = cleanTarget
			payload["final_addr"] = ""
			if s3Cfg != nil {
				payload["s3"] = s3Cfg
			}
		} else {
			owner := s.nodeByID(job.Placement.NodeID)
			if owner == nil {
				return fmt.Errorf("storage node '%s' is offline", job.Placement.NodeID)
			}
			payload["scratch"] = "true"
			payload["final_node_id"] = job.Placement.NodeID
			payload["final_dir"] = cleanTarget
			payload["final_addr"] = AgentBaseURL(owner)
		}
	}

	body, _ := json.Marshal(payload)

	ingestReq, err := http.NewRequest(http.MethodPost, AgentBaseURL(worker)+"/api/v1/ingest", strings.NewReader(string(body)))
	if err != nil {
		return fmt.Errorf("create ingest request failed: %w", err)
	}
	ingestReq.Header.Set("Content-Type", "application/json")
	if s.secret != "" {
		ingestReq.Header.Set("X-Cluster-Secret", s.secret)
	}

	resp, err := s.client.Do(ingestReq)
	if err != nil {
		return fmt.Errorf("worker unreachable: %w", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode >= 300 {
		return fmt.Errorf("worker rejected job (%s)", resp.Status)
	}

	job.Attempts++
	job.State = StateDownloading
	job.Progress = 0
	_ = s.store.SaveFileJob(job)
	return nil
}

// ApplyProgress ingests worker callbacks into the job table.
func (s *Service) ApplyProgress(key string, up *ProgressUpdate) error {
	job, err := s.store.GetFileJob(key)
	if err != nil || job == nil {
		return fmt.Errorf("unknown file key %s", key)
	}
	if up.State != "" {
		job.State = up.State
	}
	job.Progress = up.Percent
	job.Speed = up.Speed
	job.UpdatedAt = time.Now().UTC()
	if up.Error != "" {
		job.Error = up.Error
	}
	if up.Stage != "" {
		job.Stage = up.Stage
	}
	if up.StageName != "" {
		job.StageName = up.StageName
	}
	if up.StagePercent > 0 {
		job.StagePercent = up.StagePercent
	}
	if up.TransferredBytes > 0 {
		job.TransferredBytes = up.TransferredBytes
	}
	if up.TotalBytes > 0 {
		job.TotalBytes = up.TotalBytes
	}
	if up.ETA != "" {
		job.ETA = up.ETA
	}
	if up.Details != "" {
		job.Details = up.Details
	}
	if len(up.CMAFJSON) > 0 {
		job.MetadataJSON = up.CMAFJSON
		var cmaf struct {
			TotalBytes int64 `json:"total_bytes"`
			SizeBytes  int64 `json:"size_bytes"`
		}
		if err := json.Unmarshal(up.CMAFJSON, &cmaf); err == nil {
			size := cmaf.TotalBytes
			if size == 0 {
				size = cmaf.SizeBytes
			}
			if size > 0 {
				if rec, err := s.store.GetFileRecord(key); err == nil && rec != nil {
					if rec.SizeBytes == 0 || rec.SizeBytes != size {
						rec.SizeBytes = size
						_ = s.store.SaveFileRecord(rec)
					}
				}
			}
		}
	}
	if err := s.store.SaveFileJob(job); err != nil {
		return err
	}
	s.logProgress(job, up)
	return nil
}

var (
	progressLogMu   sync.Mutex
	lastProgressLog = make(map[string]time.Time)
	lastStateLog    = make(map[string]FileState)
)

func (s *Service) logProgress(job *FileJob, up *ProgressUpdate) {
	progressLogMu.Lock()
	defer progressLogMu.Unlock()

	key := job.Key
	lastState := lastStateLog[key]
	lastTime := lastProgressLog[key]
	now := time.Now()

	stateChanged := lastState != up.State
	timeToLog := now.Sub(lastTime) >= 3*time.Second

	if stateChanged || timeToLog || up.State == StateCompleted || up.State == StateFailed {
		lastStateLog[key] = up.State
		lastProgressLog[key] = now

		switch up.State {
		case StateDownloading:
			fmt.Printf("[Transfer %s] 📥 downloading on %s: %.1f%% (%s)\n", key, job.NodeID, up.Percent, up.Speed)
		case StateProcessing:
			fmt.Printf("[Transfer %s] ⚙️ processing on %s: %.1f%% (%s)\n", key, job.NodeID, up.Percent, up.Speed)
		case StateTransferring:
			fmt.Printf("[Transfer %s] 🔀 transferring %s -> %s: %.1f%% (Speed: %s)\n", key, job.NodeID, job.Placement.NodeID, up.Percent, up.Speed)
		case StateCompleted:
			rec, _ := s.store.GetFileRecord(key)
			var sizeStr string
			if rec != nil && rec.SizeBytes > 0 {
				sizeStr = fmt.Sprintf(" | Size: %.1f MB", float64(rec.SizeBytes)/(1024*1024))
			}
			fmt.Printf("[Transfer %s] ✅ Completed! Stored on %s:%s%s\n", key, job.Placement.NodeID, job.Placement.Path, sizeStr)
			delete(lastStateLog, key)
			delete(lastProgressLog, key)
		case StateFailed:
			fmt.Printf("[Transfer %s] ❌ Failed on %s: %s\n", key, job.NodeID, up.Error)
			delete(lastStateLog, key)
			delete(lastProgressLog, key)
		}
	}
}

// Status assembles the public status view for a key.
func (s *Service) Retry(key string) (*FileJob, error) {
	job, err := s.store.GetFileJob(key)
	if err != nil || job == nil {
		return nil, fmt.Errorf("job not found for key %s", key)
	}
	rec, err := s.store.GetFileRecord(key)
	if err != nil || rec == nil {
		return nil, fmt.Errorf("file record not found for key %s", key)
	}
	if job.State == StateCompleted {
		return nil, fmt.Errorf("job '%s' is already completed and ready", key)
	}
	job.State = StateDownloading
	job.Error = ""
	job.Progress = 0
	job.Speed = "Restarting..."
	job.Stage = "download"
	job.StageName = "Restarting Job"
	job.StagePercent = 0
	job.UpdatedAt = time.Now().UTC()
	if err := s.store.SaveFileJob(job); err != nil {
		return nil, err
	}
	if err := s.Dispatch(job, rec); err != nil {
		job.State = StateFailed
		job.Error = err.Error()
		_ = s.store.SaveFileJob(job)
		return nil, err
	}
	return job, nil
}
