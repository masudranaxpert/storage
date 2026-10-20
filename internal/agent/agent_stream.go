package agent

import (
	"encoding/json"
	"net"
	"net/http"
	"path/filepath"
	"strings"

	"stream/internal/cluster"
)
// handleMedia serves media files over HTTP; callers need the cluster secret, a signed token or a playback ticket.
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
	mediaID := parts[0]
	hasMP4Suffix := strings.HasSuffix(mediaID, ".mp4")
	baseID := strings.TrimSuffix(mediaID, ".mp4")

	if !a.verifySecret(r) {
		if strings.HasPrefix(baseID, "t_") {
			resolvedKey, _, err := cluster.ResolvePlaybackTicket(baseID, a.Secret)
			if err != nil {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusForbidden)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": "forbidden: invalid or expired stream ticket"})
				return
			}
			mediaID = resolvedKey
			parts[0] = resolvedKey
			cleanSubPath = filepath.Join(parts...)
			if hasMP4Suffix && len(parts) == 1 {
				cleanSubPath += ".mp4"
			}
		} else {
			if a.Secret == "" {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusForbidden)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": "forbidden: authentication required"})
				return
			}
			token := r.URL.Query().Get("token")
			expStr := r.URL.Query().Get("exp")
			if expStr == "" {
				expStr = r.URL.Query().Get("expires")
			}
			if !cluster.ValidateStreamToken(baseID, token, expStr, a.Secret) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusForbidden)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": "forbidden: invalid or expired stream token"})
				return
			}
			mediaID = baseID
			parts[0] = baseID
			cleanSubPath = filepath.Join(parts...)
			if hasMP4Suffix && len(parts) == 1 {
				cleanSubPath += ".mp4"
			}
		}
	} else if strings.HasPrefix(baseID, "t_") {
		if resolvedKey, _, err := cluster.ResolvePlaybackTicket(baseID, a.Secret); err == nil {
			mediaID = resolvedKey
			parts[0] = resolvedKey
			cleanSubPath = filepath.Join(parts...)
			if hasMP4Suffix && len(parts) == 1 {
				cleanSubPath += ".mp4"
			}
		}
	} else if hasMP4Suffix {
		mediaID = baseID
		parts[0] = baseID
		cleanSubPath = filepath.Join(parts...)
		if len(parts) == 1 {
			cleanSubPath += ".mp4"
		}
	}

	targetFilePath := a.resolveMediaPath(cleanSubPath)
	if targetFilePath == "" {
		http.NotFound(w, r)
		return
	}

	isPlay := (len(parts) == 1 || hasMP4Suffix) &&
		!strings.Contains(cleanSubPath, "audio_") &&
		!strings.Contains(cleanSubPath, "sub_") &&
		!strings.HasSuffix(cleanSubPath, ".vtt") &&
		!strings.HasSuffix(cleanSubPath, ".m4a")

	if isPlay && mediaID != "" {
		clientIP := r.Header.Get("CF-Connecting-IP")
		if clientIP == "" {
			clientIP = r.Header.Get("X-Forwarded-For")
		}
		if clientIP == "" {
			clientIP = r.RemoteAddr
		}
		if idx := strings.Index(clientIP, ","); idx != -1 {
			clientIP = strings.TrimSpace(clientIP[:idx])
		}
		if host, _, err := net.SplitHostPort(clientIP); err == nil {
			clientIP = host
		}
		a.recordPlayHit(mediaID, clientIP)
		a.recordViewerHeartbeat(mediaID, clientIP)
	}

	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")

	if strings.HasSuffix(targetFilePath, ".mp4") {
		w.Header().Set("Content-Type", "video/mp4")
	} else if strings.HasSuffix(targetFilePath, ".json") {
		w.Header().Set("Content-Type", "application/json")
	} else if strings.HasSuffix(targetFilePath, ".vtt") {
		w.Header().Set("Content-Type", "text/vtt")
	} else if strings.HasSuffix(targetFilePath, ".m4a") {
		w.Header().Set("Content-Type", "audio/mp4")
	} else if strings.HasSuffix(targetFilePath, ".m4s") {
		w.Header().Set("Content-Type", "video/iso.segment")
	}

	http.ServeFile(w, r, targetFilePath)
}
