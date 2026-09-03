package web

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"stream/pkg/cluster"
	"stream/pkg/fileapi"
)

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

func (s *Server) handleStreamManifest(w http.ResponseWriter, r *http.Request) {
	subPath := strings.TrimPrefix(r.URL.Path, "/stream/")
	cleanSubPath := filepath.Clean(subPath)
	if strings.Contains(cleanSubPath, "..") || cleanSubPath == "." || cleanSubPath == "" {
		http.NotFound(w, r)
		return
	}

	parts := strings.Split(cleanSubPath, string(filepath.Separator))
	mediaID := parts[0]
	var filePath string
	if len(parts) == 1 {
		filePath = filepath.Join("data", "media", mediaID)
	} else {
		filePath = filepath.Join("data", "media", mediaID, filepath.Join(parts[1:]...))
	}

	stat, err := os.Stat(filePath)
	if err != nil {
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
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".mp4") {
				filePath = filepath.Join(filePath, e.Name())
				found = true
				break
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
