package web

import (
	"fmt"
	"net/http"
	"os"

	"stream/pkg/provision"
	"stream/pkg/tools"
)

// Handler registers all REST APIs, static files, and HTML views.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// Static Assets
	if _, err := os.Stat(s.staticDir); err == nil {
		mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir(s.staticDir))))
	}

	// API Documentation & OpenAPI Spec
	mux.HandleFunc("/docs/swagger-embed", s.handleSwaggerDocs)
	mux.HandleFunc("/docs/swagger-embed/", s.handleSwaggerDocs)
	mux.HandleFunc("/api/openapi.json", s.handleOpenAPISpec)

	// Cluster Nodes & Pool
	mux.HandleFunc("/api/heartbeat", s.handleHeartbeat)
	mux.HandleFunc("/api/nodes", s.handleGetNodes)
	mux.HandleFunc("/api/pool", s.handleGetPool)
	mux.HandleFunc("/api/nodes/provision", s.handleProvisionNode)
	mux.HandleFunc("/api/nodes/", s.handleNodeAllocation)

	// Storage, Partitions & Tiers
	mux.HandleFunc("/api/storage/folders", s.handleStorageFolders)
	mux.HandleFunc("/api/storage/clean", s.handleStorageClean)
	mux.HandleFunc("/api/tiers", s.handleTiers)
	mux.HandleFunc("/api/tiers/", s.handleTiers)
	mux.HandleFunc("/api/processing", s.handleProcessing)
	mux.HandleFunc("/api/processing/", s.handleProcessing)

	// Database Admin
	mux.HandleFunc("/api/dbadmin/", s.handleDBAdmin)

	// Ingestion & File API
	if s.files != nil {
		s.files.Register(mux)
	}

	// Media Streaming & Playback
	mux.HandleFunc("/stream/", s.handleStreamManifest)

	// System Health
	mux.HandleFunc("/api/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	})

	// Linux Agent Binary Distribution
	mux.HandleFunc("/download/stream-linux-amd64", func(w http.ResponseWriter, r *http.Request) {
		binData, err := provision.GetOrBuildLinuxBinary()
		if err != nil {
			http.Error(w, "Failed to get binary", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", "attachment; filename=stream-linux-amd64")
		w.Write(binData)
	})

	// Automated Node Upgrade / Fix Shell Script
	mux.HandleFunc("/fix-node.sh", func(w http.ResponseWriter, r *http.Request) {
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		coordURL := fmt.Sprintf("%s://%s", scheme, r.Host)
		script := fmt.Sprintf(`#!/bin/bash
set -e
echo "[Stream Fix] Installing aria2, ffmpeg, rclone..."
%s
echo "[Stream Fix] Downloading latest Stream Agent binary from %s..."
curl -fsSL %s/download/stream-linux-amd64 -o /usr/local/bin/stream
chmod +x /usr/local/bin/stream
echo "[Stream Fix] Restarting stream-agent daemon..."
systemctl restart stream-agent 2>/dev/null || true
echo "[Stream Fix] Done! Node successfully upgraded."
`, tools.InstallShell("all"), coordURL, coordURL)
		w.Header().Set("Content-Type", "text/x-shellscript")
		w.Write([]byte(script))
	})

	// HTML Dashboard Views
	mux.HandleFunc("/", s.handleDashboard)

	// CORS and Header Middleware
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS, HEAD")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, Range, Origin, Accept, X-Cluster-Secret")
		w.Header().Set("Access-Control-Expose-Headers", "Content-Length, Content-Range, Accept-Ranges")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}

		mux.ServeHTTP(w, r)
	})
}
