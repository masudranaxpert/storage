package fileapi

import (
	"fmt"
	"mime"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"stream/pkg/media"
)

// videoExtensions is the extension allowlist. Many CDNs answer HEAD with a
// generic application/octet-stream, so the filename extension is the second
// signal (mirrors aria2c --use-head + rclone http backend metadata probing).
var videoExtensions = map[string]bool{
	".mp4": true, ".m4v": true, ".mkv": true, ".webm": true,
	".mov": true, ".avi": true, ".flv": true, ".wmv": true,
	".mpg": true, ".mpeg": true, ".ts": true, ".m2ts": true,
}

// ProbeResult is the metadata harvested from the remote source before any
// byte is downloaded.
type ProbeResult struct {
	Filename    string
	SizeBytes   int64
	ContentType string
	AcceptRanges bool
}

// ProbeRemote issues a HEAD request to read metadata (filename via
// Content-Disposition, size via Content-Length) without downloading. Servers
// that reject HEAD fall back to a 0-3071 range GET, the same ladder
// rclone's http backend and aria2c --use-head use.
func ProbeRemote(rawURL string) (*ProbeResult, error) {
	client := &http.Client{Timeout: 15 * time.Second}

	res := &ProbeResult{}
	req, err := http.NewRequest(http.MethodHead, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("invalid url: %w", err)
	}
	req.Header.Set("User-Agent", "StreamMesh-FileAPI/1.0 (+HEAD probe)")

	resp, err := client.Do(req)
	if err == nil {
		defer resp.Body.Close()
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			res.Filename = media.ExtractFilenameFromHeaderAndURL(resp.Header, rawURL)
			res.SizeBytes = resp.ContentLength
			res.ContentType = resp.Header.Get("Content-Type")
			res.AcceptRanges = resp.Header.Get("Accept-Ranges") == "bytes"
			return res, nil
		}
	}

	// HEAD rejected (403/405/501 is common): range-GET a tiny slice instead.
	greq, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("invalid url: %w", err)
	}
	greq.Header.Set("Range", "bytes=0-3071")
	greq.Header.Set("User-Agent", "StreamMesh-FileAPI/1.0 (+HEAD probe)")

	gresp, err := client.Do(greq)
	if err != nil {
		return nil, fmt.Errorf("source unreachable: %w", err)
	}
	defer gresp.Body.Close()
	if gresp.StatusCode >= 400 {
		return nil, fmt.Errorf("source responded %s", gresp.Status)
	}

	res.Filename = media.ExtractFilenameFromHeaderAndURL(gresp.Header, rawURL)
	res.ContentType = gresp.Header.Get("Content-Type")
	if gresp.ContentLength > 0 {
		if cr := gresp.Header.Get("Content-Range"); strings.Contains(cr, "/") {
			// "bytes 0-3071/1234567" — total lives after the slash.
			if total := cr[strings.LastIndex(cr, "/")+1:]; total != "" && total != "*" {
				fmt.Sscanf(total, "%d", &res.SizeBytes)
			}
		} else {
			res.SizeBytes = gresp.ContentLength
		}
	}
	return res, nil
}

// ValidateVideo judges whether the probed metadata describes a video. The
// authoritative magic-byte check still happens on the worker after the
// download (media.ValidateVideoFile), so a mislabeled source cannot sneak in.
func ValidateVideo(filename, contentType string) error {
	ext := strings.ToLower(filepath.Ext(filename))
	if ext == "" || !videoExtensions[ext] {
		return fmt.Errorf("'%s' is not a supported video extension (mp4, mkv, webm, mov, avi...)", filename)
	}
	if contentType == "" {
		return nil // no header advertised; extension decided
	}
	mediatype, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return nil // malformed header; extension decided
	}
	if mediatype == "application/octet-stream" || mediatype == "binary/octet-stream" {
		return nil // generic bucket answer; extension decided
	}
	if strings.HasPrefix(mediatype, "video/") || media.SupportedVideoMIMEs[mediatype] {
		return nil
	}
	return fmt.Errorf("content-type '%s' is not a video", mediatype)
}
