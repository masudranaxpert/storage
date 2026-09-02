package web

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"stream/pkg/cluster"
	"stream/pkg/db"
	"stream/pkg/fileapi"
	"stream/pkg/ingest"
	"stream/pkg/media"
	"stream/pkg/provision"
	"stream/pkg/storage"
	"stream/pkg/telemetry"
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
	ingestQueue *ingest.IngestQueue
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
		ingestQueue: ingest.NewQueue(),
	}

	if hub != nil {
		hub.OnJobProgress = func(p cluster.JobProgress) {
			s.ingestQueue.UpdateStatus(p.JobID, ingest.JobStatus(p.Status), p.Percent, p.Speed, p.ErrorMsg)
			if len(p.CMAFJSON) > 0 {
				var cmaf media.CMAFPackage
				if err := json.Unmarshal(p.CMAFJSON, &cmaf); err == nil {
					s.ingestQueue.SetCMAF(p.JobID, &cmaf)
				}
			}
		}
	}

	s.loadTemplates()
	s.loadExistingMedia()

	s.files = fileapi.NewService(store, tiers, coord, s.processingProfiles, s.mediaUsagePerNode)
	return s
}

func (s *Server) loadExistingMedia() {
	mediaRoot := filepath.Join("data", "media")
	entries, err := os.ReadDir(mediaRoot)
	if err != nil {
		return
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		mediaID := entry.Name()
		metaPath := filepath.Join(mediaRoot, mediaID, "metadata.json")
		rawDir := filepath.Join(mediaRoot, mediaID, "raw")

		var filename string
		var rawSize int64
		if rawEntries, err := os.ReadDir(rawDir); err == nil && len(rawEntries) > 0 {
			filename = rawEntries[0].Name()
			if info, err := rawEntries[0].Info(); err == nil {
				rawSize = info.Size()
			}
		} else {
			filename = mediaID + ".mp4"
		}

		cmafPkg := &media.CMAFPackage{
			MediaID:    mediaID,
			TotalBytes: rawSize,
			CreatedAt:  time.Now(),
		}

		if metaData, err := os.ReadFile(metaPath); err == nil {
			_ = json.Unmarshal(metaData, cmafPkg)
		}
		if rawSize > cmafPkg.TotalBytes {
			cmafPkg.TotalBytes = rawSize
		}

		job := &ingest.IngestJob{
			JobID:           mediaID,
			SourceURL:       filename,
			AssignedNodeID:  "local-master",
			Status:          ingest.StatusCompleted,
			ProgressPercent: 100.0,
			CMAF:            cmafPkg,
			CreatedAt:       cmafPkg.CreatedAt,
			UpdatedAt:       time.Now(),
		}
		s.ingestQueue.Add(job)
	}
}

func (s *Server) loadTemplates() {
	pattern := filepath.Join(s.templateDir, "*", "*.html")
	if tmpl, err := template.ParseGlob(pattern); err == nil {
		s.tmpl = tmpl
	}
}

// Handler registers all REST APIs, static files, and HTML views.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	if _, err := os.Stat(s.staticDir); err == nil {
		mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir(s.staticDir))))
	}

	mux.HandleFunc("/docs/swagger-embed", s.handleSwaggerDocs)
	mux.HandleFunc("/docs/swagger-embed/", s.handleSwaggerDocs)
	mux.HandleFunc("/api/openapi.json", s.handleOpenAPISpec)

	mux.HandleFunc("/api/heartbeat", s.handleHeartbeat)
	mux.HandleFunc("/api/nodes", s.handleGetNodes)
	mux.HandleFunc("/api/pool", s.handleGetPool)
	mux.HandleFunc("/api/tiers", s.handleTiers)
	mux.HandleFunc("/api/tiers/", s.handleTiers)
	mux.HandleFunc("/api/processing", s.handleProcessing)
	mux.HandleFunc("/api/nodes/provision", s.handleProvisionNode)
	mux.HandleFunc("/api/nodes/", s.handleNodeAllocation)
	mux.HandleFunc("/api/ingest/upload", s.handleUploadIngest)
	mux.HandleFunc("/api/ingest", s.handleIngestJob)
	mux.HandleFunc("/api/ingest/", s.handleIngestJob)
	mux.HandleFunc("/api/dbadmin/", s.handleDBAdmin)
	mux.HandleFunc("/api/jobs", s.handleIngestJob)
	mux.HandleFunc("/api/jobs/", s.handleIngestJob)
	if s.files != nil {
		s.files.Register(mux)
	}
	mux.HandleFunc("/stream/", s.handleStreamManifest)
	mux.HandleFunc("/api/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	})

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

	mux.HandleFunc("/fix-node.sh", func(w http.ResponseWriter, r *http.Request) {
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		coordURL := fmt.Sprintf("%s://%s", scheme, r.Host)
		script := fmt.Sprintf(`#!/bin/bash
set -e
echo "[Stream Fix] Updating package lists & installing aria2, ffmpeg..."
apt-get update -qq && apt-get install -y -qq aria2 ffmpeg
echo "[Stream Fix] Downloading latest Stream Agent binary from %s..."
curl -fsSL %s/download/stream-linux-amd64 -o /usr/local/bin/stream
chmod +x /usr/local/bin/stream
echo "[Stream Fix] Restarting stream-agent daemon..."
systemctl restart stream-agent 2>/dev/null || true
echo "[Stream Fix] Done! Node successfully upgraded."
`, coordURL, coordURL)
		w.Header().Set("Content-Type", "text/x-shellscript")
		w.Write([]byte(script))
	})

	mux.HandleFunc("/", s.handleDashboard)

	return mux
}

// Start launches the HTTP server listening on the configured address.
func (s *Server) Start() error {
	s.httpServer = &http.Server{
		Addr:         s.addr,
		Handler:      s.Handler(),
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
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

// heartbeatLogThrottle rate-limits repetitive per-node heartbeat console logs
// to one line per minute (telemetry processing itself is unaffected).
var heartbeatLogThrottle sync.Map // nodeID -> time.Time

func heartbeatLogAllowed(nodeID string) bool {
	now := time.Now()
	if v, ok := heartbeatLogThrottle.Load(nodeID); ok {
		if t := v.(time.Time); time.Since(t) < time.Minute {
			return false
		}
	}
	heartbeatLogThrottle.Store(nodeID, now)
	return true
}

func (s *Server) handleHeartbeat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var metrics telemetry.NodeMetrics
	if err := json.NewDecoder(r.Body).Decode(&metrics); err != nil {
		http.Error(w, fmt.Sprintf("invalid payload: %v", err), http.StatusBadRequest)
		return
	}

	if metrics.NodeID == "" {
		http.Error(w, "node_id is required", http.StatusBadRequest)
		return
	}

	if cluster.IsCoordinatorNode(metrics.NodeID) || !cluster.IsPoolVPS(metrics) {
		http.Error(w, "only Linux VPS agents can join the storage pool (desktop OS rejected)", http.StatusForbidden)
		return
	}

	// Reject decommissioned nodes so they cannot re-register.
	if s.store != nil {
		if cfg, err := s.store.GetNodeConfig(metrics.NodeID); err == nil && cfg != nil && !cfg.Enabled {
			if heartbeatLogAllowed(metrics.NodeID) {
				fmt.Printf("[Heartbeat] ⚠️ Rejected heartbeat from disabled node '%s' (re-provision to enable)\n", metrics.NodeID)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusGone)
			w.Write([]byte(`{"status":"decommissioned","message":"Node has been removed from cluster"}`))
			return
		}
	}

	record := s.coord.RegisterHeartbeat(metrics)
	if record == nil {
		http.Error(w, "node rejected from pool", http.StatusForbidden)
		return
	}
	if heartbeatLogAllowed(metrics.NodeID) {
		fmt.Printf("[Heartbeat] ❤️ Live heartbeat registered for node '%s' (CPU: %.1f%%, RAM: %.1f%%)\n",
			metrics.NodeID, metrics.CPU.UsedPercent, metrics.Memory.UsedPercent)
	}
	w.Header().Set("Content-Type", "application/json")

	needsUpgrade := metrics.Capabilities.Version != "" && metrics.Capabilities.Version != telemetry.CurrentVersion

	resp := map[string]interface{}{
		"status":                "ok",
		"record":                record,
		"node_id":               record.Metrics.NodeID,
		"latest_version":        telemetry.CurrentVersion,
		"download_url":          "/download/stream-linux-amd64",
		"update_available":      needsUpgrade,
		"install_missing_tools": false,
	}
	json.NewEncoder(w).Encode(resp)
}

func (s *Server) handleGetNodes(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(s.coord.GetNodes())
}

func (s *Server) handleGetPool(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(s.coord.GetPoolSummary())
}

func (s *Server) handleNodeAllocation(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/nodes/")
	w.Header().Set("Content-Type", "application/json")

	if r.Method == http.MethodDelete {
		nodeID := strings.Trim(path, "/")

		var payload struct {
			SSHPassword string `json:"ssh_password"`
			SSHUser     string `json:"ssh_user"`
			SSHHost     string `json:"ssh_host"`
		}
		_ = json.NewDecoder(r.Body).Decode(&payload)

		// Capture live IPs before RemoveNode drops telemetry — old nodes often
		// have no SSH fields saved, so delete must fall back to heartbeat IPs.
		var liveIPs []string
		for _, n := range s.coord.GetNodes() {
			if n.Metrics.NodeID == nodeID {
				liveIPs = append(liveIPs, n.Metrics.IPs...)
				break
			}
		}

		s.coord.RemoveNode(nodeID)

		if s.store != nil {
			cfg, _ := s.store.GetNodeConfig(nodeID)
			if cfg == nil {
				cfg = &db.NodeConfig{NodeID: nodeID}
			}
			cfg.Enabled = false
			if payload.SSHPassword != "" {
				cfg.SSHPassword = payload.SSHPassword
			}
			if payload.SSHUser != "" {
				cfg.SSHUser = payload.SSHUser
			}
			if payload.SSHHost != "" {
				cfg.SSHHost = payload.SSHHost
			}
			if cfg.SSHHost == "" {
				for _, ip := range liveIPs {
					if ip != "" && ip != "127.0.0.1" && !strings.HasPrefix(ip, "172.") {
						cfg.SSHHost = ip
						break
					}
				}
			}
			if cfg.SSHUser == "" {
				cfg.SSHUser = "root"
			}
			if cfg.SSHPort == 0 {
				cfg.SSHPort = 22
			}
			_ = s.store.SaveNodeConfig(cfg)

			if cfg.SSHHost != "" && cfg.SSHPassword != "" {
				go func(c db.NodeConfig) {
					req := provision.Request{
						Host:     c.SSHHost,
						Port:     c.SSHPort,
						User:     c.SSHUser,
						Password: c.SSHPassword,
						UseSudo:  c.SSHUseSudo,
						NodeName: c.NodeID,
					}
					if err := provision.StopServiceOverSSH(context.Background(), req); err != nil {
						fmt.Printf("[Delete Node] SSH decommission of '%s' (%s) failed: %v\n", c.NodeID, c.SSHHost, err)
						return
					}
					fmt.Printf("[Delete Node] Remote agent on '%s' (%s) fully removed\n", c.NodeID, c.SSHHost)
				}(*cfg)
			} else {
				fmt.Printf("[Delete Node] '%s' disabled locally but no SSH password — remote agent may keep running until stopped manually\n", nodeID)
			}
		}

		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"deleted"}`))
		return
	}

	parts := strings.Split(path, "/")
	if len(parts) < 2 {
		http.NotFound(w, r)
		return
	}

	nodeID := parts[0]
	action := parts[1]

	if action == "config" && r.Method == http.MethodGet {
		if s.store != nil {
			cfg, err := s.store.GetNodeConfig(nodeID)
			if err == nil {
				json.NewEncoder(w).Encode(cfg)
				return
			}
		}
		json.NewEncoder(w).Encode(db.NodeConfig{NodeID: nodeID, Enabled: true})
		return
	}

	if action == "rescan" && r.Method == http.MethodPost {
		if strings.Contains(nodeID, "master") {
			if metrics, err := telemetry.Collect(nodeID); err == nil {
				s.coord.RegisterHeartbeat(*metrics)
			}
		} else {
			if nodes := s.coord.GetNodes(); len(nodes) > 0 {
				for _, n := range nodes {
					if n.Metrics.NodeID == nodeID {
						s.coord.RegisterHeartbeat(n.Metrics)
						break
					}
				}
			}
		}
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{
			"status":  "ok",
			"message": fmt.Sprintf("Node '%s' probed & telemetry refreshed", nodeID),
		})
		return
	}

	if action == "install-tools" && r.Method == http.MethodPost {
		var payload struct {
			Tool        string `json:"tool,omitempty"`
			SSHPassword string `json:"ssh_password,omitempty"`
			SSHUser     string `json:"ssh_user,omitempty"`
		}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		tool := payload.Tool
		if tool == "" {
			tool = r.URL.Query().Get("tool")
		}

		if strings.Contains(nodeID, "master") {
			go func() {
				var cmdStr string
				switch tool {
				case "aria2", "aria2c":
					cmdStr = "DEBIAN_FRONTEND=noninteractive apt-get update -qq && DEBIAN_FRONTEND=noninteractive apt-get install -y -qq aria2"
				case "ffmpeg":
					cmdStr = "DEBIAN_FRONTEND=noninteractive apt-get update -qq && DEBIAN_FRONTEND=noninteractive apt-get install -y -qq ffmpeg"
				default:
					cmdStr = "DEBIAN_FRONTEND=noninteractive apt-get update -qq && DEBIAN_FRONTEND=noninteractive apt-get install -y -qq aria2 ffmpeg"
				}
				_ = exec.Command("bash", "-c", cmdStr).Run()
				if metrics, err := telemetry.Collect(nodeID); err == nil {
					s.coord.RegisterHeartbeat(*metrics)
				}
			}()
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]string{
				"status":  "installing",
				"message": fmt.Sprintf("Local master '%s' installation started in background", tool),
			})
			return
		}

		var cfg *db.NodeConfig
		if s.store != nil {
			cfg, _ = s.store.GetNodeConfig(nodeID)
		}
		if cfg == nil {
			cfg = &db.NodeConfig{NodeID: nodeID, Enabled: true}
		}

		sshHost := cfg.SSHHost
		sshUser := cfg.SSHUser
		if sshUser == "" {
			sshUser = "root"
		}
		sshPassword := cfg.SSHPassword
		sshPort := cfg.SSHPort
		if sshPort <= 0 {
			sshPort = 22
		}
		useSudo := cfg.SSHUseSudo

		if payload.SSHPassword != "" {
			sshPassword = payload.SSHPassword
		}
		if payload.SSHUser != "" {
			sshUser = payload.SSHUser
		}

		if sshHost == "" {
			nodes := s.coord.GetNodes()
			for _, n := range nodes {
				if n.Metrics.NodeID == nodeID && len(n.Metrics.IPs) > 0 {
					sshHost = n.Metrics.IPs[0]
					break
				}
			}
		}

		if sshPassword == "" {
			// Require password once, then it will be permanently saved
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"status":  "ssh_auth_required",
				"node_id": nodeID,
				"host":    sshHost,
				"user":    sshUser,
			})
			return
		}

		// Persist SSH configuration so future auto-installs & upgrades are 100% hands-free
		if s.store != nil {
			cfg.SSHHost = sshHost
			cfg.SSHUser = sshUser
			cfg.SSHPassword = sshPassword
			cfg.SSHPort = sshPort
			cfg.SSHUseSudo = useSudo
			_ = s.store.SaveNodeConfig(cfg)
		}

		// Trigger SSH installation in background
		go func() {
			req := provision.Request{
				Host:     sshHost,
				Port:     sshPort,
				User:     sshUser,
				Password: sshPassword,
				UseSudo:  useSudo,
				NodeName: nodeID,
			}
			_ = provision.InstallToolsOverSSH(context.Background(), req, tool)
		}()

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{
			"status":  "installing",
			"message": fmt.Sprintf("Tool installation started for node '%s'", nodeID),
		})
		return
	}

	if action == "uninstall-tools" && r.Method == http.MethodPost {
		var payload struct {
			Tool        string `json:"tool,omitempty"`
			SSHPassword string `json:"ssh_password,omitempty"`
			SSHUser     string `json:"ssh_user,omitempty"`
		}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		tool := payload.Tool
		if tool == "" {
			tool = r.URL.Query().Get("tool")
		}

		if strings.Contains(nodeID, "master") {
			go func() {
				var cmdStr string
				switch tool {
				case "aria2", "aria2c":
					cmdStr = "DEBIAN_FRONTEND=noninteractive apt-get purge -y -qq aria2 2>/dev/null; DEBIAN_FRONTEND=noninteractive apt-get autoremove -y -qq 2>/dev/null; rm -f /usr/local/bin/aria2c /usr/bin/aria2c /bin/aria2c; hash -r 2>/dev/null || true"
				case "ffmpeg":
					cmdStr = "DEBIAN_FRONTEND=noninteractive apt-get purge -y -qq ffmpeg 2>/dev/null; DEBIAN_FRONTEND=noninteractive apt-get autoremove -y -qq 2>/dev/null; rm -f /usr/local/bin/ffmpeg /usr/bin/ffmpeg /bin/ffmpeg /usr/local/bin/ffprobe /usr/bin/ffprobe /bin/ffprobe; hash -r 2>/dev/null || true"
				default:
					cmdStr = "DEBIAN_FRONTEND=noninteractive apt-get purge -y -qq aria2 ffmpeg 2>/dev/null; DEBIAN_FRONTEND=noninteractive apt-get autoremove -y -qq 2>/dev/null; rm -f /usr/local/bin/aria2c /usr/bin/aria2c /bin/aria2c /usr/local/bin/ffmpeg /usr/bin/ffmpeg /bin/ffmpeg /usr/local/bin/ffprobe /usr/bin/ffprobe /bin/ffprobe; hash -r 2>/dev/null || true"
				}
				_ = exec.Command("bash", "-c", cmdStr).Run()
				if metrics, err := telemetry.Collect(nodeID); err == nil {
					s.coord.RegisterHeartbeat(*metrics)
				}
			}()
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]string{
				"status":  "uninstalling",
				"message": fmt.Sprintf("Local master '%s' uninstallation started in background", tool),
			})
			return
		}

		var cfg *db.NodeConfig
		if s.store != nil {
			cfg, _ = s.store.GetNodeConfig(nodeID)
		}
		if cfg == nil {
			cfg = &db.NodeConfig{NodeID: nodeID, Enabled: true}
		}

		sshHost := cfg.SSHHost
		sshUser := cfg.SSHUser
		if sshUser == "" {
			sshUser = "root"
		}
		sshPassword := cfg.SSHPassword
		sshPort := cfg.SSHPort
		if sshPort <= 0 {
			sshPort = 22
		}
		useSudo := cfg.SSHUseSudo

		if payload.SSHPassword != "" {
			sshPassword = payload.SSHPassword
		}
		if payload.SSHUser != "" {
			sshUser = payload.SSHUser
		}

		if sshHost == "" {
			nodes := s.coord.GetNodes()
			for _, n := range nodes {
				if n.Metrics.NodeID == nodeID && len(n.Metrics.IPs) > 0 {
					sshHost = n.Metrics.IPs[0]
					break
				}
			}
		}

		if sshPassword == "" {
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"status":  "ssh_auth_required",
				"node_id": nodeID,
				"host":    sshHost,
				"user":    sshUser,
			})
			return
		}

		if s.store != nil {
			cfg.SSHHost = sshHost
			cfg.SSHUser = sshUser
			cfg.SSHPassword = sshPassword
			cfg.SSHPort = sshPort
			cfg.SSHUseSudo = useSudo
			_ = s.store.SaveNodeConfig(cfg)
		}

		go func() {
			req := provision.Request{
				Host:     sshHost,
				Port:     sshPort,
				User:     sshUser,
				Password: sshPassword,
				UseSudo:  useSudo,
				NodeName: nodeID,
			}
			_ = provision.UninstallToolsOverSSH(context.Background(), req, tool)
		}()

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{
			"status":  "uninstalling",
			"message": fmt.Sprintf("Tool uninstallation started for node '%s'", nodeID),
		})
		return
	}

	if action == "allocate" && r.Method == http.MethodPost {
		var req struct {
			AllocatedMaxBytes uint64 `json:"allocated_max_bytes"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid json payload", http.StatusBadRequest)
			return
		}

		cfg := &db.NodeConfig{
			NodeID:            nodeID,
			AllocatedMaxBytes: req.AllocatedMaxBytes,
			Enabled:           true,
		}

		if s.store != nil {
			if existing, _ := s.store.GetNodeConfig(nodeID); existing != nil {
				cfg = existing
				cfg.AllocatedMaxBytes = req.AllocatedMaxBytes
			}
			if err := s.store.SaveNodeConfig(cfg); err != nil {
				http.Error(w, fmt.Sprintf("failed to save quota in BadgerDB: %v", err), http.StatusInternalServerError)
				return
			}
		}

		json.NewEncoder(w).Encode(cfg)
		return
	}

	if action == "processing" && r.Method == http.MethodPost {
		var req struct {
			Enabled              bool   `json:"enabled"`
			ProcessingEnabled    bool   `json:"processing_enabled"`
			ReservedCPUCores     int    `json:"reserved_cpu_cores"`
			ReservedRAMMB        uint64 `json:"reserved_ram_mb"`
			ReservedRAMBytes     uint64 `json:"reserved_ram_bytes"`
			PreferredDiskType    string `json:"preferred_disk_type"`
			PreferredDisk        string `json:"preferred_disk"`
			ReservedStorageGB    uint64 `json:"reserved_storage_gb"`
			ReservedStorageBytes uint64 `json:"reserved_storage_bytes"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid json payload", http.StatusBadRequest)
			return
		}

		if s.store == nil {
			http.Error(w, "persistent store unavailable", http.StatusServiceUnavailable)
			return
		}

		cfg, _ := s.store.GetNodeConfig(nodeID)
		if cfg == nil {
			cfg = &db.NodeConfig{NodeID: nodeID, Enabled: true}
		}
		cfg.ProcessingEnabled = req.Enabled || req.ProcessingEnabled
		cfg.ReservedCPUCores = req.ReservedCPUCores
		if req.ReservedRAMBytes > 0 {
			cfg.ReservedRAMBytes = req.ReservedRAMBytes
		} else {
			cfg.ReservedRAMBytes = req.ReservedRAMMB * 1024 * 1024
		}
		prefDisk := strings.TrimSpace(req.PreferredDisk)
		if prefDisk == "" {
			prefDisk = strings.TrimSpace(req.PreferredDiskType)
		}
		if strings.EqualFold(prefDisk, "SSD") || strings.EqualFold(prefDisk, "HDD") || strings.EqualFold(prefDisk, "NVME") {
			cfg.PreferredDiskType = strings.ToUpper(prefDisk)
		} else {
			cfg.PreferredDiskType = prefDisk
		}
		if req.ReservedStorageBytes > 0 {
			cfg.ReservedStorageBytes = req.ReservedStorageBytes
		} else {
			cfg.ReservedStorageBytes = req.ReservedStorageGB * 1024 * 1024 * 1024
		}

		if err := s.store.SaveNodeConfig(cfg); err != nil {
			http.Error(w, fmt.Sprintf("failed to save processing profile: %v", err), http.StatusInternalServerError)
			return
		}

		json.NewEncoder(w).Encode(cfg)
		return
	}

	http.NotFound(w, r)
}

// mediaUsagePerNode aggregates stored media bytes per node from the ingest
// library, used for tier quota headroom calculations.
func (s *Server) mediaUsagePerNode() map[string]uint64 {
	usage := make(map[string]uint64)
	for _, j := range s.ingestQueue.List() {
		if j.CMAF != nil && j.CMAF.TotalBytes > 0 && j.AssignedNodeID != "" {
			usage[j.AssignedNodeID] += uint64(j.CMAF.TotalBytes)
		}
	}
	return usage
}

// processingProfiles builds the live reservation map from persisted configs.
func (s *Server) processingProfiles() map[string]cluster.ProcessingProfile {
	profiles := make(map[string]cluster.ProcessingProfile)
	if s.store == nil {
		return profiles
	}
	configs, err := s.store.GetAllNodeConfigs()
	if err != nil {
		return profiles
	}
	for id, cfg := range configs {
		profiles[id] = cluster.ProcessingProfile{
			NodeID:               id,
			Enabled:              cfg.Enabled && cfg.ProcessingEnabled,
			ReservedCPUCores:     cfg.ReservedCPUCores,
			ReservedRAMBytes:     cfg.ReservedRAMBytes,
			PreferredDiskType:    strings.ToUpper(cfg.PreferredDiskType),
			ReservedStorageBytes: cfg.ReservedStorageBytes,
		}
	}
	return profiles
}

func (s *Server) handleProcessing(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	nodes := s.coord.GetNodes()
	result := cluster.EvaluateProcessing(nodes, s.processingProfiles())

	list := make([]cluster.Eligibility, 0, len(result))
	for _, n := range nodes {
		if e, ok := result[n.Metrics.NodeID]; ok {
			list = append(list, e)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"nodes": list,
	})
}

func (s *Server) handleTiers(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/tiers")
	path = strings.Trim(path, "/")
	w.Header().Set("Content-Type", "application/json")

	if r.Method == http.MethodGet {
		nodes := s.coord.GetNodes()
		usage := s.mediaUsagePerNode()

		type tiersResponse struct {
			Tiers           []storage.TierStatus `json:"tiers"`
			UnassignedBlocks []storage.Block    `json:"unassigned_blocks"`
		}

		statuses := s.tiers.Resolve(nodes, usage)

		assigned := make(map[string]bool)
		for _, ts := range statuses {
			for _, b := range ts.Blocks {
				assigned[b.Block.NodeID+"|"+b.Block.Path] = true
			}
		}

		var unassigned []storage.Block
		for _, n := range nodes {
			for _, d := range n.Metrics.Disks {
				key := n.Metrics.NodeID + "|" + d.Path
				if !assigned[key] {
					unassigned = append(unassigned, storage.Block{
						NodeID:     n.Metrics.NodeID,
						Path:       d.Path,
						DiskType:   d.DiskType,
						TotalBytes: d.TotalBytes,
						FreeBytes:  d.FreeBytes,
					})
				}
			}
		}

		json.NewEncoder(w).Encode(tiersResponse{Tiers: statuses, UnassignedBlocks: unassigned})
		return
	}

	if r.Method == http.MethodPost {
		var tier storage.Tier
		if err := json.NewDecoder(r.Body).Decode(&tier); err != nil {
			http.Error(w, "invalid tier payload", http.StatusBadRequest)
			return
		}
		if err := s.tiers.Upsert(tier); err != nil {
			http.Error(w, fmt.Sprintf("failed to save tier: %v", err), http.StatusInternalServerError)
			return
		}
		json.NewEncoder(w).Encode(s.tiers.List())
		return
	}

	if r.Method == http.MethodDelete && path != "" {
		var id int
		if _, err := fmt.Sscanf(path, "%d", &id); err != nil {
			http.Error(w, "invalid tier id", http.StatusBadRequest)
			return
		}
		if err := s.tiers.Delete(id); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		json.NewEncoder(w).Encode(s.tiers.List())
		return
	}

	http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
}

// selectIngestTarget picks where a new ingest job runs. Preference order:
//  1. a processing-eligible node that also owns the default tier block with
//     the most quota headroom (tier- and reservation-aware placement),
//  2. any processing-eligible node,
//  3. the legacy least-loaded worker,
//  4. local master.
func (s *Server) selectIngestTarget() string {
	nodes := s.coord.GetNodes()
	profiles := s.processingProfiles()
	eligibility := cluster.EvaluateProcessing(nodes, profiles)

	var eligibleIDs []string
	for id, e := range eligibility {
		if e.Eligible {
			eligibleIDs = append(eligibleIDs, id)
		}
	}

	if s.tiers != nil && len(eligibleIDs) > 0 {
		usage := s.mediaUsagePerNode()
		if def := s.tiers.DefaultTier(); def != nil {
			if p, err := s.tiers.PickBlock(def.ID, 0, nodes, usage); err == nil {
				if e, ok := eligibility[p.NodeID]; ok && e.Eligible {
					return p.NodeID
				}
			}
		}
	}

	if len(eligibleIDs) > 0 {
		eligibleRecords := make([]*cluster.NodeRecord, 0, len(eligibleIDs))
		for _, n := range nodes {
			if eligibility[n.Metrics.NodeID].Eligible {
				eligibleRecords = append(eligibleRecords, n)
			}
		}
		if id, err := cluster.SelectOptimalWorker(eligibleRecords); err == nil {
			return id
		}
	}

	if id, err := cluster.SelectOptimalWorker(nodes); err == nil {
		return id
	}
	return ""
}

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	validPaths := map[string]bool{
		"/":          true,
		"/dashboard": true,
		"/nodes":     true,
		"/storage":   true,
		"/tiers":     true,
		"/media":     true,
		"/database":  true,
		"/settings":  true,
		"/docs":      true,
	}

	if !validPaths[r.URL.Path] {
		http.NotFound(w, r)
		return
	}

	currentPage := "dashboard"
	switch r.URL.Path {
	case "/nodes":
		currentPage = "nodes"
	case "/storage":
		currentPage = "storage"
	case "/tiers":
		currentPage = "tiers"
	case "/media":
		currentPage = "media"
	case "/database":
		currentPage = "database"
	case "/settings":
		currentPage = "settings"
	case "/docs":
		currentPage = "docs"
	default:
		currentPage = "dashboard"
	}

	// Reload templates dynamically during development
	pattern := filepath.Join(s.templateDir, "*", "*.html")
	if tmpl, err := template.ParseGlob(pattern); err == nil {
		s.tmpl = tmpl
	}

	if s.tmpl != nil {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		data := map[string]interface{}{
			"CurrentPage": currentPage,
		}
		if err := s.tmpl.ExecuteTemplate(w, "base.html", data); err != nil {
			http.Error(w, fmt.Sprintf("Template rendering error: %v", err), http.StatusInternalServerError)
		}
		return
	}

	http.Error(w, fmt.Sprintf("Templates not found in %s", s.templateDir), http.StatusInternalServerError)
}

func (s *Server) handleProvisionNode(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req provision.Request
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("invalid payload: %v", err), http.StatusBadRequest)
		return
	}

	if req.Host == "" {
		http.Error(w, "host is required (e.g. 203.0.113.10 or vps.example.com)", http.StatusBadRequest)
		return
	}
	if req.User == "" {
		req.User = "root"
	}
	if req.NodeName == "" {
		req.NodeName = fmt.Sprintf("vps-%s", strings.ReplaceAll(strings.Split(req.Host, ":")[0], ".", "-"))
	}
	if req.CoordinatorURL == "" {
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		req.CoordinatorURL = fmt.Sprintf("%s://%s", scheme, r.Host)
	}

	fmt.Printf("[Server] 🚀 Received Provisioning Request: Node='%s', Host='%s', User='%s', Port=%d, Sudo=%v\n",
		req.NodeName, req.Host, req.User, req.Port, req.UseSudo)

	binData, err := provision.GetOrBuildLinuxBinary()
	if err != nil {
		fmt.Printf("[Server] ❌ Failed to build/get Linux binary: %v\n", err)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": fmt.Sprintf("binary error: %v", err),
		})
		return
	}

	result, err := provision.ProvisionVPS(r.Context(), req, binData)
	w.Header().Set("Content-Type", "application/json")
	if err != nil {
		fmt.Printf("[Server] ❌ Provisioning failed for '%s': %v\n", req.NodeName, err)
		w.WriteHeader(http.StatusBadGateway)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error":  err.Error(),
			"result": result,
		})
		return
	}

	fmt.Printf("[Server] ✅ Node '%s' successfully provisioned and connected!\n", req.NodeName)

	if s.store != nil {
		cfg, _ := s.store.GetNodeConfig(req.NodeName)
		if cfg == nil {
			cfg = &db.NodeConfig{NodeID: req.NodeName}
		}
		cfg.Enabled = true // Enable node so heartbeats are accepted immediately
		cfg.SSHHost = req.Host
		cfg.SSHUser = req.User
		cfg.SSHPassword = req.Password
		cfg.SSHPort = req.Port
		cfg.SSHUseSudo = req.UseSudo
		_ = s.store.SaveNodeConfig(cfg)
	}

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(result)
}

func (s *Server) handleIngestJob(w http.ResponseWriter, r *http.Request) {
	if strings.HasSuffix(r.URL.Path, "/progress") {
		s.handleJobProgress(w, r)
		return
	}

	if r.Method == http.MethodGet {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(s.ingestQueue.List())
		return
	}

	if r.Method == http.MethodPost {
		var req struct {
			URL    string `json:"url"`
			NodeID string `json:"node_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.URL == "" {
			http.Error(w, "invalid request: url is required", http.StatusBadRequest)
			return
		}

		targetNode := req.NodeID
		if targetNode == "" || targetNode == "auto" {
			targetNode = s.selectIngestTarget()
		}

		jobID := fmt.Sprintf("media_%d", time.Now().Unix())
		job := &ingest.IngestJob{
			JobID:          jobID,
			SourceURL:      req.URL,
			AssignedNodeID: targetNode,
			Status:         ingest.StatusQueued,
			CreatedAt:      time.Now(),
			UpdatedAt:      time.Now(),
		}
		s.ingestQueue.Add(job)

		// Dispatch ingest job: delegate to high-speed remote VPS node or execute on local master
		go func(target string, j *ingest.IngestJob) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
			defer cancel()

				if target != "" && target != "local-master" {
				// Preferred path: push the job down the node's live gRPC mesh
				// stream (instant delivery, no inbound port needed on the node).
				if s.hub != nil && s.hub.DispatchJob(target, j.JobID, j.SourceURL) {
					fmt.Printf("[Ingest Router] Delegated Job '%s' to '%s' over persistent gRPC stream\n",
						j.JobID, target)
					return
				}

				// Legacy HTTP dispatch for agents without a mesh stream.
				nodes := s.coord.GetNodes()
				for _, n := range nodes {
					if n.Metrics.NodeID == target && len(n.Metrics.IPs) > 0 {
						nodeHost := n.Metrics.IPs[0]
						agentPort := n.Metrics.Capabilities.AgentPort
						if agentPort <= 0 {
							agentPort = 2052
						}
						endpoint := fmt.Sprintf("http://%s:%d/api/ingest", nodeHost, agentPort)
						payload, _ := json.Marshal(map[string]string{
							"job_id": j.JobID,
							"url":    j.SourceURL,
						})
						fmt.Printf("[Ingest Router] Delegating Job '%s' to remote VPS '%s' at %s (legacy HTTP)\n",
							j.JobID, target, endpoint)

						client := &http.Client{Timeout: 10 * time.Second}
						resp, err := client.Post(endpoint, "application/json", bytes.NewBuffer(payload))
						if err == nil && (resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusAccepted) {
							resp.Body.Close()
							return
						}
						fmt.Printf("[Ingest Router] Remote VPS '%s' delegation failed (%v), falling back to local master engine\n", target, err)
						break
					}
				}
			}

			// Local master fallback.
			_ = ingest.ProcessIngestJob(ctx, j, filepath.Join("data", "media"), s.ingestQueue)
		}(targetNode, job)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		json.NewEncoder(w).Encode(job)
		return
	}

	if r.Method == http.MethodDelete {
		jobID := strings.TrimPrefix(r.URL.Path, "/api/jobs/")
		jobID = strings.TrimPrefix(jobID, "/api/ingest/")
		if jobID != "" {
			s.ingestQueue.Delete(jobID)
			_ = os.RemoveAll(filepath.Join("data", "media", jobID))
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"deleted"}`))
		return
	}

	http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
}

func (s *Server) handleJobProgress(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	jobID := strings.TrimPrefix(r.URL.Path, "/api/jobs/")
	jobID = strings.TrimSuffix(jobID, "/progress")
	jobID = strings.Trim(jobID, "/")

	var update struct {
		Status          string             `json:"status"`
		ProgressPercent float64            `json:"progress_percent"`
		DownloadSpeed   string             `json:"download_speed"`
		ErrorMsg        string             `json:"error_msg"`
		CMAF            *media.CMAFPackage `json:"cmaf"`
	}

	if err := json.NewDecoder(r.Body).Decode(&update); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}

	s.ingestQueue.UpdateStatus(jobID, ingest.JobStatus(update.Status), update.ProgressPercent, update.DownloadSpeed, update.ErrorMsg)
	if update.CMAF != nil {
		s.ingestQueue.SetCMAF(jobID, update.CMAF)
	}

	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"status":"updated"}`))
}

func (s *Server) handleUploadIngest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if err := r.ParseMultipartForm(32 << 20); err != nil {
		http.Error(w, fmt.Sprintf("invalid upload: %v", err), http.StatusBadRequest)
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "file parameter is required in multipart form", http.StatusBadRequest)
		return
	}
	defer file.Close()

	mediaID := fmt.Sprintf("media_%d", time.Now().Unix())
	folder, err := media.PrepareMediaFolder(filepath.Join("data", "media"), mediaID, header.Filename)
	if err != nil {
		http.Error(w, fmt.Sprintf("prepare folder error: %v", err), http.StatusInternalServerError)
		return
	}

	out, err := os.Create(folder.RawFilePath)
	if err != nil {
		http.Error(w, fmt.Sprintf("create file error: %v", err), http.StatusInternalServerError)
		return
	}
	defer out.Close()

	if _, err := io.Copy(out, file); err != nil {
		http.Error(w, fmt.Sprintf("save file error: %v", err), http.StatusInternalServerError)
		return
	}
	out.Close()

	val, err := media.DetectFileMIME(folder.RawFilePath)
	if err != nil || !val.IsVideo {
		os.RemoveAll(folder.BaseDir)
		http.Error(w, fmt.Sprintf("invalid format '%s'. Only video files (MP4, MKV, WebM, MOV) are allowed", val.MIMEType), http.StatusBadRequest)
		return
	}

	fileStat, _ := os.Stat(folder.RawFilePath)
	rawSize := int64(0)
	if fileStat != nil {
		rawSize = fileStat.Size()
	}

	initialCmaf := &media.CMAFPackage{
		MediaID:    mediaID,
		TotalBytes: rawSize,
		CreatedAt:  time.Now(),
		InitChunk: &media.MediaChunk{
			Index:     0,
			Filename:  header.Filename,
			SizeBytes: rawSize,
			TrackType: "raw_video",
			Tier:      2,
		},
	}

	job := &ingest.IngestJob{
		JobID:           mediaID,
		SourceURL:       header.Filename,
		AssignedNodeID:  "local-master",
		Status:          ingest.StatusPackaging,
		ProgressPercent: 90.0,
		DownloadSpeed:   "Packaging CMAF",
		CMAF:            initialCmaf,
		CreatedAt:       time.Now(),
		UpdatedAt:       time.Now(),
	}
	s.ingestQueue.Add(job)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":   "processing",
		"media_id": mediaID,
		"message":  "Upload complete. Packaging and normalizing audio in background.",
	})

	go func(f *media.MediaFolderStructure, mID, origName string, rSize int64, queue *ingest.IngestQueue) {
		cmafPkg, err := media.RemuxAndPackageCMAF(f.RawFilePath, f.CMAFDir, mID)
		if err != nil {
			cmafPkg = &media.CMAFPackage{
				MediaID:    mID,
				TotalBytes: rSize,
				CreatedAt:  time.Now(),
				InitChunk: &media.MediaChunk{
					Index:     0,
					Filename:  origName,
					SizeBytes: rSize,
					TrackType: "raw_video",
					Tier:      2,
				},
			}
		}
		if rSize > cmafPkg.TotalBytes {
			cmafPkg.TotalBytes = rSize
		}

		manifest := media.GenerateHLSManifest(cmafPkg, fmt.Sprintf("/stream/%s/cmaf", mID))
		_ = f.SaveHLSManifest(manifest)
		_ = f.SaveMetadata(cmafPkg)

		// Drop the raw upload once the remuxed copy exists.
		if _, err := os.Stat(filepath.Join(f.CMAFDir, "video.mp4")); err == nil {
			_ = f.CleanRaw()
		}

		// Update job state to completed
		queue.SetCMAF(mID, cmafPkg)
	}(folder, mediaID, header.Filename, rawSize, s.ingestQueue)
}

func (s *Server) handleStreamManifest(w http.ResponseWriter, r *http.Request) {
	subPath := strings.TrimPrefix(r.URL.Path, "/stream/")
	parts := strings.Split(subPath, "/")
	if len(parts) < 2 {
		http.NotFound(w, r)
		return
	}

	mediaID := parts[0]
	filePath := filepath.Join("data", "media", mediaID, filepath.Join(parts[1:]...))

	if _, err := os.Stat(filePath); err != nil {
		http.NotFound(w, r)
		return
	}

	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Headers", "Range, Accept-Encoding, Origin")
	w.Header().Set("Access-Control-Expose-Headers", "Content-Length, Content-Range, Accept-Ranges")
	w.Header().Set("Accept-Ranges", "bytes")

	if strings.HasSuffix(filePath, ".m3u8") {
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	} else if strings.HasSuffix(filePath, ".mp4") {
		w.Header().Set("Content-Type", "video/mp4")
	} else if strings.HasSuffix(filePath, ".m4s") {
		w.Header().Set("Content-Type", "video/iso.segment")
	}

	http.ServeFile(w, r, filePath)
}
