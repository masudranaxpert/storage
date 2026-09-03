package web

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
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
	"stream/pkg/provision"
	"stream/pkg/storage"
	"stream/pkg/telemetry"
	"stream/pkg/tools"
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
	mux.HandleFunc("/api/dbadmin/", s.handleDBAdmin)
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

	mux.HandleFunc("/", s.handleDashboard)

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
				_ = exec.Command("bash", "-c", tools.InstallShell(tool)).Run()
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
				_ = exec.Command("bash", "-c", tools.UninstallShell(tool)).Run()
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

// mediaUsagePerNode computes the actual storage footprint per block and node from persisted records.
func (s *Server) mediaUsagePerNode() map[string]uint64 {
	usage := make(map[string]uint64)
	if s.store == nil {
		return usage
	}
	recs, err := s.store.ListFileRecords()
	if err != nil {
		return usage
	}
	jobs := map[string]*fileapi.FileJob{}
	if list, err := s.store.ListFileJobs(); err == nil {
		for _, j := range list {
			jobs[j.Key] = j
		}
	}
	for _, rec := range recs {
		if job, ok := jobs[rec.Key]; ok && job.State != fileapi.StateFailed {
			bytes := uint64(rec.SizeBytes)
			usage[job.Placement.NodeID] += bytes
			if job.Placement.Path != "" {
				usage[job.Placement.NodeID+"|"+job.Placement.Path] += bytes
			}
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
	defer func() {
		if rec := recover(); rec != nil {
			fmt.Printf("[Server] ❌ Panic recovered in handleProvisionNode: %v\n", rec)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"error": fmt.Sprintf("internal server error: %v", rec),
			})
		}
	}()

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
