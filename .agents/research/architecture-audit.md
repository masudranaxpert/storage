# Brutal Architecture & Code Quality Audit: Stream Distributed Mesh

**Date:** 2026-10-08  
**Audit Standard:** Codebase Design (Deep Modules, Seam Discipline) & 5-Axis Code Review (Correctness, Simplicity, Architecture, Security, Performance)  
**Tone:** Brutally Honest, Zero Flattery, Evidence-Based  

---

## Executive Summary

The `stream` codebase attempts to build a distributed video streaming, transcoding, and multi-VPS storage cluster from scratch in ~12,300 lines of Go. 

While the concept of orchestrating cheap commodity VPS nodes is valid, the current implementation is **dangerously fragile, architectural debt-ridden, and riddled with catastrophic security and data-loss vulnerabilities**. It reinvents distributed storage, consensus, transfer protocols, and worker scheduling poorly instead of leveraging mature, battle-tested foundations.

---

## 1. System Architecture & Seam Design Failures (`codebase-design`)

### 1.1 The God-Object Anti-Pattern (`pkg/cluster/agent.go`)
At **1,954 lines**, `agent.go` is an extreme architectural smell. In deep module design, a module should hide significant behavior behind a narrow interface. Instead, `Agent` is an unmaintainable God Object handling 10 distinct, unsegregated responsibilities:
1. Host telemetry & hardware probing (`gopsutil`).
2. Bi-directional gRPC streaming with coordinator.
3. Public-facing HTTP byte-range video streaming server.
4. Downloader management (`aria2c` execution & Go fallback).
5. Media remuxing & FFmpeg child process lifecycle.
6. Local daemon lifecycle management (`rclone rcd` process spawner).
7. WebDAV server implementation with custom auth fallback.
8. Shell script execution for OS package management (`apt-get install/purge` as root).
9. Self-upgrade binary downloads and systemd service restarts.
10. Low-level file descriptor tracking (`map[string]*os.File`).

**Consequence:** Any change to streaming risks breaking transfers, tool installation, or process isolation. It has zero locality and negative leverage.

### 1.2 Leaking Storage Boundaries & Host Coupling
The system leaks raw filesystem paths across network and protocol boundaries:
* `TransferTarget.Dir`, `Placement.Path`, and client HTTP requests specify literal directory strings (e.g. `/mnt/hdd`, `/stream/media`).
* `autoSelectBestMediaDir` contains hardcoded host-level heuristics that inspect all mounted partitions on Linux, silently overriding default storage directories with whichever partition happens to have the most free space (e.g., picking `/mnt/Windows/stream/media`). This directly broke unit tests (`TestResolveTargetDir` in `pkg/cluster/test`).
* The coordinator and agents assume POSIX paths, Debian/Ubuntu package managers (`apt-get`), and systemd, completely coupling the core storage logic to specific Linux distributions.

### 1.3 Three Competing, Half-Baked Transfer Implementations
Instead of settling on a single proven transfer protocol, the codebase contains three separate, intertwined node-to-node transfer mechanisms:
1. **Custom Parallel Chunked HTTP Streaming:** Slices files into 16MB chunks, preallocates files with `/api/v1/ingest-file-init`, streams chunks to `/api/v1/ingest-chunk`, and writes via `os.File.WriteAt`.
2. **Raw / Gzip Tar HTTP Streaming:** Serializes folder trees into tar streams (`PackFolder`/`UnpackFolder`).
3. **Rclone RCD over WebDAV:** Spawns an external `rclone rcd` daemon on `127.0.0.1:5572`, obscures passwords on the fly, and uses WebDAV endpoints (`/api/v1/webdav/`).
4. **Dead Code Artifact:** `pkg/cluster/transfer_native_backup.go` (402 lines) is dead reference code committed directly inside the production package.

---

## 2. Five-Axis Code Review & Fragility Audit (`code-review-and-quality`)

### Axis 1: Correctness & Data Integrity

#### 🚨 Critical: Eviction Policy Permanently Deletes NEWEST Files (`pkg/fileapi/service.go:200-224`)
When an HDD tier runs low on storage during an upload, `PlanPlacement` triggers an eviction loop:
```go
// Line 202: Calls newestHDDFile to pick eviction victims!
victim, err := s.newestHDDFile(hdd, receivers)
// Line 209: Permanently deletes the file from worker disk and database!
if err := s.deleteFromWorker(victim, hdd); err != nil { ... }
```
* **The Flaw:** Every standard caching/storage system evicts according to LRU (Least Recently Used) or LFU (Least Frequently Used). This codebase explicitly evicts the **NEWEST** uploaded files (`newestHDDFile` scans newest-first)!
* **Data Loss by Design:** A user uploading a 50GB file to a cluster with 40GB free space will cause existing user files uploaded moments before to be permanently erased from disk and database (`s.store.DeleteFileRecord(rec.Key)`).

#### 🚨 Critical: Watchdog Permanently Wipes Valid DB Records (`pkg/fileapi/service.go:102-132`)
`ReconcileGhostRecords` runs every 60 seconds on the coordinator. It issues a quick `HEAD` probe with a 2-second timeout (`/media/<key>/metadata.json`) to the worker:
```go
if resp.StatusCode == http.StatusNotFound {
    _ = s.store.DeleteFileJob(rec.Key)
    _ = s.store.DeleteFileRecord(rec.Key)
    purged++
}
```
If a worker disk is remounting, experiencing I/O saturation, or if an internal path lookup transiently fails with 404, the coordinator **permanently deletes** the file metadata and record from BadgerDB. There is zero grace period, zero retry count, and zero tombstone quarantine.

#### 🚨 Major: Multi-Audio Videos Silently Muted (`pkg/media/cmaf.go:56-66`)
In `RemuxAndPackageCMAF`:
```go
if numAudios > 1 {
    ffmpegArgs = []string{
        "-y",
        "-i", srcPath,
        "-map", "0:v:0?",
        "-an", // <-- SILENTLY STRIPS ALL AUDIO TRACKS!
        "-c:v", "copy",
        ...
```
If a video contains dual audio (e.g., English + Spanish or commentary), the remuxer passes `-an`, completely stripping all audio tracks. The resulting video is 100% mute.

#### 🚨 Major: "Fake CMAF" Implementation
The documentation and code comments claim to generate CMAF fragmented MP4 with `+frag_keyframe+empty_moov+default_base_moof`. In reality, the FFmpeg command runs `-movflags +faststart`, which produces an ordinary progressive MP4 file with the moov atom at the beginning. It does not produce CMAF chunks or adaptive bitrate HLS/DASH streams.

---

### Axis 2: Security & Vulnerabilities

#### 🚨 Disaster: Unauthenticated Remote Execution & VPS Tool Purge (`pkg/cluster/agent.go:530-554`)
The agent runs on public port 2052 (designed for direct viewer streaming). The following endpoints have **zero authentication**:
* `GET /api/install-tools?tool=...`: Spawns a shell running `exec.Command("bash", "-c", tools.InstallShell(tool))` as **root**.
* `GET /api/uninstall-tools?tool=...`: Runs `apt-get purge` and `rm -f /usr/bin/ffmpeg /usr/bin/rclone` as **root**.
* **Impact:** Anyone on the internet who scans port 2052 can remotely trigger package installations or purge all streaming tools from the VPS.

#### 🚨 Disaster: Arbitrary File Deletion / Path Traversal (`pkg/cluster/agent.go:916-938`)
`POST /api/v1/ingest-delete` has zero authentication and accepts `key` and `dir`:
```go
key := r.URL.Query().Get("key")
dir := a.resolveTargetDir(filepath.Clean(r.URL.Query().Get("dir")))
target := filepath.Join(dir, key)
_ = os.RemoveAll(target)
```
* **The Flaw:** `dir` is checked for `..`, but `key` is **never checked**.
* If an attacker passes `key=../../etc` or `key=../../var`, `filepath.Join` traverses out of `dir`, and `os.RemoveAll` attempts to delete arbitrary system directories with root privileges.

#### 🚨 Disaster: Coordinator Database Dump & Root Password Leak (`pkg/web/dbadmin.go` & `handlers_nodes.go`)
* `pkg/db/badger.go` stores raw `NodeConfig` containing `SSHPassword`, `SSHUser`, and `SSHHost` in plain text.
* `pkg/web/dbadmin.go` exposes `GET /api/dbadmin/rows?table=nodes` and `/api/dbadmin/entry` without any authentication.
* Any visitor to coordinator port 1212 can query `/api/dbadmin/rows?table=nodes` and dump the root SSH passwords of all connected VPS nodes.
* `POST /api/dbadmin/truncate` allows any anonymous visitor to drop entire database tables with a single JSON request.

#### 🚨 High: MitM via Disabled Host Key & Certificate Checks
* `pkg/provision/provision.go:85`: Uses `ssh.InsecureIgnoreHostKey()`. Vulnerable to SSH Man-in-the-Middle attacks during automated node provisioning.
* `pkg/download/downloader.go:72, 147`: Uses `--check-certificate=false` and `tls.Config{InsecureSkipVerify: true}`. Any attacker on the WAN path can inject poisoned payloads during file downloads.

---

### Axis 3: Architecture & Reliability

#### Single Point of Failure (SPOF)
The coordinator maintains an embedded BadgerDB instance on a single host. There is no cluster consensus (Raft/Paxos), no leader election, and no metadata replication. If the coordinator VPS fails, the entire cluster becomes unresponsive and unmanageable.

#### No Storage Redundancy or Erasure Coding
Storage blocks sit on individual VPS disks with zero data replication across nodes. If a single VPS provider has hardware failure or terminates a VPS, all video assets on that node are permanently destroyed.

#### Synchronous Blocking HTTP Loops in Request Path
`mediaUsagePerNode` in `pkg/web/handlers_stream.go` executes sequential HTTP GET requests to `/api/v1/storage-folders` on every active node with a 1-second timeout. With 30 cluster nodes, this loop can block web request handlers for up to 30 seconds synchronously.

---

### Axis 4: Performance & Resource Leaks

#### Open File Descriptor Leaks (`pkg/cluster/agent.go:758-797`)
`activeTransferFiles` caches open `*os.File` handles in a map (`map[string]*os.File`) during chunk ingest.
* If a sender fails mid-upload or network drops before `/api/v1/ingest-complete` is called, the file handle remains open forever.
* Under real-world traffic, the agent will exhaust OS file descriptor limits (`too many open files`).

#### Zombie Process Leaks (`pkg/cluster/rclone_rc.go:228-240`)
`ensureRcloneRCD` launches `cmd := exec.Command("rclone", "rcd", ...)` with `cmd.Start()`, but discards the `*exec.Cmd` reference once the function returns. The background process is orphaned with no lifecycle tracking, graceful shutdown, or health supervision.

#### Unnecessary Database Mutex Bottleneck
BadgerDB is natively optimized for high-concurrency ACID transactions using lock-free data structures. In `pkg/db/badger.go`, every database read and write is wrapped in `s.mu.Lock()` or `s.mu.RLock()`, effectively turning a high-performance LSM storage engine into a single-threaded bottleneck.

---

### Axis 5: Testing & Verification Deficits

* **Running `go test ./...` currently FAILS:**
  ```
  --- FAIL: TestResolveTargetDir (0.00s)
      agent_storage_test.go:30: ResolveTargetDir("") = "/mnt/Windows/stream/media", expected "data/media"
      FAIL stream/pkg/cluster/test
  ```
* **Zero test coverage:** Over 70% of packages (`cmd/stream`, `pkg/download`, `pkg/media`, `pkg/provision`, `pkg/storage`, `pkg/telemetry`, `pkg/tools`, `pkg/web`) have no test files whatsoever.

---

## Conclusion & Verdict

The current architecture is a classic **"Distributed Monolith with DIY Storage"**. It suffers from:
1. Re-inventing file systems and transfer mechanisms that already exist in far superior open-source tools.
2. Catastrophic security vulnerabilities (unauthenticated root commands, path traversal, plaintext credential dumps).
3. Critical data loss flaws (evicting newest user files, deleting DB records on transient 404s).

**Recommendation:** Do NOT continue piling features onto this foundation. Decouple storage, queuing, and compute using proven open-source primitives. Detailed blueprints are in `open-source-alternatives-and-redesign.md`.
