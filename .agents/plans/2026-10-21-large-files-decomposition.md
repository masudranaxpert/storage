# Large God-Files Decomposition Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use subagent-driven-development (recommended) or executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

## Overview
Deconstruct the four largest "God-Files" (> 1,000 lines each) into single-responsibility, highly cohesive modules strictly following Go naming conventions (`golang-naming`), idiomatic flat style (`golang-code-style`), and Clean Architecture (`software-architecture`):
1. `internal/api/handlers_stream.go` (1,321 lines)
2. `internal/agent/agent_stream.go` (1,106 lines)
3. `internal/api/file_service.go` (1,142 lines)
4. `internal/api/handlers_nodes.go` (1,025 lines)

Target metric: Every decomposed file is focused, has a single clear purpose, and stays under ~350 lines, maintaining 100% test pass rate and backward-compatible APIs.

---

## File Structure & Decomposition Target

### 1. `internal/api/handlers_stream.go` (1,321 lines) $\to$ 4 files:
- `internal/api/handlers_stream.go` (~320 lines): Core route handler `handleStreamManifest`, ticket/token verification, CORS header injection, 307 redirect logic.
- `internal/api/handlers_stream_embed.go` (~280 lines): `renderStreamEmbed`, `formatCleanTrackTitle`, subtitle/audio track parsing.
- `internal/api/handlers_stream_embed_tmpl.go` (~620 lines): `embedPlayerTmpl` and `embedPlayerHTML` template string constant.
- `internal/api/handlers_stream_proxy.go` (~90 lines): `proxyStream` reverse-proxy fallback for offline or unroutable worker nodes.
- `internal/api/node_metrics.go` (~120 lines): `mediaUsagePerNode` live agent query and `processingProfiles` evaluator.

### 2. `internal/agent/agent_stream.go` (1,106 lines) $\to$ 4 files:
- `internal/agent/agent_stream.go` (~230 lines): `handleMedia` (HTTP byte-range video streaming, range parsing, CORS origin reflection, token validation).
- `internal/agent/agent_stream_server.go` (~350 lines): `startStreamingServer` HTTP mux configuration and lifecycle listeners.
- `internal/agent/agent_storage_api.go` (~220 lines): `getStreamDrives`, `handleWebDAV`, storage folders, and clean endpoints.
- `internal/agent/agent_viewer_tracker.go` (~110 lines): `recordViewerHeartbeat` and `recordPlayHit` throttled coordinator sync.

### 3. `internal/api/file_service.go` (1,142 lines) $\to$ 4 files:
- `internal/api/file_service.go` (~260 lines): `Service` struct, options/setters, `NewService`, `uniqueKey`, `workerPool`.
- `internal/api/file_service_ingest.go` (~380 lines): `CreateFromURL`, `ReserveUpload`, `Dispatch`, `ApplyProgress`, `Retry`.
- `internal/api/file_service_placement.go` (~260 lines): `PlanPlacement`, `placementOf`, `deleteFromWorker`, `callIngestDelete`.
- `internal/api/file_service_reconcile.go` (~240 lines): `Run`, `sweepStaleJobs`, `ReconcileGhostRecords`, `MissingStrikeStats`, `Status`, `List`, `StreamURL`.

### 4. `internal/api/handlers_nodes.go` (1,025 lines) $\to$ 3 files:
- `internal/api/handlers_nodes.go` (~350 lines): `handleNodes`, node listing, detail inspection, and heartbeat console throttle.
- `internal/api/handlers_nodes_config.go` (~350 lines): Node configuration save, enable/disable, disk quota, CORS origins, and processing profiles.
- `internal/api/handlers_nodes_remote.go` (~320 lines): Remote SSH provisioning triggers, tool installation/uninstallation (`InstallShell`/`UninstallShell`), and restart/forget actions.

---

## Task List

- [x] **Task 1: Decompose `internal/api/handlers_stream.go`**
  - [x] Extract `embedPlayerHTML` and `embedPlayerTmpl` into `internal/api/handlers_stream_embed_tmpl.go`.
  - [x] Extract `renderStreamEmbed`, track formatting, and structs into `internal/api/handlers_stream_embed.go`.
  - [x] Extract `proxyStream` into `internal/api/handlers_stream_proxy.go`.
  - [x] Extract `mediaUsagePerNode` and `processingProfiles` into `internal/api/node_metrics.go`.
  - [x] Run `go test -v ./internal/api/...` to verify 100% pass.
  - [x] Commit: `refactor(api): split handlers_stream into embed, proxy, and node metrics modules`.

- [x] **Task 2: Decompose `internal/agent/agent_stream.go`**
  - [x] Extract `recordViewerHeartbeat` and `recordPlayHit` into `internal/agent/agent_viewer_tracker.go`.
  - [x] Extract `getStreamDrives` and `handleWebDAV` into `internal/agent/agent_storage_api.go`.
  - [x] Extract `startStreamingServer` into `internal/agent/agent_stream_server.go`.
  - [x] Keep `handleMedia` and core byte-range streaming in `internal/agent/agent_stream.go`.
  - [x] Run `go test -v ./internal/agent/...` to verify 100% pass.
  - [x] Commit: `refactor(agent): split agent_stream into server, storage api, and viewer tracker`.

- [x] **Task 3: Decompose `internal/api/file_service.go`**
  - [x] Extract URL ingest and upload dispatching into `internal/api/file_service_ingest.go`.
  - [x] Extract tier placement planning and physical worker deletes into `internal/api/file_service_placement.go`.
  - [x] Extract ghost reconcile, sweep loop, and status list queries into `internal/api/file_service_reconcile.go`.
  - [x] Keep constructor, setters, and core definitions in `internal/api/file_service.go`.
  - [x] Run `go test -v ./internal/api/...` to verify 100% pass.
  - [x] Commit: `refactor(api): split file_service into ingest, placement, and reconcile modules`.

- [x] **Task 4: Decompose `internal/api/handlers_nodes.go`**
  - [x] Extract configuration and quota handlers into `internal/api/handlers_nodes_config.go`.
  - [x] Extract SSH remote actions and tool installers into `internal/api/handlers_nodes_remote.go`.
  - [x] Keep core node registry and heartbeat handlers in `internal/api/handlers_nodes.go`.
  - [x] Run `go test -v ./internal/api/...` to verify 100% pass.
  - [x] Commit: `refactor(api): split handlers_nodes into config and remote action modules`.

- [x] **Task 5: Full Repository Verification, Build & Deployment**
  - [x] Run `go vet ./...` (0 warnings).
  - [x] Run `go test -count=1 ./...` (100% pass across all packages).
  - [x] Build binary: `go build -o stream ./cmd/stream`.
  - [x] Deploy to NextDeploy: `nd deploy stream-a050 -f`.
  - [x] Verify live dashboard: `curl -I http://204.136.10.31:1213/media`.
