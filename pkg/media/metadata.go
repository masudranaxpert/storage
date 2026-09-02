package media

import (
	"encoding/json"
	"fmt"
	"io"
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
	MIMEType         string              `json:"mime_type"`
	DurationSec      float64             `json:"duration_sec"`
	BitrateBps       int64               `json:"bitrate_bps"`
	Video            *VideoStreamInfo    `json:"video,omitempty"`
	AudioTracks      []AudioTrackInfo    `json:"audio_tracks,omitempty"`
	Subtitles        []SubtitleTrackInfo `json:"subtitles,omitempty"`
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

// RemuxToStreamableMP4 normalizes audio to web AAC and writes a fast-start MP4 at dstPath.
func RemuxToStreamableMP4(srcPath, dstPath, mediaID, originalFilename string) (*MediaMetadata, error) {
	ffmpegPath, err := exec.LookPath("ffmpeg")
	if err == nil && ffmpegPath != "" {
		cmd := exec.Command(ffmpegPath,
			"-y",
			"-i", srcPath,
			"-c:v", "copy",
			"-c:a", "aac",
			"-b:a", "192k",
			"-movflags", "+faststart",
			dstPath,
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			_ = os.Remove(dstPath)
			// If copy failed due to incompatible container/codecs, fallback to direct copy
			if copyErr := CopyFileSimple(srcPath, dstPath); copyErr != nil {
				return nil, fmt.Errorf("remux failed: %v (%s)", err, string(out))
			}
		}
	} else {
		if copyErr := CopyFileSimple(srcPath, dstPath); copyErr != nil {
			return nil, fmt.Errorf("copy fallback failed: %w", copyErr)
		}
	}

	return ExtractMetadata(dstPath, mediaID, originalFilename)
}

// CopyFileSimple copies data from src to dst.
func CopyFileSimple(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}
