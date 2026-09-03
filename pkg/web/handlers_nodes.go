package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"time"

	"stream/pkg/cluster"
	"stream/pkg/db"
	"stream/pkg/provision"
	"stream/pkg/telemetry"
	"stream/pkg/tools"
)

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
