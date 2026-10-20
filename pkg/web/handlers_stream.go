package web

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
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

func (s *Server) handleStreamManifest(w http.ResponseWriter, r *http.Request) {
	subPath := strings.TrimPrefix(r.URL.Path, "/stream/")
	cleanSubPath := filepath.Clean(subPath)
	if strings.Contains(cleanSubPath, "..") || cleanSubPath == "." || cleanSubPath == "" {
		http.NotFound(w, r)
		return
	}

	parts := strings.Split(cleanSubPath, string(filepath.Separator))
	rawKeyOrTicket := parts[0]
	mediaID := parts[0]
	isTicket := strings.HasPrefix(rawKeyOrTicket, "t_")

	secret := adminSecret(s)

	// Ticket or Token verification (Fail Closed)
	if !s.isCallerAuthenticated(r) {
		if isTicket {
			resolvedKey, _, err := cluster.ResolvePlaybackTicket(rawKeyOrTicket, secret)
			if err != nil {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusForbidden)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": "forbidden: invalid or expired stream ticket"})
				return
			}
			parts[0] = resolvedKey
			cleanSubPath = filepath.Join(parts...)
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
			if !fileapi.ValidateStreamToken(mediaID, token, expStr, secret) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusForbidden)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": "forbidden: invalid or expired stream token"})
				return
			}
		}
	} else if isTicket {
		if resolvedKey, _, err := cluster.ResolvePlaybackTicket(rawKeyOrTicket, secret); err == nil {
			parts[0] = resolvedKey
			cleanSubPath = filepath.Join(parts...)
			mediaID = resolvedKey
		}
	}

	// Embed Player: When a single file or ticket is requested by a browser or iframe
	// without ?raw=1 and without Range header, render the standalone ArtPlayer embed page.
	wantsHTML := len(parts) == 1 &&
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
								targetURL = fmt.Sprintf("%s/stream/%s", fileapi.AgentBaseURL(n), job.Key)
								break
							}
						}
					}
					// Preserve the opaque playback ticket on worker URL: never leak raw DB key.
					if isTicket && strings.Contains(targetURL, "/stream/"+job.Key) {
						targetURL = strings.Replace(targetURL, "/stream/"+job.Key, "/stream/"+rawKeyOrTicket, 1)
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

	sec := adminSecret(s)
	proxy.Director = func(req *http.Request) {
		req.URL.Scheme = parsedURL.Scheme
		req.URL.Host = parsedURL.Host
		req.URL.Path = parsedURL.Path
		req.URL.RawQuery = parsedURL.RawQuery
		req.Host = parsedURL.Host
		if sec != "" {
			req.Header.Set("X-Cluster-Secret", sec)
		}
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

type embedTrackInfo struct {
	Index    int    `json:"index"`
	Language string `json:"language"`
	Title    string `json:"title"`
	Codec    string `json:"codec,omitempty"`
	Channels int    `json:"channels,omitempty"`
	URL      string `json:"url"`
}

var isoLangNames = map[string]string{
	"hin": "Hindi", "hi": "Hindi",
	"ben": "Bengali", "bn": "Bengali",
	"eng": "English", "en": "English",
	"spa": "Spanish", "es": "Spanish",
	"fra": "French", "fre": "French", "fr": "French",
	"ger": "German", "deu": "German", "de": "German",
	"ita": "Italian", "it": "Italian",
	"jpn": "Japanese", "ja": "Japanese",
	"kor": "Korean", "ko": "Korean",
	"chi": "Chinese", "zho": "Chinese", "zh": "Chinese",
	"rus": "Russian", "ru": "Russian",
	"ara": "Arabic", "ar": "Arabic",
	"tam": "Tamil", "ta": "Tamil",
	"tel": "Telugu", "te": "Telugu",
	"mal": "Malayalam", "ml": "Malayalam",
	"kan": "Kannada", "kn": "Kannada",
	"mar": "Marathi", "mr": "Marathi",
	"pan": "Punjabi", "pa": "Punjabi",
	"guj": "Gujarati", "gu": "Gujarati",
	"urd": "Urdu", "ur": "Urdu",
	"por": "Portuguese", "pt": "Portuguese",
	"tur": "Turkish", "tr": "Turkish",
	"pol": "Polish", "pl": "Polish",
	"nld": "Dutch", "nl": "Dutch",
	"swe": "Swedish", "sv": "Swedish",
	"tha": "Thai", "th": "Thai",
	"vie": "Vietnamese", "vi": "Vietnamese",
	"ind": "Indonesian", "id": "Indonesian",
}

func formatCleanTrackTitle(rawTitle, lang, fallback string) string {
	raw := strings.TrimSpace(rawTitle)
	langCode := strings.ToLower(strings.TrimSpace(lang))
	langName := isoLangNames[langCode]
	if langName == "" && len(langCode) > 0 {
		langName = strings.ToUpper(langCode)
	}

	isSpammy := raw == ""
	if !isSpammy {
		lower := strings.ToLower(raw)
		spamWords := []string{"visit", "download", "fmxbd", "fmovies", "1337x", "yts", "psa", "galaxytv"}
		for _, w := range spamWords {
			if strings.Contains(lower, w) {
				isSpammy = true
				break
			}
		}
		if !isSpammy && (strings.HasPrefix(lower, "sub") || strings.Contains(lower, "subrip")) {
			isSpammy = true
		}
	}

	if !isSpammy {
		return raw
	}
	if langName != "" {
		return langName
	}
	return fallback
}

var embedPlayerTmpl = template.Must(template.New("embedPlayer").Parse(embedPlayerHTML))

func (s *Server) renderStreamEmbed(w http.ResponseWriter, r *http.Request, job *fileapi.FileJob, keyOrTicket string) {
	title := job.Key
	if s.store != nil {
		if rec, _ := s.store.GetFileRecord(job.Key); rec != nil && rec.Filename != "" {
			title = rec.Filename
		}
	}
	if len(job.MetadataJSON) > 0 {
		var m struct {
			Filename string `json:"filename"`
		}
		if json.Unmarshal(job.MetadataJSON, &m) == nil && m.Filename != "" {
			title = m.Filename
		}
	}

	streamBase := fmt.Sprintf("/stream/%s", keyOrTicket)
	videoURL := fmt.Sprintf("%s?raw=1", streamBase)

	var subs []embedTrackInfo
	var audios []embedTrackInfo

	if len(job.MetadataJSON) > 0 {
		var meta struct {
			DurationSec float64 `json:"duration_sec"`
			AudioTracks []struct {
				Index    int    `json:"index"`
				Language string `json:"language"`
				Title    string `json:"title"`
				Codec    string `json:"codec"`
				Channels int    `json:"channels"`
				File     string `json:"file"`
			} `json:"audio_tracks"`
			Subtitles []struct {
				Index    int    `json:"index"`
				Language string `json:"language"`
				Title    string `json:"title"`
				Codec    string `json:"codec"`
				File     string `json:"file"`
			} `json:"subtitles"`
		}
		if err := json.Unmarshal(job.MetadataJSON, &meta); err == nil {
			for _, sub := range meta.Subtitles {
				file := sub.File
				if file == "" {
					file = fmt.Sprintf("subtitle_%d_%s.vtt", sub.Index, sub.Language)
				}
				subTitle := formatCleanTrackTitle(sub.Title, sub.Language, fmt.Sprintf("Subtitle %d", sub.Index))
				subs = append(subs, embedTrackInfo{
					Index:    sub.Index,
					Language: sub.Language,
					Title:    subTitle,
					Codec:    sub.Codec,
					URL:      fmt.Sprintf("%s/%s?raw=1", streamBase, file),
				})
			}

			for _, a := range meta.AudioTracks {
				file := a.File
				if file == "" {
					file = fmt.Sprintf("audio_%d_%s.m4a", a.Index, a.Language)
				}
				audioTitle := formatCleanTrackTitle(a.Title, a.Language, fmt.Sprintf("Track %d", a.Index))
				audios = append(audios, embedTrackInfo{
					Index:    a.Index,
					Language: a.Language,
					Title:    audioTitle,
					Codec:    a.Codec,
					Channels: a.Channels,
					URL:      fmt.Sprintf("%s/%s?raw=1", streamBase, file),
				})
			}
		}
	}

	subsJSON, _ := json.Marshal(subs)
	audiosJSON, _ := json.Marshal(audios)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("X-Frame-Options", "ALLOWALL")

	data := map[string]interface{}{
		"Title":           title,
		"StreamBase":      streamBase,
		"VideoURL":        videoURL,
		"SubtitlesJSON":   template.JS(string(subsJSON)),
		"AudioTracksJSON": template.JS(string(audiosJSON)),
	}

	_ = embedPlayerTmpl.Execute(w, data)
}

const embedPlayerHTML = `<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0, maximum-scale=1.0, user-scalable=no">
    <title>{{.Title}}</title>
    <style>
        * { box-sizing: border-box; }
        html, body {
            margin: 0;
            padding: 0;
            width: 100%;
            height: 100%;
            overflow: hidden;
            background: #000;
            color: #fff;
            font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, Helvetica, Arial, sans-serif;
        }
        #artplayer {
            width: 100%;
            height: 100%;
            position: absolute;
            inset: 0;
        }
    </style>
    <script src="/static/js/vendor/artplayer.js?v=2"></script>
    <script>if (typeof Artplayer === 'undefined') { document.write('<script src="https://cdn.jsdelivr.net/npm/artplayer@5.2.3/dist/artplayer.js"><\/script>'); }</script>
    <script src="/static/js/vendor/hls.min.js?v=2"></script>
    <script>if (typeof Hls === 'undefined') { document.write('<script src="https://cdn.jsdelivr.net/npm/hls.js@1.5.8/dist/hls.min.js"><\/script>'); }</script>
</head>
<body>
    <div id="artplayer"></div>
    <script>
    (function () {
        const TITLE = {{.Title}};
        const VIDEO_URL = {{.VideoURL}};
        const SUBTITLES = {{.SubtitlesJSON}};
        const AUDIO_TRACKS = {{.AudioTracksJSON}};

        const customSettings = [];

        // 1. Multi-Audio Selector
        if (AUDIO_TRACKS && AUDIO_TRACKS.length > 1) {
            var seenAudioCount = {};
            var audioItems = AUDIO_TRACKS.map(function (t, idx) {
                var title = t.title || ('Track ' + (idx + 1));
                seenAudioCount[title] = (seenAudioCount[title] || 0) + 1;
                if (seenAudioCount[title] > 1) {
                    title = title + ' #' + seenAudioCount[title];
                }
                var metaDetail = (t.codec || 'aac') + (t.channels ? (', ' + t.channels + 'ch') : '');
                return {
                    default: idx === 0,
                    html: '<span style="display:inline-flex;align-items:center;gap:6px;">🎵 ' + title + ' <span style="font-size:10px;opacity:0.6;">(' + metaDetail + ')</span></span>',
                    url: t.url,
                    index: idx,
                    displayTitle: title
                };
            });

            customSettings.push({
                icon: '<svg width="20" height="20" viewBox="0 0 24 24"><path fill="#fff" d="M12 3v10.55c-.59-.34-1.27-.55-2-.55-2.21 0-4 1.79-4 4s1.79 4 4 4 4-1.79 4-4V7h4V3h-6z"/></svg>',
                width: 240,
                html: 'Audio Track',
                name: 'audio_track',
                tooltip: audioItems[0] ? audioItems[0].displayTitle : 'Default',
                selector: audioItems,
                onSelect: function (item) {
                    if (window.artAudioCtrl) {
                        window.artAudioCtrl.switchAudio(item.url);
                    }
                    return item.displayTitle;
                }
            });
        }

        // 2. Subtitles Selector
        var defaultSubtitle = undefined;
        if (SUBTITLES && SUBTITLES.length > 0) {
            var firstSub = SUBTITLES[0];
            defaultSubtitle = {
                url: firstSub.url,
                type: 'vtt',
                style: {
                    color: '#ffffff',
                    fontSize: '20px',
                    textShadow: '0 2px 4px rgba(0,0,0,0.85)',
                    fontWeight: '600'
                },
                encoding: 'utf-8'
            };

            var seenSubCount = {};
            var subItems = SUBTITLES.map(function (s, idx) {
                var title = s.title || ('Subtitle ' + (idx + 1));
                seenSubCount[title] = (seenSubCount[title] || 0) + 1;
                if (seenSubCount[title] > 1) {
                    title = title + ' #' + seenSubCount[title];
                }
                return {
                    default: idx === 0,
                    html: '<span style="display:inline-flex;align-items:center;gap:6px;">💬 ' + title + ' <span style="font-size:10px;opacity:0.6;">(VTT)</span></span>',
                    url: s.url,
                    displayTitle: title
                };
            });
            subItems.push({
                default: false,
                html: '<span>❌ Turn Off Subtitles</span>',
                url: '',
                displayTitle: 'Off'
            });

            customSettings.push({
                icon: '<svg width="20" height="20" viewBox="0 0 24 24"><path fill="#fff" d="M20 4H4c-1.1 0-2 .9-2 2v12c0 1.1.9 2 2 2h16c1.1 0 2-.9 2-2V6c0-1.1-.9-2-2-2m-9 5H5V7h6zm8 8H5v-2h14zm-4-4H5v-2h10zm4 0h-2v-2h2z"/></svg>',
                width: 240,
                html: 'Subtitles',
                name: 'subtitles',
                tooltip: subItems[0] ? subItems[0].displayTitle : 'Default',
                selector: subItems,
                onSelect: function (item) {
                    if (window.art && window.art.subtitle) {
                        if (item.url) {
                            window.art.subtitle.show = true;
                            window.art.subtitle.switch(item.url, { name: item.displayTitle });
                        } else {
                            window.art.subtitle.show = false;
                        }
                    }
                    return item.displayTitle;
                }
            });
        }

        var isMobile = /iPhone|iPad|iPod|Android/i.test(navigator.userAgent);
        var artOptions = {
            container: '#artplayer',
            url: VIDEO_URL,
            title: TITLE,
            type: VIDEO_URL.indexOf('.m3u8') !== -1 ? 'm3u8' : 'mp4',
            volume: 0.8,
            isLive: false,
            muted: false,
            autoplay: !isMobile,
            pip: false,
            autoSize: false,
            autoMini: false,
            screenshot: false,
            setting: true,
            loop: false,
            flip: true,
            playbackRate: true,
            aspectRatio: true,
            fullscreen: true,
            fullscreenWeb: false,
            subtitleOffset: true,
            miniProgressBar: true,
            theme: '#ff7a18',
            playsInline: true,
            autoPlayback: true,
            airplay: true,
            hotkey: true,
            fastForward: true,
            lock: true,
            moreVideoAttr: {
                'playsinline': 'true',
                'webkit-playsinline': 'true'
            },
            settings: customSettings,
            customType: {
                m3u8: function (video, url, art) {
                    if (window.Hls && Hls.isSupported()) {
                        if (art.hls) art.hls.destroy();
                        var hls = new Hls({ enableWorker: true, backBufferLength: 90 });
                        hls.loadSource(url);
                        hls.attachMedia(video);
                        art.hls = hls;
                        art.on('destroy', function () { hls.destroy(); });
                    } else if (video.canPlayType('application/vnd.apple.mpegurl')) {
                        video.src = url;
                    }
                }
            },
            contextmenu: [
                {
                    html: 'Video Info / Stats',
                    click: function (c) {
                        var dur = window.art ? Math.round(window.art.duration) : 0;
                        if (window.art) window.art.notice.show = TITLE + ' (' + dur + 's)';
                        c.show = false;
                    }
                },
                {
                    html: 'Copy Embed Code',
                    click: function (c) {
                        var embedCode = '<iframe src="' + window.location.href + '" width="100%" height="100%" frameborder="0" allowfullscreen><\/iframe>';
                        navigator.clipboard.writeText(embedCode).then(function () {
                            if (window.art) window.art.notice.show = 'Embed code copied to clipboard!';
                        }).catch(function () {});
                        c.show = false;
                    }
                },
                {
                    html: 'Copy Stream Link',
                    click: function (c) {
                        navigator.clipboard.writeText(window.location.href).then(function () {
                            if (window.art) window.art.notice.show = 'Stream link copied to clipboard!';
                        }).catch(function () {});
                        c.show = false;
                    }
                }
            ]
        };

        if (defaultSubtitle) {
            artOptions.subtitle = defaultSubtitle;
        }

        var ArtClass = window.Artplayer || window.ArtPlayer;
        if (!ArtClass) {
            document.getElementById('artplayer').innerHTML = '<div style="display:flex;align-items:center;justify-content:center;height:100%;color:#fff;"><video src="' + VIDEO_URL + '" controls style="max-width:100%;max-height:100%;"></video></div>';
            return;
        }

        var art = new ArtClass(artOptions);
        window.art = art;

        function LockstepAudio(artInstance) {
            this.art = artInstance;
            this.video = artInstance.video;
            this.audio = null;
            this.syncTimer = null;
            this.userPaused = this.video.paused;
            this.internalPauseExpected = 0;
            this.switchSeq = 0;
            this.isSwitchingTrack = false;
            this.bindVideoEvents();
        }

        LockstepAudio.prototype.bindVideoEvents = function () {
            var self = this;
            var v = this.video;

            var playAudio = function () {
                self.userPaused = false;
                self.internalPauseExpected = 0;
                if (!self.audio || !self.audio.src || self.isSwitchingTrack) return;

                var startA = function () {
                    if (!self.audio || v.paused || self.userPaused || self.isSwitchingTrack) return;
                    if (Math.abs(v.currentTime - self.audio.currentTime) > 0.03) {
                        self.audio.currentTime = v.currentTime;
                    }
                    self.audio.playbackRate = v.playbackRate;
                    self.audio.play().catch(function () {});
                };

                if (self.audio.readyState >= 2) {
                    startA();
                } else {
                    self.audio.addEventListener('canplay', startA, { once: true });
                }
            };

            v.addEventListener('play', playAudio);
            v.addEventListener('playing', playAudio);

            v.addEventListener('pause', function () {
                if (self.internalPauseExpected > 0) {
                    self.internalPauseExpected--;
                    if (self.audio) self.audio.pause();
                    return;
                }
                self.userPaused = true;
                if (self.audio) self.audio.pause();
            });

            v.addEventListener('seeking', function () {
                if (self.audio) {
                    self.audio.pause();
                    self.audio.currentTime = v.currentTime;
                }
            });

            v.addEventListener('seeked', function () {
                if (self.audio) self.audio.currentTime = v.currentTime;
            });

            v.addEventListener('ratechange', function () {
                if (self.audio) self.audio.playbackRate = v.playbackRate;
            });

            v.addEventListener('volumechange', function () {
                if (self.audio) {
                    self.audio.volume = v.volume;
                    self.audio.muted = v.muted;
                }
            });
        };

        LockstepAudio.prototype.switchAudio = function (audioUrl) {
            this.switchSeq = (this.switchSeq || 0) + 1;
            var seq = this.switchSeq;
            this.isSwitchingTrack = true;

            if (this.syncTimer) {
                clearInterval(this.syncTimer);
                this.syncTimer = null;
            }

            if (this.audio) {
                this.audio.pause();
                this.audio.removeAttribute('src');
                this.audio.load();
                this.audio = null;
            }

            if (!audioUrl) {
                this.isSwitchingTrack = false;
                this.video.muted = false;
                return;
            }

            var v = this.video;
            var wasPlaying = !v.paused && !this.userPaused;

            if (!v.paused) {
                this.internalPauseExpected = (this.internalPauseExpected || 0) + 1;
                v.pause();
            }

            v.muted = true;
            var a = new Audio();
            this.audio = a;
            a.crossOrigin = 'anonymous';
            a.preload = 'auto';
            a.volume = v.volume;
            a.muted = v.muted;

            var self = this;

            var resumePlayback = function () {
                if (self.switchSeq !== seq || !self.audio) return;
                self.isSwitchingTrack = false;
                if (wasPlaying && !self.userPaused) {
                    v.play().catch(function () {});
                }
            };

            var onMetadataOrCanPlay = function () {
                a.removeEventListener('loadedmetadata', onMetadataOrCanPlay);
                a.removeEventListener('canplay', onMetadataOrCanPlay);
                if (self.switchSeq !== seq || !self.audio) return;

                var targetTime = v.currentTime;
                if (targetTime < 0.05) {
                    if (a.readyState >= 3) {
                        resumePlayback();
                    } else {
                        a.addEventListener('canplay', resumePlayback, { once: true });
                    }
                    return;
                }

                var onSeeked = function () {
                    a.removeEventListener('seeked', onSeeked);
                    if (self.switchSeq !== seq || !self.audio) return;
                    if (a.readyState >= 3) {
                        resumePlayback();
                    } else {
                        a.addEventListener('canplay', resumePlayback, { once: true });
                    }
                };

                a.addEventListener('seeked', onSeeked, { once: true });
                a.currentTime = targetTime;
            };

            a.addEventListener('loadedmetadata', onMetadataOrCanPlay, { once: true });
            a.addEventListener('canplay', onMetadataOrCanPlay, { once: true });

            a.addEventListener('waiting', function () {
                if (v.paused || self.userPaused || self.isSwitchingTrack) return;
                self.internalPauseExpected = (self.internalPauseExpected || 0) + 1;
                v.pause();
                var onAudioResume = function () {
                    a.removeEventListener('canplay', onAudioResume);
                    if (!self.userPaused && !self.isSwitchingTrack) {
                        v.play().catch(function () {});
                    }
                };
                a.addEventListener('canplay', onAudioResume, { once: true });
            });

            this.syncTimer = setInterval(function () {
                if (self.audio && !v.paused && !self.userPaused && !self.isSwitchingTrack && !self.audio.paused) {
                    var diff = v.currentTime - self.audio.currentTime;
                    var absDiff = Math.abs(diff);
                    if (absDiff > 0.3) {
                        self.audio.currentTime = v.currentTime;
                        self.audio.playbackRate = v.playbackRate;
                    } else if (diff > 0.03) {
                        self.audio.playbackRate = v.playbackRate * 1.03;
                    } else if (diff < -0.03) {
                        self.audio.playbackRate = v.playbackRate * 0.97;
                    } else {
                        if (self.audio.playbackRate !== v.playbackRate) {
                            self.audio.playbackRate = v.playbackRate;
                        }
                    }
                }
            }, 200);

            a.src = audioUrl;
            a.load();
        };

        window.artAudioCtrl = new LockstepAudio(art);
        if (AUDIO_TRACKS && AUDIO_TRACKS.length > 0) {
            window.artAudioCtrl.switchAudio(AUDIO_TRACKS[0].url);
        }
    })();
    </script>
</body>
</html>`

