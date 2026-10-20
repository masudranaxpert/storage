package api

import (
	"encoding/json"
	"math"
	"net/http"

	"stream/internal/cache"
)

// UsageCacheStats reports the cached per-node media usage snapshot.
func (s *Server) UsageCacheStats() cache.Snapshot {
	s.usageMu.Lock()
	defer s.usageMu.Unlock()
	var bytes int64
	for node, usage := range s.usageCache {
		bytes += int64(len(node)) + 8
		_ = usage
	}
	return cache.Snapshot{Name: "node_usage_snapshot", Entries: len(s.usageCache), Bytes: bytes}
}

// handleSystemCache reports the in-memory cache budget and live per-cache usage.
func (s *Server) handleSystemCache(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	w.Header().Set("Content-Type", "application/json")
	caches, total := cache.Default.Snapshot()
	pct := 0.0
	if cache.MaxTotalBytes > 0 {
		pct = math.Round(float64(total)/float64(cache.MaxTotalBytes)*1000) / 10
	}
	json.NewEncoder(w).Encode(map[string]interface{}{
		"limit_bytes": cache.MaxTotalBytes,
		"used_bytes":  total,
		"usage_pct":   pct,
		"caches":      caches,
	})
}
