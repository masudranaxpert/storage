# Project Layout Refactor: Standard Go Structure

## Goal
Restructure the `storage` codebase from the fragmented and redundant `pkg/` structure to the idiomatic **Standard Go Project Layout** using `internal/`, clean package boundaries, and co-located tests.

## Target Architecture
- `cmd/stream/main.go` - Entry point and CLI subcommand routers
- `internal/`
  - `api/` - HTTP server, routes, middleware, handlers, swagger (consolidating `web` and `fileapi`)
  - `cluster/` - Coordinator, scheduler, gRPC mesh, node records
  - `agent/` - VPS worker agent, streaming server, local workers
  - `storage/` - Storage manager, tiers, quotas, S3/R2 remote storage
  - `media/` - Probing, CMAF packaging, downloads, ffmpeg/tools
  - `lifecycle/` - Engine, policies, eviction, hotness tracking
  - `database/` - BadgerDB storage engine and data access models (renamed from `db`)
  - `provision/` - Remote VPS bundle provisioning & SSH
  - `telemetry/` - Hardware & system telemetry
- `web/` - Embedded static assets & templates (`static/`, `templates/`)

## Execution Steps
1. Consolidate test folders (`pkg/*/test/`) into co-located `_test.go` files.
2. Merge micro-packages: `pkg/tools` into `pkg/media`, `pkg/cache` into `pkg/cluster`/`pkg/lifecycle`, `pkg/download` into `pkg/media`.
3. Separate `agent` from `cluster` into `pkg/agent`.
4. Migrate `pkg/` to `internal/`:
   - `pkg/db` -> `internal/database`
   - `pkg/fileapi` & `pkg/web` -> `internal/api`
   - `pkg/cluster` -> `internal/cluster`
   - `pkg/agent` -> `internal/agent`
   - `pkg/storage` -> `internal/storage`
   - `pkg/media` -> `internal/media`
   - `pkg/lifecycle` -> `internal/lifecycle`
   - `pkg/provision` -> `internal/provision`
   - `pkg/telemetry` -> `internal/telemetry`
5. Update all import paths from `stream/pkg/...` to `stream/internal/...`.
6. Run `go test ./...` and `go vet ./...` to verify zero regression.
7. Verify binary build `go build -o stream ./cmd/stream`.
8. Commit, push, and deploy to NextDeploy (`stream-a050`).
