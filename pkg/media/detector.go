package media

import (
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/gabriel-vasile/mimetype"
)

// SupportedVideoMIMEs lists the acceptable video container MIME types.
var SupportedVideoMIMEs = map[string]bool{
	"video/mp4":                true,
	"video/x-matroska":         true, // .mkv
	"video/webm":               true,
	"video/quicktime":          true, // .mov
	"video/x-msvideo":          true, // .avi
	"video/x-flv":              true,
	"application/mp4":          true,
	"application/x-matroska":   true,
	"application/octet-stream": true, // fallback when stream is generic
}

// MediaValidation holds detected type and sanitization results.
type MediaValidation struct {
	MIMEType     string `json:"mime_type"`
	Extension    string `json:"extension"`
	IsVideo      bool   `json:"is_video"`
	OriginalName string `json:"original_name"`
}

// DetectFileMIME inspects the file header (first 3072 bytes) and returns MIME information.
func DetectFileMIME(filePath string) (*MediaValidation, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("open file for detection: %w", err)
	}
	defer f.Close()

	// Read only first 3072 bytes to save memory
	header := make([]byte, 3072)
	n, err := f.Read(header)
	if err != nil && err != io.EOF {
		return nil, fmt.Errorf("read header: %w", err)
	}

	mtype := mimetype.Detect(header[:n])
	mimeString := mtype.String()

	isVideo := strings.HasPrefix(mimeString, "video/") || SupportedVideoMIMEs[mimeString]

	return &MediaValidation{
		MIMEType:     mimeString,
		Extension:    mtype.Extension(),
		IsVideo:      isVideo,
		OriginalName: filepath.Base(filePath),
	}, nil
}

// ValidateVideoFile validates that the file is indeed a supported video format.
func ValidateVideoFile(filePath string) error {
	val, err := DetectFileMIME(filePath)
	if err != nil {
		return err
	}

	if !val.IsVideo {
		return fmt.Errorf("unsupported format '%s' (%s). Only video files (MP4, MKV, WebM, MOV) are allowed", val.MIMEType, val.Extension)
	}

	return nil
}

// ExtractFilenameFromHeaderAndURL extracts the cleanest filename using RFC 6266 Content-Disposition header
// or falls back to the sanitized URL path.
func ExtractFilenameFromHeaderAndURL(header http.Header, rawURL string) string {
	// 1. Try Content-Disposition header
	if cd := header.Get("Content-Disposition"); cd != "" {
		if _, params, err := mime.ParseMediaType(cd); err == nil {
			if filename, ok := params["filename*"]; ok {
				return SanitizeFilename(filename)
			}
			if filename, ok := params["filename"]; ok {
				return SanitizeFilename(filename)
			}
		}
	}

	// 2. Fallback to URL Path
	if parsedURL, err := url.Parse(rawURL); err == nil {
		base := filepath.Base(parsedURL.Path)
		if base != "" && base != "." && base != "/" {
			return SanitizeFilename(base)
		}
	}

	// 3. Generic default
	return fmt.Sprintf("media_%d.mp4", os.Getpid())
}

var sanitizeRegex = regexp.MustCompile(`[^a-zA-Z0-9._-]`)

// SanitizeFilename cleans illegal filesystem characters and path traversals.
func SanitizeFilename(name string) string {
	name = filepath.Base(name)
	name = strings.ReplaceAll(name, " ", "_")
	name = sanitizeRegex.ReplaceAllString(name, "")
	if name == "" || name == "." {
		name = "video.mp4"
	}
	return name
}

// ProbeRemoteHeader issues an HTTP HEAD or Range GET request to detect filename and content type before downloading.
func ProbeRemoteHeader(rawURL string) (string, int64, string, error) {
	req, err := http.NewRequest("HEAD", rawURL, nil)
	if err != nil {
		return "", 0, "", err
	}
	req.Header.Set("User-Agent", "StreamMesh-MediaProbe/2.0")

	client := &http.Client{Timeout: 8 * time.Second}
	resp, err := client.Do(req)
	if err != nil || resp.StatusCode >= 400 {
		// Fallback to GET with range: bytes=0-3071
		getReq, _ := http.NewRequest("GET", rawURL, nil)
		getReq.Header.Set("Range", "bytes=0-3071")
		getReq.Header.Set("User-Agent", "StreamMesh-MediaProbe/2.0")
		resp, err = client.Do(getReq)
		if err != nil {
			return ExtractFilenameFromHeaderAndURL(nil, rawURL), 0, "video/mp4", nil
		}
	}
	defer resp.Body.Close()

	filename := ExtractFilenameFromHeaderAndURL(resp.Header, rawURL)
	contentType := resp.Header.Get("Content-Type")
	size := resp.ContentLength

	return filename, size, contentType, nil
}
