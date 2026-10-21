package media

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// VideoStreamInfo captures primary video codec and display specifications.
type VideoStreamInfo struct {
	Codec       string  `json:"codec,omitempty"`
	Profile     string  `json:"profile,omitempty"`
	Width       int     `json:"width,omitempty"`
	Height      int     `json:"height,omitempty"`
	FPS         float64 `json:"fps,omitempty"`
	PixFmt      string  `json:"pix_fmt,omitempty"`
	AspectRatio string  `json:"aspect_ratio,omitempty"`
	BitrateBps  int64   `json:"bitrate_bps,omitempty"`
}

// AudioTrackInfo captures embedded or external audio channel details.
type AudioTrackInfo struct {
	Index         int    `json:"index"`
	Codec         string `json:"codec,omitempty"`
	Channels      int    `json:"channels,omitempty"`
	ChannelLayout string `json:"channel_layout,omitempty"`
	SampleRate    int    `json:"sample_rate,omitempty"`
	BitrateBps    int64  `json:"bitrate_bps,omitempty"`
	Language      string `json:"language,omitempty"`
	Title         string `json:"title,omitempty"`
	File          string `json:"file,omitempty"`
}

// SubtitleTrackInfo captures embedded or sidecar subtitle track details.
type SubtitleTrackInfo struct {
	Index    int    `json:"index"`
	Codec    string `json:"codec,omitempty"`
	Language string `json:"language,omitempty"`
	Title    string `json:"title,omitempty"`
	File     string `json:"file,omitempty"`
}

// MediaMetadata represents the comprehensive technical specifications for a stored media asset.
type MediaMetadata struct {
	Key              string              `json:"key"`
	Filename         string              `json:"filename"`
	OriginalFilename string              `json:"original_filename"`
	SizeBytes        int64               `json:"size_bytes"`
	TotalBytes       int64               `json:"total_bytes,omitempty"`
	MIMEType         string              `json:"mime_type"`
	DurationSec      float64             `json:"duration_sec"`
	BitrateBps       int64               `json:"bitrate_bps"`
	Video            *VideoStreamInfo    `json:"video,omitempty"`
	AudioTracks      []AudioTrackInfo    `json:"audio_tracks,omitempty"`
	Subtitles        []SubtitleTrackInfo `json:"subtitles,omitempty"`
	MasterM3U8       string              `json:"master_m3u8,omitempty"`
	Processing       *ProcessingInfo     `json:"processing,omitempty"`
	CreatedAt        time.Time           `json:"created_at"`
}

// ToCMAFPackage provides backwards-compatibility for components expecting legacy CMAFPackage objects.
func (m *MediaMetadata) ToCMAFPackage() *CMAFPackage {
	return &CMAFPackage{
		MediaID:     m.Key,
		TotalBytes:  m.SizeBytes,
		DurationSec: m.DurationSec,
		CreatedAt:   m.CreatedAt,
		InitChunk: &MediaChunk{
			Index:     0,
			Filename:  m.Filename,
			SizeBytes: m.SizeBytes,
			TrackType: "video_mp4",
			Tier:      2,
		},
	}
}

type ffprobeStream struct {
	CodecType          string `json:"codec_type"`
	CodecName          string `json:"codec_name"`
	Profile            string `json:"profile"`
	PixFmt             string `json:"pix_fmt"`
	Width              int    `json:"width"`
	Height             int    `json:"height"`
	RFrameRate         string `json:"r_frame_rate"`
	AvgFrameRate       string `json:"avg_frame_rate"`
	DisplayAspectRatio string `json:"display_aspect_ratio"`
	Channels           int    `json:"channels"`
	ChannelLayout      string `json:"channel_layout"`
	SampleRate         string `json:"sample_rate"`
	BitRate            string `json:"bit_rate"`
	Tags               struct {
		Language string `json:"language"`
		Title    string `json:"title"`
	} `json:"tags"`
}

type ffprobeFormat struct {
	Duration string `json:"duration"`
	BitRate  string `json:"bit_rate"`
}

type ffprobeResult struct {
	Streams []ffprobeStream `json:"streams"`
	Format  ffprobeFormat   `json:"format"`
}

// ExtractMetadata probes a media file using ffprobe (if present) or basic file stats.
func ExtractMetadata(filePath, key, originalFilename string) (*MediaMetadata, error) {
	stat, err := os.Stat(filePath)
	if err != nil {
		return nil, err
	}

	meta := &MediaMetadata{
		Key:              key,
		Filename:         filepath.Base(filePath),
		OriginalFilename: originalFilename,
		SizeBytes:        stat.Size(),
		MIMEType:         "video/mp4",
		CreatedAt:        time.Now().UTC(),
		AudioTracks:      []AudioTrackInfo{},
		Subtitles:        []SubtitleTrackInfo{},
	}

	ffprobePath, err := exec.LookPath("ffprobe")
	if err == nil && ffprobePath != "" {
		if err := enrichViaFFprobe(ffprobePath, filePath, meta); err == nil {
			return meta, nil
		}
	}

	if meta.BitrateBps == 0 && meta.DurationSec > 0 {
		meta.BitrateBps = int64(float64(meta.SizeBytes*8) / meta.DurationSec)
	}
	return meta, nil
}

func enrichViaFFprobe(ffprobePath, filePath string, meta *MediaMetadata) error {
	cmd := exec.Command(ffprobePath,
		"-v", "quiet",
		"-print_format", "json",
		"-show_format",
		"-show_streams",
		filePath,
	)
	out, err := cmd.Output()
	if err != nil {
		return err
	}

	var res ffprobeResult
	if err := json.Unmarshal(out, &res); err != nil {
		return err
	}

	if res.Format.Duration != "" {
		meta.DurationSec, _ = strconv.ParseFloat(res.Format.Duration, 64)
	}
	if res.Format.BitRate != "" {
		meta.BitrateBps, _ = strconv.ParseInt(res.Format.BitRate, 10, 64)
	}

	for i, s := range res.Streams {
		switch s.CodecType {
		case "video":
			if meta.Video == nil {
				fps := parseFPS(s.RFrameRate)
				if fps <= 0 {
					fps = parseFPS(s.AvgFrameRate)
				}
				br, _ := strconv.ParseInt(s.BitRate, 10, 64)
				meta.Video = &VideoStreamInfo{
					Codec:       s.CodecName,
					Profile:     s.Profile,
					PixFmt:      s.PixFmt,
					Width:       s.Width,
					Height:      s.Height,
					FPS:         fps,
					AspectRatio: s.DisplayAspectRatio,
					BitrateBps:  br,
				}
			}
		case "audio":
			sr, _ := strconv.Atoi(s.SampleRate)
			br, _ := strconv.ParseInt(s.BitRate, 10, 64)
			meta.AudioTracks = append(meta.AudioTracks, AudioTrackInfo{
				Index:         i,
				Codec:         s.CodecName,
				Channels:      s.Channels,
				ChannelLayout: s.ChannelLayout,
				SampleRate:    sr,
				BitrateBps:    br,
				Language:      s.Tags.Language,
				Title:         s.Tags.Title,
			})
		case "subtitle":
			meta.Subtitles = append(meta.Subtitles, SubtitleTrackInfo{
				Index:    i,
				Codec:    s.CodecName,
				Language: s.Tags.Language,
				Title:    s.Tags.Title,
			})
		}
	}

	return nil
}

func parseFPS(val string) float64 {
	parts := strings.Split(val, "/")
	if len(parts) == 1 {
		fps, _ := strconv.ParseFloat(parts[0], 64)
		return fps
	}
	if len(parts) == 2 {
		num, err1 := strconv.ParseFloat(parts[0], 64)
		den, err2 := strconv.ParseFloat(parts[1], 64)
		if err1 == nil && err2 == nil && den > 0 {
			return num / den
		}
	}
	return 0
}

func isWebAudioCodec(codec string) bool {
	c := strings.ToLower(strings.TrimSpace(codec))
	return c == "aac" || c == "mp4a" || c == "mp3"
}

func isTextSubtitle(codec string) bool {
	c := strings.ToLower(strings.TrimSpace(codec))
	return c == "subrip" || c == "srt" || c == "ass" || c == "ssa" || c == "webvtt" || c == "vtt" || c == "mov_text" || c == "text"
}

// RemuxToStreamableMP4 produces a single-file byte-range fMP4 HLS package (master.m3u8):
//   - Video: stream_0.m4s & stream_0.m3u8 (copied when browser-decodable, else re-encoded).
//   - Audio: stream_{lang}.m4s & stream_{lang}.m3u8 (AAC copied or normalized).
//   - Subtitles: subtitle_{lang}.vtt extracted as sidecars.
func RemuxToStreamableMP4(srcPath, dstPath, mediaID, originalFilename string) (*MediaMetadata, error) {
	dstDir := filepath.Dir(dstPath)
	ffmpegPath, err := exec.LookPath("ffmpeg")
	if err != nil || ffmpegPath == "" {
		return nil, fmt.Errorf("ffmpeg not found: required to produce streamable package")
	}
	ffprobePath, _ := exec.LookPath("ffprobe")

	srcMeta := &MediaMetadata{}
	probed := false
	if ffprobePath != "" {
		probed = enrichViaFFprobe(ffprobePath, srcPath, srcMeta) == nil
	}

	masterM3U8Path := filepath.Join(dstDir, "master.m3u8")
	cleanup := func() {
		_ = os.Remove(masterM3U8Path)
		_ = os.Remove(dstPath)
		entries, _ := os.ReadDir(dstDir)
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), "stream_") || strings.HasPrefix(e.Name(), "subtitle_") {
				_ = os.Remove(filepath.Join(dstDir, e.Name()))
			}
		}
	}

	plan := PlanVideo(srcMeta.Video, probed)
	forceAAC := false
	out, runErr := runRemux(ffmpegPath, srcPath, dstDir, plan, srcMeta.AudioTracks, probed, forceAAC)
	if runErr != nil && plan.Mode == VideoCopy {
		cleanup()
		plan = TranscodePlan("copy pass failed: " + lastLine(out))
		forceAAC = true
		out, runErr = runRemux(ffmpegPath, srcPath, dstDir, plan, srcMeta.AudioTracks, probed, forceAAC)
	}
	if runErr != nil {
		cleanup()
		return nil, fmt.Errorf("ffmpeg %s failed: %v: %s", plan.Mode, runErr, lastLines(out, 6))
	}

	// The primary video stream is stream_0.m4s
	primaryVideoPath := filepath.Join(dstDir, "stream_0.m4s")
	if _, err := os.Stat(primaryVideoPath); err != nil {
		// Fallback check if dstPath exists
		if _, dstErr := os.Stat(dstPath); dstErr == nil {
			primaryVideoPath = dstPath
		}
	}

	meta, err := ExtractMetadata(primaryVideoPath, mediaID, originalFilename)
	if err != nil {
		cleanup()
		return nil, err
	}
	meta.MasterM3U8 = "master.m3u8"
	meta.Filename = "master.m3u8"
	meta.MIMEType = "application/vnd.apple.mpegurl"

	meta.Processing = &ProcessingInfo{
		VideoMode:   plan.Mode,
		VideoReason: plan.Reason,
		AudioMode:   audioMode(srcMeta.AudioTracks, forceAAC),
	}

	// Register audio tracks with their stream_{lang}.m3u8 / .m4s names
	meta.AudioTracks = make([]AudioTrackInfo, 0, len(srcMeta.AudioTracks))
	for idx, aTrack := range srcMeta.AudioTracks {
		lang := sanitizeStreamLang(idx, aTrack.Language)
		m3u8Name := fmt.Sprintf("stream_%s.m3u8", lang)
		m4sName := fmt.Sprintf("stream_%s.m4s", lang)
		if stat, statErr := os.Stat(filepath.Join(dstDir, m4sName)); statErr == nil && stat.Size() > 0 {
			aTrack.File = m3u8Name
			if forceAAC || !isWebAudioCodec(aTrack.Codec) {
				aTrack.Codec = "aac"
			}
			meta.AudioTracks = append(meta.AudioTracks, aTrack)
		}
	}

	// Subtitles: extract all text-based subtitles to WebVTT (.vtt) in a single ffmpeg pass
	if ffmpegPath != "" && len(srcMeta.Subtitles) > 0 {
		type subTarget struct {
			track    SubtitleTrackInfo
			filename string
			path     string
		}
		var targets []subTarget
		sArgs := []string{
			"-y",
			"-err_detect", "ignore_err",
			"-fflags", "+genpts+discardcorrupt",
			"-i", srcPath,
		}
		for idx, sTrack := range srcMeta.Subtitles {
			if !isTextSubtitle(sTrack.Codec) {
				continue
			}
			lang := sTrack.Language
			if lang == "" {
				lang = fmt.Sprintf("sub_%d", idx+1)
			}
			vttFilename := fmt.Sprintf("subtitle_%d_%s.vtt", idx+1, lang)
			vttOutPath := filepath.Join(dstDir, vttFilename)
			targets = append(targets, subTarget{
				track:    sTrack,
				filename: vttFilename,
				path:     vttOutPath,
			})
			sArgs = append(sArgs, "-map", fmt.Sprintf("0:s:%d", idx), vttOutPath)
		}

		if len(targets) > 0 {
			sCmd := exec.Command(ffmpegPath, sArgs...)
			if _, sErr := sCmd.CombinedOutput(); sErr == nil {
				for _, target := range targets {
					if stat, statErr := os.Stat(target.path); statErr == nil && stat.Size() > 0 {
						t := target.track
						t.File = target.filename
						meta.Subtitles = append(meta.Subtitles, t)
					}
				}
			}
		}
	}

	if totalBytes, err := CalculateFolderSize(dstDir); err == nil && totalBytes > 0 {
		meta.TotalBytes = totalBytes
		meta.SizeBytes = totalBytes
	}
	return meta, nil
}

// sanitizeStreamLang sanitizes the language tag for stream naming (e.g. hin, tam, eng).
func sanitizeStreamLang(idx int, lang string) string {
	l := strings.ToLower(strings.TrimSpace(lang))
	if l == "" || l == "und" {
		return fmt.Sprintf("audio_%d", idx+1)
	}
	var clean strings.Builder
	for _, r := range l {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			clean.WriteRune(r)
		}
	}
	res := clean.String()
	if res == "" {
		return fmt.Sprintf("audio_%d", idx+1)
	}
	return res
}

// runRemux executes one ffmpeg pass producing single-file fMP4 byte-range HLS:
// stream_0 (video), stream_{lang} (audio tracks), master.m3u8.
func runRemux(ffmpegPath, srcPath, dstDir string, plan VideoPlan, tracks []AudioTrackInfo,
	probed, forceAAC bool) ([]byte, error) {
	audioFor := func(codec string) []string {
		if forceAAC {
			return []string{"-c:a", "aac", "-b:a", "192k"}
		}
		return AudioArgs(codec)
	}

	args := []string{
		"-y",
		"-err_detect", "ignore_err",
		"-fflags", "+genpts+discardcorrupt",
		"-i", srcPath,
	}

	varMapParts := []string{}

	if plan.Mode != VideoNone {
		args = append(args, "-map", "0:v:0?")
		args = append(args, plan.Args...)
		varMapParts = append(varMapParts, "v:0,agroup:audio")
	}

	if len(tracks) > 0 {
		seenLangCount := make(map[string]int)
		for idx, t := range tracks {
			args = append(args, "-map", fmt.Sprintf("0:a:%d", idx))
			args = append(args, audioFor(t.Codec)...)

			baseLang := sanitizeStreamLang(idx, t.Language)
			seenLangCount[baseLang]++
			streamName := baseLang
			if seenLangCount[baseLang] > 1 {
				streamName = fmt.Sprintf("%s_%d", baseLang, seenLangCount[baseLang])
			}

			defStr := "no"
			if idx == 0 {
				defStr = "yes"
			}
			varMapParts = append(varMapParts, fmt.Sprintf("a:%d,agroup:audio,language:%s,name:%s,default:%s", idx, baseLang, streamName, defStr))
		}
	} else if !probed {
		args = append(args, "-map", "0:a:0?")
		args = append(args, "-c:a", "aac", "-b:a", "192k")
		varMapParts = append(varMapParts, "a:0,agroup:audio,language:und,name:audio_1,default:yes")
	}

	args = append(args,
		"-sn", "-dn",
		"-f", "hls",
		"-hls_time", "4",
		"-hls_playlist_type", "vod",
		"-hls_flags", "single_file",
		"-hls_segment_type", "fmp4",
		"-var_stream_map", strings.Join(varMapParts, " "),
		"-master_pl_name", "master.m3u8",
		filepath.Join(dstDir, "stream_%v.m3u8"),
	)

	out, err := exec.Command(ffmpegPath, args...).CombinedOutput()
	if err != nil {
		return out, err
	}

	// Safety check: ensure master.m3u8 explicitly includes the video playlist reference
	masterPath := filepath.Join(dstDir, "master.m3u8")
	if plan.Mode != VideoNone {
		if content, rErr := os.ReadFile(masterPath); rErr == nil {
			strContent := string(content)
			if !strings.Contains(strContent, "stream_0.m3u8") {
				// Inject the video stream entry if ffmpeg omitted it
				var streamInf strings.Builder
				streamInf.WriteString("\n#EXT-X-STREAM-INF:BANDWIDTH=1500000")
				if len(tracks) > 0 {
					streamInf.WriteString(`,AUDIO="group_audio"`)
				}
				streamInf.WriteString("\nstream_0.m3u8\n")
				_ = os.WriteFile(masterPath, []byte(strContent+streamInf.String()), 0644)
			}
		}
	}

	return out, nil
}

// lastLines returns the tail of ffmpeg output, where the actual error lives.
func lastLines(out []byte, n int) string {
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, " | ")
}

func lastLine(out []byte) string {
	return lastLines(out, 1)
}
