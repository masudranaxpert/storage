package media

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	// ffmpegTimeout bounds one packaging pass so a wedged ffmpeg cannot pin a worker forever.
	ffmpegTimeout = 8 * time.Hour
	// ffprobeTimeout bounds metadata and keyframe probes.
	ffprobeTimeout = 2 * time.Minute
	// hlsSegmentSeconds is the target segment length; copied video can only be cut on keyframes.
	hlsSegmentSeconds = 4
	// maxCopyKeyframeGap is the longest keyframe interval that still yields responsive
	// segments when video is copied. Sparser sources are re-encoded with forced keyframes.
	maxCopyKeyframeGap = 10.0
	// keyframeProbeSeconds is how much of the source is inspected for the keyframe interval.
	keyframeProbeSeconds = 300
)

// PackageHLS produces a single-file byte-range fMP4 HLS package in dstDir:
//   - master.m3u8 plus stream_0.{m3u8,m4s}: video, with the audio muxed in when the source has one track.
//   - stream_<name>.{m3u8,m4s}: one rendition per audio track when the source has several.
//   - subtitle_*.vtt: text subtitles extracted as sidecars.
func PackageHLS(srcPath, dstDir, mediaID, originalFilename string) (*MediaMetadata, error) {
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

	cleanup := func() { removePackageFiles(dstDir) }

	plan := PlanVideo(srcMeta.Video, probed)
	tracks := srcMeta.AudioTracks
	if plan.Mode == VideoNone && len(tracks) > 1 {
		// Audio-only packages carry the primary track; renditions need a video variant to attach to.
		tracks = tracks[:1]
	}
	names := audioStreamNames(tracks)

	videoBitrate := int64(0)
	if srcMeta.Video != nil {
		videoBitrate = srcMeta.Video.BitrateBps
	}
	if videoBitrate <= 0 {
		videoBitrate = srcMeta.BitrateBps
	}
	// Fallback calculation using exact file size / duration if container bitrate is omitted.
	if videoBitrate <= 0 && srcMeta.DurationSec > 0 {
		if fi, sErr := os.Stat(srcPath); sErr == nil && fi.Size() > 0 {
			videoBitrate = int64(float64(fi.Size()*8) / srcMeta.DurationSec)
		}
	}
	// Industry standard default buffer for 1080p/720p streams if entirely unprobed (2.5 Mbps).
	if videoBitrate <= 0 {
		videoBitrate = 2500000
	}

	forceAAC := false
	out, runErr := runHLSPass(ffmpegPath, srcPath, dstDir, plan, videoBitrate, tracks, names, probed, forceAAC)
	if runErr != nil {
		cleanup()
		return nil, fmt.Errorf("ffmpeg packaging failed: %v: %s", runErr, lastLines(out, 6))
	}

	meta, err := ExtractMetadata(filepath.Join(dstDir, "stream_0.m4s"), mediaID, originalFilename)
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
		AudioMode:   audioMode(tracks, forceAAC),
	}
	meta.AudioTracks = registerAudioTracks(dstDir, tracks, names, forceAAC)
	meta.Subtitles = extractSubtitles(ffmpegPath, srcPath, dstDir, srcMeta.Subtitles)

	if totalBytes, err := CalculateFolderSize(dstDir); err == nil && totalBytes > 0 {
		meta.TotalBytes = totalBytes
		meta.SizeBytes = totalBytes
	}
	return meta, nil
}

// removePackageFiles deletes everything a packaging pass writes into dir.
func removePackageFiles(dir string) {
	_ = os.Remove(filepath.Join(dir, "master.m3u8"))
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "stream_") || strings.HasPrefix(e.Name(), "subtitle_") {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}

// registerAudioTracks describes the audio tracks of the finished package. A single
// track is muxed into stream_0 (no File); several tracks each point at their playlist.
func registerAudioTracks(dstDir string, tracks []AudioTrackInfo, names []string, forceAAC bool) []AudioTrackInfo {
	registered := make([]AudioTrackInfo, 0, len(tracks))
	for idx, t := range tracks {
		if forceAAC || !isWebAudioCodec(t.Codec) {
			t.Codec = "aac"
		}
		if len(tracks) > 1 {
			stat, err := os.Stat(filepath.Join(dstDir, "stream_"+names[idx]+".m4s"))
			if err != nil || stat.Size() == 0 {
				continue
			}
			t.File = "stream_" + names[idx] + ".m3u8"
		} else {
			t.File = ""
		}
		registered = append(registered, t)
	}
	return registered
}

// audioStreamNames derives a unique, filesystem-safe rendition name per audio track.
// Packaging and metadata registration share it so playlists and metadata never diverge.
func audioStreamNames(tracks []AudioTrackInfo) []string {
	names := make([]string, len(tracks))
	used := make(map[string]bool, len(tracks))
	for idx, t := range tracks {
		base := sanitizeStreamLang(idx, t.Language)
		name := base
		for n := 2; used[name]; n++ {
			name = fmt.Sprintf("%s_%d", base, n)
		}
		used[name] = true
		names[idx] = name
	}
	return names
}

// sanitizeStreamLang reduces a language tag to [a-z0-9] for use in file names (hin, eng, ...).
func sanitizeStreamLang(idx int, lang string) string {
	var clean strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(lang)) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			clean.WriteRune(r)
		}
	}
	if res := clean.String(); res != "" && res != "und" {
		return res
	}
	return fmt.Sprintf("audio_%d", idx+1)
}

// runHLSPass executes one ffmpeg pass producing the HLS package described on PackageHLS.
func runHLSPass(ffmpegPath, srcPath, dstDir string, plan VideoPlan, videoBitrate int64, tracks []AudioTrackInfo,
	names []string, probed, forceAAC bool) ([]byte, error) {
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

	hasVideo := plan.Mode != VideoNone
	var varMap []string
	if hasVideo {
		args = append(args, "-map", "0:v:0?")
		args = append(args, plan.Args...)
		// When copying video, if the container does not specify bitrate, FFmpeg omits
		// #EXT-X-STREAM-INF from master.m3u8 unless -b:v is explicitly supplied.
		if plan.Mode == VideoCopy {
			vb := videoBitrate
			if vb <= 0 {
				vb = 2000000
			}
			args = append(args, "-b:v", strconv.FormatInt(vb, 10))
		}
	}

	switch {
	case len(tracks) > 1:
		varMap = append(varMap, "v:0,agroup:audio")
		for idx, t := range tracks {
			args = append(args, "-map", fmt.Sprintf("0:a:%d", idx))
			args = append(args, audioFor(t.Codec)...)
			def := "no"
			if idx == 0 {
				def = "yes"
			}
			varMap = append(varMap, fmt.Sprintf("a:%d,agroup:audio,language:%s,name:%s,default:%s",
				idx, sanitizeStreamLang(idx, t.Language), names[idx], def))
		}
	case len(tracks) == 1:
		args = append(args, "-map", "0:a:0")
		args = append(args, audioFor(tracks[0].Codec)...)
		if hasVideo {
			varMap = append(varMap, "v:0,a:0")
		} else {
			varMap = append(varMap, "a:0")
		}
	case !probed:
		args = append(args, "-map", "0:a:0?", "-c:a", "aac", "-b:a", "192k")
		varMap = append(varMap, "v:0,a:0")
	default:
		varMap = append(varMap, "v:0")
	}

	args = append(args,
		"-sn", "-dn",
		"-f", "hls",
		"-hls_time", strconv.Itoa(hlsSegmentSeconds),
		"-hls_playlist_type", "vod",
		"-hls_flags", "single_file+independent_segments",
		"-hls_segment_type", "fmp4",
		"-var_stream_map", strings.Join(varMap, " "),
		"-master_pl_name", "master.m3u8",
		filepath.Join(dstDir, "stream_%v.m3u8"),
	)

	ctx, cancel := context.WithTimeout(context.Background(), ffmpegTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, ffmpegPath, args...).CombinedOutput()
	if err != nil {
		if ctx.Err() != nil {
			err = fmt.Errorf("timed out after %s: %w", ffmpegTimeout, err)
		}
		return out, err
	}

	master, rErr := os.ReadFile(filepath.Join(dstDir, "master.m3u8"))
	if rErr != nil {
		return out, fmt.Errorf("master playlist missing: %w", rErr)
	}
	if !bytes.Contains(master, []byte("stream_0.m3u8")) {
		return out, fmt.Errorf("master playlist has no primary variant")
	}
	return out, nil
}

// extractSubtitles writes every text subtitle track as a WebVTT sidecar in one ffmpeg pass
// and returns the tracks that were produced. Bitmap tracks (PGS, VobSub) are skipped.
func extractSubtitles(ffmpegPath, srcPath, dstDir string, subs []SubtitleTrackInfo) []SubtitleTrackInfo {
	type target struct {
		track SubtitleTrackInfo
		file  string
	}
	var targets []target
	args := []string{
		"-y",
		"-err_detect", "ignore_err",
		"-fflags", "+genpts+discardcorrupt",
		"-i", srcPath,
	}
	for idx, s := range subs {
		if !isTextSubtitle(s.Codec) {
			continue
		}
		file := fmt.Sprintf("subtitle_%d_%s.vtt", idx+1, sanitizeSubtitleLang(idx, s.Language))
		targets = append(targets, target{track: s, file: file})
		args = append(args, "-map", fmt.Sprintf("0:s:%d", idx), filepath.Join(dstDir, file))
	}
	if len(targets) == 0 {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), ffmpegTimeout)
	defer cancel()
	if err := exec.CommandContext(ctx, ffmpegPath, args...).Run(); err != nil {
		return nil
	}

	var produced []SubtitleTrackInfo
	for _, t := range targets {
		if stat, err := os.Stat(filepath.Join(dstDir, t.file)); err == nil && stat.Size() > 0 {
			track := t.track
			track.File = t.file
			produced = append(produced, track)
		}
	}
	return produced
}

// sanitizeSubtitleLang keeps subtitle file names free of path separators and odd characters.
func sanitizeSubtitleLang(idx int, lang string) string {
	var clean strings.Builder
	for _, r := range strings.TrimSpace(lang) {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' {
			clean.WriteRune(r)
		}
	}
	if clean.Len() == 0 {
		return fmt.Sprintf("sub_%d", idx+1)
	}
	return clean.String()
}

// longestKeyframeGap probes the start of the source and returns the longest distance
// between consecutive video keyframes, in seconds.
func longestKeyframeGap(ffprobePath, srcPath string) (float64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), ffprobeTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, ffprobePath,
		"-v", "error",
		"-select_streams", "v:0",
		"-read_intervals", fmt.Sprintf("%%+%d", keyframeProbeSeconds),
		"-show_entries", "packet=pts_time,flags",
		"-of", "csv=p=0",
		srcPath,
	).Output()
	if err != nil {
		return 0, err
	}
	return gapFromPackets(string(out)), nil
}

// gapFromPackets reads ffprobe "pts_time,flags" CSV rows and returns the longest keyframe
// interval. When the probe window ends without a further keyframe, the open interval counts,
// so a source with one keyframe per several minutes is reported as sparse.
func gapFromPackets(csv string) float64 {
	var keys []float64
	last := 0.0
	for _, line := range strings.Split(csv, "\n") {
		fields := strings.Split(strings.TrimSpace(line), ",")
		if len(fields) < 2 {
			continue
		}
		pts, err := strconv.ParseFloat(fields[0], 64)
		if err != nil {
			continue
		}
		if pts > last {
			last = pts
		}
		if strings.Contains(fields[1], "K") {
			keys = append(keys, pts)
		}
	}
	if len(keys) == 0 {
		return 0
	}
	sort.Float64s(keys)
	gap := last - keys[len(keys)-1]
	for i := 1; i < len(keys); i++ {
		if d := keys[i] - keys[i-1]; d > gap {
			gap = d
		}
	}
	return gap
}
