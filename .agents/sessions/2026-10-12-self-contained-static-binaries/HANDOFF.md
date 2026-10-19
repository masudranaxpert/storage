# Handoff: Self-Contained Tuned Static Binaries Architecture

**Date:** 2026-10-12  
**Topic:** Self-Contained Static Binaries Bundle Packaging, Autonomous Bundle Upgrades, Distro-Independence  
**Git Branch:** `main`  
**Version:** `v2.7.0`  

---

## 1. Context & Motivation
- Previously, the cluster relied on dynamic host binaries or distro packages (`apt-get install -y ffmpeg aria2`), causing version discrepancies (e.g. Ubuntu 22.04 on ffmpeg 4.4.2 vs Ubuntu 24.04 on ffmpeg 6.1.1) and broken shared library crashes (`libavdevice.so.62`).
- The user requested uniform, tuned, statically-linked standalone binaries bundled into the deployment so all worker nodes run the exact same tuned binaries (`ffmpeg`, `ffprobe`, `aria2c`, `rclone`, `stream`) in `/opt/stream/bin/`, with full autonomous upgrade capability upon version changes.

---

## 2. Changes Completed
1. **Static Media Binaries Staging (`bin/`):**
   - Downloaded and verified 100% statically-linked x86-64 Linux binaries with zero `.so` dependencies (`not a dynamic executable`):
     - `bin/ffmpeg` (v7.0.2 static build)
     - `bin/ffprobe` (v7.0.2 static build)
     - `bin/aria2c` (v1.37.0 musl static build)
     - `bin/rclone` (standalone Go static build)
   - Staged in `bin/` (ignored by git in `.gitignore`).

2. **Bundle Packaging Engine (`pkg/provision/bundle.go`):**
   - Added `isStaticELF(path string) bool` using Go's `debug/elf` to ensure only verified statically linked ELF binaries enter the worker bundle, preventing host dynamically linked binaries from leaking.
   - Added `findProjectRoot()` to locate the project root and `bin/` directory reliably regardless of caller CWD.
   - Package all 4 static media binaries alongside `stream` binary into `stream-bundle-linux-amd64.tar.gz`.
   - Automatic cache invalidation: re-generates bundle if any tool binary has a newer modification time than the archive.

3. **Autonomous Bundle Upgrade Engine (`pkg/cluster/agent.go`):**
   - Updated `triggerSelfUpgradeAsync`:
     - Detects gzip magic bytes (`0x1f 0x8b`) in the upgrade payload.
     - Automatically unpacks regular files inside `stream/bin/*` to `/opt/stream/bin/*` via atomic temporary files (`.new`) to prevent `ETXTBSY`.
     - Refreshes `/usr/local/bin/stream` symlink.
     - Triggers systemd restart and cleanly exits (`os.Exit(0)`).
   - Serves bundle tarball as default download URL in `grpc_hub.go` and `handlers_nodes.go`.

4. **Telemetry & Version Bump:**
   - Bumped `telemetry.CurrentVersion` to `v2.7.0`.
   - Improved `checkTool` in `pkg/telemetry/telemetry.go` to inspect `/opt/stream/bin/` directly in addition to `PATH`.

5. **Testing & Verification:**
   - Added `pkg/provision/bundle_test.go` verifying static binary detection and tarball contents.
   - All Go tests pass: `go test ./...`.
   - Both live cluster nodes deployed and confirmed running `v2.7.0` with active static FFmpeg:
     - `VPS-01` (`204.136.10.31`): `online`, `v2.7.0`.
     - `vps-104-244-76-154` (`104.244.76.154`): `online`, `v2.7.0`.
   - Verified live test transcoding and probing (`libx264 -movflags +faststart`) as unprivileged user `stream` on both VPS nodes with exit code 0.
