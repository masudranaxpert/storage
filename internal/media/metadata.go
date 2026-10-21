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

// RemuxToStreamableMP4 produces a browser-playable, fast-start MP4 in one ffmpeg pass:
//   - Video: copied when the codec is browser-decodable (H.264 8-bit, HEVC, AV1, VP9),
//     otherwise re-encoded to H.264 (see PlanVideo). A failed copy pass is retried once
//     as a transcode; there is no fallback that stores an unplayable file.
//   - Audio: AAC/MP3 copied, anything else encoded to AAC. Multiple tracks become
//     standalone .m4a files plus an fMP4 HLS master playlist.
//   - Subtitles: text tracks extracted to WebVTT sidecars; bitmap tracks are skipped.
func RemuxToStreamableMP4(srcPath, dstPath, mediaID, originalFilename string) (*MediaMetadata, error) {
	dstDir := filepath.Dir(dstPath)
	ffmpegPath, err := exec.LookPath("ffmpeg")
	if err != nil || ffmpegPath == "" {
		return nil, fmt.Errorf("ffmpeg not found: required to produce streamable MP4 container")
	}
	ffprobePath, _ := exec.LookPath("ffprobe")

	srcMeta := &MediaMetadata{}
	probed := false
	if ffprobePath != "" {
		probed = enrichViaFFprobe(ffprobePath, srcPath, srcMeta) == nil
	}
	numAudios := len(srcMeta.AudioTracks)
	audioPaths := make([]string, numAudios)
	for idx, aTrack := range srcMeta.AudioTracks {
		audioPaths[idx] = filepath.Join(dstDir, audioTrackFilename(idx, aTrack))
	}
	cleanup := func() {
		_ = os.Remove(dstPath)
		if numAudios > 1 {
			for _, p := range audioPaths {
				_ = os.Remove(p)
			}
		}
	}

	plan := PlanVideo(srcMeta.Video, probed)
	forceAAC := false
	out, runErr := runRemux(ffmpegPath, srcPath, dstPath, plan, srcMeta.AudioTracks, audioPaths, probed, forceAAC)
	if runErr != nil && plan.Mode == VideoCopy {
		// Copy can still fail on broken timestamps or odd bitstreams; one
		// re-encode pass normalises both video and audio.
		cleanup()
		plan = TranscodePlan("copy pass failed: " + lastLine(out))
		forceAAC = true
		out, runErr = runRemux(ffmpegPath, srcPath, dstPath, plan, srcMeta.AudioTracks, audioPaths, probed, forceAAC)
	}
	if runErr != nil {
		cleanup()
		return nil, fmt.Errorf("ffmpeg %s failed: %v: %s", plan.Mode, runErr, lastLines(out, 6))
	}

	meta, err := ExtractMetadata(dstPath, mediaID, originalFilename)
	if err != nil {
		cleanup()
		return nil, err
	}
	// Guard: never hand back an MP4 whose video a browser cannot decode.
	if srcMeta.Video != nil && meta.Video != nil && !isBrowserVideo(meta.Video) {
		cleanup()
		return nil, fmt.Errorf("output video codec %q is not browser-playable", meta.Video.Codec)
	}
	meta.Processing = &ProcessingInfo{
		VideoMode:   plan.Mode,
		VideoReason: plan.Reason,
		AudioMode:   audioMode(srcMeta.AudioTracks, forceAAC),
	}

	// Multi-audio: register extracted external audio tracks
	if numAudios > 1 {
		meta.AudioTracks = make([]AudioTrackInfo, 0, numAudios)
		for idx, aTrack := range srcMeta.AudioTracks {
			audioFilename := audioTrackFilename(idx, aTrack)
			audioOutPath := audioPaths[idx]
			if stat, statErr := os.Stat(audioOutPath); statErr == nil && stat.Size() > 0 {
				aTrack.File = audioFilename
				if forceAAC || !isWebAudioCodec(aTrack.Codec) {
					aTrack.Codec = "aac"
				}
				meta.AudioTracks = append(meta.AudioTracks, aTrack)
			}
		}
	}

	// Subtitles: extract each text-based subtitle to WebVTT (.vtt)
	if ffmpegPath != "" && len(srcMeta.Subtitles) > 0 {
		for idx, sTrack := range srcMeta.Subtitles {
			if !isTextSubtitle(sTrack.Codec) {
				continue // Skip bitmap subtitles (e.g. PGS / VobSub) which cannot be converted to WebVTT without OCR
			}
			lang := sTrack.Language
			if lang == "" {
				lang = fmt.Sprintf("sub_%d", idx+1)
			}
			vttFilename := fmt.Sprintf("subtitle_%d_%s.vtt", idx+1, lang)
			vttOutPath := filepath.Join(dstDir, vttFilename)

			sCmd := exec.Command(ffmpegPath,
				"-y",
				"-err_detect", "ignore_err",
				"-fflags", "+genpts+discardcorrupt",
				"-i", srcPath,
				"-map", fmt.Sprintf("0:s:%d", idx),
				vttOutPath,
			)
			if _, sErr := sCmd.CombinedOutput(); sErr == nil {
				if stat, statErr := os.Stat(vttOutPath); statErr == nil && stat.Size() > 0 {
					sTrack.File = vttFilename
					meta.Subtitles = append(meta.Subtitles, sTrack)
				}
			}
		}
	}

	if totalBytes, err := CalculateFolderSize(dstDir); err == nil && totalBytes > 0 {
		meta.TotalBytes = totalBytes
	} else {
		meta.TotalBytes = meta.SizeBytes
	}
	return meta, nil
}

// runRemux executes one ffmpeg pass. Subtitles and data streams are never
// muxed into the MP4 (-sn -dn); text subtitles are extracted separately.
func runRemux(ffmpegPath, srcPath, dstPath string, plan VideoPlan, tracks []AudioTrackInfo,
	audioPaths []string, probed, forceAAC bool) ([]byte, error) {
	audioFor := func(codec string) []string {
		if forceAAC {
			return []string{"-c:a", "aac", "-b:a", "192k"}
		}
		return AudioArgs(codec)
	}
	mp4Opts := []string{
		"-max_muxing_queue_size", "2048",
		"-avoid_negative_ts", "make_zero",
		"-movflags", "+delay_moov+frag_keyframe+default_base_moof+global_sidx",
		"-min_frag_duration", "2000000",
	}

	args := []string{
		"-y",
		"-err_detect", "ignore_err",
		"-fflags", "+genpts+discardcorrupt",
		"-i", srcPath,
	}
	if plan.Mode != VideoNone {
		args = append(args, "-map", "0:v:0?")
		args = append(args, plan.Args...)
	}
	if len(tracks) > 1 {
		// Multi-audio: video-only MP4, then one .m4a per track in the same pass.
		args = append(args, "-an", "-sn", "-dn")
		args = append(args, mp4Opts...)
		args = append(args, dstPath)
		for idx, t := range tracks {
			args = append(args, "-map", fmt.Sprintf("0:a:%d", idx))
			args = append(args, audioFor(t.Codec)...)
			args = append(args, "-vn", "-sn", "-dn", "-movflags", "+faststart", audioPaths[idx])
		}
	} else {
		args = append(args, "-map", "0:a:0?")
		switch {
		case len(tracks) == 1:
			args = append(args, audioFor(tracks[0].Codec)...)
		case !probed:
			args = append(args, "-c:a", "aac", "-b:a", "192k")
		default:
			args = append(args, "-c:a", "aac", "-b:a", "192k")
		}
		args = append(args, "-sn", "-dn")
		args = append(args, mp4Opts...)
		args = append(args, dstPath)
	}
	return exec.Command(ffmpegPath, args...).CombinedOutput()
}

func audioTrackFilename(idx int, t AudioTrackInfo) string {
	lang := t.Language
	if lang == "" {
		lang = fmt.Sprintf("track_%d", idx+1)
	}
	return fmt.Sprintf("audio_%d_%s.m4a", idx+1, lang)
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
