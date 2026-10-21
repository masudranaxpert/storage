package agent

import (
	"encoding/json"
	"net"
	"net/http"
	"path/filepath"
	"strings"

	"stream/internal/cluster"
)

// handleMedia serves the files of an HLS package (/stream/<key|ticket>/<file>) with Range support;
// callers need the cluster secret, a signed token or a playback ticket.
func (a *Agent) handleMedia(w http.ResponseWriter, r *http.Request) {
	subPath := strings.TrimPrefix(r.URL.Path, "/media/")
	subPath = strings.TrimPrefix(subPath, "/stream/")
	cleanSubPath := filepath.Clean(subPath)

	if strings.Contains(cleanSubPath, "..") {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	allowedOrigin := cluster.MatchCORSOrigin(a.GetCORSOrigins(), r.Header.Get("Origin"))
	if allowedOrigin != "" {
		w.Header().Set("Access-Control-Allow-Origin", allowedOrigin)
		w.Header().Set("Access-Control-Allow-Credentials", "true")
		w.Header().Set("Vary", "Origin")
	}
	w.Header().Set("Access-Control-Allow-Methods", "GET, HEAD, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Range, Origin, Accept, Content-Type, Authorization")
	w.Header().Set("Access-Control-Expose-Headers", "Content-Length, Content-Range, Accept-Ranges")
	w.Header().Set("Accept-Ranges", "bytes")

	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}

	parts := strings.Split(cleanSubPath, string(filepath.Separator))
	if len(parts) < 2 {
		http.NotFound(w, r)
		return
	}
	mediaID, ok := a.authorizeMedia(w, r, parts[0])
	if !ok {
		return
	}
	parts[0] = mediaID

	targetFilePath := a.resolveMediaPath(filepath.Join(parts...))
	if targetFilePath == "" {
		http.NotFound(w, r)
		return
	}

	a.recordPlayback(r, mediaID, parts[1:])

	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	if ct := mediaContentType(targetFilePath); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	http.ServeFile(w, r, targetFilePath)
}

// authorizeMedia maps the first path element (raw key, playback ticket or token-signed key) to
// the media key. On failure it writes the 403 response itself and returns false.
func (a *Agent) authorizeMedia(w http.ResponseWriter, r *http.Request, first string) (string, bool) {
	deny := func(msg string) (string, bool) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
		return "", false
	}
	isTicket := strings.HasPrefix(first, "t_")

	if a.verifySecret(r) {
		if isTicket {
			if key, _, err := cluster.ResolvePlaybackTicket(first, a.Secret); err == nil {
				return key, true
			}
		}
		return first, true
	}
	if isTicket {
		key, _, err := cluster.ResolvePlaybackTicket(first, a.Secret)
		if err != nil {
			return deny("forbidden: invalid or expired stream ticket")
		}
		return key, true
	}
	if a.Secret == "" {
		return deny("forbidden: authentication required")
	}
	expStr := r.URL.Query().Get("exp")
	if expStr == "" {
		expStr = r.URL.Query().Get("expires")
	}
	if !cluster.ValidateStreamToken(first, r.URL.Query().Get("token"), expStr, a.Secret) {
		return deny("forbidden: invalid or expired stream token")
	}
	return first, true
}

// recordPlayback feeds the heat and live-viewer trackers. Fetching master.m3u8 starts a play;
// every media segment keeps the viewer alive. Both trackers dedupe per client internally.
func (a *Agent) recordPlayback(r *http.Request, mediaID string, rest []string) {
	file := rest[len(rest)-1]
	isMaster := len(rest) == 1 && file == "master.m3u8"
	if !isMaster && !strings.HasSuffix(file, ".m4s") {
		return
	}
	ip := requestClientIP(r)
	if isMaster {
		a.recordPlayHit(mediaID, ip)
	}
	a.recordViewerHeartbeat(mediaID, ip)
}

// requestClientIP returns the viewer address, preferring the Cloudflare and proxy headers.
func requestClientIP(r *http.Request) string {
	ip := r.Header.Get("CF-Connecting-IP")
	if ip == "" {
		ip = r.Header.Get("X-Forwarded-For")
	}
	if ip == "" {
		ip = r.RemoteAddr
	}
	if idx := strings.Index(ip, ","); idx != -1 {
		ip = strings.TrimSpace(ip[:idx])
	}
	if host, _, err := net.SplitHostPort(ip); err == nil {
		ip = host
	}
	return ip
}

// mediaContentType maps the files of an HLS package to their MIME types.
func mediaContentType(path string) string {
	switch {
	case strings.HasSuffix(path, ".m3u8"):
		return "application/vnd.apple.mpegurl"
	case strings.HasSuffix(path, ".m4s"):
		return "video/iso.segment"
	case strings.HasSuffix(path, ".vtt"):
		return "text/vtt"
	case strings.HasSuffix(path, ".json"):
		return "application/json"
	}
	return ""
}
