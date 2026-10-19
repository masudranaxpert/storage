# Phase 3: Heat, Demand API & Evaluator Handoff

**Date**: 2026-10-11  
**Status**: Complete  
**Plan Reference**: `.agents/plans/tier-lifecycle-flows.md` (Phase 3)

---

## 1. Summary of Changes

Phase 3 implements automatic heat tracking, explicit demand pinning, and the pure policy evaluator per spec §3.3, §5.2 steps 1–4 and 6, §5.4, and §6 HTTP endpoints:

1. **Playback Heat & Deduplication (`pkg/lifecycle/state.go` & `pkg/web/handlers_stream.go`)**:
   - `PlayDeduper`: Bounded LRU cache (capacity 50,000, 30-minute TTL) tracking `(clientIP, key)`. Uses `container/list` + map for O(1) eviction.
   - `handleStreamManifest`: On redirect branch to worker, extracts client IP host via `net.SplitHostPort(r.RemoteAddr)` (audit #7b) and records hits exclusively for root path, manifest files (`.m3u8`, `.mpd`), or whole `.mp4` video files. Subsegment chunk requests (`.m4s`, `.ts`) are strictly ignored to prevent score explosion.
   - `HitRecorder`: Thread-safe in-memory buffer flushed periodically (every 10 seconds) or on demand (`Flush`), applying decayed score math (`Score = Decayed + Hits`) and updating `LastHitAt`.

2. **Coordinator Play URL (`pkg/fileapi/model.go` & `pkg/fileapi/service.go`)**:
   - Added additive `PlayURL: "/stream/" + key` to `StatusResponse` and `CreateResponse` in `CreateFromURL`, `ReserveUpload`, and `Status`.
   - Wired `SetPlacementHandler` and `SetDemandHandler` hooks on `fileapi.Service` to avoid circular dependencies with `lifecycle`.

3. **Pure Policy Evaluator (`pkg/lifecycle/planner.go`)**:
   - `Evaluate(Snapshot) ([]Task, []SkippedCandidate)`:
     - Pure functional evaluation with zero disk or network I/O.
     - Simulates capacity and block assignments across cloned `TierStatus` copies.
     - **Step 1 (Pins)**: Moves active pinned files not yet on their designated tier (`kind=promote` if target is faster, else `manual`).
     - **Step 2 (Promote)**: Evaluates enabled promote edges in policy order, hottest files first (`Score >= HeatAtLeast`). When target tier lacks space, triggers make-room demotions of coldest unpinned files. Pinned files on target tier are strictly protected.
     - **Step 3 (Watermark Demotes)**: Triggers when source tier crosses `HighWatermark` (default 90%), demoting coldest unpinned files until projected usage drops to `LowWatermark` (default 75%).
     - **Step 4 (Idle Demotes)**: Demotes unpinned files idle longer than `IdleDays` (`now - max(LastHitAt, TierSince) >= IdleDays`) and below `HeatBelow` threshold (hysteresis gap preventing flapping).
     - **Step 5 (Priority & Capping)**: Orders tasks by `pins -> promote (with make-room) -> watermark -> idle -> expire`, capping at `MaxTasksPerTick` (default 20).
     - Step 6 stub hook left clearly marked for Phase 4 Expire.

4. **Lifecycle Engine & Endpoints (`pkg/lifecycle/engine.go`, `pkg/web/handlers_flows.go`, `pkg/web/routes.go`)**:
   - Background ticker loop in `Engine.Run(ctx)` running evaluator and executor ticks, with `Kick()` channel for instant wakeups.
   - `GET /api/flows/preview`: Dry-run policy evaluation against live cluster capacity returning planned tasks and skipped candidates.
   - `GET /api/v1/files/{key}/placement`: Returns detailed placement, lifecycle state (decayed score, pin details, move history), active task, and projected next action with ETA and reason.
   - `POST /api/v1/files/{key}/demand`: Explicit demand endpoint (`requireAdmin`).
     - `level: "high"`: pins file to `tier_id` (or fastest reachable promote target), boosts heat score to edge's `HeatAtLeast`, and immediately kicks evaluation. TTL configurable (1–720 hours, default 24h).
     - `level: "normal"` / `"none"`: clears active pin and returns file to standard lifecycle.
     - Maps domain errors to HTTP 404, 409, and 422.

5. **API Documentation & Dashboard Guides (`pkg/web/swagger.go`, `web/templates/pages/docs.html`)**:
   - Added OpenAPI specifications for `/api/flows/preview`, `/api/v1/files/{key}/placement`, `/api/v1/files/{key}/demand`, and documented `play_url`.
   - Added "Auto-tiering & Lifecycle Flows" documentation card in `docs.html` explaining `play_url`, demand levels, and evaluator rules.

---

## 2. Modified and Created Files

- `pkg/fileapi/model.go` (added `PlayURL`)
- `pkg/fileapi/service.go` (populated `PlayURL`, wired placement and demand hooks)
- `pkg/lifecycle/state.go` (implemented `PlayDeduper`, `HitRecorder`, `IsPinned`)
- `pkg/lifecycle/planner.go` (implemented `Evaluate`, projected capacity simulation, make-room demote logic)
- `pkg/lifecycle/engine.go` (wired hits, tick loop, preview, placement view, set demand)
- `pkg/web/server.go` (initialized `PlayDeduper`, registered placement and demand handlers)
- `pkg/web/handlers_stream.go` (added play hit recording on redirect branch)
- `pkg/web/handlers_flows.go` (added `handleFlowsPreview`, `handleFilePlacement`, `handleFileDemand`)
- `pkg/web/routes.go` (registered `/api/flows/preview`)
- `pkg/web/swagger.go` (documented preview, placement, demand, play_url)
- `web/templates/pages/docs.html` (added auto-tiering documentation card)
- `pkg/lifecycle/planner_test.go` (comprehensive unit & table-driven tests)
- `pkg/web/handlers_flows_test.go` (integration tests for preview, placement, demand, play deduping)
- `.agents/plans/tier-lifecycle-flows.md` (checked off Phase 3 tasks)

---

## 3. Verification

All automated verification commands passed cleanly:
- `gofmt -l ./pkg ./cmd`: Clean (0 lines).
- `go vet ./...`: Clean (exit code 0).
- `go test ./pkg/lifecycle/...`: Passed (0.22s, including table tests for decay math, promote threshold, hysteresis flapping across 10 ticks, make-room demote ordering, pin protection, watermark cutoff, idle duration, `MaxTasksPerTick` priority, and dedupe LRU).
- `go test ./pkg/web/...`: Passed (including HTTP tests for preview, placement, demand validation, and redirect play counting).
- `go test ./...`: Passed across all packages in the repository.

---

## 4. Next Steps

Proceed to **Phase 4** of `.agents/plans/tier-lifecycle-flows.md`:
- Implement opt-in Expire step in `planner.Evaluate` and executor delete branch.
- Audit logging table in BadgerDB (`lc:audit:`) and `/api/lifecycle/audit`.
- `ExpireAcknowledged` flag and safety confirmation phrase on PUT (`"DELETE FILES"`).
