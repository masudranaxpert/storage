# Research: Tier Data-Flow & Lifecycle Engine

**Date:** 2026-10-11
**Plan:** [../../plans/tier-lifecycle-flows.md](../../plans/tier-lifecycle-flows.md)

## 1. Codebase health check (2026-10-11)

| Check | Result |
|---|---|
| `go build ./...` | pass |
| `go vet ./...` | pass |
| `go test ./...` | pass |
| `gofmt -l` | 18 files unformatted → **fixed** (`gofmt -w`) |

### Defects found

| # | Severity | Location | Issue | Status |
|---|---|---|---|---|
| 1 | High | `pkg/web/handlers_stream.go` `mediaUsagePerNode` | Per-disk loop overwrote every measured block with the node-wide media total. A node with 2 blocks in 2 tiers charged both tiers the full node usage → wrong tier free space → wrong placement / premature "tier full". | **Fixed** (fallback only for unmeasured disks) |
| 2 | Medium | `pkg/fileapi/service.go` `ReconcileGhostRecords` | Deletes DB rows on a single 2s HEAD 404. Admin-triggered only (`/api/v1/files-sync`), so not automatic, but still no retry/quarantine. | Open → plan Phase 0 |
| 3 | Medium | `pkg/fileapi/service.go` `NewService` | Watchdog goroutine started in constructor with no stop/context (violates "never leak goroutines"). | Open → plan Phase 0 |
| 4 | Medium | `pkg/web/handlers_stream.go` `mediaUsagePerNode` | Synchronous HTTP fan-out (1s timeout per node) on every `GET /api/tiers` and every placement. Latency grows linearly with node count. | Open → cache via telemetry in Phase 1 |
| 5 | Low | `pkg/fileapi/service.go` `StreamURL` | Returns an agent URL even when the owning node is offline. | Open → replica-aware routing fixes it |
| 6 | Low | `pkg/fileapi/service.go` `PlanPlacement` | Second return value (`evicted`) is always nil — vestigial from removed eviction. | Open |
| 7 | Low | `pkg/storage/tier.go` `ensureSystemTiers` | `migrated` assigned and discarded; all `SaveTier` errors ignored. | Open |
| 7a | **High (security)** | `pkg/fileapi/api.go` `handleProgress` | `POST /api/v1/files/{key}/progress` is unauthenticated; anyone can flip a job to completed/failed. Agent reporter (`agent.go` `fileProgressReporter`) sends no secret either. | Open → Phase 0 |
| 7b | **High (security)** | `pkg/web/dbadmin.go` | Mutation guard trusts `Sec-Fetch-Site: same-origin`, a client-controlled header; `curl -H 'Sec-Fetch-Site: same-origin'` bypasses the secret. | Open → Phase 0 |
| 8 | Structure | `web/static/js/app.js` (3,535 lines) | Dashboard, nodes, storage, media, settings logic in one file; only `tiers.js`/`database.js` split out. `components.js` (`window.UI`) exists but is used by few pages. Not component-based yet. | Open → plan Phase 0 (split per page) |
| 9 | Structure | `pkg/cluster/agent.go` (2,094 lines) | Still a god object (see [architecture audit](../architecture-audit.md) §1.1). | Tracked in [cluster redesign](../../plans/cluster-and-agent-redesign.md) |

## 2. What already exists that the feature can build on

- **Tiers** (`pkg/storage/tier.go`): system tiers 0 (NVMe hot), 1 (SSD warm), 2 (HDD cold, default ingest); custom tiers; exclusive blocks with quota and `PublicHost`.
- **Placement** (`fileapi.PlanPlacement`): default tier → tier 1 overflow → any tier. Static, ingest-time only. No movement after ingest.
- **Transfer primitive** (`Agent.transferFolder`): rclone rcd → parallel chunk HTTP → tar fallback. Already moves a finished folder node→node. Reusable for relocation.
- **Delete primitive** (`POST /api/v1/ingest-delete` on agent, quota reclaim included).
- **Routing**: coordinator `/stream/{key}` 307-redirects to `fileapi.StreamURL(job)` → agent or block `PublicHost`. This redirect is the only point where the coordinator sees plays.

## 3. Gaps

1. One file = one location (`FileJob.Placement`). No replica concept → promotion can only be a move, and eviction would delete the only copy.
2. No access/demand signal anywhere. Agents serve bytes directly; coordinator only sees the initial redirect.
3. No rule storage, no background mover, no move-task table.
4. UI has no view of "where does a file go and why".

## 4. Constraint from AGENTS.md

> Never: delete user files for cache eviction.

User decisions (2026-10-11): promotion is a **move** (single copy per file), so "eviction" means *demote to a colder tier*, never delete. Permanent deletion is a separate **opt-in expire rule** the user explicitly approved as an exception, guarded by typed confirmation and an audit log. Agent heartbeat hit reporting (proto change) is **deferred**.
