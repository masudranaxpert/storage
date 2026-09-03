package fileapi

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"stream/pkg/cluster"
	"stream/pkg/media"
	"stream/pkg/storage"
)

// JobStore persists file records and jobs (implemented by db.Store).
type JobStore interface {
	SaveFileRecord(rec *FileRecord) error
	GetFileRecord(key string) (*FileRecord, error)
	FileRecordExists(key string) (bool, error)
	DeleteFileRecord(key string) error
	ListFileRecords() ([]*FileRecord, error)
	SaveFileJob(job *FileJob) error
	GetFileJob(key string) (*FileJob, error)
	DeleteFileJob(key string) error
	ListFileJobs() ([]*FileJob, error)
}

// TierSource resolves tier definitions against live telemetry.
type TierSource interface {
	List() []storage.Tier
	Resolve(nodes []*cluster.NodeRecord, usedPerNode map[string]uint64) []storage.TierStatus
}

// Service orchestrates the v1 file API on the master.
type Service struct {
	store    JobStore
	tiers    TierSource
	coord    *cluster.Coordinator
	profiles func() map[string]cluster.ProcessingProfile
	usage    func() map[string]uint64
	client   *http.Client
}

// NewService wires the orchestration service. profiles and usage are injected
// as funcs so the service always sees live values.
func NewService(store JobStore, tiers TierSource, coord *cluster.Coordinator,
	profiles func() map[string]cluster.ProcessingProfile, usage func() map[string]uint64) *Service {
	s := &Service{
		store:    store,
		tiers:    tiers,
		coord:    coord,
		profiles: profiles,
		usage:    usage,
		client:   &http.Client{Timeout: 30 * time.Second},
	}
	go s.startWatchdogLoop()
	return s
}

// startWatchdogLoop scans active jobs periodically. If a worker goes offline for > 3m,
// the job is marked as failed to prevent permanent UI lockup.
func (s *Service) startWatchdogLoop() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		jobs, err := s.store.ListFileJobs()
		if err != nil {
			continue
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

// tierByID finds a resolved tier status by ID.
func (s *Service) tierByID(statuses []storage.TierStatus, id int) *storage.TierStatus {
	for i := range statuses {
		if statuses[i].ID == id {
			return &statuses[i]
		}
	}
	return nil
}

// resolveUsage starts from the injected usage hook, then adds every non-failed
// fileapi library entry (bytes counted against the placement block owner).
func (s *Service) resolveUsage() map[string]uint64 {
	merged := make(map[string]uint64)
	for nodeID, bytes := range s.usage() {
		merged[nodeID] += bytes
	}
	if recs, err := s.store.ListFileRecords(); err == nil {
		jobs := map[string]*FileJob{}
		if list, err := s.store.ListFileJobs(); err == nil {
			for _, j := range list {
				jobs[j.Key] = j
			}
		}
		for _, rec := range recs {
			if job, ok := jobs[rec.Key]; ok && job.State != StateFailed {
				bytes := uint64(rec.SizeBytes)
				merged[job.Placement.NodeID] += bytes
				if job.Placement.Path != "" {
					merged[job.Placement.NodeID+"|"+job.Placement.Path] += bytes
				}
			}
		}
	}
	return merged
}

// PlanPlacement picks the target block for requiredBytes: Tier 2 (HDD)
// first; if nothing fits, last-uploaded HDD files are evicted (worker-side
// delete + DB rows) and the pick retried; then Tier 1 (SSD) as overflow.
// Tier 0 is never considered. Blocks may sit on any receiver node — the
// processing worker is chosen separately and transfers when it does not
// own the block.
func (s *Service) PlanPlacement(requiredBytes uint64, receivers map[string]bool) (Placement, []string, error) {
	nodes := s.coord.GetNodes()
	statuses := s.tiers.Resolve(nodes, s.resolveUsage())

	hdd := s.tierByID(statuses, DefaultPolicy.HDDTierID)
	ssd := s.tierByID(statuses, DefaultPolicy.SSDTierID)

	// 1. Straight fit on the cold tier.
	if bs := PickBlockInTier(hdd, requiredBytes, receivers); bs != nil {
		return s.placementOf(hdd, bs), nil, nil
	}

	// 2. Evict last-uploaded files from HDD blocks (never SSD) until one fits.
	evicted := make([]string, 0)
	for bs := PickBlockInTier(hdd, requiredBytes, receivers); bs == nil; bs = PickBlockInTier(hdd, requiredBytes, receivers) {
		victim, err := s.newestHDDFile(hdd, receivers)
		if err != nil {
			return Placement{}, nil, err
		}
		if victim == nil {
			break // nothing left to evict on HDD
		}
		if err := s.deleteFromWorker(victim, hdd); err != nil {
			log.Printf("[FileAPI] ⚠️ eviction delete failed for %s: %v", victim.Key, err)
			// Mark as failed locally so the loop does not retry it forever.
			if job, _ := s.store.GetFileJob(victim.Key); job != nil {
				job.State = StateFailed
				job.Error = "eviction delete failed: " + err.Error()
				_ = s.store.SaveFileJob(job)
				continue
			}
		}
		evicted = append(evicted, victim.Key)
		if len(evicted) > 1000 {
			break // hard safety valve
		}
	}
	if len(evicted) > 0 {
		if bs := PickBlockInTier(hdd, requiredBytes, receivers); bs != nil {
			return s.placementOf(hdd, bs), evicted, nil
		}
	}

	// 3. Overflow onto the warm tier.
	if bs := PickBlockInTier(ssd, requiredBytes, receivers); bs != nil {
		return s.placementOf(ssd, bs), evicted, nil
	}

	return Placement{}, evicted, fmt.Errorf("no storage: tier2 (HDD) and tier1 (SSD) are full for %d bytes (visitor-based eviction not implemented yet)", requiredBytes)
}

func (s *Service) placementOf(tier *storage.TierStatus, bs *storage.BlockStatus) Placement {
	return Placement{
		TierID:     tier.ID,
		TierLabel:  TierLabel(tier.ID),
		NodeID:     bs.Block.NodeID,
		Path:       bs.Block.Path,
		PublicHost: bs.Block.PublicHost,
	}
}

// newestHDDFile returns the most recently uploaded completed file that lives
// on an HDD-tier block owned by a receiver node.
func (s *Service) newestHDDFile(hdd *storage.TierStatus, receivers map[string]bool) (*FileRecord, error) {
	hddNodes := make(map[string]bool)
	if hdd != nil {
		for _, b := range hdd.Blocks {
			hddNodes[b.Block.NodeID] = true
		}
	}
	recs, err := s.store.ListFileRecords()
	if err != nil {
		return nil, err
	}
	for _, rec := range recs { // already newest-first
		job, err := s.store.GetFileJob(rec.Key)
		if err != nil || job == nil {
			continue
		}
		if job.State != StateCompleted {
			continue
		}
		if job.Placement.TierID != DefaultPolicy.HDDTierID {
			continue // only ever evict from HDD
		}
		if !hddNodes[job.Placement.NodeID] || !receivers[job.Placement.NodeID] {
			continue
		}
		return rec, nil
	}
	return nil, nil
}

// deleteFromWorker removes a file's folder from its worker and drops the DB rows.
func (s *Service) deleteFromWorker(rec *FileRecord, hdd *storage.TierStatus) error {
	job, err := s.store.GetFileJob(rec.Key)
	if err != nil || job == nil {
		return fmt.Errorf("job row missing for %s", rec.Key)
	}
	node := s.nodeByID(job.Placement.NodeID)
	if node == nil {
		return fmt.Errorf("node %s offline", job.Placement.NodeID)
	}
	agentURL := AgentBaseURL(node)
	req, err := http.NewRequest(http.MethodPost, agentURL+"/api/v1/ingest-delete", nil)
	if err != nil {
		return err
	}
	q := req.URL.Query()
	q.Set("key", rec.Key)
	q.Set("dir", job.Placement.Path)
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
func (s *Service) CreateFromURL(rawURL string) (*CreateResponse, error) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return nil, fmt.Errorf("url is required")
	}
	if !strings.HasPrefix(rawURL, "http://") && !strings.HasPrefix(rawURL, "https://") {
		return nil, fmt.Errorf("url must start with http:// or https://")
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
	placement, evicted, err := s.PlanPlacement(required, receivers)
	if err != nil {
		return nil, err
	}
	if len(evicted) > 0 {
		log.Printf("[FileAPI] ♻️ evicted %d file(s) from tier2 to fit %s", len(evicted), key)
	}

	// Pick the processing worker independently: prefer the block owner
	// (direct write, no transfer), else the worker with the roomiest scratch.
	worker := PickProcessingWorker(workers, placement.NodeID, uint64(probe.SizeBytes))
	if worker == nil {
		return nil, fmt.Errorf("no processing worker has enough scratch space for %d bytes (need ~2x for raw + CMAF)", probe.SizeBytes)
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
		Placement: placement,
		StatusURL: "/api/v1/files/" + key,
		StreamURL: s.StreamURL(job),
		CreatedAt: now,
	}, nil
}

// ReserveUpload creates an upload slot: the client receives the key plus a
// one-shot upload URL. Placement is fixed now so bytes stream straight to
// the owning worker.
func (s *Service) ReserveUpload(rawFilename string, expectedSize int64) (*CreateResponse, error) {
	if strings.TrimSpace(rawFilename) == "" {
		return nil, fmt.Errorf("filename is required")
	}
	filename := media.SanitizeFilename(rawFilename)
	if err := ValidateVideo(filename, ""); err != nil {
		return nil, err
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
	placement, _, err := s.PlanPlacement(required, receivers)
	if err != nil {
		return nil, err
	}

	worker := PickProcessingWorker(workers, placement.NodeID, uint64(expectedSize))
	if worker == nil {
		return nil, fmt.Errorf("no processing worker has enough scratch space for %d bytes", expectedSize)
	}

	now := time.Now().UTC()
	rec := &FileRecord{
		Key:        key,
		Filename:   filename,
		SizeBytes:  expectedSize,
		SourceType: SourceUpload,
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
		UploadURL: "/api/v1/files/" + key + "/upload",
		Placement: placement,
		StatusURL: "/api/v1/files/" + key,
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

	payload := map[string]string{
		"key":        job.Key,
		"source_url": job.SourceURL,
		"filename":   rec.Filename,
	}

	if job.NodeID == job.Placement.NodeID {
		// Direct: write onto the block this worker owns.
		payload["target_dir"] = job.Placement.Path
	} else {
		// Decoupled: process in scratch, then transfer to the block owner.
		owner := s.nodeByID(job.Placement.NodeID)
		if owner == nil {
			return fmt.Errorf("storage node '%s' is offline", job.Placement.NodeID)
		}
		payload["scratch"] = "true"
		payload["final_node_id"] = job.Placement.NodeID
		payload["final_dir"] = job.Placement.Path
		payload["final_addr"] = AgentBaseURL(owner)
	}

	body, _ := json.Marshal(payload)

	resp, err := s.client.Post(AgentBaseURL(worker)+"/api/v1/ingest", "application/json", strings.NewReader(string(body)))
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
		var cmaf struct {
			TotalBytes int64 `json:"total_bytes"`
		}
		if err := json.Unmarshal(up.CMAFJSON, &cmaf); err == nil && cmaf.TotalBytes > 0 {
			if rec, err := s.store.GetFileRecord(key); err == nil && rec != nil {
				if rec.SizeBytes == 0 || rec.SizeBytes != cmaf.TotalBytes {
					rec.SizeBytes = cmaf.TotalBytes
					_ = s.store.SaveFileRecord(rec)
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

	resp := &StatusResponse{
		Key:              key,
		State:            job.State,
		Filename:         rec.Filename,
		SizeBytes:        rec.SizeBytes,
		MimeType:         rec.MimeType,
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
		WorkerNodeID:     job.NodeID,
		Placement:        job.Placement,
		StreamURL:        s.StreamURL(job),
		CreatedAt:        rec.CreatedAt,
		UpdatedAt:        job.UpdatedAt,
	}
	if resp.State == StateCompleted {
		resp.Progress = 100
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

	// 2. If not cached (e.g. older job, or updated tier block configuration),
	// dynamically look up the live block's PublicHost in the tier definition
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
		return fmt.Sprintf("%s/stream/%s", host, job.Key)
	}

	// 3. Fallback: worker agent's direct reachable address
	node := s.nodeByID(job.Placement.NodeID)
	if node == nil {
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
