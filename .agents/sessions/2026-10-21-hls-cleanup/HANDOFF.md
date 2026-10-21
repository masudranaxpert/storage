# HLS pipeline cleanup (2026-10-21)

Follow-up to the byte-range fMP4 HLS switch ([research](../../research/fmp4-playback/research.md)). Legacy MP4 playback is removed; no fallback is kept.

## Done
- **Legacy removal:** progressive `.mp4`/`.mkv` resolution, `.mp4` URL suffixes, the iframe embed player (dash.js/shaka CDN scripts included), `RemuxAndPackageCMAF`, `CMAFPackage`, external-audio lockstep sync and `play_url` handling in `media.js`. Media created before the HLS switch must be re-processed (force retry).
- **Packaging** moved to [hls_package.go](../../../internal/media/hls_package.go): `PackageHLS(src, dstDir, ...)`.
  - Audio-only sources package correctly.
  - Duplicate-language audio tracks get unique rendition names shared by ffmpeg and metadata (`stream_hin`, `stream_hin_2`).
  - Copied video with a keyframe interval above 10 s is re-encoded; every transcode forces a keyframe each 4 s.
  - ffmpeg/ffprobe calls have timeouts; the hand-written `BANDWIDTH` playlist patch is replaced by a hard error.
  - Subtitle file names are sanitized.
- **Play counting:** `master.m3u8` fetch records one play (heat); `.m4s` fetches keep the live-viewer heartbeat. Agent and coordinator proxy both follow it ([agent_stream.go](../../../internal/agent/agent_stream.go), [handlers_stream.go](../../../internal/api/handlers_stream.go)).
- **Player:** audio track menu is driven by hls.js with metadata labels (language, 5.1/Stereo/Mono) instead of hardcoded Hindi/Tamil text.
- **Coordinator stream handler** rewritten around `/stream/<key|ticket>/<file>`; OPTIONS preflight answered before redirect.
- Progress payload now carries `MediaMetadata` (same `cmaf_json` wire field, no protocol change).

## Verified
`go build`, `go vet`, `go test ./...` and `-race` on agent/media/api/lifecycle pass. New tests: audio-only, duplicate language, sparse keyframes, video-only, keyframe-gap parsing, playback counting. A multi-audio package plays and switches tracks in hls.js.

## Not done / next
- `stream_url` and `play_url` in the status API still end at `/stream/<ticket>` (no file); changing them is an API schema change, ask first.
- `video_url` duplicates `master_url` in the token response (kept for API compatibility).
- Real-VPS check of a multi-GB file and Safari are still open.
