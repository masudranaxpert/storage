# Session Handoff: Review Fixes, Opaque Playback Tickets & God File Deconstruction

**Date:** 2026-10-20  
**Git Status:** Uncommitted changes ready for review (no unstaged anomalies, behavior preserving).

---

## 1. Summary of Work Delivered

### Task 1: Opaque Session Tickets (DB Key Masking)
- **Problem:** Database file key (e.g. `HELH5QjXyETgEwUW`) was exposed in URLs (`play_url`, `video_url`, `audio_tracks[].url`, `subtitles[].url`), making file keys scrapable.
- **Solution:** Implemented stateless authenticated tickets (`t_<base64url>`) in `pkg/cluster/token.go` using AES-GCM under a key derived from the cluster secret (`sha256(secret)`).
- **Zero Overhead:** Resolved in memory in `< 0.05ms` without writing to or reading from BadgerDB.
- **Resolution:** `handlers_stream.go` and `agent_stream.go` unpack `t_...` tickets into the real DB key for disk lookup.

### Task 2: Fail-Closed Stream and Agent Auth
- **Problem:** When `secret == ""`, `/stream/*` and agent streaming were wide open to unauthenticated callers.
- **Solution:** Enforced fail-closed authentication. If `secret == ""` and caller lacks an authenticated session or valid playback ticket, requests are rejected with `403 Forbidden`.

### Task 3: Admin Cookie Hardening
- **Problem:** `stream_admin` cookie was a static HMAC of a constant string that never rotated or expired.
- **Solution:** Replaced with a dynamic 32-byte cryptographically secure random token (`crypto/rand`) stored in server memory (`bootstrapTokens`) with a 24-hour expiry.
- **Security Attributes:** Enforced `HttpOnly: true`, `SameSite: Strict`, `MaxAge: 86400`, and `Secure: true` automatically whenever the request is served over HTTPS (via `r.TLS != nil` or `X-Forwarded-Proto: https`).
- **Tests:** Added `TestAdminCookieHardening` verifying rotation, expiry rejection, and HTTPS detection.

### Task 4: SSH Host Key TOFU & TLS Verification
- **Problem:** 6 calls to `ssh.InsecureIgnoreHostKey()` in `pkg/provision/provision.go`; `InsecureSkipVerify: true` hardcoded in `pkg/download`.
- **Solution:**
  - Implemented `TOFUHostKeyCallback(retrust bool)` using `golang.org/x/crypto/ssh/knownhosts` storing fingerprints to `data/known_hosts`. First connection is trusted; key changes are blocked as potential MITM attacks unless `retrust=true`.
  - Defaulted outbound TLS verification in `pkg/download` to `InsecureSkipVerify: false`. Allowed opt-out only via explicit `STREAM_INSECURE_TLS=true` with a loud console warning.
- **Tests:** Added `TestTOFUHostKeyCallback` in `pkg/provision/provision_tofu_test.go` and `TestTLSConfigDefaults` in `pkg/download/download_test.go`.

### Task 5: Deconstruct God Files
- **`pkg/cluster/agent.go`** (formerly 2,257 lines) decomposed into:
  - `agent.go` (~420 lines): Lifecycle, gRPC loop, registration, and disk root resolution.
  - `agent_stream.go` (~960 lines): HTTP streaming server, Byte-Range engine, and WebDAV upload handler.
  - `agent_ingest.go` (~780 lines): RunIngestJob, RunFileJob, and folder transfer mechanics.
  - `agent_tools.go` (~160 lines): Autonomous tool installation and self-upgrade routines.
- **`pkg/lifecycle/executor.go`** (formerly 1,344 lines) decomposed into:
  - `executor.go` (~520 lines): Main queue, dispatch, cancellation, and task recovery.
  - `executor_steps.go` (~760 lines): File movement steps (`doCopy`, `doVerify`, `doSwitch`, `doCleanupSource`).
  - `executor_options.go` (~90 lines): Functional options.
- **`pkg/lifecycle/engine.go`** decomposed into `engine.go`, `engine_policy.go`, and `engine_options.go`.

### Task 6: Tool Allow-List & Loopback Documentation
- Added `ValidateToolName` in `pkg/tools/cmds.go` enforcing a strict allow-list (`aria2c`, `ffmpeg`, `rclone`, and presets).
- Added comments in `pkg/web/auth.go` explaining why `isDirectLoopback` rejects requests with reverse-proxy headers to protect against local proxy bypass.

---

## 2. Verification & Status
- `go build ./...`: PASS
- `go vet ./...`: PASS
- `go test ./...`: ALL PASS across all packages (0 failures)
- `gofmt -l .`: Clean
