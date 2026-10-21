package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"stream/internal/database"
	"stream/internal/provision"
)

func (s *Server) handleNodeConfigGet(w http.ResponseWriter, r *http.Request, nodeID string) {
		if s.store != nil {
			cfg, err := s.store.GetNodeConfig(nodeID)
			if err == nil {
				safeCfg := *cfg
				if safeCfg.SSHPassword != "" {
					safeCfg.SSHPassword = "******"
				}
				json.NewEncoder(w).Encode(safeCfg)
				return
			}
		}
		json.NewEncoder(w).Encode(database.NodeConfig{NodeID: nodeID, Enabled: true})
		return
}

func (s *Server) handleNodeConfigSave(w http.ResponseWriter, r *http.Request, nodeID string) {
		var payload struct {
			CustomLabel       *string `json:"custom_label,omitempty"`
			AllocatedMaxBytes *uint64 `json:"allocated_max_bytes,omitempty"`
			Enabled           *bool   `json:"enabled,omitempty"`
			CoordinatorURL    *string `json:"coordinator_url,omitempty"`
			SSHHost           *string `json:"ssh_host,omitempty"`
			SSHPort           *int    `json:"ssh_port,omitempty"`
			SSHUser           *string `json:"ssh_user,omitempty"`
			SSHPassword       *string `json:"ssh_password,omitempty"`
			SSHUseSudo        *bool   `json:"ssh_use_sudo,omitempty"`
			GRPCTarget        *string `json:"grpc_target,omitempty"`
			CORSAllowedOrigins *string `json:"cors_allowed_origins,omitempty"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, fmt.Sprintf("invalid payload: %v", err), http.StatusBadRequest)
			return
		}

		var cfg *database.NodeConfig
		if s.store != nil {
			cfg, _ = s.store.GetNodeConfig(nodeID)
		}
		if cfg == nil {
			cfg = &database.NodeConfig{NodeID: nodeID, Enabled: true}
		}

		var coordChanged bool
		var quotaChanged bool
		var corsChanged bool
		var grpcChanged bool

		if payload.GRPCTarget != nil {
			newGRPC := strings.TrimSpace(*payload.GRPCTarget)
			if newGRPC != cfg.GRPCTarget {
				cfg.GRPCTarget = newGRPC
				grpcChanged = true
			}
		}

		if payload.CORSAllowedOrigins != nil {
			trimmed := strings.TrimSpace(*payload.CORSAllowedOrigins)
			if trimmed != cfg.CORSAllowedOrigins {
				corsChanged = true
			}
			cfg.CORSAllowedOrigins = trimmed
		}

		if payload.CustomLabel != nil {
			cfg.CustomLabel = strings.TrimSpace(*payload.CustomLabel)
			if s.coord != nil {
				s.coord.SetNodeCustomLabel(nodeID, cfg.CustomLabel)
			}
		}
		if payload.AllocatedMaxBytes != nil {
			if *payload.AllocatedMaxBytes != cfg.AllocatedMaxBytes {
				quotaChanged = true
			}
			cfg.AllocatedMaxBytes = *payload.AllocatedMaxBytes
		}
		if payload.Enabled != nil {
			cfg.Enabled = *payload.Enabled
		}
		if payload.CoordinatorURL != nil {
			newURL := strings.TrimSpace(*payload.CoordinatorURL)
			if newURL != "" && newURL != cfg.CoordinatorURL {
				cfg.CoordinatorURL = newURL
				coordChanged = true
			}
		}
		if payload.SSHHost != nil && *payload.SSHHost != "" {
			cfg.SSHHost = strings.TrimSpace(*payload.SSHHost)
		}
		if payload.SSHPort != nil && *payload.SSHPort > 0 {
			cfg.SSHPort = *payload.SSHPort
		}
		if payload.SSHUser != nil && *payload.SSHUser != "" {
			cfg.SSHUser = strings.TrimSpace(*payload.SSHUser)
		}
		if payload.SSHPassword != nil && *payload.SSHPassword != "" && *payload.SSHPassword != "******" {
			cfg.SSHPassword = *payload.SSHPassword
		}
		if payload.SSHUseSudo != nil {
			cfg.SSHUseSudo = *payload.SSHUseSudo
		}

		if s.store != nil {
			if err := s.store.SaveNodeConfig(cfg); err != nil {
				http.Error(w, fmt.Sprintf("failed to save node config: %v", err), http.StatusInternalServerError)
				return
			}
		}

		if (coordChanged || quotaChanged || corsChanged || grpcChanged) && cfg.SSHHost != "" && cfg.SSHPassword != "" {
			go func(c database.NodeConfig) {
				secret := ""
				if s.hub != nil {
					secret = s.hub.Secret()
				}
				req := provision.Request{
					Host:           c.SSHHost,
					Port:           c.SSHPort,
					User:           c.SSHUser,
					Password:       c.SSHPassword,
					UseSudo:        c.SSHUseSudo,
					NodeName:       c.NodeID,
					CoordinatorURL: c.CoordinatorURL,
					GRPCTarget:     c.GRPCTarget,
					QuotaBytes:     c.AllocatedMaxBytes,
					CORSOrigins:    c.CORSAllowedOrigins,
					Secret:         secret,
				}
				if err := provision.UpdateServiceOverSSH(context.Background(), req); err != nil {
					fmt.Printf("[Config Update] Remote agent reconfiguration on '%s' (%s) failed: %v\n", c.NodeID, c.SSHHost, err)
					return
				}
				fmt.Printf("[Config Update] Remote agent on '%s' reconfigured (join=%s, grpc=%s, quota=%d, cors=%s)\n", c.NodeID, c.CoordinatorURL, c.GRPCTarget, c.AllocatedMaxBytes, c.CORSAllowedOrigins)
			}(*cfg)
		}

		safeCfg := *cfg
		if safeCfg.SSHPassword != "" {
			safeCfg.SSHPassword = "******"
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(safeCfg)
		return
	}

func (s *Server) handleNodeAllocate(w http.ResponseWriter, r *http.Request, nodeID string) {
		var req struct {
			AllocatedMaxBytes uint64 `json:"allocated_max_bytes"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid json payload", http.StatusBadRequest)
			return
		}

		cfg := &database.NodeConfig{
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

func (s *Server) handleNodeProcessing(w http.ResponseWriter, r *http.Request, nodeID string) {
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
			MaxConcurrentIngests int    `json:"max_concurrent_ingests"`
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
			cfg = &database.NodeConfig{NodeID: nodeID, Enabled: true}
		}
		cfg.ProcessingEnabled = req.Enabled || req.ProcessingEnabled
		cfg.ReservedCPUCores = req.ReservedCPUCores
		if req.MaxConcurrentIngests > 0 {
			cfg.MaxConcurrentIngests = req.MaxConcurrentIngests
		} else if cfg.MaxConcurrentIngests <= 0 {
			cfg.MaxConcurrentIngests = 2
		}
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
