package media

import (
	"fmt"
	"strings"
)

// GenerateHLSManifest builds an RFC 8216 compliant HLS master and media playlist pointing to CMAF segments.
func GenerateHLSManifest(pkg *CMAFPackage, chunkBaseURL string) string {
	var sb strings.Builder
	sb.Grow(1024)

	sb.WriteString("#EXTM3U\n")
	sb.WriteString("#EXT-X-VERSION:7\n")
	sb.WriteString("#EXT-X-TARGETDURATION:6\n")
	sb.WriteString("#EXT-X-MEDIA-SEQUENCE:0\n")
	sb.WriteString("#EXT-X-PLAYLIST-TYPE:VOD\n")
	sb.WriteString("#EXT-X-INDEPENDENT-SEGMENTS\n\n")

	if len(pkg.Chunks) == 0 {
		// Single-file CMAF / Byte-Range playback manifest
		mediaFile := "video.mp4"
		if pkg.InitChunk != nil && pkg.InitChunk.Filename != "" {
			mediaFile = pkg.InitChunk.Filename
		}
		if chunkBaseURL != "" {
			mediaFile = fmt.Sprintf("%s/%s", strings.TrimRight(chunkBaseURL, "/"), mediaFile)
		}
		dur := pkg.DurationSec
		if dur <= 0 {
			dur = 60.0
		}
		sb.WriteString(fmt.Sprintf("#EXT-X-MAP:URI=\"%s\"\n", mediaFile))
		sb.WriteString(fmt.Sprintf("#EXTINF:%.3f,\n", dur))
		sb.WriteString(fmt.Sprintf("%s\n", mediaFile))
		sb.WriteString("#EXT-X-ENDLIST\n")
		return sb.String()
	}

	// CMAF Initialization Header map
	if pkg.InitChunk != nil {
		initURL := pkg.InitChunk.Filename
		if chunkBaseURL != "" {
			initURL = fmt.Sprintf("%s/%s", strings.TrimRight(chunkBaseURL, "/"), initURL)
		}
		sb.WriteString(fmt.Sprintf("#EXT-X-MAP:URI=\"%s\"\n\n", initURL))
	}

	for _, chunk := range pkg.Chunks {
		chunkURL := chunk.Filename
		if chunkBaseURL != "" {
			chunkURL = fmt.Sprintf("%s/%s", strings.TrimRight(chunkBaseURL, "/"), chunkURL)
		}

		dur := chunk.DurationSec
		if dur <= 0 {
			dur = 4.0 // Standard 4s segment target duration
		}

		sb.WriteString(fmt.Sprintf("#EXTINF:%.3f,\n", dur))
		sb.WriteString(fmt.Sprintf("%s\n", chunkURL))
	}

	sb.WriteString("#EXT-X-ENDLIST\n")
	return sb.String()
}
