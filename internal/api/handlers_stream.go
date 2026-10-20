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
)

func (s *Server) handleStreamManifest(w http.ResponseWriter, r *http.Request) {
	subPath := strings.TrimPrefix(r.URL.Path, "/stream/")
	cleanSubPath := filepath.Clean(subPath)
	if strings.Contains(cleanSubPath, "..") || cleanSubPath == "." || cleanSubPath == "" {
		http.NotFound(w, r)
		return
	}

	parts := strings.Split(cleanSubPath, string(filepath.Separator))
	rawKeyOrTicket := parts[0]
	hasMP4Suffix := strings.HasSuffix(rawKeyOrTicket, ".mp4")
	baseKeyOrTicket := strings.TrimSuffix(rawKeyOrTicket, ".mp4")
	mediaID := baseKeyOrTicket
	isTicket := strings.HasPrefix(baseKeyOrTicket, "t_")

	secret := adminSecret(s)

	// Ticket or Token verification (Fail Closed)
	if !s.isCallerAuthenticated(r) {
		if isTicket {
			resolvedKey, _, err := cluster.ResolvePlaybackTicket(baseKeyOrTicket, secret)
			if err != nil {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusForbidden)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": "forbidden: invalid or expired stream ticket"})
				return
			}
			parts[0] = resolvedKey
			cleanSubPath = filepath.Join(parts...)
			if hasMP4Suffix && len(parts) == 1 {
				cleanSubPath += ".mp4"
			}
			mediaID = resolvedKey
		} else {
			if secret == "" {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusForbidden)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": "forbidden: stream authentication required"})
				return
			}
			token := r.URL.Query().Get("token")
			expStr := r.URL.Query().Get("exp")
			if expStr == "" {
				expStr = r.URL.Query().Get("expires")
			}
			if !ValidateStreamToken(baseKeyOrTicket, token, expStr, secret) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusForbidden)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": "forbidden: invalid or expired stream token"})
				return
			}
			parts[0] = baseKeyOrTicket
			cleanSubPath = filepath.Join(parts...)
			if hasMP4Suffix && len(parts) == 1 {
				cleanSubPath += ".mp4"
			}
			mediaID = baseKeyOrTicket
		}
	} else if isTicket {
		if resolvedKey, _, err := cluster.ResolvePlaybackTicket(baseKeyOrTicket, secret); err == nil {
			parts[0] = resolvedKey
			cleanSubPath = filepath.Join(parts...)
			if hasMP4Suffix && len(parts) == 1 {
				cleanSubPath += ".mp4"
			}
			mediaID = resolvedKey
		}
	} else if hasMP4Suffix {
		parts[0] = baseKeyOrTicket
		cleanSubPath = filepath.Join(parts...)
		if len(parts) == 1 {
			cleanSubPath += ".mp4"
		}
		mediaID = baseKeyOrTicket
	}

	// Embed Player: When a single file or ticket is requested by a browser or iframe
	// without ?raw=1, without .mp4 suffix, and without Range header, render the standalone ArtPlayer embed page.
	wantsHTML := len(parts) == 1 && !hasMP4Suffix &&
		(strings.Contains(r.Header.Get("Accept"), "text/html") || r.URL.Query().Get("embed") == "1" || r.URL.Query().Get("player") == "1") &&
		r.URL.Query().Get("raw") != "1" &&
		r.Header.Get("Range") == ""

	if wantsHTML && s.store != nil {
		if job, _ := s.store.GetFileJob(mediaID); job != nil {
			if s.engine != nil && s.deduper != nil {
				clientIP := r.RemoteAddr
				if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
					clientIP = host
				}
				if s.deduper.ShouldCount(clientIP, mediaID, time.Now()) {
					s.engine.RecordHit(mediaID)
				}
			}
			s.renderStreamEmbed(w, r, job, rawKeyOrTicket)
			return
		}
	}

	if strings.HasSuffix(cleanSubPath, "metadata.json") && s.store != nil {
		if job, _ := s.store.GetFileJob(mediaID); job != nil && len(job.MetadataJSON) > 0 {
			w.Header().Set("Content-Type", "application/json")
			nodeCORS := "*"
			if job.Placement.NodeID != "" && s.store != nil {
				if cfg, _ := s.store.GetNodeConfig(job.Placement.NodeID); cfg != nil && cfg.CORSAllowedOrigins != "" {
					nodeCORS = cfg.CORSAllowedOrigins
				}
			}
			allowed := cluster.MatchCORSOrigin(nodeCORS, r.Header.Get("Origin"))
			if allowed != "" {
				w.Header().Set("Access-Control-Allow-Origin", allowed)
				w.Header().Set("Access-Control-Allow-Credentials", "true")
				w.Header().Set("Vary", "Origin")
			}
			w.Header().Set("Access-Control-Allow-Methods", "GET, HEAD, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Range, Origin, Accept, Content-Type, Authorization")
			w.Header().Set("Access-Control-Expose-Headers", "Content-Length, Content-Range, Accept-Ranges")
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
	if err == nil && stat.IsDir() {
		if entries, rErr := os.ReadDir(filePath); rErr == nil {
			for _, e := range entries {
				if !e.IsDir() && (strings.HasSuffix(e.Name(), ".mp4") || strings.HasSuffix(e.Name(), ".mkv")) {
					filePath = filepath.Join(filePath, e.Name())
					stat, err = os.Stat(filePath)
					break
				}
			}
		}
	} else if err != nil && hasMP4Suffix && len(parts) == 1 {
		dirPath := filepath.Join("data", "media", mediaID)
		if dStat, dErr := os.Stat(dirPath); dErr == nil && dStat.IsDir() {
			if entries, rErr := os.ReadDir(dirPath); rErr == nil {
				for _, e := range entries {
					if !e.IsDir() && (strings.HasSuffix(e.Name(), ".mp4") || strings.HasSuffix(e.Name(), ".mkv")) {
						filePath = filepath.Join(dirPath, e.Name())
						stat, err = os.Stat(filePath)
						break
					}
				}
			}
		}
	}

	if err != nil {
		if s.files != nil && s.store != nil {
			if job, _ := s.store.GetFileJob(mediaID); job != nil && job.Placement.NodeID != "" {
				targetURL := s.files.StreamURL(job)
				if strings.HasPrefix(job.Placement.NodeID, "bucket:") {
					if len(parts) > 1 {
						publicHost := job.Placement.PublicHost
						if publicHost == "" && s.files != nil {
							publicHost = s.files.StreamURL(job)
						}
						// If StreamURL is full URL, trim off the filename to append requested part
						if idx := strings.LastIndex(targetURL, "/"); idx != -1 {
							targetURL = targetURL[:idx] + "/" + strings.Join(parts[1:], "/")
						} else {
							targetURL += "/" + strings.Join(parts[1:], "/")
						}
					}
				} else {
					if targetURL == "" && s.coord != nil {
						// Fallback: even if node status is offline due to a delayed heartbeat,
						// attempt proxy/stream via node's recorded IP if node record exists.
						for _, n := range s.coord.GetNodes() {
							if n.Metrics.NodeID == job.Placement.NodeID {
								targetURL = fmt.Sprintf("%s/stream/%s", AgentBaseURL(n), job.Key)
								break
							}
						}
					}
					// Preserve the opaque playback ticket on worker URL: never leak raw DB key.
					if isTicket && strings.Contains(targetURL, "/stream/"+job.Key) {
						targetURL = strings.Replace(targetURL, "/stream/"+job.Key, "/stream/"+baseKeyOrTicket, 1)
					}
					if hasMP4Suffix && len(parts) == 1 && !strings.HasSuffix(targetURL, ".mp4") {
						targetURL += ".mp4"
					}
					if len(parts) > 1 {
						targetURL += "/" + strings.Join(parts[1:], "/")
					}
				}
				if targetURL != "" {
					if r.URL.RawQuery != "" {
						if strings.Contains(targetURL, "?") {
							targetURL += "&" + r.URL.RawQuery
						} else {
							targetURL += "?" + r.URL.RawQuery
						}
					}
					isPlay := len(parts) == 1 || strings.HasSuffix(cleanSubPath, ".mp4") || strings.HasSuffix(cleanSubPath, ".mkv")
					if isPlay && s.engine != nil && s.deduper != nil {
						clientIP := r.RemoteAddr
						if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
							clientIP = host
						}
						if s.deduper.ShouldCount(clientIP, mediaID, time.Now()) {
							s.engine.RecordHit(mediaID)
						}
					}

					isHTTPS := r.TLS != nil ||
						strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") ||
						r.Header.Get("CF-Ray") != "" ||
						strings.Contains(r.Header.Get("CF-Visitor"), "https")

					// Proxy is only required when HTTPS clients access unencrypted HTTP nodes to prevent mixed-content blocks,
					// or when explicitly requested via ?proxy=1 / ?raw=1. External HTTPS CDN / R2 targets are redirected directly.
					isProxy := (isHTTPS && !strings.HasPrefix(targetURL, "https://")) ||
						r.URL.Query().Get("proxy") == "true" ||
						r.URL.Query().Get("proxy") == "1" ||
						r.URL.Query().Get("raw") == "1" ||
						strings.EqualFold(r.Header.Get("X-Stream-Proxy"), "true")

					nodeCORS := "*"
					if job.Placement.NodeID != "" && s.store != nil {
						if cfg, _ := s.store.GetNodeConfig(job.Placement.NodeID); cfg != nil && cfg.CORSAllowedOrigins != "" {
							nodeCORS = cfg.CORSAllowedOrigins
						}
					}
					allowed := cluster.MatchCORSOrigin(nodeCORS, r.Header.Get("Origin"))
					if allowed != "" {
						w.Header().Set("Access-Control-Allow-Origin", allowed)
						w.Header().Set("Access-Control-Allow-Credentials", "true")
						w.Header().Set("Vary", "Origin")
					}
					w.Header().Set("Access-Control-Allow-Methods", "GET, HEAD, OPTIONS")
					w.Header().Set("Access-Control-Allow-Headers", "Range, Origin, Accept, Content-Type, Authorization")
					w.Header().Set("Access-Control-Expose-Headers", "Content-Length, Content-Range, Accept-Ranges")

					if isProxy {
						s.proxyStream(w, r, targetURL, nodeCORS)
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

	nodeCORS := "*"
	if parts := strings.Split(cleanSubPath, "/"); len(parts) > 0 && s.store != nil {
		if job, _ := s.store.GetFileJob(parts[0]); job != nil && job.Placement.NodeID != "" {
			if cfg, _ := s.store.GetNodeConfig(job.Placement.NodeID); cfg != nil && cfg.CORSAllowedOrigins != "" {
				nodeCORS = cfg.CORSAllowedOrigins
			}
		}
	}
	allowed := cluster.MatchCORSOrigin(nodeCORS, r.Header.Get("Origin"))
	if allowed != "" {
		w.Header().Set("Access-Control-Allow-Origin", allowed)
		w.Header().Set("Access-Control-Allow-Credentials", "true")
		w.Header().Set("Vary", "Origin")
	}
	w.Header().Set("Access-Control-Allow-Methods", "GET, HEAD, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Range, Accept-Encoding, Origin, Content-Type, Authorization")
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

