# Plan: Tier Data-Flow Canvas & Lifecycle Engine

**Date:** 2026-10-11
**Status:** APPROVED design — implementation delegated via [agent prompts](tier-lifecycle-prompts.md)
**Research:** [../research/tier-lifecycle/research.md](../research/tier-lifecycle/research.md)

## 0. User decisions (2026-10-11)

| Question | Decision |
|---|---|
| Promote mode | **Move**: one physical copy per file at all times, except during a transfer |
| Permanent expire rule | **Yes, opt-in**: typed confirmation plus an audit log |
| Agent heartbeat hit report (proto change) | **Deferred**: no wire protocol change in this plan |
| `app.js` split | Done by a separate agent ([prompt B](tier-lifecycle-prompts.md#prompt-b--frontend-component-split)) |

## 1. Goal

A new **Data Flow** sidebar menu where the admin wires tiers together like a network diagram. Every wire is a rule:

- **Ingest**: new files land on tier X, with overflow to Y, then Z.
- **Promote**: when a file becomes hot (plays, or an explicit API "high demand" call), **move** it to a faster tier.
- **Demote**: after N idle days, or when a tier crosses its high watermark, move the coldest files down until the tier reaches its low watermark. A demote never deletes anything.
- **Expire** (opt-in): permanently delete a file after N days on a tier, optionally only under capacity pressure.

For any file the admin can see where it is, why it is there, and what will happen to it next.

## 2. Invariants (non-negotiable)

1. **One authoritative location.** `FileJob.Placement` is the single source of truth for where a file lives and where the stream points.
2. **Move protocol:** `copy → verify → switch → cleanup`. The source is deleted only after the destination has been verified **and** `FileJob.Placement` has switched to it. If a crash happens at any step, the system can either resume or leave an orphan copy that a later run deletes. No step can lose data.
3. **Demote/evict never deletes.** Only an `expire` edge deletes, and only if `FlowPolicy.ExpireAcknowledged == true`. Every expire delete is written to `lcaudit:`.
4. **One active task per file key.** Pinned files are never demoted or expired while the pin is active.
5. **No wire-protocol change.** Relocation progress uses HTTP to the coordinator, not gRPC `JobProgress`.
6. Only `StateCompleted` files are eligible for any lifecycle action.

## 3. Data model

### 3.1 Tier lifecycle settings (additive field on `storage.Tier`)

```go
// pkg/storage/tier.go
type TierLifecycle struct {
    SpeedRank     int     `json:"speed_rank"`     // 0 = fastest; defaults: tier0=0, tier1=1, tier2=2, custom=10+ID
    HighWatermark float64 `json:"high_watermark"` // % of usable; 0 = disabled; default 90
    LowWatermark  float64 `json:"low_watermark"`  // % of usable; default 75; must be < High
}
// Tier gets: Lifecycle TierLifecycle `json:"lifecycle"`
```
`ensureSystemTiers` backfills defaults when the values are zero. `TierStatus` mirrors `Lifecycle`.

### 3.2 Flow policy (`pkg/lifecycle/policy.go`, table `flow:policy`, single document)

```go
type EdgeKind string // "promote" | "demote" | "expire"

const TrashTierID = -1 // sink node for expire edges

type Trigger struct {
    HeatAtLeast  float64 `json:"heat_at_least,omitempty"`  // promote: decayed score threshold
    HeatBelow    float64 `json:"heat_below,omitempty"`     // demote hysteresis: only if score < this
    IdleDays     int     `json:"idle_days,omitempty"`      // demote/expire: no hit for N days
    AgeDays      int     `json:"age_days,omitempty"`       // expire: on this tier for N days (TierSince)
    OnWatermark  bool    `json:"on_watermark,omitempty"`   // demote/expire: run only when From tier ≥ HighWatermark, until ≤ LowWatermark
}

type Edge struct {
    ID      string   `json:"id"`      // "e_" + 8 random URL-safe chars
    Kind    EdgeKind `json:"kind"`
    From    int      `json:"from"`
    To      int      `json:"to"`      // TrashTierID for expire
    Trigger Trigger  `json:"trigger"`
    Enabled bool     `json:"enabled"`
}

type FlowPolicy struct {
    Version            int       `json:"version"`             // optimistic concurrency: PUT must send current version
    Ingest             []int     `json:"ingest"`              // tier priority for new files
    Edges              []Edge    `json:"edges"`
    HalfLifeHours      float64   `json:"half_life_hours"`     // heat decay, default 24
    TickSeconds        int       `json:"tick_seconds"`        // evaluator period, default 60
    MaxTasksPerTick    int       `json:"max_tasks_per_tick"`  // default 20
    ExpireAcknowledged bool      `json:"expire_acknowledged"` // must be true for any expire edge to run
    UpdatedAt          time.Time `json:"updated_at"`
}
```

**Default policy** (written on first boot) reproduces today's behaviour: `Ingest = [defaultTier, 1, …remaining by SpeedRank]`, `Edges = []`.

**`Validate(p, tiers)`** must reject:
- unknown tier IDs, and an empty `Ingest`
- `promote` where `To.SpeedRank >= From.SpeedRank`, or `demote` where `To.SpeedRank <= From.SpeedRank`
- cycles within promote edges or within demote edges
- more than one enabled promote edge from the same `From`
- `expire` where `To != TrashTierID`, or an `expire` edge with neither `AgeDays` nor `IdleDays`
- a `demote` edge with no trigger
- a promote `HeatAtLeast` ≤ the paired demote `HeatBelow` (the hysteresis gap must be > 0)

### 3.3 Per-file lifecycle state (`pkg/lifecycle/state.go`, table `lcstate:<key>`)

```go
type FileState struct {
    Key        string     `json:"key"`
    Score      float64    `json:"score"`       // decayed heat at ScoreAt
    ScoreAt    time.Time  `json:"score_at"`
    LastHitAt  time.Time  `json:"last_hit_at"` // zero → use FileRecord.CreatedAt
    TotalHits  int64      `json:"total_hits"`
    TierSince  time.Time  `json:"tier_since"`  // set on ingest completion and on every switch
    PinTierID  *int       `json:"pin_tier_id,omitempty"`
    PinUntil   time.Time  `json:"pin_until,omitempty"`
    History    []Move     `json:"history"`     // last 20 moves (from, to, kind, reason, at)
}
// Decayed(now) = Score * 2^(-(now-ScoreAt)/halfLife)
```
Hits are buffered in memory (`map[key]int` behind a mutex) and flushed every 10s, which keeps BadgerDB writes off the hot path.

### 3.4 Lifecycle task (`pkg/lifecycle/task.go`, table `lctask:<id>`)

```go
type TaskKind  string // "promote" | "demote" | "expire" | "manual"
type TaskState string // queued → copying → verifying → switching → cleanup → done | failed | cancelled

type Task struct {
    ID, Key      string
    Kind         TaskKind
    EdgeID       string             // "" for manual / demand
    Reason       string             // human-readable, e.g. "heat 42.1 ≥ 30 (edge e_ab12)"
    From         fileapi.Placement
    To           fileapi.Placement  // zero for expire
    State        TaskState
    Attempts     int
    Error        string
    Progress     float64
    Speed        string
    Bytes        int64
    CreatedAt, UpdatedAt, StartedAt time.Time
}
```
An active-key index (`lcactive:<key>` → task ID) enforces one task per file.

### 3.5 Audit (`lcaudit:<unix-nano>-<key>`)

`{key, filename, size, tier, node, path, edge_id, reason, at}` is written **before** each expire delete is issued.

## 4. Components & seams

```
pkg/lifecycle/          (new; depends on storage, fileapi, cluster — fileapi must NOT import lifecycle)
  policy.go     FlowPolicy, Edge, Validate, DefaultPolicy
  state.go      FileState, decay, HitRecorder (buffered)
  task.go       Task, states
  store.go      Store interface (implemented by db.Store)
  planner.go    Evaluate(now, snapshot) []Task   ← pure function, table-driven tests
  executor.go   Executor: queue, concurrency, run task state machine
  engine.go     Engine: New(opts...), Run(ctx), Kick(), RecordHit(key), Demand(...), Preview(), Simulate(size)
pkg/db/lifecycle.go     BadgerDB persistence for flow:, lcstate:, lctask:, lcactive:, lcaudit:
pkg/web/handlers_flows.go  HTTP handlers for /api/flows*, /api/lifecycle/*
pkg/fileapi              + Relocate hook points (see Phase 2), PlanPlacement reads Ingest order
pkg/cluster/agent.go     + POST /api/v1/relocate, GET /api/v1/media-size
web/templates/pages/flows.html, web/static/js/flows.js, components in components.js
```

`planner.Evaluate` is **pure**: it takes a `Snapshot{Policy, Tiers []TierStatus, Files []FileView, States map[string]FileState, Active map[string]bool, Now}` and returns tasks. All I/O lives in the engine and executor.

## 5. Algorithms

### 5.1 Ingest (replaces the hardcoded order in `PlanPlacement`)

`PlanPlacement` iterates `policy.Ingest` in order and picks the first tier with a block that fits (existing `PickBlockInTier`). `fileapi` receives the order through an injected `func() []int`, which avoids an import cycle. When nothing fits → return the same "insufficient storage" error as today. Ingest never evicts synchronously. The evaluator's watermark demotes keep headroom free ahead of time.

### 5.2 Evaluate (each tick, and on `Kick()`)

Skip any file that is not completed, has an active task, or sits on an offline node.

1. **Pins:** if the pin is active and the file is not on the pin tier, emit a `promote` (or `manual`) task to the pin tier. If the pin has expired, clear it.
2. **Promote:** for each enabled promote edge, take the files on `From` with `Decayed ≥ HeatAtLeast`, sort them by score descending, and check capacity on `To` (projected free = free − bytes already planned this tick). If a file does not fit and `To` has a demote edge, plan **make-room demotes** of the coldest unpinned files on `To` with a score below the candidate's, until the candidate fits. Otherwise skip it and add a `Reason` to the preview.
3. **Idle demote:** for each enabled demote edge with `IdleDays`, take the files on `From` where `now − max(LastHitAt, TierSince) ≥ IdleDays`, `Decayed < HeatBelow`, and the file is not pinned. The destination must fit, otherwise the reason is "destination full".
4. **Watermark demote:** for each enabled demote edge with `OnWatermark`: if the `From` used% is at or above `HighWatermark`, demote files in order (lowest score, then oldest `LastHitAt`) until the projected used% is at or below `LowWatermark`.
5. **Expire** (only if `ExpireAcknowledged`): for each enabled expire edge, take the files on `From` matching `AgeDays` (`now − TierSince`) and/or `IdleDays`. If `OnWatermark` is set, expire only while `From` is at or above its high watermark and no demote edge from `From` has room. Order: oldest first. Pinned files are excluded.
6. Cap the total at `MaxTasksPerTick` (priority: pins, then promote, then watermark, then idle, then expire). Persist the tasks and set `lcactive`.

`Preview()` runs the same `Evaluate` and returns the tasks plus skipped candidates with their reasons, without persisting anything.

### 5.3 Executor state machine

- Concurrency: a global limit of 4, and at most 2 per node (counting both source and destination). Per-task timeout of 6h. Up to 3 attempts with exponential backoff (1m, 5m, 15m).
- **copying:** pick the destination block with `PickBlockInTier(To tier, RequiredSize, ReceiverCandidates)`, using size × 1.05 (no remux headroom is needed). Call the source agent `POST /api/v1/relocate` with `{task_id, key, src_dir, final_node_id, final_dir, final_addr, progress_url}`. The agent copies asynchronously and posts progress to `progress_url` with `X-Cluster-Secret`.
  - Same node, different dir: the agent copies locally (rename if it is the same filesystem, otherwise copy).
- **verifying:** `GET {dest}/api/v1/media-size?key=&dir=` must equal the source size (`GET {src}/api/v1/media-size`), and `HEAD {dest}/media/{key}/metadata.json` must return 200.
- **switching:** reload `FileJob`. If `Placement != task.From`, fail with "placement changed". Otherwise set `Placement = task.To` (with `TierLabel` and `PublicHost`), save, then set `FileState.TierSince = now` and append to the history.
- **cleanup:** call `POST {src}/api/v1/ingest-delete?key=&dir=` (existing endpoint). On failure, retry. The file is already served from the destination, so a failed cleanup only leaves an orphan. Then mark the task `done` and remove `lcactive`.
- **expire task:** write the audit row, call `ingest-delete` on the current placement, delete `FileJob`, `FileRecord` and `lcstate`, then mark it `done`.
- **Restart recovery:** at startup, load the non-terminal tasks.
  - `copying`/`verifying`: restart from copying (the destination is overwritten idempotently).
  - `switching`: if `Placement == To`, go to cleanup; otherwise re-run the switch.
  - `cleanup`: re-run the cleanup.
- **Cancel:** allowed in `queued`/`copying`/`verifying`. It deletes the partial destination folder through `ingest-delete` on the destination.

### 5.4 Demand signal

- **Play counting:** in `pkg/web/handlers_stream.go` `handleStreamManifest`, on the redirect branch, call `engine.RecordHit(key)` **only when** the subpath is the root key or a manifest (`.m3u8`, `.mpd`, or a `.mp4` whole-file request), and dedupe by `(client IP, key)` within 30 minutes (an in-memory LRU of 50k entries).
- Add a `play_url` field (additive) to `StatusResponse`/`CreateResponse` = coordinator `/stream/{key}`. Document that clients should use `play_url` if they want plays counted for auto-tiering. `stream_url` stays unchanged.
- **Explicit API:** `POST /api/v1/files/{key}/demand` with `{"level":"high"|"normal","tier_id":<optional>,"ttl_hours":<optional, default 24, max 720>}`.
  - `high`: pin to `tier_id`, or to the fastest tier reachable via promote edges from the current tier (or the lowest `SpeedRank` tier when there are no edges). Also boost the score to at least that edge's `HeatAtLeast`, then call `Kick()`.
  - `normal`: clear the pin.
  - Returns `{key, pinned_tier, pin_until, current_tier, task_id?}`.

## 6. HTTP API (all additive)

Mutations use one shared `requireAdmin(r)` helper: a valid `X-Cluster-Secret` or `Bearer`, or a request from loopback. **Do not** trust `Sec-Fetch-Site` (see research #7b).

| Method | Path | Body / Query | Returns |
|---|---|---|---|
| GET | `/api/flows` | — | `{policy, tiers: []TierStatus (with lifecycle), stats: {per tier file count, hot count}}` |
| PUT | `/api/flows` | `FlowPolicy` (+ `"expire_confirm":"DELETE FILES"` when enabling expire) | 200 policy; 409 version conflict; 422 validation errors `[{edge_id, field, message}]` |
| PUT | `/api/tiers/{id}/lifecycle` | `TierLifecycle` | tier |
| GET | `/api/flows/preview` | — | `{tasks: [], skipped: [{key, edge_id, reason}]}` |
| GET | `/api/flows/simulate` | `?size_bytes=` | `{tier_id, node_id, path, reason}` or 507 |
| GET | `/api/lifecycle/tasks` | `?state=active\|done\|failed&limit=` | `[]Task` |
| POST | `/api/lifecycle/tasks/{id}/cancel` | — | task |
| POST | `/api/lifecycle/tasks/{id}/progress` | agent progress (secret required) | 202 |
| GET | `/api/lifecycle/audit` | `?limit=` | audit rows |
| GET | `/api/v1/files/{key}/placement` | — | `{placement, state: FileState(decayed score), active_task, next: {action, edge_id, eta, reason}}` |
| POST | `/api/v1/files/{key}/demand` | see §5.4 | see §5.4 |
| POST | `/api/v1/files/{key}/move` | `{"tier_id":N}` | task (kind `manual`) |

Add every endpoint to `pkg/web/swagger.go`.

**Agent (port 2052, secret required):**
- `POST /api/v1/relocate`: 202 accepted. Copies asynchronously using the existing `transferFolder` (rclone, then parallel, then tar), followed by a local registerRoot on the receiver side as today.
- `GET /api/v1/media-size?key=&dir=` → `{bytes, files}`.
- Both validate `key` (`validKey`) and the dirs (`resolveTargetDir`, no `..`).

## 7. UI: "Data Flow" page (`/flows`)

- **Sidebar:** a new link "Data Flow" between Tiers and Media (`data-page="flows"`), plus a route in the dashboard handler and `pages/flows.html`.
- **Canvas** (inline SVG, vanilla JS, no deps):
  - A **Source** node on the left, tier nodes laid out by `SpeedRank` (fastest on top), and a **Trash** node shown only when an expire edge exists.
  - Each tier card shows the name, a capacity bar with high/low watermark ticks, the file count, the hot count, and an online-blocks badge.
  - Edges: ingest is a solid neutral line with priority numbers; promote is the accent colour with an up arrow; demote is a dashed line with a down arrow; expire is red. Each label is a one-line trigger summary (e.g. "heat ≥ 30", "idle 7d", "≥ 90% → 75%").
  - Interaction: drag from a node's output port to another node to create an edge (kind is inferred from SpeedRank direction, Trash makes it expire). Click an edge to edit it in the side panel. Press Delete to remove it. Keyboard support: Tab moves between nodes and edges, Enter opens the editor.
  - **Save** bar: "Unsaved changes · Preview · Save". Save runs a PUT with the version. 422 errors are shown inline on the matching edge.
- **Right panel tabs:**
  1. **Rule**: editor for the selected edge or tier (watermarks, SpeedRank).
  2. **Preview**: the result of `/api/flows/preview` (what would run now, and what was skipped and why).
  3. **Queue**: live tasks (poll every 3s) with progress, speed and cancel.
  4. **Hot files**: top 50 by decayed score, with a "Pin to tier" action.
  5. **Simulate**: a size input → landing tier and node.
  6. **Audit**: expire deletions.
- **Expire safety:** enabling the first expire edge opens a modal that requires typing `DELETE FILES` and lists the count of currently eligible files (from the preview).
- **File inspector:** a drawer reachable from the Media table row action "Lifecycle". It calls `/placement` and shows the current tier, node and path, the heat sparkline (score now), the pin, the next action with its reason, the move history, and buttons for "Move to tier…" and "Mark high demand".
- Reusable parts go in `components.js` as `UI.flowCanvas(el, model, handlers)`, `UI.ruleEditor(el, edge, tiers, onChange)`, `UI.taskRow(task)` and `UI.capacityBar(opts)`. Page logic goes in `web/static/js/flows.js`. Use the existing tokens from `app.css` (amber accent, tabular nums) and keep the layout working at 320px (the canvas becomes a vertical list of rules on narrow screens).

## 8. Phases & checklist

Execution order and the full prompt for each phase are in [tier-lifecycle-prompts.md](tier-lifecycle-prompts.md).

### Phase 0: Audit fixes & security (prompt A)
- [x] Fix per-block usage overwrite in `mediaUsagePerNode`.
- [x] `gofmt` all packages.
- [x] Authenticate `POST /api/v1/files/{key}/progress`, and have the agent reporter send `X-Cluster-Secret`.
- [x] Replace the `Sec-Fetch-Site` trust in `dbadmin.go` with a shared `requireAdmin`.
- [x] `fileapi.Service.Run(ctx)` for the watchdog; stop starting it in `NewService`; wire it in `cmd/stream/main.go`.
- [x] `ReconcileGhostRecords`: require 3 consecutive 404 strikes (in-memory map) before purging.
- [x] Remove the vestigial `evicted` return; log `SaveTier` errors in `ensureSystemTiers`; remove the dead `migrated` variable.
- [x] `StreamURL`: return "" when the owner node is offline and has no `PublicHost`.
- [x] Cache `mediaUsagePerNode` for 5s and fan out to nodes in parallel.

### Phase 0b: Frontend component split (prompt B)
- [x] Split `app.js` into per-page modules on `window.UI`.

### Phase 1: Model, policy, ingest order (prompt C)
- [x] `TierLifecycle` on `Tier`, with defaults and `PUT /api/tiers/{id}/lifecycle`.
- [x] `pkg/lifecycle` policy, state and task types, `Validate`, `DefaultPolicy`.
- [x] `pkg/db/lifecycle.go` persistence, with tables registered in the DB Studio admin list.
- [x] `PlanPlacement` uses `Ingest` order; `GET/PUT /api/flows`; `/api/flows/simulate`.
- [x] Tests: `Validate` table tests; placement order tests.

### Phase 2: Relocation primitive (prompt D)
- [x] Agent `POST /api/v1/relocate` + `GET /api/v1/media-size`.
- [x] Executor state machine with restart recovery, cancel and concurrency limits.
- [x] `POST /api/v1/files/{key}/move`, the task list/cancel/progress endpoints.
- [x] Tests: executor with fake agent `httptest` servers covering a crash at each state.

### Phase 3: Heat, demand, evaluator (prompt E)
- [x] `HitRecorder`, play counting at the redirect, `play_url`.
- [x] `POST /demand`, `GET /placement`.
- [x] `planner.Evaluate` (pins, promote with make-room, idle demote, watermark demote), `Engine.Run`, `/api/flows/preview`.
- [x] Tests: decay, hysteresis, make-room ordering, watermark stop point, pin protection, MaxTasksPerTick.

### Phase 4: Expire (prompt F)
- [x] Expire planning plus the executor branch, audit table, `ExpireAcknowledged` and the confirm phrase on PUT, `/api/lifecycle/audit`.
- [x] Tests: no expire without the acknowledgement; pinned files are never expired; audit is written before the delete.

### Phase 5: Data Flow UI (prompt G)
- [x] Sidebar, page, canvas, rule editor, the panel tabs, the expire modal, the file inspector in Media.

### Phase 6: Docs & handoff (in each prompt)
- [x] Swagger entries, `docs.html` section "Auto-tiering", session HANDOFF.

## 9. Acceptance criteria (end-to-end)

1. Policy ingest `[2,1]` → a new file lands on tier2. `/simulate` agrees, and `/placement` shows the reason.
2. A promote edge 2→0 with `heat ≥ 5`: 5 distinct plays via `play_url` → within one tick plus the copy time, `stream_url` points at the tier0 node and the tier2 folder is gone.
3. `POST /demand level=high` → a task is queued immediately and the file is pinned. While the pin is active, idle and watermark demotes skip it.
4. A demote edge 0→2 with `idle 7d, heat < 2` → after the window, the file moves back down. At no point does the stream return 404 (switch happens before cleanup).
5. Tier0 at 92% with `90 → 75` → the coldest files are demoted until the projection is ≤ 75%. No file is deleted.
6. Expire edge 2→Trash with `age 30d`: rejected without the confirm phrase. Once accepted, eligible files are deleted, audit rows exist, and pinned files survive.
7. Kill the coordinator during `copying` and during `cleanup` → after restart the tasks finish. Exactly one copy remains, and `Placement` matches the disk.
8. `go vet ./...` and `go test ./...` pass, and `gofmt -l` is empty.
