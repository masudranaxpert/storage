package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"stream/pkg/cluster"
	"stream/pkg/fileapi"
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

				req, err := http.NewRequestWithContext(ctx, http.MethodGet, fileapi.AgentBaseURL(n)+"/api/v1/storage-folders", nil)
				if err != nil {
					return
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
					Drives         []cluster.StreamDriveStat `json:"drives"`
					MediaSizeBytes uint64                    `json:"media_size_bytes"`
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
			jobs := map[string]*fileapi.FileJob{}
			if list, err := s.store.ListFileJobs(); err == nil {
				for _, j := range list {
					jobs[j.Key] = j
				}
			}
			for _, rec := range recs {
				if job, ok := jobs[rec.Key]; ok && job.State == fileapi.StateCompleted {
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

func (s *Server) handleStreamManifest(w http.ResponseWriter, r *http.Request) {
	subPath := strings.TrimPrefix(r.URL.Path, "/stream/")
	cleanSubPath := filepath.Clean(subPath)
	if strings.Contains(cleanSubPath, "..") || cleanSubPath == "." || cleanSubPath == "" {
		http.NotFound(w, r)
		return
	}

	parts := strings.Split(cleanSubPath, string(filepath.Separator))
	mediaID := parts[0]

	if strings.HasSuffix(cleanSubPath, "metadata.json") && s.store != nil {
		if job, _ := s.store.GetFileJob(mediaID); job != nil && len(job.MetadataJSON) > 0 {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Access-Control-Allow-Origin", "*")
			_, _ = w.Write(job.MetadataJSON)
			return
		}
	}
	var filePath string
	if len(parts) == 1 {
		filePath = filepath.Join("data", "media", mediaID)
	} else {
		filePath = filepath.Join("data", "media", mediaID, filepath.Join(parts[1:]...))
	}

	stat, err := os.Stat(filePath)
	if err != nil {
		if s.files != nil && s.store != nil {
			if job, _ := s.store.GetFileJob(mediaID); job != nil && job.Placement.NodeID != "" {
				targetURL := s.files.StreamURL(job)
				if targetURL == "" && s.coord != nil {
					// Fallback: even if node status is offline due to a delayed heartbeat,
					// attempt proxy/stream via node's recorded IP if node record exists.
					for _, n := range s.coord.GetNodes() {
						if n.Metrics.NodeID == job.Placement.NodeID {
							targetURL = fmt.Sprintf("%s/stream/%s", fileapi.AgentBaseURL(n), job.Key)
							break
						}
					}
				}
				if targetURL != "" {
					isPlay := len(parts) == 1 || strings.HasSuffix(cleanSubPath, ".m3u8") || strings.HasSuffix(cleanSubPath, ".mpd") || strings.HasSuffix(cleanSubPath, ".mp4")
					if isPlay && s.engine != nil && s.deduper != nil {
						clientIP := r.RemoteAddr
						if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
							clientIP = host
						}
						if s.deduper.ShouldCount(clientIP, mediaID, time.Now()) {
							s.engine.RecordHit(mediaID)
						}
					}
					if len(parts) > 1 {
						targetURL += "/" + strings.Join(parts[1:], "/")
					}

					isHTTPS := r.TLS != nil ||
						strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") ||
						r.Header.Get("CF-Ray") != "" ||
						strings.Contains(r.Header.Get("CF-Visitor"), "https")

					isProxy := isHTTPS ||
						r.URL.Query().Get("proxy") == "true" ||
						r.URL.Query().Get("proxy") == "1" ||
						strings.EqualFold(r.Header.Get("X-Stream-Proxy"), "true")

					if isProxy {
						s.proxyStream(w, r, targetURL)
						return
					}
					http.Redirect(w, r, targetURL, http.StatusTemporaryRedirect)
					return
				}
			}
		}
		http.NotFound(w, r)
		return
	}
	if stat.IsDir() {
		entries, err := os.ReadDir(filePath)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		found := false
		for _, e := range entries {
			if !e.IsDir() && e.Name() == "master.m3u8" {
				filePath = filepath.Join(filePath, e.Name())
				found = true
				break
			}
		}
		if !found {
			for _, e := range entries {
				if !e.IsDir() && strings.HasSuffix(e.Name(), ".mp4") {
					filePath = filepath.Join(filePath, e.Name())
					found = true
					break
				}
			}
		}
		if !found {
			for _, e := range entries {
				if !e.IsDir() && strings.HasSuffix(e.Name(), ".mkv") {
					filePath = filepath.Join(filePath, e.Name())
					found = true
					break
				}
			}
		}
		if !found {
			http.NotFound(w, r)
			return
		}
	}

	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, HEAD, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Range, Accept-Encoding, Origin, Content-Type")
	w.Header().Set("Access-Control-Expose-Headers", "Content-Length, Content-Range, Accept-Ranges")
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")

	if strings.HasSuffix(filePath, ".m3u8") {
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	} else if strings.HasSuffix(filePath, ".mp4") {
		w.Header().Set("Content-Type", "video/mp4")
	} else if strings.HasSuffix(filePath, ".m4a") {
		w.Header().Set("Content-Type", "audio/mp4")
	} else if strings.HasSuffix(filePath, ".vtt") {
		w.Header().Set("Content-Type", "text/vtt")
	} else if strings.HasSuffix(filePath, ".json") {
		w.Header().Set("Content-Type", "application/json")
	} else if strings.HasSuffix(filePath, ".m4s") {
		w.Header().Set("Content-Type", "video/iso.segment")
	}

	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}

	http.ServeFile(w, r, filePath)
}

func (s *Server) proxyStream(w http.ResponseWriter, r *http.Request, targetURL string) {
	parsedURL, err := url.Parse(targetURL)
	if err != nil {
		http.Error(w, "invalid target stream url", http.StatusBadGateway)
		return
	}

	proxy := httputil.NewSingleHostReverseProxy(&url.URL{
		Scheme: parsedURL.Scheme,
		Host:   parsedURL.Host,
	})

	proxy.Director = func(req *http.Request) {
		req.URL.Scheme = parsedURL.Scheme
		req.URL.Host = parsedURL.Host
		req.URL.Path = parsedURL.Path
		req.URL.RawQuery = parsedURL.RawQuery
		req.Host = parsedURL.Host
		if r.Header.Get("Range") != "" {
			req.Header.Set("Range", r.Header.Get("Range"))
		}
	}

	proxy.ModifyResponse = func(resp *http.Response) error {
		resp.Header.Set("Access-Control-Allow-Origin", "*")
		resp.Header.Set("Access-Control-Allow-Methods", "GET, HEAD, OPTIONS")
		resp.Header.Set("Access-Control-Allow-Headers", "Range, Origin, Accept, Content-Type")
		resp.Header.Set("Access-Control-Expose-Headers", "Content-Length, Content-Range, Accept-Ranges")
		return nil
	}

	proxy.ErrorHandler = func(rw http.ResponseWriter, req *http.Request, err error) {
		http.Error(rw, "storage node stream unavailable: "+err.Error(), http.StatusBadGateway)
	}

	proxy.ServeHTTP(w, r)
}
