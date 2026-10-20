package web

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"stream/pkg/cluster"
	"stream/pkg/db"
	"stream/pkg/fileapi"
	"stream/pkg/storage"
)

func (s *Server) handleStorageFolders(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	w.Header().Set("Content-Type", "application/json")
	nodes := s.coord.GetNodes()

	type NodeFolderResponse struct {
		NodeID              string                    `json:"node_id"`
		Hostname            string                    `json:"hostname"`
		PrimaryIP           string                    `json:"primary_ip"`
		IPs                 []string                  `json:"ips"`
		Status              string                    `json:"status"`
		Drives              []cluster.StreamDriveStat `json:"drives"`
		MediaSizeBytes      uint64                    `json:"media_size_bytes"`
		MediaFileCount      int                       `json:"media_file_count"`
		ProcessingSizeBytes uint64                    `json:"processing_size_bytes"`
		ProcessingFileCount int                       `json:"processing_file_count"`
		TotalStreamBytes    uint64                    `json:"total_stream_bytes"`
	}

	var results []NodeFolderResponse
	var wg sync.WaitGroup
	var mu sync.Mutex
	client := &http.Client{Timeout: 3 * time.Second}

	for _, n := range nodes {
		if !cluster.IsPoolVPS(n.Metrics) {
			continue
		}
		wg.Add(1)
		go func(node *cluster.NodeRecord) {
			defer wg.Done()
			item := NodeFolderResponse{
				NodeID:    node.Metrics.NodeID,
				Hostname:  node.Metrics.Hostname,
				PrimaryIP: cluster.PreferAgentAddr(node.Metrics.IPs),
				IPs:       node.Metrics.IPs,
				Status:    string(node.Status),
			}

			baseURL := fileapi.AgentBaseURL(node)
			agentReq, reqErr := http.NewRequestWithContext(r.Context(), http.MethodGet, baseURL+"/api/v1/storage-folders", nil)
			if reqErr == nil {
				if s.hub != nil && s.hub.Secret() != "" {
					agentReq.Header.Set("X-Cluster-Secret", s.hub.Secret())
				}
				resp, err := client.Do(agentReq)
				if err == nil {
					if resp.StatusCode == http.StatusOK {
						var folderStats struct {
							Drives              []cluster.StreamDriveStat `json:"drives"`
							MediaSizeBytes      uint64                    `json:"media_size_bytes"`
							MediaFileCount      int                       `json:"media_file_count"`
							ProcessingSizeBytes uint64                    `json:"processing_size_bytes"`
							ProcessingFileCount int                       `json:"processing_file_count"`
							TotalStreamBytes    uint64                    `json:"total_stream_bytes"`
						}
						if err := json.NewDecoder(resp.Body).Decode(&folderStats); err == nil {
							item.Drives = folderStats.Drives
							item.MediaSizeBytes = folderStats.MediaSizeBytes
							item.MediaFileCount = folderStats.MediaFileCount
							item.ProcessingSizeBytes = folderStats.ProcessingSizeBytes
							item.ProcessingFileCount = folderStats.ProcessingFileCount
							item.TotalStreamBytes = folderStats.TotalStreamBytes
						}
					}
					resp.Body.Close()
				}
			}
			mu.Lock()
			results = append(results, item)
			mu.Unlock()
		}(n)
	}
	wg.Wait()

	// Include registered S3/R2 remote object storage buckets in the folders list
	if s.store != nil {
		if buckets, err := s.store.ListBuckets(); err == nil {
			for _, b := range buckets {
				provName := strings.ToUpper(b.Provider)
				if provName == "" {
					provName = "R2"
				}
				results = append(results, NodeFolderResponse{
					NodeID:              "bucket:" + b.ID,
					Hostname:            b.Name,
					PrimaryIP:           b.Endpoint,
					Status:              "online",
					MediaSizeBytes:      uint64(b.UsedBytes),
					MediaFileCount:      int(b.ObjectCount),
					ProcessingSizeBytes: 0,
					ProcessingFileCount: 0,
					TotalStreamBytes:    uint64(b.UsedBytes),
					Drives: []cluster.StreamDriveStat{
						{
							DriveName:        fmt.Sprintf("%s (%s)", b.Name, provName),
							MountPoint:       b.Name,
							StreamRoot:       b.Endpoint,
							MediaDir:         b.Name,
							MediaSizeBytes:   uint64(b.UsedBytes),
							MediaFileCount:   int(b.ObjectCount),
							TotalStreamBytes: uint64(b.UsedBytes),
						},
					},
				})
			}
		}
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].NodeID < results[j].NodeID
	})

	json.NewEncoder(w).Encode(map[string]interface{}{
		"nodes": results,
	})
}

func (s *Server) handleStorageClean(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.requireAdmin(w, r) {
		return
	}

	var req struct {
		NodeID string `json:"node_id"`
		Target string `json:"target"`
		Dir    string `json:"dir"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	client := &http.Client{Timeout: 10 * time.Second}
	nodes := s.coord.GetNodes()

	if req.NodeID == "all" || req.NodeID == "*" {
		var totalFreedBytes uint64
		var totalFreedItems int
		for _, n := range nodes {
			if !cluster.IsPoolVPS(n.Metrics) {
				continue
			}
			baseURL := fileapi.AgentBaseURL(n)
			cleanReq, reqErr := http.NewRequestWithContext(r.Context(), http.MethodPost, baseURL+"/api/v1/storage-clean?target="+url.QueryEscape(req.Target), nil)
			if reqErr == nil {
				if s.hub != nil && s.hub.Secret() != "" {
					cleanReq.Header.Set("X-Cluster-Secret", s.hub.Secret())
				}
				resp, err := client.Do(cleanReq)
				if err == nil {
					if resp.StatusCode == http.StatusOK {
						var res struct {
							FreedBytes uint64 `json:"freed_bytes"`
							FreedItems int    `json:"freed_items"`
						}
						if err := json.NewDecoder(resp.Body).Decode(&res); err == nil {
							totalFreedBytes += res.FreedBytes
							totalFreedItems += res.FreedItems
						}
					}
					resp.Body.Close()
				}
			}
		}

		if req.Target == "media" || req.Target == "all" {
			// Also clean connected remote buckets when purging all media
			if s.store != nil {
				if buckets, err := s.store.ListBuckets(); err == nil {
					for _, b := range buckets {
						cfg := cluster.S3Config{
							Endpoint:  b.Endpoint,
							Region:    b.Region,
							Name:      b.Name,
							AccessKey: b.AccessKey,
							SecretKey: b.SecretKey,
						}
						if objects, err := cluster.ListObjectsS3(r.Context(), cfg, ""); err == nil {
							for _, obj := range objects {
								if err := cluster.DeleteObjectS3(r.Context(), cfg, obj.Key); err == nil {
									totalFreedBytes += uint64(obj.Size)
									totalFreedItems++
								}
							}
						}
						b.UsedBytes = 0
						b.ObjectCount = 0
						_ = s.store.SaveBucket(b)
					}
				}
			}
			// Purge all file records from coordinator DB
			if s.store != nil {
				if recs, err := s.store.ListFileRecords(); err == nil {
					for _, rec := range recs {
						if s.files != nil {
							_ = s.files.Delete(rec.Key)
						} else {
							_ = s.store.DeleteFileJob(rec.Key)
							_ = s.store.DeleteFileRecord(rec.Key)
						}
					}
				}
			}
		}

		json.NewEncoder(w).Encode(map[string]interface{}{
			"status":      "cleaned_all",
			"target":      req.Target,
			"freed_bytes": totalFreedBytes,
			"freed_items": totalFreedItems,
		})
		return
	}

	if strings.HasPrefix(req.NodeID, "bucket:") {
		if s.store == nil {
			http.Error(w, `{"error":"storage database not initialized"}`, http.StatusInternalServerError)
			return
		}
		bucketID := strings.TrimPrefix(req.NodeID, "bucket:")
		b, err := s.store.GetBucket(bucketID)
		if err != nil || b == nil {
			http.Error(w, `{"error":"bucket not found"}`, http.StatusNotFound)
			return
		}

		cfg := cluster.S3Config{
			Endpoint:  b.Endpoint,
			Region:    b.Region,
			Name:      b.Name,
			AccessKey: b.AccessKey,
			SecretKey: b.SecretKey,
		}

		prefix := req.Dir
		if req.Target == "processing" || req.Target == "scratch" || req.Target == "temp" {
			if prefix == "" {
				prefix = "temp/"
			}
		} else if req.Dir == b.Name {
			prefix = ""
		}

		objects, err := cluster.ListObjectsS3(r.Context(), cfg, prefix)
		if err != nil {
			http.Error(w, fmt.Sprintf(`{"error":"failed to list bucket objects: %s"}`, err.Error()), http.StatusBadGateway)
			return
		}

		var totalFreedBytes uint64
		var totalFreedItems int
		for _, obj := range objects {
			if err := cluster.DeleteObjectS3(r.Context(), cfg, obj.Key); err == nil {
				totalFreedBytes += uint64(obj.Size)
				totalFreedItems++
			}
		}

		if req.Target == "media" || req.Target == "all" || prefix == "" {
			b.UsedBytes = 0
			b.ObjectCount = 0
			_ = s.store.SaveBucket(b)

			if recs, err := s.store.ListFileRecords(); err == nil {
				for _, rec := range recs {
					if job, err := s.store.GetFileJob(rec.Key); err == nil && job != nil {
						if job.Placement.NodeID == req.NodeID || job.Placement.NodeID == b.ID {
							if s.files != nil {
								_ = s.files.Delete(rec.Key)
							} else {
								_ = s.store.DeleteFileJob(rec.Key)
								_ = s.store.DeleteFileRecord(rec.Key)
							}
						}
					}
				}
			}
		}

		json.NewEncoder(w).Encode(map[string]interface{}{
			"status":      "cleaned",
			"target":      req.Target,
			"freed_bytes": totalFreedBytes,
			"freed_items": totalFreedItems,
		})
		return
	}

	var targetNode *cluster.NodeRecord
	for _, n := range nodes {
		if n.Metrics.NodeID == req.NodeID {
			targetNode = n
			break
		}
	}
	if targetNode == nil {
		http.Error(w, "node not found", http.StatusNotFound)
		return
	}

	cleanURL := fmt.Sprintf("%s/api/v1/storage-clean?target=%s", fileapi.AgentBaseURL(targetNode), url.QueryEscape(req.Target))
	if req.Dir != "" {
		cleanURL += "&dir=" + url.QueryEscape(req.Dir)
	}

	cleanReq, reqErr := http.NewRequestWithContext(r.Context(), http.MethodPost, cleanURL, nil)
	if reqErr != nil {
		http.Error(w, "failed to create request: "+reqErr.Error(), http.StatusInternalServerError)
		return
	}
	if s.hub != nil && s.hub.Secret() != "" {
		cleanReq.Header.Set("X-Cluster-Secret", s.hub.Secret())
	}
	resp, err := client.Do(cleanReq)
	if err != nil {
		http.Error(w, "failed to dial agent: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK && req.Target == "media" && s.store != nil {
		if recs, err := s.store.ListFileRecords(); err == nil {
			for _, rec := range recs {
				if job, err := s.store.GetFileJob(rec.Key); err == nil && job != nil {
					if job.Placement.NodeID == targetNode.Metrics.NodeID {
						if s.files != nil {
							_ = s.files.Delete(rec.Key)
						} else {
							_ = s.store.DeleteFileJob(rec.Key)
							_ = s.store.DeleteFileRecord(rec.Key)
						}
					}
				}
			}
		}
	}

	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

func (s *Server) handleProcessing(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if !s.requireAdmin(w, r) {
		return
	}

	if r.Method == http.MethodPost {
		var req struct {
			Enabled bool `json:"enabled"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid json payload", http.StatusBadRequest)
			return
		}
		if s.store != nil {
			nodes := s.coord.GetNodes()
			for _, n := range nodes {
				cfg, _ := s.store.GetNodeConfig(n.Metrics.NodeID)
				if cfg == nil {
					cfg = &db.NodeConfig{NodeID: n.Metrics.NodeID, Enabled: true}
				}
				cfg.ProcessingEnabled = req.Enabled
				_ = s.store.SaveNodeConfig(cfg)
			}
		}
		json.NewEncoder(w).Encode(map[string]interface{}{
			"status":  "ok",
			"enabled": req.Enabled,
		})
		return
	}

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

	json.NewEncoder(w).Encode(map[string]interface{}{
		"nodes": list,
	})
}

func (s *Server) handleDriveEligibility(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		NodeID   string `json:"node_id"`
		Path     string `json:"path"`
		Eligible bool   `json:"eligible"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.NodeID == "" || req.Path == "" {
		http.Error(w, "invalid payload", http.StatusBadRequest)
		return
	}

	if s.store != nil {
		cfg, _ := s.store.GetNodeConfig(req.NodeID)
		if cfg == nil {
			cfg = &db.NodeConfig{NodeID: req.NodeID, Enabled: true}
		}
		var updated []string
		for _, p := range cfg.ExcludedDrives {
			if p != req.Path {
				updated = append(updated, p)
			}
		}
		if !req.Eligible {
			updated = append(updated, req.Path)
		}
		cfg.ExcludedDrives = updated
		_ = s.store.SaveNodeConfig(cfg)
	}

	// Detach excluded drive from any tier where it is currently assigned.
	if !req.Eligible && s.tiers != nil {
		for _, t := range s.tiers.List() {
			var kept []storage.Block
			changed := false
			for _, b := range t.Blocks {
				if b.NodeID == req.NodeID && b.Path == req.Path {
					changed = true
				} else {
					kept = append(kept, b)
				}
			}
			if changed {
				t.Blocks = kept
				_ = s.tiers.Upsert(t)
			}
		}
	}

	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":   "ok",
		"node_id":  req.NodeID,
		"path":     req.Path,
		"eligible": req.Eligible,
	})
}

func (s *Server) handleTiers(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/api/tiers")
	path = strings.Trim(path, "/")
	w.Header().Set("Content-Type", "application/json")

	if r.Method == http.MethodGet {
		nodes := s.coord.GetNodes()
		usage := s.mediaUsagePerNode()

		type tiersResponse struct {
			Tiers            []storage.TierStatus `json:"tiers"`
			UnassignedBlocks []storage.Block      `json:"unassigned_blocks"`
			ExcludedBlocks   []string             `json:"excluded_blocks"`
		}

		statuses := s.tiers.Resolve(nodes, usage)

		assigned := make(map[string]bool)
		for _, ts := range statuses {
			for _, b := range ts.Blocks {
				assigned[b.Block.NodeID+"|"+b.Block.Path] = true
			}
		}

		excluded := make(map[string]bool)
		var excludedList []string
		if s.store != nil {
			if cfgs, err := s.store.GetAllNodeConfigs(); err == nil {
				for nodeID, cfg := range cfgs {
					for _, p := range cfg.ExcludedDrives {
						k := nodeID + "|" + p
						if !excluded[k] {
							excluded[k] = true
							excludedList = append(excludedList, k)
						}
					}
				}
			}
		}

		var unassigned []storage.Block
		for _, n := range nodes {
			for _, d := range n.Metrics.Disks {
				key := n.Metrics.NodeID + "|" + d.Path
				if !assigned[key] && !excluded[key] {
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

		// Include unassigned remote S3/R2 storage buckets
		if s.store != nil {
			if buckets, err := s.store.ListBuckets(); err == nil {
				for _, b := range buckets {
					nodeID := "bucket:" + b.ID
					key := nodeID + "|" + b.Name
					if !assigned[key] {
						diskType := strings.ToUpper(b.Provider)
						if diskType == "" {
							diskType = "R2"
						}
						unassigned = append(unassigned, storage.Block{
							NodeID:     nodeID,
							Path:       b.Name,
							DiskType:   diskType,
							PublicHost: b.PublicHost,
							TotalBytes: 100 * 1024 * 1024 * 1024 * 1024,
							FreeBytes:  100 * 1024 * 1024 * 1024 * 1024,
						})
					}
				}
			}
		}

		json.NewEncoder(w).Encode(tiersResponse{
			Tiers:            statuses,
			UnassignedBlocks: unassigned,
			ExcludedBlocks:   excludedList,
		})
		return
	}

	if r.Method == http.MethodPut && strings.HasSuffix(path, "/lifecycle") {
		if !s.requireAdmin(w, r) {
			return
		}
		idPart := strings.TrimSuffix(path, "/lifecycle")
		var id int
		if _, err := fmt.Sscanf(idPart, "%d", &id); err != nil {
			http.Error(w, `{"error":"invalid tier id"}`, http.StatusBadRequest)
			return
		}
		var lc storage.TierLifecycle
		if err := json.NewDecoder(r.Body).Decode(&lc); err != nil {
			http.Error(w, `{"error":"invalid lifecycle payload"}`, http.StatusBadRequest)
			return
		}
		updated, err := s.tiers.UpdateLifecycle(id, lc)
		if err != nil {
			status := http.StatusUnprocessableEntity
			if strings.Contains(err.Error(), "not found") {
				status = http.StatusNotFound
			}
			w.WriteHeader(status)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"error": err.Error(),
				"errors": []map[string]string{
					{"field": "lifecycle", "message": err.Error()},
				},
			})
			return
		}
		json.NewEncoder(w).Encode(updated)
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
