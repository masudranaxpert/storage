package web

import (
	"context"
	"fmt"
	"html/template"
	"net/http"
	"path/filepath"
	"sync"
	"time"

	"stream/pkg/cache"
	"stream/pkg/cluster"
	"stream/pkg/db"
	"stream/pkg/fileapi"
	"stream/pkg/lifecycle"
	"stream/pkg/storage"
)

// Server encapsulates the HTTP API and modular template dashboard.
type Server struct {
	addr          string
	coord         *cluster.Coordinator
	hub           *cluster.GRPCHub
	store         *db.Store
	tiers         *storage.Manager
	staticDir     string
	templateDir   string
	tmpl          *template.Template
	httpServer    *http.Server
	files         *fileapi.Service
	engine        *lifecycle.Engine
	deduper       *lifecycle.PlayDeduper
	usageMu       sync.Mutex
	usageCache    map[string]uint64
	usageCachedAt time.Time
}

// Files returns the underlying file orchestration service.
func (s *Server) Files() *fileapi.Service {
	return s.files
}

// Engine returns the tier lifecycle engine.
func (s *Server) Engine() *lifecycle.Engine {
	return s.engine
}

// NewServer initializes a new web server for the control plane. The tier
// manager may be nil, in which case tier APIs respond with defaults only.
func NewServer(addr string, coord *cluster.Coordinator, hub *cluster.GRPCHub, store *db.Store, tiers *storage.Manager, staticDir, templateDir string) *Server {
	s := &Server{
		addr:        addr,
		coord:       coord,
		hub:         hub,
		store:       store,
		tiers:       tiers,
		staticDir:   staticDir,
		templateDir: templateDir,
	}

	s.loadTemplates()
	var jobStore fileapi.JobStore
	var lcStore lifecycle.Store
	if store != nil {
		jobStore = store
		lcStore = store
	}
	var clusterSecret string
	if hub != nil {
		clusterSecret = hub.Secret()
	}

	bucketResolver := func(id string) (*storage.S3Config, error) {
		if store == nil {
			return nil, fmt.Errorf("database unavailable")
		}
		b, err := store.GetBucket(id)
		if err != nil || b == nil {
			return nil, err
		}
		return &storage.S3Config{
			Name:       b.Name,
			Provider:   b.Provider,
			Endpoint:   b.Endpoint,
			Region:     b.Region,
			AccessKey:  b.AccessKey,
			SecretKey:  b.SecretKey,
			PublicHost: b.PublicHost,
		}, nil
	}

	s.files = fileapi.NewService(jobStore, tiers, coord, s.processingProfiles, s.mediaUsagePerNode)
	s.files.SetBucketResolver(bucketResolver)
	s.engine = lifecycle.New(
		lcStore,
		tiers,
		coord,
		s.files,
		lifecycle.WithUsageFunc(s.mediaUsagePerNode),
		lifecycle.WithJobStore(jobStore),
		lifecycle.WithSecret(clusterSecret),
		lifecycle.WithBucketResolver(bucketResolver),
	)
	s.deduper = lifecycle.NewPlayDeduper(50000, 30*time.Minute)
	s.files.SetIngestOrder(s.engine.IngestOrder)
	s.files.SetMoveHandler(s.handleFileMove)
	s.files.SetPlacementHandler(s.handleFilePlacement)
	s.files.SetDemandHandler(s.handleFileDemand)
	s.files.SetDeleteHandler(s.engine.OnFileDeleted)
	s.files.SetAuthValidator(s.validateFileAPIAuth)
	s.files.SetCreatorResolver(s.resolveFileAPICreator)
	if hub != nil {
		s.files.SetSecret(clusterSecret)
		hub.OnJobProgress = func(p cluster.JobProgress) {
			_ = s.files.ApplyProgress(p.JobID, &fileapi.ProgressUpdate{
				State:    fileapi.FileState(p.Status),
				Percent:  p.Percent,
				Speed:    p.Speed,
				Error:    p.ErrorMsg,
				CMAFJSON: p.CMAFJSON,
			})
		}
	}

	// Expose live cache usage to the settings dashboard.
	cache.Default.Register("play_deduper", s.deduper.Snapshot)
	cache.Default.Register("node_usage_snapshot", s.UsageCacheStats)
	if s.engine != nil {
		cache.Default.Register("play_hit_buffer", s.engine.HitBufferStats)
		cache.Default.Register("relocation_inflight", s.engine.InFlightStats)
	}
	if s.files != nil {
		cache.Default.Register("ghost_strike_tracker", s.files.MissingStrikeStats)
	}
	return s
}

func (s *Server) loadTemplates() {
	pattern := filepath.Join(s.templateDir, "*", "*.html")
	if tmpl, err := template.ParseGlob(pattern); err == nil {
		s.tmpl = tmpl
	}
}

// Start launches the HTTP server listening on the configured address.
func (s *Server) Start() error {
	s.httpServer = &http.Server{
		Addr:              s.addr,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 30 * time.Second,
		ReadTimeout:       30 * time.Minute,
		WriteTimeout:      30 * time.Minute,
		IdleTimeout:       2 * time.Minute,
	}

	return s.httpServer.ListenAndServe()
}

// Shutdown gracefully stops the web server.
func (s *Server) Shutdown(ctx context.Context) error {
	if s.httpServer != nil {
		return s.httpServer.Shutdown(ctx)
	}
	return nil
}
