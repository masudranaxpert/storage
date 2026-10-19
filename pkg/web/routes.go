package web

import (
	"fmt"
	"net/http"
	"os"

	"stream/pkg/provision"
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
	mux.HandleFunc("/api/system/cache", s.handleSystemCache)
	mux.HandleFunc("/api/nodes/provision", s.handleProvisionNode)
	mux.HandleFunc("/api/nodes/", s.handleNodeAllocation)

	// Storage, Partitions & Tiers
	mux.HandleFunc("/api/storage/folders", s.handleStorageFolders)
	mux.HandleFunc("/api/storage/clean", s.handleStorageClean)
	mux.HandleFunc("/api/storage/drive-eligibility", s.handleDriveEligibility)
	mux.HandleFunc("/api/tiers", s.handleTiers)
	mux.HandleFunc("/api/tiers/", s.handleTiers)
	mux.HandleFunc("/api/flows", s.handleFlows)
	mux.HandleFunc("/api/flows/preview", s.handleFlowsPreview)
	mux.HandleFunc("/api/flows/simulate", s.handleFlowsSimulate)
	mux.HandleFunc("/api/lifecycle/tasks", s.handleLifecycleTasks)
	mux.HandleFunc("/api/lifecycle/tasks/", s.handleLifecycleTaskAction)
	mux.HandleFunc("/api/lifecycle/audit", s.handleLifecycleAudit)
	mux.HandleFunc("/api/lifecycle/hot", s.handleLifecycleHot)
	mux.HandleFunc("/api/processing", s.handleProcessing)
	mux.HandleFunc("/api/processing/", s.handleProcessing)

	// Database Admin
	mux.HandleFunc("/api/dbadmin/", s.handleDBAdmin)

	// Authentication & Session Management
	mux.HandleFunc("/api/auth/status", s.handleAuthStatus)
	mux.HandleFunc("/api/auth/setup", s.handleAuthSetup)
	mux.HandleFunc("/api/auth/login", s.handleAuthLogin)
	mux.HandleFunc("/api/auth/logout", s.handleAuthLogout)
	mux.HandleFunc("/api/auth/password", s.handleAuthPasswordChange)
	mux.HandleFunc("/api/auth/users", s.handleAuthUsers)
	mux.HandleFunc("/api/auth/users/", s.handleAuthUserDelete)

	// API Keys Management
	mux.HandleFunc("/api/keys", s.handleAPIKeys)
	mux.HandleFunc("/api/keys/", s.handleAPIKeySub)

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

	// Self-Contained Worker Bundle Distribution (/opt/stream)
	mux.HandleFunc("/download/stream-bundle-linux-amd64.tar.gz", func(w http.ResponseWriter, r *http.Request) {
		bundleData, err := provision.BuildOrGetWorkerBundle()
		if err != nil {
			http.Error(w, "Failed to build worker bundle: "+err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/gzip")
		w.Header().Set("Content-Disposition", "attachment; filename=stream-bundle-linux-amd64.tar.gz")
		w.Write(bundleData)
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
echo "[Stream Fix] Downloading latest Stream Worker Bundle from %s..."
curl -fsSL %s/download/stream-bundle-linux-amd64.tar.gz -o /tmp/stream-bundle.tar.gz
tar -xzf /tmp/stream-bundle.tar.gz -C /opt/stream --strip-components=1
chmod +x /opt/stream/bin/*
ln -sf /opt/stream/bin/stream /usr/local/bin/stream
rm -f /tmp/stream-bundle.tar.gz
echo "[Stream Fix] Ensuring storage directories and permissions for stream user..."
for d in /mnt/hdd /data /mnt/storage; do
  if [ -d "$d" ]; then
    mkdir -p "$d/stream/media" "$d/stream/processing"
    chown -R stream:stream "$d/stream" 2>/dev/null || true
    chmod -R 775 "$d/stream" 2>/dev/null || true
  fi
done
chown -R stream:stream /opt/stream /var/lib/stream /stream 2>/dev/null || true
chmod -R 775 /stream 2>/dev/null || true
echo "[Stream Fix] Restarting stream-agent daemon..."
systemctl restart stream-agent 2>/dev/null || true
echo "[Stream Fix] Done! Node successfully upgraded."
`, coordURL, coordURL)
		w.Header().Set("Content-Type", "text/x-shellscript")
		w.Write([]byte(script))
	})

	// HTML Dashboard Views
	mux.HandleFunc("/", s.handleDashboard)

	// CORS and Header Middleware
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS, HEAD")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, Range, Origin, Accept, X-Cluster-Secret, X-API-Key")
		w.Header().Set("Access-Control-Expose-Headers", "Content-Length, Content-Range, Accept-Ranges")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}

		mux.ServeHTTP(w, r)
	})
}
