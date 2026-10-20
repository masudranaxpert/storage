# Node CORS Allowed Origins Configuration & Stream Integration

## Summary
- **Date**: 2026-10-20
- **Scope**: Added per-node configurable CORS allowed origins (`cors_allowed_origins`) across the database schema, coordinator API, worker heartbeat synchronization, media stream headers, proxy streaming, and the `/nodes` management UI.
- **Git Commit**: `0fc983f` (and docs update)
- **Deployment**: Live on NextDeploy (`stream-a050`, `http://204.136.10.31:1213`).

## Key Changes
1. **Database Model (`pkg/db/badger.go`)**:
   - Added `CORSAllowedOrigins string` to `NodeConfig` struct.
2. **CORS Helper & Unit Tests (`pkg/cluster/cors.go`, `pkg/cluster/cors_test.go`)**:
   - `MatchCORSOrigin(allowedOrigins, requestOrigin string) string` matching `*` or comma-separated allowed origins, returning reflected origin for credentials or `""` when disallowed.
3. **Cluster Agent (`pkg/cluster/agent.go`, `pkg/cluster/agent_stream.go`)**:
   - Agent dynamically stores `corsOrigins` received via heartbeat response without needing a restart.
   - Streaming handler evaluates incoming `Origin` and sets appropriate `Access-Control-Allow-Origin`, `Vary: Origin`, and `Access-Control-Allow-Credentials`.
4. **Coordinator Handlers (`pkg/web/handlers_nodes.go`, `pkg/web/handlers_stream.go`)**:
   - `handleHeartbeat` supplies `cors_allowed_origins`.
   - Node config endpoint saves `cors_allowed_origins`.
   - Stream manifests, byte-range files, and proxy streamer dynamically set matched CORS headers.
5. **Frontend UI (`web/templates/components/modal_node_detail.html`, `web/static/js/nodes.js`)**:
   - Added "CORS Allowed Origins" input field in Node Edit modal with default `*` and clear helper tooltip.
