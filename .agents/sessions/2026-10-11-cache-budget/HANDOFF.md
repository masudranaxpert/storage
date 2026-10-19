# HANDOFF — Global In-Memory Cache Budget (50 MB)

**Date:** 2026-10-11

## What was done

1. **New `pkg/cache` package** — single source of truth: `MaxTotalBytes = 50 << 20` (50 MB) constant + name-keyed `Registry` aggregating per-cache `Reporter` callbacks (re-register replaces; snapshot sorted by name).
2. **PlayDeduper byte budget** (`pkg/lifecycle/state.go`): per-entry cost = `len("ip|key") + 128B` overhead estimate; inserts enforce the budget by evicting **LRU-oldest first** (`WithBudget` option for tests; count cap 50k kept as secondary bound). `Snapshot()` exposes entries/bytes/evicted.
3. **Reporters wired in `NewServer`** (`pkg/web/server.go`): `play_deduper`, `play_hit_buffer` (HitRecorder buffer, 10s flush), `relocation_inflight` (Executor), `node_usage_snapshot` (5s TTL), `ghost_strike_tracker` (fileapi strikes).
4. **`GET /api/system/cache`** (`pkg/web/handlers_system.go`, route in `routes.go`): `{limit_bytes, used_bytes, usage_pct, caches[]}`.
5. **Settings page** ("In-Memory Cache Budget" card in `settings.html` + `renderCacheCard` in `settings.js`): total usage bar (amber → amber >50% → red >80%), per-cache rows (entries · bytes · evicted), 5s polling.

## Cache inventory result

Structurally bounded (report-only): hit buffer (10s flush), relocation in-flight (task lifetime), node usage snapshot (node count), ghost strikes (file count). **Traffic-scaled = play_deduper only** → it enforces the global byte ceiling. Agent-process caches (`activeTransferEntry` + 10m reaper, `winDiskCache`) are per-VPS and bounded; BadgerDB memtable/block cache is engine-internal (not ours to cap).

## Verification

- `TestRegistryRegisterAndSnapshot`, `TestRegistryNilReporterDefaultsName`, `TestPlayDeduperByteBudget` (oldest-first eviction + evicted-counter + survivor dedupe), `TestPlayDeduperDefaultBudget` — pass.
- `go build`, `go vet`, `gofmt -l`, `go test ./...` all clean.
- Live on :1212 (supervisor restarted from rebuilt `./stream`): `/api/system/cache` returns limit 52428800; Settings card renders "119 B / 50 MB (0.0%)" with 5 rows.

## Notes

- `base.html` asset versions were bumped to `?v=16` outside this session (parallel work); cache-bust discipline: bump after CSS/JS edits.
- `pkg/web/handlers_dashboard.go` was gofmt-fixed in passing (drift from parallel edits).

## Git state

Added: `pkg/cache/`, `pkg/web/handlers_system.go`, this handoff, `.agents/plans/cache-memory-limit.md`. Modified: `pkg/lifecycle/state.go`, `pkg/lifecycle/engine.go`, `pkg/lifecycle/executor.go`, `pkg/lifecycle/planner_test.go`, `pkg/fileapi/service.go`, `pkg/web/server.go`, `pkg/web/routes.go`, `web/templates/pages/settings.html`, `web/static/js/settings.js`.
