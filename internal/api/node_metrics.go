package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"stream/internal/cluster"
	"stream/internal/model"
)

// mediaUsagePerNode computes the actual storage footprint per block and node.
// Online agents are queried in parallel (1s per-node timeout) via /api/v1/storage-folders,
// falling back to completed DB records for offline nodes, with a 5s cache.
func (s *Server) mediaUsagePerNode() map[string]uint64 {
	s.usageMu.Lock()
	if s.usageCache != nil && time.Since(s.usageCachedAt) < 5*time.Second {
		cached := make(map[string]uint64, len(s.usageCache))
		for k, v := range s.usageCache {
			cached[k] = v
		}
		s.usageMu.Unlock()
		return cached
	}
	s.usageMu.Unlock()

	usage := make(map[string]uint64)

	// 1. Query live online agents in parallel for their actual measured media directory bytes.
	if s.coord != nil {
		nodes := s.coord.GetNodes()
		client := &http.Client{Timeout: 1 * time.Second}
		var wg sync.WaitGroup
		var mu sync.Mutex

		for _, n := range nodes {
			if n.Status != cluster.StatusOnline || !cluster.IsPoolVPS(n.Metrics) {
				continue
			}
			wg.Add(1)
			go func(n *cluster.NodeRecord) {
				defer wg.Done()
				ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
				defer cancel()

				req, err := http.NewRequestWithContext(ctx, http.MethodGet, AgentBaseURL(n)+"/api/v1/storage-folders", nil)
				if err != nil {
					return
				}
				if s.hub != nil && s.hub.Secret() != "" {
					req.Header.Set("X-Cluster-Secret", s.hub.Secret())
				}
				resp, err := client.Do(req)
				if err != nil {
					return
				}
				defer resp.Body.Close()
				if resp.StatusCode != http.StatusOK {
					return
				}
				var folderStats struct {
					Drives         []StreamDriveStat `json:"drives"`
					MediaSizeBytes uint64            `json:"media_size_bytes"`
				}
				if json.NewDecoder(resp.Body).Decode(&folderStats) != nil {
					return
				}
				mu.Lock()
				usage[n.Metrics.NodeID] = folderStats.MediaSizeBytes
				for _, d := range folderStats.Drives {
					usage[n.Metrics.NodeID+"|"+d.MountPoint] = d.MediaSizeBytes
					if d.MountPoint == "/" || d.MountPoint == "" {
						usage[n.Metrics.NodeID+"|/"] = d.MediaSizeBytes
					}
				}
				// Fallback only for disks the agent did not measure; overwriting
				// would charge every block on the node with the node-wide total.
				for _, disk := range n.Metrics.Disks {
					if _, ok := usage[n.Metrics.NodeID+"|"+disk.Path]; !ok {
						usage[n.Metrics.NodeID+"|"+disk.Path] = folderStats.MediaSizeBytes
					}
				}
				mu.Unlock()
			}(n)
		}
		wg.Wait()
	}

	// 2. For any nodes not yet resolved from live agents, count completed DB records.
	if s.store != nil {
		recs, err := s.store.ListFileRecords()
		if err == nil {
			jobs := map[string]*model.FileJob{}
			if list, err := s.store.ListFileJobs(); err == nil {
				for _, j := range list {
					jobs[j.Key] = j
				}
			}
			for _, rec := range recs {
				if job, ok := jobs[rec.Key]; ok && job.State == model.StateCompleted {
					if _, has := usage[job.Placement.NodeID]; !has {
						bytes := uint64(rec.SizeBytes)
						usage[job.Placement.NodeID] += bytes
						if job.Placement.Path != "" {
							usage[job.Placement.NodeID+"|"+job.Placement.Path] += bytes
						}
					}
				}
			}
		}
	}

	s.usageMu.Lock()
	s.usageCache = make(map[string]uint64, len(usage))
	for k, v := range usage {
		s.usageCache[k] = v
	}
	s.usageCachedAt = time.Now()
	s.usageMu.Unlock()

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
		maxConcurrent := cfg.MaxConcurrentIngests
		if maxConcurrent <= 0 {
			maxConcurrent = 2
		}
		profiles[id] = cluster.ProcessingProfile{
			NodeID:               id,
			Enabled:              cfg.Enabled && cfg.ProcessingEnabled,
			ReservedCPUCores:     cfg.ReservedCPUCores,
			ReservedRAMBytes:     cfg.ReservedRAMBytes,
			PreferredDiskType:    strings.ToUpper(cfg.PreferredDiskType),
			ReservedStorageBytes: cfg.ReservedStorageBytes,
			MaxConcurrentIngests: maxConcurrent,
		}
	}
	return profiles
}
