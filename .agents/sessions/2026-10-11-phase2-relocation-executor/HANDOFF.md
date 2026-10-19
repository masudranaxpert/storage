# Session Handoff: Phase 2 — Relocation Primitive & Executor

**Date:** 2026-10-11
**Git State:** `efab9fe` (uncommitted working tree changes as requested)

## What Changed

1. **Agent Relocation & Media Size Endpoints (`pkg/cluster/agent_relocate.go`, `pkg/cluster/agent.go`):**
   - Added `POST /api/v1/relocate`: verifies `X-Cluster-Secret` / Bearer auth, `validKey`, and rejects `..` on both source and destination directories. Resolves `<src_dir>/<key>` (404 if missing), responds `202 Accepted`, and runs asynchronously with a per-`task_id` cancellable context stored in `a.relocateTasks`:
     - Same-node (`final_node_id == a.NodeID`): copies recursively into `.reloc.tmp` files with `fsync` and atomic per-file `os.Rename`, leaving the source folder untouched until the coordinator's `cleanup` step, and calls `a.registerRoot(finalDir)`.
     - Cross-node: reuses `a.transferFolder(ctx, key, srcFolder, target, report)` where `report` posts `{state, progress_percent, speed, error, transferred_bytes, total_bytes}` to `progress_url` with `X-Cluster-Secret`, never deleting the source folder on the agent side.
   - Added `POST /api/v1/relocate/cancel?task_id=`: cancels the active relocation context.
   - Added `GET /api/v1/media-size?key=&dir=`: returns `{bytes, files}` of `<dir>/<key>` (ignoring `.tmp` files).
   - Added quota reservation/commit (`a.Quota.Reserve` / `a.Quota.Commit`) and `a.registerRoot(dir)` to receiver paths (`ingest-receive`, `ingest-complete`, and `handleWebDAV`).
2. **Coordinator Relocation Executor (`pkg/lifecycle/executor.go`, `pkg/lifecycle/task.go`, `pkg/lifecycle/engine.go`):**
   - Implemented `Executor` state machine (`queued -> copying -> verifying -> switching -> cleanup -> done|failed|cancelled`) with global concurrency limit 4, per-node concurrency limit 2, 6h timeout, 3 attempts with 1m/5m/15m backoff, and `lcactive:<key>` single-active-task enforcement.
   - Implemented restart recovery (`recoverTasks`): `copying`/`verifying` restart from `copying`; `switching` checks whether `FileJob.Placement == task.To` (advancing to `cleanup`) or re-runs `doSwitch`; `cleanup` re-runs source deletion.
   - Implemented atomic `doSwitch`: re-reads `FileJob`, verifies `Placement == task.From` before updating `Placement = task.To`, and updates `lcstate` `TierSince` + `History` (capped at 20 entries).
   - Implemented `Cancel(taskID)`: allowed in `queued`, `copying`, and `verifying`; cancels agent relocation, deletes partial destination via `POST /api/v1/ingest-delete`, preserves source, and marks `cancelled`.
   - Wired `Executor` into `lifecycle.Engine` (`Executor()`, `Run(ctx)`, `MoveFile(key, targetTierID)`) and started `go srv.Engine().Run(procCtx)` in `cmd/stream/main.go`.
3. **HTTP Endpoints & OpenAPI (`pkg/web/handlers_flows.go`, `pkg/web/routes.go`, `pkg/web/server.go`, `pkg/web/auth.go`, `pkg/fileapi/service.go`, `pkg/fileapi/api.go`, `pkg/web/swagger.go`):**
   - Added `GET /api/lifecycle/tasks` (`?state=active|done|failed&limit=`).
   - Added `POST /api/lifecycle/tasks/{id}/cancel` (`requireAdmin`).
   - Added `POST /api/lifecycle/tasks/{id}/progress` (`requireSecret`).
   - Added `POST /api/v1/files/{key}/move` (`{"tier_id":N}`) via `fileapi.Service.SetMoveHandler` so `fileapi` does not import `lifecycle`.
   - Documented all four endpoints in `pkg/web/swagger.go`.
4. **Tests (`pkg/lifecycle/executor_test.go`, `pkg/web/handlers_flows_test.go`):**
   - Cross-node relocation happy path with progress callbacks, `MoveFile`, `lcactive` conflict check, and `lcstate` history check.
   - Same-node relocation happy path against real `cluster.Agent` routes.
   - Verify size mismatch -> 3 retries -> `failed` with source intact and partial destination removed.
   - Placement mutated during copy -> `failed` (`placement changed`) without deleting source.
   - Simulated coordinator restart in each state (`queued`, `copying`, `verifying`, `switching` before/after job save, `cleanup`) -> converges to `done` with single destination copy and `Placement == disk`.
   - Cancel during `copying` deletes partial destination only and leaves source intact.
   - HTTP endpoint tests for `/api/v1/files/{key}/move` and `/api/lifecycle/tasks*`.

## Files Modified / Created

- `pkg/cluster/agent.go`
- `pkg/cluster/agent_relocate.go` (new)
- `pkg/lifecycle/task.go`
- `pkg/lifecycle/executor.go` (new)
- `pkg/lifecycle/engine.go`
- `pkg/lifecycle/executor_test.go` (new)
- `pkg/fileapi/service.go`
- `pkg/fileapi/api.go`
- `pkg/web/auth.go`
- `pkg/web/server.go`
- `pkg/web/routes.go`
- `pkg/web/handlers_flows.go`
- `pkg/web/handlers_flows_test.go`
- `pkg/web/swagger.go`
- `cmd/stream/main.go`
- `.agents/plans/tier-lifecycle-flows.md`
- `.agents/INDEX.md`

## How Verified

- `gofmt -l ./pkg ./cmd` -> empty output
- `go vet ./...` -> zero warnings/errors
- `go test ./...` -> all packages pass

## Open Issues / Next Steps

- Proceed to Phase 3 (Heat, demand API & evaluator: `HitRecorder`, `play_url`, `planner.Evaluate`, `POST /api/v1/files/{key}/demand`, `GET /api/v1/files/{key}/placement`, `GET /api/flows/preview`).
