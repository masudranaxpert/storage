# Large God-Files Decomposition Session Handoff

## Summary
Deconstructed all four large "God-Files" (> 1,000 lines each) into single-responsibility, cohesive Go modules following standard Go conventions (`golang-naming`, `golang-code-style`, `ponytail`), preserving 100% backward compatibility and test passing rate.

## Target Files & Deconstructed Layout

### 1. `internal/api/handlers_stream.go` (Originally 1,321 lines $\to$ 339 lines)
- `internal/api/handlers_stream.go` (339 lines): Core streaming router `handleStreamManifest`, ticket/token auth, CORS origin reflection, 307 redirect.
- `internal/api/handlers_stream_embed.go` (178 lines): HTML embed player rendering (`renderStreamEmbed`), subtitle/audio formatting.
- `internal/api/handlers_stream_embed_tmpl.go` (622 lines): Video.js & Plyr HTML embed template string constants.
- `internal/api/handlers_stream_proxy.go` (59 lines): `proxyStream` reverse-proxy fallback for unroutable agents.
- `internal/api/node_metrics.go` (153 lines): `mediaUsagePerNode` live agent query and `processingProfiles` evaluator.

### 2. `internal/agent/agent_stream.go` (Originally 1,106 lines $\to$ 143 lines)
- `internal/agent/agent_stream.go` (143 lines): `handleMedia` (HTTP byte-range video streaming, token/ticket verification, CORS headers).
- `internal/agent/agent_stream_server.go` (688 lines): `startStreamingServer` HTTP mux setup, tool installer/uninstaller, TCP buffer tuning.
- `internal/agent/agent_storage_api.go` (181 lines): `getStreamDrives`, WebDAV rclone ingest handler, `validKey`.
- `internal/agent/agent_viewer_tracker.go` (112 lines): In-memory viewer heartbeat throttler and play hit telemetry reporter.

### 3. `internal/api/file_service.go` (Originally 1,142 lines $\to$ 207 lines)
- `internal/api/file_service.go` (207 lines): Struct definition, `NewService`, functional setters, `uniqueKey`, `workerPool`.
- `internal/api/file_service_ingest.go` (423 lines): `CreateFromURL`, `ReserveUpload`, `Dispatch`, `ApplyProgress`, `Retry`.
- `internal/api/file_service_placement.go` (172 lines): `PlanPlacement`, `placementOf`, `deleteFromWorker`, `callIngestDelete`.
- `internal/api/file_service_reconcile.go` (378 lines): `Run`, `sweepStaleJobs`, `ReconcileGhostRecords`, `Status`, `List`, `StreamURL`.

### 4. `internal/api/handlers_nodes.go` (Originally 1,025 lines $\to$ 165 lines)
- `internal/api/handlers_nodes.go` (165 lines): Registry queries (`handleGetNodes`, `handleGetPool`), throttled heartbeat console logs, subroute dispatcher.
- `internal/api/handlers_nodes_config.go` (239 lines): Node configuration save, quota allocation, processing profile configuration.
- `internal/api/handlers_nodes_remote.go` (672 lines): Remote VPS provisioning, SSH service restart, redeploy, tool installation/uninstallation.

## Verification
- `go vet ./...`: 0 warnings.
- `go test -count=1 ./...`: 100% pass across all packages (`agent`, `api`, `cache`, `cluster`, `database`, `lifecycle`, `media`, `provision`, `storage`).
- `go build -o stream ./cmd/stream`: Succeeded.
