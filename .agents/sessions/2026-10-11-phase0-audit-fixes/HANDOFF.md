# Session Handoff: Phase 0 — Security & Audit Fixes

**Date:** 2026-10-11
**Git State:** `efab9fe` (dirty working tree; uncommitted Phase 0 changes)
**Plan:** [../../plans/tier-lifecycle-flows.md](../../plans/tier-lifecycle-flows.md)

## What Changed

1. **Authenticated Progress Callback (`POST /api/v1/files/{key}/progress`):**
   - `pkg/fileapi/api.go`: Added `validSecret(r)` using `crypto/subtle.ConstantTimeCompare` against `X-Cluster-Secret` or `Authorization: Bearer`. Allows unauthenticated requests only when `s.secret == ""` (dev mode).
   - `pkg/cluster/agent.go`: Updated `fileProgressReporter` to construct requests with `http.NewRequest`, attach `X-Cluster-Secret` when `a.Secret != ""`, and drain/close response bodies.

2. **Shared Admin Authorization & Browser Session Cookie (`pkg/web/auth.go`, `pkg/web/dbadmin.go`, `pkg/web/handlers_dashboard.go`):**
   - Replaced the spoofable `Sec-Fetch-Site: same-origin` check and prefix-based `127.0.0.1` check in `pkg/web/dbadmin.go` with `s.requireAdmin(w, r)`.
   - `isLoopbackAddr` parses `r.RemoteAddr` via `net.SplitHostPort` + `net.ParseIP(host).IsLoopback()`.
   - **Browser UI Session Choice:** On any `GET` to a valid HTML dashboard route (`handleDashboard`), `setAdminCookie` sets an `HttpOnly`, `SameSite=Strict` cookie named `stream_admin` containing `hex(HMAC-SHA256(secret, "admin-ui"))`. `requireAdmin` verifies this cookie in constant time so non-loopback browsers can perform Database Studio mutations without exposing the cluster secret in client-side JS, while cross-site requests and spoofed `Sec-Fetch-Site` headers from `curl` are rejected.

3. **Context-Managed FileAPI Watchdog (`pkg/fileapi/service.go`, `pkg/web/server.go`, `cmd/stream/main.go`):**
   - Removed the unstopped `go s.startWatchdogLoop()` goroutine from `fileapi.NewService`.
   - Added `func (s *Service) Run(ctx context.Context)` and wired it in `cmd/stream/main.go` (`runCoordinator`) with a process-scoped context cancelled on `SIGINT`/`SIGTERM`.

4. **3-Strike Ghost Reconciliation (`pkg/fileapi/service.go`):**
   - `ReconcileGhostRecords` now tracks consecutive `404 Not Found` responses per key in `s.missingStrike` (guarded by `s.strikeMu`), resetting on any non-404 response and purging DB rows only on the 3rd consecutive 404. Sends `X-Cluster-Secret` on probe requests when configured.

5. **Placement Signature & Tier Storage Cleanup (`pkg/fileapi/service.go`, `pkg/storage/tier.go`):**
   - Dropped the vestigial `evicted []string` return value from `PlanPlacement` and updated callers (`CreateFromURL`, `ReserveUpload`).
   - Removed the unused `migrated` variable in `ensureSystemTiers` and logged all `SaveTier`/`DeleteTier` errors in `ensureSystemTiers` and `Upsert`.

6. **Offline Owner `StreamURL` Guard (`pkg/fileapi/service.go`):**
   - `StreamURL` returns `""` when no `PublicHost` is configured and the owning node is missing or `cluster.StatusOffline`.

7. **Parallelized & Cached `mediaUsagePerNode` (`pkg/web/handlers_stream.go`, `pkg/web/server.go`):**
   - Queries online pool nodes concurrently using `sync.WaitGroup` and a per-node 1s context timeout, merges results under a mutex, and caches the resulting map on `Server` for 5 seconds.

## Files Modified / Created
- `pkg/web/auth.go` (new)
- `pkg/web/dbadmin.go`
- `pkg/web/handlers_dashboard.go`
- `pkg/web/handlers_stream.go`
- `pkg/web/server.go`
- `pkg/web/test/server_test.go`
- `pkg/fileapi/api.go`
- `pkg/fileapi/service.go`
- `pkg/fileapi/planner_test.go`
- `pkg/cluster/agent.go`
- `pkg/storage/tier.go`
- `cmd/stream/main.go`
- `.agents/plans/tier-lifecycle-flows.md`
- `.agents/INDEX.md`

## How Verified
- `gofmt -l ./pkg ./cmd` → empty
- `go vet ./...` → pass
- `go test -v ./...` → all unit & integration tests pass (`TestRequireAdmin`, `TestProgressAuth`, `TestReconcileGhostRecordsThreeStrikes`, `TestStreamURLOfflineAndRunContext`)

## Open Issues / Next Steps
- Proceed to Phase 0b (Frontend component split) and Phase 1 (TierLifecycle model, policy validation, and dynamic ingest order).
