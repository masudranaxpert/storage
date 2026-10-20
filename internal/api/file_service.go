package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"stream/internal/cache"
	"stream/internal/cluster"
	"stream/internal/media"
	"stream/internal/storage"
)


// TierSource resolves tier definitions against live telemetry.
type TierSource interface {
	List() []storage.Tier
	Resolve(nodes []*cluster.NodeRecord, usedPerNode map[string]uint64) []storage.TierStatus
}

// Service orchestrates the v1 file API on the master.
type Service struct {
	store            JobStore
	tiers            TierSource
	coord            *cluster.Coordinator
	profiles         func() map[string]cluster.ProcessingProfile
	usage            func() map[string]uint64
	ingestOrder      func() []int
	moveHandler      func(w http.ResponseWriter, r *http.Request, key string)
	placementHandler func(w http.ResponseWriter, r *http.Request, key string)
	demandHandler    func(w http.ResponseWriter, r *http.Request, key string)
	heartbeatHandler func(w http.ResponseWriter, r *http.Request, key string)
	client           *http.Client
	secret           string
	authValidator    func(r *http.Request) bool
	creatorResolver  func(r *http.Request) string
	bucketResolver   func(id string) (*storage.S3Config, error)
	deleteHandler    func(key string)
	strikeMu         sync.Mutex
	missingStrike    map[string]int
}

// SetBucketResolver configures remote S3/R2 bucket configuration lookup for storage placement.
func (s *Service) SetBucketResolver(fn func(id string) (*storage.S3Config, error)) {
	s.bucketResolver = fn
}

// SetCreatorResolver configures request provenance identification (API key label or Dashboard).
func (s *Service) SetCreatorResolver(fn func(r *http.Request) string) {
	s.creatorResolver = fn
}

// SetAuthValidator configures custom request authentication (API keys or admin session).
func (s *Service) SetAuthValidator(fn func(r *http.Request) bool) {
	s.authValidator = fn
}

// SetSecret sets the cluster secret used to authenticate requests to workers.
func (s *Service) SetSecret(secret string) {
	s.secret = secret
}

// SetIngestOrder injects a provider returning the ordered tier IDs for new file placement.
func (s *Service) SetIngestOrder(fn func() []int) {
	s.ingestOrder = fn
}

// SetMoveHandler registers the HTTP handler for POST /api/v1/files/{key}/move without importing lifecycle.
func (s *Service) SetMoveHandler(fn func(w http.ResponseWriter, r *http.Request, key string)) {
	s.moveHandler = fn
}

// SetPlacementHandler registers the HTTP handler for GET /api/v1/files/{key}/placement without importing lifecycle.
func (s *Service) SetPlacementHandler(fn func(w http.ResponseWriter, r *http.Request, key string)) {
	s.placementHandler = fn
}

// SetDemandHandler registers the HTTP handler for POST /api/v1/files/{key}/demand without importing lifecycle.
func (s *Service) SetDemandHandler(fn func(w http.ResponseWriter, r *http.Request, key string)) {
	s.demandHandler = fn
}

// SetHeartbeatHandler registers the HTTP handler for POST /api/v1/files/{key}/heartbeat.
func (s *Service) SetHeartbeatHandler(fn func(w http.ResponseWriter, r *http.Request, key string)) {
	s.heartbeatHandler = fn
}

// SetDeleteHandler registers a callback invoked whenever a file is deleted from the cluster.
func (s *Service) SetDeleteHandler(fn func(key string)) {
	s.deleteHandler = fn
}

// NewService wires the orchestration service. profiles and usage are injected
// as funcs so the service always sees live values.
func NewService(store JobStore, tiers TierSource, coord *cluster.Coordinator,
	profiles func() map[string]cluster.ProcessingProfile, usage func() map[string]uint64) *Service {
	return &Service{
		store:         store,
		tiers:         tiers,
		coord:         coord,
		profiles:      profiles,
		usage:         usage,
		client:        &http.Client{Timeout: 30 * time.Second},
		missingStrike: make(map[string]int),
	}
}

// Run executes the background job watchdog until ctx is cancelled.
func (s *Service) Run(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.sweepStaleJobs()
		}
	}
}

// sweepStaleJobs scans active jobs and marks them failed if their worker goes offline for > 3m.
// Without a backing store (degraded in-memory coordinator) there is nothing to sweep.
func (s *Service) sweepStaleJobs() {
	if s.store == nil {
		return
	}
	jobs, err := s.store.ListFileJobs()
	if err != nil {
		return
	}
	now := time.Now().UTC()
	for _, job := range jobs {
		if job.State == StateCompleted || job.State == StateFailed {
			continue
		}
		if now.Sub(job.UpdatedAt) > 3*time.Minute {
			worker := s.nodeByID(job.NodeID)
			if worker == nil || worker.Status == cluster.StatusOffline {
				job.State = StateFailed
				job.Error = fmt.Sprintf("worker node '%s' went offline or timed out", job.NodeID)
				job.UpdatedAt = now
				_ = s.store.SaveFileJob(job)
				fmt.Printf("[FileAPI Watchdog] ⚠️ File job '%s' on %s marked failed: worker went offline\n", job.Key, job.NodeID)
			}
		}
	}
}

// ReconcileGhostRecords verifies that completed file records actually exist on the target worker or bucket.
// Requires 3 consecutive 404 strikes before deleting DB rows to avoid purging on transient glitches,
// unless force is true (e.g. manual admin sync).
func (s *Service) ReconcileGhostRecords() (int, error) {
	return s.ReconcileGhostRecordsOpt(false)
}

// ReconcileGhostRecordsOpt reconciles completed file records against live workers and cloud buckets.
func (s *Service) ReconcileGhostRecordsOpt(force bool) (int, error) {
	recs, err := s.store.ListFileRecords()
	if err != nil {
		return 0, err
	}
	client := &http.Client{Timeout: 3 * time.Second}
	purged := 0
	for _, rec := range recs {
		job, err := s.store.GetFileJob(rec.Key)
		if err != nil || job == nil || job.State != StateCompleted {
			continue
		}

		// 1. Reconcile remote S3 / Cloudflare R2 bucket objects
		if strings.HasPrefix(job.Placement.NodeID, "bucket:") {
			bucketID := strings.TrimPrefix(job.Placement.NodeID, "bucket:")
			if s.bucketResolver != nil {
				cfg, err := s.bucketResolver(bucketID)
				if err == nil && cfg != nil {
					cCfg := storage.S3Config{
						Endpoint:  cfg.Endpoint,
						Region:    cfg.Region,
						Name:      cfg.Name,
						AccessKey: cfg.AccessKey,
						SecretKey: cfg.SecretKey,
					}
					objs, err := storage.ListObjectsS3(context.Background(), cCfg, rec.Key)
					if err == nil && len(objs) == 0 {
						s.strikeMu.Lock()
						if s.missingStrike == nil {
							s.missingStrike = make(map[string]int)
						}
						s.missingStrike[rec.Key]++
						strikes := s.missingStrike[rec.Key]
						if force || strikes >= 3 {
							delete(s.missingStrike, rec.Key)
							s.strikeMu.Unlock()
							fmt.Printf("[FileAPI Reconcile] 🧹 Ghost file '%s' (%s) missing in bucket %s — cleaning DB record\n",
								rec.Key, rec.Filename, cfg.Name)
							if s.deleteHandler != nil {
								s.deleteHandler(rec.Key)
							}
							_ = s.store.DeleteFileJob(rec.Key)
							_ = s.store.DeleteFileRecord(rec.Key)
							purged++
							continue
						}
						s.strikeMu.Unlock()
					}
				}
			}
			continue
		}

		// 2. Reconcile VPS worker node files
		worker := s.nodeByID(job.Placement.NodeID)
		if worker == nil && s.coord != nil {
			for _, n := range s.coord.GetNodes() {
				if n.Metrics.NodeID == job.Placement.NodeID {
					worker = n
					break
				}
			}
		}
		if worker == nil {
			continue
		}

		probeURL := fmt.Sprintf("%s/media/%s/metadata.json", AgentBaseURL(worker), rec.Key)
		req, err := http.NewRequest(http.MethodHead, probeURL, nil)
		if err != nil {
			continue
		}
		if s.secret != "" {
			req.Header.Set("X-Cluster-Secret", s.secret)
		}
		resp, err := client.Do(req)
		if err != nil {
			continue
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()

		s.strikeMu.Lock()
		if s.missingStrike == nil {
			s.missingStrike = make(map[string]int)
		}
		if resp.StatusCode == http.StatusNotFound {
			s.missingStrike[rec.Key]++
			strikes := s.missingStrike[rec.Key]
			if force || strikes >= 3 {
				delete(s.missingStrike, rec.Key)
				s.strikeMu.Unlock()
				fmt.Printf("[FileAPI Reconcile] 🧹 Ghost file '%s' (%s) missing on %s disk — cleaning DB record\n",
					rec.Key, rec.Filename, job.Placement.NodeID)
				if s.deleteHandler != nil {
					s.deleteHandler(rec.Key)
				}
				_ = s.store.DeleteFileJob(rec.Key)
				_ = s.store.DeleteFileRecord(rec.Key)
				purged++
				continue
			}
		} else {
			delete(s.missingStrike, rec.Key)
		}
		s.strikeMu.Unlock()
	}
	return purged, nil
}

// MissingStrikeStats reports the ghost-record strike tracker's live usage.
func (s *Service) MissingStrikeStats() cache.Snapshot {
	s.strikeMu.Lock()
	defer s.strikeMu.Unlock()
	var bytes int64
	for key := range s.missingStrike {
		bytes += int64(len(key)) + 16
	}
	return cache.Snapshot{Name: "ghost_strike_tracker", Entries: len(s.missingStrike), Bytes: bytes}
}

// uniqueKey generates a key until it is free in the file table.
func (s *Service) uniqueKey() (string, error) {
	for i := 0; i < 32; i++ {
		key, err := NewKey()
		if err != nil {
			return "", err
		}
		exists, err := s.store.FileRecordExists(key)
		if err != nil {
			return "", fmt.Errorf("key check: %w", err)
		}
		if !exists {
			return key, nil
		}
	}
	return "", fmt.Errorf("key space exhaustion, retry")
}

// workerPool returns the online processing VPS set with tools ready.
func (s *Service) workerPool() ([]*cluster.NodeRecord, error) {
	nodes := s.coord.GetNodes()
	candidates := WorkerCandidates(nodes, s.profiles())
	if len(candidates) == 0 {
		return nil, ErrNoWorker
	}
	return candidates, nil
}

// activeIngestsPerWorker tallies active (non-completed, non-failed) jobs assigned to each worker.
func (s *Service) activeIngestsPerWorker() map[string]int {
	counts := make(map[string]int)
	if s.store == nil {
		return counts
	}
	jobs, err := s.store.ListFileJobs()
	if err != nil {
		return counts
	}
	for _, j := range jobs {
		if j != nil && j.State != StateCompleted && j.State != StateFailed && j.NodeID != "" {
			counts[j.NodeID]++
		}
	}
	return counts
}

// tierByID finds a resolved tier status by ID.
func (s *Service) tierByID(statuses []storage.TierStatus, id int) *storage.TierStatus {
	for i := range statuses {
		if statuses[i].ID == id {
			return &statuses[i]
		}
	}
	return nil
}

// resolveUsage starts from the injected usage hook without double-counting.
func (s *Service) resolveUsage() map[string]uint64 {
	merged := make(map[string]uint64)
	for k, bytes := range s.usage() {
		merged[k] += bytes
	}
	return merged
}

// PlanPlacement picks the target block for requiredBytes:
// When an ingestOrder provider is configured, it iterates those tier IDs in order.
// Otherwise it falls back to:
// 1. Default/bulk tier (Default flag or Tier 2 HDD)
// 2. Warm tier (Tier 1 SSD) as overflow
// 3. Any available tier with headroom (Tier 0 hot buffer or custom tiers) as fallback.
func (s *Service) PlanPlacement(requiredBytes uint64, receivers map[string]bool) (Placement, error) {
	nodes := s.coord.GetNodes()
	statuses := s.tiers.Resolve(nodes, s.resolveUsage())

	if s.ingestOrder != nil {
		for _, tierID := range s.ingestOrder() {
			st := s.tierByID(statuses, tierID)
			if bs := PickBlockInTier(st, requiredBytes, receivers); bs != nil {
				return s.placementOf(st, bs), nil
			}
		}
		return Placement{}, fmt.Errorf("insufficient storage: all storage tiers are full for %d bytes", requiredBytes)
	}

	// 1. Direct fit on the preferred default / bulk tier.
	var defaultTier *storage.TierStatus
	for i := range statuses {
		if statuses[i].Default {
			defaultTier = &statuses[i]
			break
		}
	}
	if defaultTier == nil {
		defaultTier = s.tierByID(statuses, DefaultPolicy.HDDTierID)
	}

	if bs := PickBlockInTier(defaultTier, requiredBytes, receivers); bs != nil {
		return s.placementOf(defaultTier, bs), nil
	}

	// 2. Overflow onto the warm tier (SSD).
	ssd := s.tierByID(statuses, DefaultPolicy.SSDTierID)
	if ssd != nil && (defaultTier == nil || ssd.ID != defaultTier.ID) {
		if bs := PickBlockInTier(ssd, requiredBytes, receivers); bs != nil {
			return s.placementOf(ssd, bs), nil
		}
	}

	// 3. Fallback onto any other configured tier with usable capacity (Hot NVMe or custom tiers).
	for i := range statuses {
		st := &statuses[i]
		if (defaultTier != nil && st.ID == defaultTier.ID) || (ssd != nil && st.ID == ssd.ID) {
			continue
		}
		if bs := PickBlockInTier(st, requiredBytes, receivers); bs != nil {
			return s.placementOf(st, bs), nil
		}
	}

	return Placement{}, fmt.Errorf("insufficient storage: all storage tiers are full for %d bytes", requiredBytes)
}

func (s *Service) placementOf(tier *storage.TierStatus, bs *storage.BlockStatus) Placement {
	pubHost := bs.Block.PublicHost
	if pubHost == "" && tier != nil {
		pubHost = tier.PublicHost
	}
	return Placement{
		TierID:     tier.ID,
		TierLabel:  TierLabel(tier.ID),
		NodeID:     bs.Block.NodeID,
		Path:       bs.Block.Path,
		PublicHost: pubHost,
	}
}

// callIngestDelete issues an HTTP delete to a worker agent to remove files and scratch.
func (s *Service) callIngestDelete(node *cluster.NodeRecord, key, dir string) error {
	agentURL := AgentBaseURL(node)
	req, err := http.NewRequest(http.MethodPost, agentURL+"/api/v1/ingest-delete", nil)
	if err != nil {
		return err
	}
	if s.secret != "" {
		req.Header.Set("X-Cluster-Secret", s.secret)
	}
	q := req.URL.Query()
	q.Set("key", key)
	if dir != "" {
		q.Set("dir", dir)
	}
	req.URL.RawQuery = q.Encode()

	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode >= 300 {
		return fmt.Errorf("worker responded %s", resp.Status)
	}
	return nil
}

// deleteFromWorker removes a file's folder from its worker or S3 bucket and drops the DB rows.
func (s *Service) deleteFromWorker(rec *FileRecord) error {
	job, err := s.store.GetFileJob(rec.Key)
	if err != nil || job == nil {
		return fmt.Errorf("job row missing for %s", rec.Key)
	}

	// 1. Remote S3 / Cloudflare R2 bucket placement
	if strings.HasPrefix(job.Placement.NodeID, "bucket:") {
		bucketID := strings.TrimPrefix(job.Placement.NodeID, "bucket:")
		if s.bucketResolver != nil {
			cfg, err := s.bucketResolver(bucketID)
			if err == nil && cfg != nil {
				cCfg := storage.S3Config{
					Name:      cfg.Name,
					Endpoint:  cfg.Endpoint,
					Region:    cfg.Region,
					AccessKey: cfg.AccessKey,
					SecretKey: cfg.SecretKey,
				}
				prefix := rec.Key
				if job.Placement.Path != "" && job.Placement.Path != "." && job.Placement.Path != "/" && job.Placement.Path != cfg.Name {
					prefix = strings.Trim(job.Placement.Path, "/") + "/" + rec.Key
				}
				if delErr := storage.DeleteFolderS3(context.Background(), cCfg, prefix); delErr != nil {
					log.Printf("[Service Delete] ⚠️ S3 DeleteFolder '%s' error: %v", prefix, delErr)
				}
			}
		}
		// Also clean scratch if processing worker node is known
		if job.NodeID != "" {
			if wNode := s.nodeByID(job.NodeID); wNode != nil {
				_ = s.callIngestDelete(wNode, rec.Key, "")
			}
		}
		_ = s.store.DeleteFileJob(rec.Key)
		_ = s.store.DeleteFileRecord(rec.Key)
		return nil
	}

	// 2. Local VPS placement
	node := s.nodeByID(job.Placement.NodeID)
	if node != nil {
		if err := s.callIngestDelete(node, rec.Key, CleanPlacementPath(job.Placement.Path)); err != nil {
			return err
		}
	} else if job.Placement.NodeID != "" {
		log.Printf("[Service Delete] ⚠️ Storage node '%s' is offline; purging record from DB", job.Placement.NodeID)
	}

	// Clean scratch on transcoding worker if decoupled
	if job.NodeID != "" && job.NodeID != job.Placement.NodeID {
		if wNode := s.nodeByID(job.NodeID); wNode != nil {
			_ = s.callIngestDelete(wNode, rec.Key, "")
		}
	}

	_ = s.store.DeleteFileJob(rec.Key)
	_ = s.store.DeleteFileRecord(rec.Key)
	return nil
}

// nodeByID finds a live node record.
func (s *Service) nodeByID(nodeID string) *cluster.NodeRecord {
	for _, n := range s.coord.GetNodes() {
		if n.Metrics.NodeID == nodeID {
			return n
		}
	}
	return nil
}

// CreateFromURL validates a remote video via HEAD probe, reserves placement,
// persists file + job rows and dispatches the download to the worker.
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
func (s *Service) Status(key string) (*StatusResponse, error) {
	rec, err := s.store.GetFileRecord(key)
	if err != nil {
		return nil, fmt.Errorf("file '%s' not found", key)
	}
	job, err := s.store.GetFileJob(key)
	if err != nil || job == nil {
		return nil, fmt.Errorf("file '%s' not found", key)
	}

	createdBy := rec.CreatedBy
	if createdBy == "" {
		createdBy = "Dashboard"
	}

	playPath := "/stream/" + key
	streamURL := s.StreamURL(job)
	if s.secret != "" {
		exp := time.Now().Add(4 * time.Hour).Unix()
		if ticket, err := GeneratePlaybackTicket(key, exp, s.secret); err == nil && ticket != "" {
			playPath = "/stream/" + ticket
			if streamURL != "" && strings.Contains(streamURL, "/stream/"+key) {
				streamURL = strings.Replace(streamURL, "/stream/"+key, "/stream/"+ticket, 1)
			}
		}
	}

	resp := &StatusResponse{
		Key:              key,
		State:            job.State,
		Filename:         rec.Filename,
		SizeBytes:        rec.SizeBytes,
		MimeType:         rec.MimeType,
		CreatedBy:        createdBy,
		Progress:         job.Progress,
		Speed:            job.Speed,
		Error:            job.Error,
		Stage:            job.Stage,
		StageName:        job.StageName,
		StagePercent:     job.StagePercent,
		TransferredBytes: job.TransferredBytes,
		TotalBytes:       job.TotalBytes,
		ETA:              job.ETA,
		Details:          job.Details,
		Source:           rec.SourceType,
		NodeID:           job.NodeID,
		WorkerNodeID:     job.NodeID,
		Placement:        job.Placement,
		StreamURL:        streamURL,
		PlayURL:          playPath,
		CreatedAt:        rec.CreatedAt,
		UpdatedAt:        job.UpdatedAt,
	}
	if resp.State == StateCompleted {
		resp.Progress = 100
	}
	if len(job.MetadataJSON) > 0 {
		var meta map[string]interface{}
		if err := json.Unmarshal(job.MetadataJSON, &meta); err == nil {
			resp.Metadata = meta
		}
	} else if resp.State == StateCompleted && job.Placement.NodeID != "" {
		// Probe worker's /media/{key}/metadata.json to backfill historical jobs
		worker := s.nodeByID(job.Placement.NodeID)
		if worker != nil && worker.Status == cluster.StatusOnline {
			candidates := []string{
				fmt.Sprintf("%s/media/%s/metadata.json", AgentBaseURL(worker), key),
				fmt.Sprintf("http://172.17.0.1:2052/media/%s/metadata.json", key),
				fmt.Sprintf("http://127.0.0.1:2052/media/%s/metadata.json", key),
			}
			client := &http.Client{Timeout: 600 * time.Millisecond}
			for _, metaURL := range candidates {
				if r, err := client.Get(metaURL); err == nil {
					if r.StatusCode == http.StatusOK {
						data, _ := io.ReadAll(r.Body)
						r.Body.Close()
						if len(data) > 0 {
							var meta map[string]interface{}
							if err := json.Unmarshal(data, &meta); err == nil {
								resp.Metadata = meta
								job.MetadataJSON = data
								_ = s.store.SaveFileJob(job)
								break
							}
						}
					} else {
						r.Body.Close()
					}
				}
			}
		}
	}
	return resp, nil
}

// List returns status for every file, newest first.
func (s *Service) List() ([]*StatusResponse, error) {
	recs, err := s.store.ListFileRecords()
	if err != nil {
		return nil, err
	}
	out := make([]*StatusResponse, 0, len(recs))
	for _, rec := range recs {
		if st, err := s.Status(rec.Key); err == nil {
			out = append(out, st)
		}
	}
	return out, nil
}

// StreamURL points clients at the owning worker's byte-range media server,
// or the custom domain/host link configured for the storage block.
func (s *Service) StreamURL(job *FileJob) string {
	if job.State != StateCompleted || job.Placement.NodeID == "" {
		return ""
	}

	// 1. Check if placement cached a custom domain / host link
	publicHost := job.Placement.PublicHost

	// 2. If not cached (e.g. older job, or updated tier block / tier configuration),
	// dynamically look up the live block's PublicHost or fallback to tier's PublicHost
	if publicHost == "" && s.tiers != nil {
		for _, t := range s.tiers.List() {
			for _, b := range t.Blocks {
				if b.NodeID == job.Placement.NodeID && (b.Path == job.Placement.Path || job.Placement.Path == "") {
					if b.PublicHost != "" {
						publicHost = b.PublicHost
						break
					}
				}
			}
			if publicHost != "" {
				break
			}
			if (t.ID == job.Placement.TierID || t.Label() == job.Placement.TierLabel) && t.PublicHost != "" {
				publicHost = t.PublicHost
				break
			}
		}
	}

	// 2b. If target is an S3/R2 remote bucket, resolve the bucket's PublicHost
	if publicHost == "" && strings.HasPrefix(job.Placement.NodeID, "bucket:") && s.bucketResolver != nil {
		bucketID := strings.TrimPrefix(job.Placement.NodeID, "bucket:")
		if cfg, err := s.bucketResolver(bucketID); err == nil && cfg != nil && cfg.PublicHost != "" {
			publicHost = cfg.PublicHost
		}
	}

	if publicHost != "" {
		host := strings.TrimSpace(publicHost)
		host = strings.TrimRight(host, "/")
		if !strings.HasPrefix(host, "http://") && !strings.HasPrefix(host, "https://") {
			if strings.Contains(host, ":") {
				host = "http://" + host
			} else {
				host = "https://" + host
			}
		}
		if strings.HasPrefix(job.Placement.NodeID, "bucket:") {
			targetFile := ""
			if len(job.MetadataJSON) > 0 {
				var meta struct {
					Filename string `json:"filename"`
				}
				if json.Unmarshal(job.MetadataJSON, &meta) == nil && meta.Filename != "" {
					targetFile = "/" + meta.Filename
				}
			}
			return fmt.Sprintf("%s/%s%s", host, job.Key, targetFile)
		}
		return fmt.Sprintf("%s/stream/%s", host, job.Key)
	}

	// 3. Fallback: worker agent's direct reachable address
	if strings.HasPrefix(job.Placement.NodeID, "bucket:") {
		return ""
	}
	node := s.nodeByID(job.Placement.NodeID)
	if node == nil || node.Status == cluster.StatusOffline {
		return ""
	}
	return fmt.Sprintf("%s/stream/%s", AgentBaseURL(node), job.Key)
}

// AgentBaseURL builds the worker agent's HTTP root from live telemetry.
// Uses PreferAgentAddr so node-to-node transfers dial a reachable public/VPC/
// advertise address (SeaweedFS-style), not Tailscale CGNAT or loopback.
func AgentBaseURL(node *cluster.NodeRecord) string {
	ip := PreferAgentAddrSafe(node)
	port := node.Metrics.Capabilities.AgentPort
	if port <= 0 {
		port = 2052
	}
	return fmt.Sprintf("http://%s:%d", ip, port)
}

func PreferAgentAddrSafe(node *cluster.NodeRecord) string {
	if node == nil {
		return "127.0.0.1"
	}
	if addr := cluster.PreferAgentAddr(node.Metrics.IPs); addr != "" {
		return addr
	}
	return "127.0.0.1"
}

// Retry resets and re-dispatches a failed file job.
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
