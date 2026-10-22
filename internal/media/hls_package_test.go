package media

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestGapFromPackets(t *testing.T) {
	cases := []struct {
		name string
		csv  string
		want float64
	}{
		{"empty", "", 0},
		{"no keyframes flagged", "0.0,__\n1.0,__\n", 0},
		{"regular 2s gop", "0.0,K_\n0.5,__\n2.0,K_\n2.5,__\n4.0,K_\n4.5,__\n", 2},
		{"widest gap wins", "0.0,K_\n2.0,K_\n12.0,K_\n13.0,__\n", 10},
		{"sparse tail counts as open gap", "0.0,K_\n100.0,__\n300.0,__\n", 300},
		{"unparseable rows skipped", "N/A,K_\n0.0,K_\n3.0,K_\n", 3},
	}
	for _, c := range cases {
		if got := gapFromPackets(c.csv); got != c.want {
			t.Errorf("%s: gap = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestAudioStreamNamesAreUnique(t *testing.T) {
	tracks := []AudioTrackInfo{
		{Language: "hin"}, {Language: "hin"}, {Language: "eng"}, {Language: "und"}, {Language: ""}, {Language: "hin"},
	}
	names := audioStreamNames(tracks)
	seen := map[string]bool{}
	for i, n := range names {
		if n == "" || seen[n] {
			t.Fatalf("name %d = %q is empty or duplicated in %v", i, n, names)
		}
		seen[n] = true
	}
	want := []string{"hin", "hin_2", "eng", "audio_4", "audio_5", "hin_3"}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("names = %v, want %v", names, want)
		}
	}
}

func TestSanitizeSubtitleLangBlocksTraversal(t *testing.T) {
	if got := sanitizeSubtitleLang(0, "../../etc/x"); strings.ContainsAny(got, "/.") {
		t.Fatalf("unsafe subtitle tag %q", got)
	}
	if got := sanitizeSubtitleLang(2, ""); got != "sub_3" {
		t.Fatalf("empty tag = %q, want sub_3", got)
	}
}

// ffmpegFixture encodes a short synthetic source; the test is skipped when the encoder is missing.
func ffmpegFixture(t *testing.T, name string, args ...string) string {
	t.Helper()
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe not installed")
	}
	dst := filepath.Join(t.TempDir(), name)
	full := append([]string{"-v", "error", "-y"}, args...)
	full = append(full, dst)
	if out, err := exec.Command(ffmpeg, full...).CombinedOutput(); err != nil {
		t.Skipf("fixture encoder unavailable: %v: %s", err, out)
	}
	return dst
}

const (
	videoSrc = "testsrc2=size=320x240:rate=25"
	toneA    = "sine=frequency=440:sample_rate=48000"
	toneB    = "sine=frequency=880:sample_rate=48000"
)

func TestPackageHLSAudioOnly(t *testing.T) {
	src := ffmpegFixture(t, "tone.m4a", "-f", "lavfi", "-i", toneA, "-t", "6", "-c:a", "aac")
	out := t.TempDir()
	meta, err := PackageHLS(src, out, "k", "tone.m4a")
	if err != nil {
		t.Fatalf("audio-only package failed: %v", err)
	}
	if meta.Video != nil {
		t.Fatalf("audio-only package reports video: %+v", meta.Video)
	}
	if meta.Processing == nil || meta.Processing.VideoMode != VideoNone {
		t.Fatalf("processing = %+v, want video mode none", meta.Processing)
	}
	if _, err := os.Stat(filepath.Join(out, "master.m3u8")); err != nil {
		t.Fatalf("missing master playlist: %v", err)
	}
}

func TestPackageHLSDuplicateLanguageTracks(t *testing.T) {
	src := ffmpegFixture(t, "dup.mkv",
		"-f", "lavfi", "-i", videoSrc, "-f", "lavfi", "-i", toneA, "-f", "lavfi", "-i", toneB,
		"-t", "6", "-g", "25", "-c:v", "libx264", "-pix_fmt", "yuv420p", "-c:a", "aac",
		"-map", "0:v", "-map", "1:a", "-map", "2:a",
		"-metadata:s:a:0", "language=hin", "-metadata:s:a:1", "language=hin")
	out := t.TempDir()
	meta, err := PackageHLS(src, out, "k", "dup.mkv")
	if err != nil {
		t.Fatalf("package failed: %v", err)
	}
	if len(meta.AudioTracks) != 2 {
		t.Fatalf("registered %d audio tracks, want 2", len(meta.AudioTracks))
	}
	if meta.AudioTracks[0].File == meta.AudioTracks[1].File {
		t.Fatalf("both tracks map to %q", meta.AudioTracks[0].File)
	}
	for _, a := range meta.AudioTracks {
		if _, err := os.Stat(filepath.Join(out, a.File)); err != nil {
			t.Fatalf("track playlist %q not on disk: %v", a.File, err)
		}
	}
}

func TestPackageHLSSparseKeyframesAreCopiedWithoutSlowReencoding(t *testing.T) {
	src := ffmpegFixture(t, "longgop.mkv",
		"-f", "lavfi", "-i", videoSrc, "-f", "lavfi", "-i", toneA,
		"-t", "30", "-c:v", "libx264", "-g", "1000", "-keyint_min", "1000", "-sc_threshold", "0",
		"-pix_fmt", "yuv420p", "-c:a", "aac")
	out := t.TempDir()
	meta, err := PackageHLS(src, out, "k", "longgop.mkv")
	if err != nil {
		t.Fatalf("package failed: %v", err)
	}
	if meta.Processing == nil || meta.Processing.VideoMode != VideoCopy {
		t.Fatalf("processing = %+v, want copy (cpu transcode blocked)", meta.Processing)
	}
	playlist, err := os.ReadFile(filepath.Join(out, "stream_0.m3u8"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(playlist), "stream_0.m4s") {
		t.Fatalf("expected stream_0.m4s in playlist:\n%s", playlist)
	}
}

func TestPackageHLSVideoOnly(t *testing.T) {
	src := ffmpegFixture(t, "silent.mkv", "-f", "lavfi", "-i", videoSrc, "-t", "4", "-g", "25",
		"-c:v", "libx264", "-pix_fmt", "yuv420p")
	meta, err := PackageHLS(src, t.TempDir(), "k", "silent.mkv")
	if err != nil {
		t.Fatalf("video-only package failed: %v", err)
	}
	if len(meta.AudioTracks) != 0 {
		t.Fatalf("video-only package reports audio: %+v", meta.AudioTracks)
	}
}
