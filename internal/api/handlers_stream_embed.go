package api

import (
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"strings"

	"stream/internal/model"
)

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

func (s *Server) renderStreamEmbed(w http.ResponseWriter, r *http.Request, job *model.FileJob, keyOrTicket string) {
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
	videoURL := fmt.Sprintf("%s.mp4?raw=1", streamBase)

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
