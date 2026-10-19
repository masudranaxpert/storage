# Global In-Memory Cache Budget (50 MB)

## Requirement
- Process-wide ceiling for all key-value in-memory caches: **50 MB RAM** (`50 << 20` bytes).
- Single Go constants file as the source of truth.
- Over-budget caches evict **oldest entries first**.
- Settings page shows the limit and live per-cache usage.

## Cache inventory (long-lived, server process)

| Cache | Location | Growth bound | Enforcement |
|---|---|---|---|
| `play_deduper` | `pkg/lifecycle/state.go` | traffic (IP+key) | **byte budget + LRU oldest-first eviction** (was count-only 50k) |
| `play_hit_buffer` | `HitRecorder.counts` | flushes every 10s | report-only (transient) |
| `relocation_inflight` | `Executor.inFlight/cancelled` | task lifetime | report-only |
| `node_usage_snapshot` | `Server.usageCache` | node count (5s TTL) | report-only |
| `ghost_strike_tracker` | `fileapi.missingStrike` | file count | report-only |

Agent-process caches (`activeTransferEntry` + 10m reaper, `winDiskCache`) are per-VPS and structurally bounded — out of coordinator scope. BadgerDB memtable/block cache is engine-internal, not ours to cap.

## Design
- New `pkg/cache` package: `MaxTotalBytes = 50 << 20` constant + name-keyed `Registry` aggregating `Reporter` callbacks → `GET /api/system/cache`.
- `PlayDeduper` gains byte accounting (`len(key) + 128B overhead per entry`) and evicts LRU-oldest while over `budget` (default `MaxTotalBytes`, overridable via `WithBudget` for tests). Count cap (50k) stays as a secondary bound.
- Settings page card: total usage bar (amber → amber >50% → red >80%), per-cache rows with entries/bytes/evicted, 5s polling.

## Acceptance
- [x] `pkg/cache` constant + registry unit test
- [x] Byte-budget eviction test (oldest first, evicted counter)
- [x] `go build && go vet && go test ./...` pass
- [x] Settings card renders live usage on :1212
