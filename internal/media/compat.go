package media

import (
	"strconv"
	"strings"
)

// Copy versus re-encode is decided by what browsers can decode, not by what
// ffmpeg can mux: MP4 happily accepts MPEG-2, MPEG-4 Part 2 (Xvid/DivX) or
// 10-bit H.264 with -c copy, and the result is a valid file no browser plays.
// A remux (container change) is near-free; only the codec forces an encode.

// VideoMode records how the primary video stream was produced.
type VideoMode string

const (
	VideoCopy      VideoMode = "copy"      // bitstream copied, container changed only
	VideoTranscode VideoMode = "transcode" // re-encoded to H.264 for browser playback
	VideoNone      VideoMode = "none"      // source has no video stream
)

// ProcessingInfo explains what the remux pipeline did to a file.
type ProcessingInfo struct {
	VideoMode   VideoMode `json:"video_mode"`
	VideoReason string    `json:"video_reason,omitempty"`
	AudioMode   string    `json:"audio_mode,omitempty"` // "copy" | "aac" | "mixed" | "none"
}

// VideoPlan is the ffmpeg video codec arguments for one output.
type VideoPlan struct {
	Mode   VideoMode
	Args   []string
	Reason string
}

// transcodeH264 produces 8-bit 4:2:0 High-profile H.264, the one video format
// every browser decodes. veryfast keeps CPU cost low on commodity VPS nodes.
var transcodeH264 = []string{
	"-c:v", "libx264", "-preset", "veryfast", "-crf", "23",
	"-pix_fmt", "yuv420p", "-profile:v", "high",
	// A keyframe at every segment boundary keeps HLS segments short and seeks fast.
	"-force_key_frames", "expr:gte(t,n_forced*" + strconv.Itoa(hlsSegmentSeconds) + ")",
}

// PlanVideo picks copy or transcode for the source's primary video stream.
func PlanVideo(v *VideoStreamInfo, probed bool) VideoPlan {
	if !probed {
		return VideoPlan{Mode: VideoCopy, Args: []string{"-c:v", "copy"}, Reason: "source not probed; trying copy"}
	}
	if v == nil {
		return VideoPlan{Mode: VideoNone, Reason: "no video stream"}
	}
	codec := strings.ToLower(v.Codec)
	pix := strings.ToLower(v.PixFmt)
	profile := strings.ToLower(v.Profile)
	chroma420 := pix == "" || strings.HasPrefix(pix, "yuv420p") || strings.HasPrefix(pix, "yuvj420p") || pix == "nv12"
	tenBit := strings.Contains(pix, "10") || strings.Contains(pix, "12")

	switch codec {
	case "h264":
		// Browsers decode 8-bit 4:2:0 H.264 only; High 10 / 4:2:2 / 4:4:4 fail.
		if tenBit || !chroma420 || strings.Contains(profile, "10") || strings.Contains(profile, "4:2:2") || strings.Contains(profile, "4:4:4") {
			return transcodePlan("h264 " + describeFormat(profile, pix) + " is not browser-decodable")
		}
		return copyPlan("h264 8-bit 4:2:0")
	case "hevc":
		// hvc1 tag is required for Safari/iOS; Chrome/Edge decode via hardware.
		if !chroma420 {
			return transcodePlan("hevc " + describeFormat(profile, pix) + " is not browser-decodable")
		}
		p := copyPlan("hevc 4:2:0 (Safari, Edge, Chrome with hardware decode; not all Firefox builds)")
		p.Args = append(p.Args, "-tag:v", "hvc1")
		return p
	case "av1":
		if !chroma420 {
			return transcodePlan("av1 " + describeFormat(profile, pix) + " is not browser-decodable")
		}
		return copyPlan("av1 4:2:0")
	case "vp9":
		// MP4 carries VP9 natively (vp09); decoded by Chrome, Edge, Firefox, Safari 14+.
		if !chroma420 {
			return transcodePlan("vp9 " + describeFormat(profile, pix) + " is not browser-decodable")
		}
		return copyPlan("vp9 4:2:0")
	}
	return transcodePlan(codec + " is not decodable by browsers")
}

// TranscodePlan forces an H.264 encode when explicitly needed.
func TranscodePlan(reason string) VideoPlan {
	return transcodePlan(reason)
}

func copyPlan(reason string) VideoPlan {
	return VideoPlan{Mode: VideoCopy, Args: []string{"-c:v", "copy"}, Reason: reason}
}

func transcodePlan(reason string) VideoPlan {
	return VideoPlan{Mode: VideoTranscode, Args: append([]string(nil), transcodeH264...), Reason: reason}
}

func describeFormat(profile, pix string) string {
	parts := make([]string, 0, 2)
	if profile != "" {
		parts = append(parts, profile)
	}
	if pix != "" {
		parts = append(parts, pix)
	}
	if len(parts) == 0 {
		return "(unknown format)"
	}
	return "(" + strings.Join(parts, ", ") + ")"
}

// AudioArgs returns the ffmpeg "-c:a" arguments for one audio stream.
// AAC and MP3 play everywhere and are copied; AC-3, E-AC-3, DTS, TrueHD,
// FLAC, Vorbis, Opus and PCM are encoded to AAC (cheap: audio is ~1% of the
// work of video).
func AudioArgs(codec string) []string {
	if isWebAudioCodec(codec) {
		return []string{"-c:a", "copy"}
	}
	return []string{"-c:a", "aac", "-b:a", "192k"}
}

// audioMode summarises per-track audio handling for ProcessingInfo.
func audioMode(tracks []AudioTrackInfo, forceAAC bool) string {
	if len(tracks) == 0 {
		return "none"
	}
	copies := 0
	for _, t := range tracks {
		if !forceAAC && isWebAudioCodec(t.Codec) {
			copies++
		}
	}
	switch copies {
	case len(tracks):
		return "copy"
	case 0:
		return "aac"
	}
	return "mixed"
}

// browserVideoCodecs are the codecs a finished MP4 may contain.
var browserVideoCodecs = map[string]bool{"h264": true, "hevc": true, "av1": true, "vp9": true}

// isBrowserVideo reports whether a probed output stream is playable.
func isBrowserVideo(v *VideoStreamInfo) bool {
	return v != nil && browserVideoCodecs[strings.ToLower(v.Codec)]
}
