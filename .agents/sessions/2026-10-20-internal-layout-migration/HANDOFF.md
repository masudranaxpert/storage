# Standard Go Project Layout Migration (`internal/` Architecture)

## Summary
- **Date**: 2026-10-20
- **Scope**: Complete architectural migration of `storage` from flat/mixed `pkg/` into idiomatic Standard Go project layout under `internal/`, enforcing clean acyclic DAG layers, decoupled models, and 100% test pass rate across all packages.
- **Git Commit**: `7979b32`
- **Deployment**: Live on NextDeploy (`stream-a050`, `http://204.136.10.31:1213`).

## Key Changes
1. **Domain Model (`internal/model/`)**:
   - Zero-dependency core wire & storage domain entities (`FileRecord`, `FileJob`, `Placement`, `FileState`, `ProgressUpdate`, `CreateRequest`, `CreateResponse`, `StatusResponse`, `StreamDriveStat`, `JobStore`).
2. **Standard Internal Packages (`internal/`)**:
   - `internal/cache`: In-memory cache budget and registry (`cache.Default`).
   - `internal/telemetry`: Hardware and OS metrics, disk type detection.
   - `internal/cluster`: Coordinator, pool aggregation, gRPC mesh, worker eligibility.
   - `internal/storage`: Storage tiers, quota ceiling, block placement planner, and S3/R2 remote bucket storage.
   - `internal/lifecycle`: Heat evaluation, migration planner, state machine, and retention executor.
   - `internal/database`: BadgerDB key-value store, schema, file/job persistence.
   - `internal/agent`: VPS worker agent, direct byte-range HTTP streaming server, rclone S3 sync.
   - `internal/media`: FFmpeg probe, CMAF remuxing, parallel downloader, folder transfer.
   - `internal/provision`: Remote VPS provisioning over SSH and tool installation scripts.
   - `internal/api`: Unified HTTP server, admin dashboard, auth middleware, file ingest API, Swagger.
3. **Circular Import Resolution**:
   - Strictly enforced layered DAG (`model` -> `telemetry`/`cache` -> `cluster` -> `storage` -> `lifecycle` -> `database` -> `agent` -> `api`).
4. **Verification**:
   - `go vet ./...` passed with 0 errors.
   - `go test -count=1 ./...` passed 100% across all packages.
   - Successfully deployed to `stream-a050` and verified HTTP 200 OK on `/media` and `/docs/swagger-embed`.
