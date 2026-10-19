# Session Handoff: Phase 4 Expire & Audit Trail

**Date**: 2026-10-11
**Phase**: Phase 4 of [Tier Data-Flow & Lifecycle Engine](../../plans/tier-lifecycle-flows.md)
**Git State**: Branch `main`, working tree uncommitted (Phases 0–4 changes ready).

---

## 1. Summary of Changes

Phase 4 implements the opt-in Expire lifecycle action and permanent audit logging, ensuring conservative, irreversible file deletions only when explicitly permitted.

### Core Changes

1. **Expire Evaluation in Planner (`pkg/lifecycle/planner.go`)**:
   - Implemented Step 5 in pure `Evaluate`: fires only when `snap.Policy.ExpireAcknowledged == true`.
   - Evaluates `AgeDays` (`now - TierSince`), `IdleDays` (`now - max(LastHitAt, TierSince)`), and optional `OnWatermark` gating.
   - On-watermark expire rule: only fires when source tier usage $\ge$ `HighWatermark` AND no demote edge leaving that tier has room for the candidate. Expire stops as soon as the tier drops to $\le$ `LowWatermark`.
   - Strict protections: pinned files (`PinTierID != nil`) and files with active tasks are never expired.
   - Ordering: candidates are evaluated and expired oldest-first (`tierSince` ascending, then `CreatedAt` ascending).
   - Priority: expire tasks occupy the lowest priority tier under `MaxTasksPerTick` (`pins -> promote -> watermark -> idle -> expire`).

2. **Atomic Executor Expire Branch (`pkg/lifecycle/executor.go`)**:
   - `executeExpireTask` runs with guaranteed audit logging and failure atomicity:
     - **Step 1 (Audit First)**: Persists `AuditEntry` (`lcaudit:<ts>:<key>`) via `store.SaveLifecycleAudit` *before* issuing any deletion. If audit persistence fails, the task fails immediately without touching the agent.
     - **Step 2 (Agent Ingest-Delete)**: Calls worker agent `/api/v1/ingest-delete` on source placement with retries and exponential backoff. If worker deletion fails, database records (`FileJob`, `FileRecord`, `lcstate`) are **preserved** to prevent ghost missing files.
     - **Step 3 (Metadata Cleanup)**: Only after agent deletion succeeds are `FileJob`, `FileRecord`, and `lcstate` deleted, and active task references cleared.

3. **Trigger Tightening Guard (`pkg/lifecycle/engine.go`)**:
   - `SavePolicy` detects both newly enabled expire edges and tightened existing expire triggers (e.g., smaller `AgeDays`, smaller `IdleDays`, or removal of `OnWatermark` constraint).
   - Any tightening requires `confirm == "DELETE FILES"`; otherwise, returns `ErrExpireConfirmRequired` (HTTP 422).
   - Exposes `Engine.ListAudits(limit int)` and `Engine.Store()`.

4. **HTTP API & OpenAPI Specification (`pkg/web/`)**:
   - Route registered in `pkg/web/routes.go`: `mux.HandleFunc("/api/lifecycle/audit", s.handleLifecycleAudit)`.
   - Handler in `pkg/web/handlers_flows.go`: `GET /api/lifecycle/audit?limit=` returns newest-first audits.
   - Documented in `pkg/web/swagger.go` under `Tier Lifecycle` tags in the OpenAPI specification.

---

## 2. Verification & Testing

1. **Unit & Integration Tests**:
   - `pkg/lifecycle/planner_test.go`:
     - `TestEvaluateExpireRules`: verifies no tasks when `!ExpireAcknowledged`, pin protection, active task exclusion, and oldest-first ordering.
     - `TestEvaluateExpireOnWatermarkPreference`: verifies demote is preferred over expire when demote targets have capacity, and watermark expire only triggers when source tier is under watermark pressure and demote cannot fit.
   - `pkg/lifecycle/policy_test.go`:
     - `TestSavePolicyVersionConflictAndExpireConfirm`: verifies `ErrExpireConfirmRequired` when enabling expire edges or tightening triggers (`AgeDays: 30 -> 15`, removing `OnWatermark`), and success with `"DELETE FILES"`.
   - `pkg/lifecycle/executor_test.go`:
     - `TestExecutorExpireTaskSuccess`: verifies audit entry is created, agent deletes folder on disk, and DB rows (`FileJob`, `FileRecord`, `lcstate`, `lcactive`) are deleted.
     - `TestExecutorExpireTaskAgentFailurePreservesDatabase`: verifies that if the agent delete fails, the audit entry is recorded, but database records remain intact.
   - `pkg/web/handlers_flows_test.go`:
     - `TestLifecycleAuditEndpoint`: verifies `GET /api/lifecycle/audit?limit=` returns newest-first audits with correct limits and HTTP 405 on non-GET.

2. **Quality Checks**:
   - `gofmt -l ./pkg ./cmd`: Clean (0 unformatted files).
   - `go vet ./...`: Passed clean.
   - `go test ./...`: All tests pass cleanly.

---

## 3. Next Steps (Phase 5)

- Build **Data Flow UI** (`/flows` page):
  - Canvas with inline SVG for tier nodes, drag-to-connect rules, watermark capacity bars, and rule summary labels.
  - Right panel tabs: Rule editor, Preview dry-run, Task queue with progress/cancel, Hot files list with pin actions, Simulate placement tool, and Expire audit log.
  - Expire safety modal: requiring confirmation typing `"DELETE FILES"` when enabling or tightening expire rules.
  - File inspector drawer in Media table.
