# Session Handoff: Phase 1 — Tier Lifecycle Model, Policy, Ingest Order & API

**Date:** 2026-10-11
**Git Base:** `efab9fe` (uncommitted working tree, per instruction)

## Summary of Changes

1. **Tier Lifecycle Model & Defaults (`pkg/storage/tier.go`, `pkg/storage/tier_test.go`):**
   - Added `TierLifecycle{SpeedRank, HighWatermark, LowWatermark}` and additive `Lifecycle` JSON field on both `Tier` and `TierStatus`.
   - Backfilled defaults (`SpeedRank`: tier0=0, tier1=1, tier2=2, custom=10+ID; `HighWatermark=90`, `LowWatermark=75`) in `DefaultTiers`, `ensureSystemTiers`, and `Upsert`.
   - Enforced `0 < LowWatermark < HighWatermark <= 100` and `SpeedRank >= 0` in `Upsert` and `UpdateLifecycle`.

2. **Lifecycle Package (`pkg/lifecycle/{policy,state,task,store,engine}.go`, `pkg/lifecycle/policy_test.go`):**
   - Implemented `FlowPolicy`, `Edge`, `Trigger`, `EdgeKind` (`promote`, `demote`, `expire`), `TrashTierID = -1`, `FieldError`, `ValidationError`, `DefaultPolicy(tiers)`, and `Validate(p, tiers)` covering all spec §3.2 rules (empty/unknown ingest tiers, speed rank ordering, promote/demote cycle detection, multiple enabled promote edges per source tier, expire target/trigger rules, demote trigger requirement, and promote/demote hysteresis gap).
   - Implemented `FileState`, `Move`, `FileState.Decayed(now, halfLifeHours)`, `Task`, `TaskKind`, `TaskState`, `AuditEntry`, and `Store` interface.
   - Implemented `Engine` (`New`, `GetPolicy`, `SavePolicy`, `IngestOrder`, `Simulate`): persists `DefaultPolicy` on first boot, enforces optimistic concurrency (`ErrVersionConflict`), requires `"DELETE FILES"` confirmation phrase when enabling an expire edge (`ErrExpireConfirmRequired`), updates `ExpireAcknowledged`, and simulates placement across the policy ingest order.

3. **BadgerDB Persistence & DB Studio Registration (`pkg/db/lifecycle.go`, `pkg/db/admin.go`, `pkg/db/badger_test.go`):**
   - Implemented `flow:policy`, `lcstate:<key>`, `lctask:<id>`, `lcactive:<key>`, and `lcaudit:<ts>-<key>` persistence methods on `*db.Store`.
   - Pre-seeded `flow`, `lcstate`, `lctask`, `lcactive`, and `lcaudit` in `AdminTables()` so they appear in DB Studio even when 0 rows exist.

4. **Ingest Order Injection (`pkg/fileapi/service.go`, `pkg/fileapi/planner_test.go`, `pkg/web/server.go`):**
   - Added `SetIngestOrder(func() []int)` on `fileapi.Service` and updated `PlanPlacement` to iterate custom ingest order when set, preserving the legacy fallback when `nil`.
   - Wired `s.engine` and `s.files.SetIngestOrder(s.engine.IngestOrder)` in `web.NewServer`.

5. **HTTP Endpoints & OpenAPI (`pkg/web/handlers_flows.go`, `pkg/web/handlers_storage.go`, `pkg/web/routes.go`, `pkg/web/swagger.go`, `pkg/web/handlers_flows_test.go`):**
   - Added `GET /api/flows`, `PUT /api/flows` (409 on version conflict, 422 `{"errors":[...]}` on validation or missing expire confirmation), `GET /api/flows/simulate?size_bytes=` (200 or 507), and `PUT /api/tiers/{id}/lifecycle`, guarded by `requireAdmin`.
   - Documented all endpoints in `pkg/web/swagger.go`.

## Verification

- `gofmt -l ./pkg ./cmd` -> empty
- `go vet ./...` -> 0 issues
- `go test -v ./...` -> all unit and integration tests pass (`pkg/storage`, `pkg/lifecycle`, `pkg/db`, `pkg/fileapi`, `pkg/web`, `pkg/cluster`, `pkg/media`)

## Open Issues / Next Steps

- Ready for **Phase 2** (Agent relocation primitive `POST /api/v1/relocate`, `GET /api/v1/media-size`, and coordinator lifecycle executor state machine).
