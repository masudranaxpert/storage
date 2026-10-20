package api

import (
	"fmt"
	"net/http"
	"sync"
	"time"

	"stream/internal/cluster"
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
