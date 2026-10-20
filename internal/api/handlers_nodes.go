package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"stream/internal/cluster"
	"stream/internal/telemetry"
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

	if !s.requireSecret(w, r) {
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

	corsOrigins := "*"
	if s.store != nil {
		if cfg, _ := s.store.GetNodeConfig(record.Metrics.NodeID); cfg != nil && cfg.CORSAllowedOrigins != "" {
			corsOrigins = cfg.CORSAllowedOrigins
		}
	}

	resp := map[string]interface{}{
		"status":                "ok",
		"record":                record,
		"node_id":               record.Metrics.NodeID,
		"latest_version":        telemetry.CurrentVersion,
		"download_url":          "/download/stream-bundle-linux-amd64.tar.gz",
		"update_available":      needsUpgrade,
		"install_missing_tools": false,
		"cors_allowed_origins":  corsOrigins,
	}
	json.NewEncoder(w).Encode(resp)
}

func (s *Server) handleGetNodes(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(s.coord.GetNodes())
}

func (s *Server) handleGetPool(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(s.coord.GetPoolSummary())
}


func (s *Server) handleNodeAllocation(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/api/nodes/")
	w.Header().Set("Content-Type", "application/json")

	if r.Method == http.MethodDelete {
		nodeID := strings.Trim(path, "/")
		s.handleNodeDelete(w, r, nodeID)
		return
	}

	parts := strings.Split(path, "/")
	if len(parts) < 2 {
		http.NotFound(w, r)
		return
	}

	nodeID := parts[0]
	action := parts[1]

	switch {
	case action == "restart" && r.Method == http.MethodPost:
		s.handleNodeRestart(w, r, nodeID)
	case action == "redeploy" && r.Method == http.MethodPost:
		s.handleNodeRedeploy(w, r, nodeID)
	case action == "config" && r.Method == http.MethodGet:
		s.handleNodeConfigGet(w, r, nodeID)
	case (action == "config" || action == "edit") && (r.Method == http.MethodPost || r.Method == http.MethodPut):
		s.handleNodeConfigSave(w, r, nodeID)
	case action == "rescan" && r.Method == http.MethodPost:
		s.handleNodeRescan(w, r, nodeID)
	case action == "install-tools" && r.Method == http.MethodPost:
		s.handleNodeInstallTools(w, r, nodeID)
	case action == "uninstall-tools" && r.Method == http.MethodPost:
		s.handleNodeUninstallTools(w, r, nodeID)
	case action == "allocate" && r.Method == http.MethodPost:
		s.handleNodeAllocate(w, r, nodeID)
	case action == "processing" && r.Method == http.MethodPost:
		s.handleNodeProcessing(w, r, nodeID)
	default:
		http.NotFound(w, r)
	}
}
