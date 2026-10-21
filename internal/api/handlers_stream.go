package api

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"stream/internal/cluster"
	"stream/internal/model"
)

// handleStreamManifest serves /stream/<key|ticket>/<file>: files of an HLS package, either from local
// disk or by redirecting/proxying to the storage node that holds the media.
func (s *Server) handleStreamManifest(w http.ResponseWriter, r *http.Request) {
	subPath := strings.TrimPrefix(r.URL.Path, "/stream/")
	cleanSubPath := filepath.Clean(subPath)
	if strings.Contains(cleanSubPath, "..") || cleanSubPath == "." || cleanSubPath == "" {
		http.NotFound(w, r)
		return
	}

	parts := strings.Split(cleanSubPath, string(filepath.Separator))
	if len(parts) < 2 {
		http.NotFound(w, r)
		return
	}
	keyOrTicket := parts[0]
	isTicket := strings.HasPrefix(keyOrTicket, "t_")

	mediaID, ok := s.authorizeStream(w, r, keyOrTicket, isTicket)
	if !ok {
		return
	}
	parts[0] = mediaID
	rest := parts[1:]

	if rest[len(rest)-1] == "metadata.json" && s.store != nil {
		if job, _ := s.store.GetFileJob(mediaID); job != nil && len(job.MetadataJSON) > 0 {
			w.Header().Set("Content-Type", "application/json")
			s.setStreamCORS(w, r, s.nodeCORS(job.Placement.NodeID), "Range, Origin, Accept, Content-Type, Authorization")
			_, _ = w.Write(job.MetadataJSON)
			return
		}
	}

	filePath := filepath.Join("data", "media", filepath.Join(parts...))
	if stat, err := os.Stat(filePath); err == nil && !stat.IsDir() {
		s.countPlay(r, mediaID, rest)
		s.serveLocalStreamFile(w, r, mediaID, filePath)
		return
	}

	if s.files == nil || s.store == nil {
		http.NotFound(w, r)
		return
	}
	job, _ := s.store.GetFileJob(mediaID)
	if job == nil || job.Placement.NodeID == "" {
		http.NotFound(w, r)
		return
	}
	targetURL := s.remoteStreamURL(job, keyOrTicket, isTicket, rest)
	if targetURL == "" {
		http.NotFound(w, r)
		return
	}
	if r.URL.RawQuery != "" {
		sep := "?"
		if strings.Contains(targetURL, "?") {
			sep = "&"
		}
		targetURL += sep + r.URL.RawQuery
	}
	s.countPlay(r, mediaID, rest)

	isHTTPS := r.TLS != nil ||
		strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") ||
		r.Header.Get("CF-Ray") != "" ||
		strings.Contains(r.Header.Get("CF-Visitor"), "https")

	// Proxy is only required when HTTPS clients access unencrypted HTTP nodes to prevent mixed-content blocks,
	// or when explicitly requested via ?proxy=1. External HTTPS CDN / R2 targets are redirected directly.
	isProxy := (isHTTPS && !strings.HasPrefix(targetURL, "https://")) ||
		r.URL.Query().Get("proxy") == "true" ||
		r.URL.Query().Get("proxy") == "1" ||
		strings.EqualFold(r.Header.Get("X-Stream-Proxy"), "true")

	nodeCORS := s.nodeCORS(job.Placement.NodeID)
	s.setStreamCORS(w, r, nodeCORS, "Range, Origin, Accept, Content-Type, Authorization")
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}
	if isProxy {
		s.proxyStream(w, r, targetURL, nodeCORS)
		return
	}
	http.Redirect(w, r, targetURL, http.StatusTemporaryRedirect)
}

// authorizeStream maps the first path element to the media key, failing closed. Authenticated
// callers may use raw keys; everyone else needs a valid playback ticket or signed token. On
// failure it writes the 403 response itself and returns false.
func (s *Server) authorizeStream(w http.ResponseWriter, r *http.Request, keyOrTicket string, isTicket bool) (string, bool) {
	secret := adminSecret(s)
	deny := func(msg string) (string, bool) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
		return "", false
	}

	if s.isCallerAuthenticated(r) {
		if isTicket {
			if key, _, err := cluster.ResolvePlaybackTicket(keyOrTicket, secret); err == nil {
				return key, true
			}
		}
		return keyOrTicket, true
	}
	if isTicket {
		key, _, err := cluster.ResolvePlaybackTicket(keyOrTicket, secret)
		if err != nil {
			return deny("forbidden: invalid or expired stream ticket")
		}
		return key, true
	}
	if secret == "" {
		return deny("forbidden: stream authentication required")
	}
	expStr := r.URL.Query().Get("exp")
	if expStr == "" {
		expStr = r.URL.Query().Get("expires")
	}
	if !ValidateStreamToken(keyOrTicket, r.URL.Query().Get("token"), expStr, secret) {
		return deny("forbidden: invalid or expired stream token")
	}
	return keyOrTicket, true
}

// remoteStreamURL builds the node (or bucket) URL for one file of the media, keeping the opaque
// playback ticket on worker URLs so the raw database key is never leaked.
func (s *Server) remoteStreamURL(job *model.FileJob, keyOrTicket string, isTicket bool, rest []string) string {
	targetURL := s.files.StreamURL(job)
	file := strings.Join(rest, "/")
	if strings.HasPrefix(job.Placement.NodeID, "bucket:") {
		if idx := strings.LastIndex(targetURL, "/"); idx != -1 {
			return targetURL[:idx] + "/" + file
		}
		return targetURL + "/" + file
	}
	if targetURL == "" && s.coord != nil {
		// Even if the node looks offline because of a delayed heartbeat, try its recorded address.
		for _, n := range s.coord.GetNodes() {
			if n.Metrics.NodeID == job.Placement.NodeID {
				targetURL = fmt.Sprintf("%s/stream/%s", AgentBaseURL(n), job.Key)
				break
			}
		}
	}
	if targetURL == "" {
		return ""
	}
	if isTicket && strings.Contains(targetURL, "/stream/"+job.Key) {
		targetURL = strings.Replace(targetURL, "/stream/"+job.Key, "/stream/"+keyOrTicket, 1)
	}
	return targetURL + "/" + file
}

// countPlay raises the media's heat when a viewer fetches the master playlist (one play per
// client, deduplicated); segment requests never count.
func (s *Server) countPlay(r *http.Request, mediaID string, rest []string) {
	if len(rest) != 1 || rest[0] != "master.m3u8" || s.engine == nil || s.deduper == nil {
		return
	}
	clientIP := r.RemoteAddr
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		clientIP = host
	}
	if s.deduper.ShouldCount(clientIP, mediaID, time.Now()) {
		s.engine.RecordHit(mediaID)
	}
}

// nodeCORS returns the CORS allow-list configured for a node, or "*" when none is set.
func (s *Server) nodeCORS(nodeID string) string {
	if nodeID != "" && s.store != nil {
		if cfg, _ := s.store.GetNodeConfig(nodeID); cfg != nil && cfg.CORSAllowedOrigins != "" {
			return cfg.CORSAllowedOrigins
		}
	}
	return "*"
}

func (s *Server) setStreamCORS(w http.ResponseWriter, r *http.Request, nodeCORS, allowHeaders string) {
	if allowed := cluster.MatchCORSOrigin(nodeCORS, r.Header.Get("Origin")); allowed != "" {
		w.Header().Set("Access-Control-Allow-Origin", allowed)
		w.Header().Set("Access-Control-Allow-Credentials", "true")
		w.Header().Set("Vary", "Origin")
	}
	w.Header().Set("Access-Control-Allow-Methods", "GET, HEAD, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", allowHeaders)
	w.Header().Set("Access-Control-Expose-Headers", "Content-Length, Content-Range, Accept-Ranges")
}

// serveLocalStreamFile serves a file stored on the coordinator itself.
func (s *Server) serveLocalStreamFile(w http.ResponseWriter, r *http.Request, mediaID, filePath string) {
	var nodeID string
	if s.store != nil {
		if job, _ := s.store.GetFileJob(mediaID); job != nil {
			nodeID = job.Placement.NodeID
		}
	}
	s.setStreamCORS(w, r, s.nodeCORS(nodeID), "Range, Accept-Encoding, Origin, Content-Type, Authorization")
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")

	switch {
	case strings.HasSuffix(filePath, ".m3u8"):
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	case strings.HasSuffix(filePath, ".m4s"):
		w.Header().Set("Content-Type", "video/iso.segment")
	case strings.HasSuffix(filePath, ".vtt"):
		w.Header().Set("Content-Type", "text/vtt")
	case strings.HasSuffix(filePath, ".json"):
		w.Header().Set("Content-Type", "application/json")
	}

	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}
	http.ServeFile(w, r, filePath)
}
