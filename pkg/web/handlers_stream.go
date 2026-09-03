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
