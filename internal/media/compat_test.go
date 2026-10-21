package media_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"stream/internal/media"
)

func TestPlanVideo(t *testing.T) {
	cases := []struct {
		name  string
		v     *media.VideoStreamInfo
		probe bool
		want  media.VideoMode
	}{
		{"h264 8-bit copies", &media.VideoStreamInfo{Codec: "h264", Profile: "High", PixFmt: "yuv420p"}, true, media.VideoCopy},
		{"h264 high10 transcodes", &media.VideoStreamInfo{Codec: "h264", Profile: "High 10", PixFmt: "yuv420p10le"}, true, media.VideoTranscode},
		{"h264 4:2:2 transcodes", &media.VideoStreamInfo{Codec: "h264", Profile: "High 4:2:2", PixFmt: "yuv422p"}, true, media.VideoTranscode},
		{"hevc main10 copies", &media.VideoStreamInfo{Codec: "hevc", Profile: "Main 10", PixFmt: "yuv420p10le"}, true, media.VideoCopy},
		{"vp9 copies", &media.VideoStreamInfo{Codec: "vp9", PixFmt: "yuv420p"}, true, media.VideoCopy},
		{"av1 copies", &media.VideoStreamInfo{Codec: "av1", PixFmt: "yuv420p"}, true, media.VideoCopy},
		{"xvid transcodes", &media.VideoStreamInfo{Codec: "mpeg4", PixFmt: "yuv420p"}, true, media.VideoTranscode},
		{"mpeg2 transcodes", &media.VideoStreamInfo{Codec: "mpeg2video", PixFmt: "yuv420p"}, true, media.VideoTranscode},
		{"vc1 transcodes", &media.VideoStreamInfo{Codec: "vc1", PixFmt: "yuv420p"}, true, media.VideoTranscode},
		{"no video", nil, true, media.VideoNone},
		{"unprobed tries copy", nil, false, media.VideoCopy},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := media.PlanVideo(c.v, c.probe)
			if got.Mode != c.want {
				t.Fatalf("mode = %s (%s), want %s", got.Mode, got.Reason, c.want)
			}
		})
	}

	hevc := media.PlanVideo(&media.VideoStreamInfo{Codec: "hevc", PixFmt: "yuv420p"}, true)
	if !bytes.Contains([]byte(joinArgs(hevc.Args)), []byte("-tag:v hvc1")) {
		t.Fatalf("hevc copy must set hvc1 tag for Safari, got %v", hevc.Args)
	}
}

func joinArgs(a []string) string {
	var b bytes.Buffer
	for i, s := range a {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(s)
	}
	return b.String()
}

// TestRemuxToStreamableMP4Codecs builds short clips in real source formats and
// checks the output is browser-playable, fast-start, and only re-encoded when
// the source codec requires it.
func TestRemuxToStreamableMP4Codecs(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe not installed")
	}

	cases := []struct {
		name      string
		encode    []string
		wantVideo string
		wantMode  media.VideoMode
	}{
		{"h264+ac3 mkv", []string{"-c:v", "libx264", "-pix_fmt", "yuv420p", "-c:a", "ac3"}, "h264", media.VideoCopy},
		{"vp9+opus mkv", []string{"-c:v", "libvpx-vp9", "-pix_fmt", "yuv420p", "-deadline", "realtime", "-cpu-used", "8", "-c:a", "libopus"}, "vp9", media.VideoCopy},
		{"xvid+mp3 mkv", []string{"-c:v", "mpeg4", "-c:a", "libmp3lame"}, "h264", media.VideoTranscode},
		{"h264 10-bit mkv", []string{"-c:v", "libx264", "-pix_fmt", "yuv420p10le", "-c:a", "aac"}, "h264", media.VideoTranscode},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			src := filepath.Join(dir, "src.mkv")
			args := []string{"-v", "error", "-y",
				"-f", "lavfi", "-i", "testsrc=size=320x240:rate=25",
				"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000",
				"-t", "2"}
			args = append(args, c.encode...)
			args = append(args, src)
			if out, err := exec.Command(ffmpeg, args...).CombinedOutput(); err != nil {
				t.Skipf("encoder unavailable for fixture: %v: %s", err, out)
			}

			outDir := filepath.Join(dir, "out")
			if err := os.MkdirAll(outDir, 0o755); err != nil {
				t.Fatal(err)
			}
			dst := filepath.Join(outDir, "video.mp4")
			meta, err := media.RemuxToStreamableMP4(src, dst, "key", "src.mkv")
			if err != nil {
				t.Fatalf("remux: %v", err)
			}
			if meta.Video == nil || meta.Video.Codec != c.wantVideo {
				t.Fatalf("video codec = %+v, want %s (processing %+v)", meta.Video, c.wantVideo, meta.Processing)
			}
			if meta.Processing == nil || meta.Processing.VideoMode != c.wantMode {
				t.Fatalf("processing = %+v, want mode %s", meta.Processing, c.wantMode)
			}
			for _, a := range meta.AudioTracks {
				if a.Codec != "aac" && a.Codec != "mp3" {
					t.Fatalf("audio codec %q is not browser-playable", a.Codec)
				}
			}
			masterPath := filepath.Join(outDir, "master.m3u8")
			if _, err := os.Stat(masterPath); err != nil {
				t.Fatalf("expected master.m3u8 to exist: %v", err)
			}
			videoM4s := filepath.Join(outDir, "stream_0.m4s")
			assertFMP4SingleFile(t, videoM4s)
		})
	}
}

// assertFMP4SingleFile checks fMP4 single-file structure containing ftyp and moov.
func assertFMP4SingleFile(t *testing.T, path string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	ftyp := bytes.Index(data, []byte("ftyp"))
	moov := bytes.Index(data, []byte("moov"))
	if ftyp < 0 || moov < 0 {
		t.Fatalf("not valid fMP4: ftyp at %d, moov at %d", ftyp, moov)
	}
}
