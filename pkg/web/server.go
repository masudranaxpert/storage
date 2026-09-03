package web

import (
	"context"
	"html/template"
	"net/http"
	"path/filepath"
	"time"

	"stream/pkg/cluster"
	"stream/pkg/db"
	"stream/pkg/fileapi"
	"stream/pkg/storage"
)

// Server encapsulates the HTTP API and modular template dashboard.
type Server struct {
	addr        string
	coord       *cluster.Coordinator
	hub         *cluster.GRPCHub
	store       *db.Store
	tiers       *storage.Manager
	staticDir   string
	templateDir string
	tmpl        *template.Template
	httpServer  *http.Server
	files       *fileapi.Service
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
	s.files = fileapi.NewService(store, tiers, coord, s.processingProfiles, s.mediaUsagePerNode)
	if hub != nil {
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
