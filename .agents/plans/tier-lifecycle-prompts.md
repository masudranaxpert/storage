# Agent Prompts: Tier Lifecycle Implementation

Paste one prompt per agent session. Each prompt is self-contained.

**Order:**

```
A (backend fixes) ─┐
B (frontend split) ┼─► C (model/policy) ─► D (relocation) ─► E (heat/evaluator) ─► F (expire) ─► G (UI)
                   │
                   └─ A and B can run in parallel; G needs B and E (it can start after C against mocked data)
```

Every prompt begins with the same shared preamble. Copy it together with the prompt.

---

## Shared preamble (prepend to every prompt)

```
You are working in /home/masud/work/storage (Go 1.x, module "stream"). First read, in order:
AGENTS.md, .agents/INDEX.md, .agents/plans/tier-lifecycle-flows.md (the design — treat it as the
spec), .agents/research/tier-lifecycle/research.md.

Rules:
- Standard library only; no new dependencies. Functional options + explicit constructors, no init().
- Concise, why-focused comments. Flat error handling; never ignore errors; no goroutine/FD leaks
  (every background loop takes a context.Context and stops on cancel).
- Do NOT change the gRPC proto or any existing JSON field names/semantics. Only additive fields/endpoints.
- Never delete user media except where the spec's expire rule explicitly says so.
- Code and git state beat docs; if the spec is wrong about existing code, follow the code, note the
  deviation in the plan file, and continue.
- Stay strictly inside your phase's scope. Do not refactor unrelated code.
- When done: run `gofmt -l ./pkg ./cmd` (must be empty), `go vet ./...`, `go test ./...`; tick your
  phase's checklist boxes in .agents/plans/tier-lifecycle-flows.md; create
  .agents/sessions/<YYYY-MM-DD>-<topic>/HANDOFF.md (git state, what changed, files, how verified,
  open issues) and link it under "## Sessions" in .agents/INDEX.md. Do not commit unless asked.
```

---

## Prompt A: Backend audit & security fixes

```
Phase 0 of the tier lifecycle plan: fix the open audit items in research.md §1. Scope:

1. SECURITY: `POST /api/v1/files/{key}/progress` (pkg/fileapi/api.go handleProgress) is
   unauthenticated. Require the cluster secret (X-Cluster-Secret or "Authorization: Bearer"). The
   fileapi.Service already holds `secret` (SetSecret). If secret is empty, allow (dev mode) as other
   endpoints do. Update the agent side: pkg/cluster/agent.go fileProgressReporter must send
   X-Cluster-Secret (a.Secret). Use http.NewRequest instead of client.Post. Close bodies.
2. SECURITY: pkg/web/dbadmin.go trusts the client-controlled `Sec-Fetch-Site: same-origin` header to
   skip the secret check. Create one helper in pkg/web (e.g. auth.go):
   `func (s *Server) requireAdmin(w, r) bool` — allow if no secret is configured, or the request
   carries the valid secret, or r.RemoteAddr is loopback (parse with net.SplitHostPort + net.IP.IsLoopback,
   not string prefixes). Use constant-time compare (crypto/subtle). Replace the dbadmin logic with it.
   The browser UI must keep working from a non-loopback browser without embedding the secret in
   JS. Implement a same-origin session token: on GET of any HTML
   page, set an HttpOnly, SameSite=Strict cookie `stream_admin` holding an HMAC(secret, "admin-ui")
   value, and accept that cookie in requireAdmin. Document the choice in the handoff.
3. pkg/fileapi/service.go: NewService starts `go s.startWatchdogLoop()` with no stop. Remove that;
   add `func (s *Service) Run(ctx context.Context)` that runs the watchdog until ctx is done. Wire it
   from cmd/stream/main.go (coordinator mode) with the process context, and make sure tests that
   construct NewService still pass.
4. ReconcileGhostRecords: require 3 consecutive 404 strikes for the same key (in-memory
   map[string]int on Service, guarded by a mutex; reset on any non-404) before deleting rows. Also
   use a request with X-Cluster-Secret if the agent requires it for /media (check agent.go).
5. PlanPlacement: drop the always-nil second return value (`evicted []string`) and update callers.
6. pkg/storage/tier.go ensureSystemTiers: delete the dead `migrated` variable; log (log.Printf)
   SaveTier/DeleteTier errors instead of discarding them. Same for Upsert's other-tier SaveTier calls.
7. fileapi StreamURL: when no PublicHost applies and the owning node is missing or
   cluster.StatusOffline, return "".
8. pkg/web/handlers_stream.go mediaUsagePerNode: fan out to nodes in parallel (sync.WaitGroup,
   per-node 1s timeout, results merged under a mutex) and cache the merged map for 5 seconds
   (mutex + timestamp on Server). Keep the existing semantics (the per-disk fallback fix already in
   place must stay).

Tests: add tests for requireAdmin (secret, bearer, loopback, spoofed Sec-Fetch-Site rejected),
progress auth (401 without secret when configured), and the reconcile strike counter (httptest agent
returning 404 three times). Acceptance: all checks pass; `curl -H 'Sec-Fetch-Site: same-origin'` to a
dbadmin mutation from a non-loopback address is rejected.
```

---

## Prompt B: Frontend component split

```
Phase 0b: the dashboard frontend is not component-based. web/static/js/app.js is ~3,535 lines and
holds dashboard, nodes, storage, media and settings logic; only tiers.js and database.js are split
out. web/static/js/components.js exposes window.UI (badge, metricRow, pagination, highlightJSON,
codeEditor, ...). Templates: web/templates/{layouts,components,pages}; base layout loads scripts.

Goal: a behaviour-identical split, with no visual changes.
1. Read app.js fully and map every function to: shared core (fetch helpers, formatters, polling,
   modal/toast helpers, navigation) vs page-specific.
2. Create web/static/js/core.js (shared helpers, exported on a single `window.App` namespace) and
   one file per page: dashboard.js, nodes.js, storage.js, media.js, settings.js. Each page file
   self-registers an init function, e.g. `App.pages.media = { init, destroy }`; core runs the init
   for the active page (the active page id is in the DOM: `.page-view.active` / CurrentPage).
3. Anything rendered in 2+ places (status pills, capacity/drive bars, tables with pagination,
   confirm modal, empty states) becomes a function in components.js under window.UI, with esc()
   on all interpolated data.
4. Inline `onclick="fn()"` handlers in templates must keep working: either expose those functions
   on window from the owning page module, or switch to data-action attributes with delegated
   listeners. Pick one consistently.
5. Update web/templates/layouts/base.html script tags (order: vendor → components.js → core.js →
   page files). Delete app.js only when nothing references it.
6. Polling intervals must be cleared when leaving a page (destroy) — no duplicated timers.

Verify: run the coordinator (`go run ./cmd/stream coordinator --port=1212`), open every page in a
browser, check the console has zero errors, and exercise the main actions on each page (open
modals, pagination, media upload modal, node detail modal, storage clean, settings save). Check
widths 375px and 1440px. List anything you could not verify in the handoff. No backend changes.
```

---

## Prompt C: Lifecycle model, policy & ingest order

```
Phase 1 of .agents/plans/tier-lifecycle-flows.md (spec §3.1, §3.2, §3.3/3.4 types only, §4, §5.1,
§6 rows: GET/PUT /api/flows, PUT /api/tiers/{id}/lifecycle, GET /api/flows/simulate).

Build:
1. pkg/storage/tier.go: add TierLifecycle + Tier.Lifecycle + TierStatus.Lifecycle (additive JSON).
   Defaults filled in ensureSystemTiers and Upsert when zero: SpeedRank tier0=0, tier1=1, tier2=2,
   custom=10+ID; High=90, Low=75. Validate Low < High ≤ 100 in Upsert.
2. New package pkg/lifecycle with policy.go (FlowPolicy, Edge, Trigger, EdgeKind, TrashTierID,
   DefaultPolicy(tiers), Validate(p, tiers) []FieldError), state.go and task.go (types only + Decayed()),
   store.go (Store interface). Import direction: lifecycle may import storage, fileapi, cluster;
   fileapi must NOT import lifecycle.
3. pkg/db/lifecycle.go: persistence for flow:policy, lcstate:<key>, lctask:<id>, lcactive:<key>,
   lcaudit:<ts>-<key> following the patterns in pkg/db/badger.go and files.go. Register these
   tables in the DB Studio admin table list (pkg/db/admin.go) so they are browsable.
4. Engine skeleton pkg/lifecycle/engine.go: New(store, tiers, coord, files, opts...) with
   GetPolicy / SavePolicy(p, expectedVersion, confirm string) (bumps Version, sets UpdatedAt;
   returns ErrVersionConflict; rejects enabling an expire edge unless confirm == "DELETE FILES",
   and sets ExpireAcknowledged accordingly) and Simulate(sizeBytes). No background loop yet.
   On first boot, persist DefaultPolicy.
5. fileapi.PlanPlacement: inject an ingest-order provider (functional option or setter on Service:
   `SetIngestOrder(func() []int)`). Iterate that order; if the provider is nil, keep today's
   behaviour exactly. Wire the provider from the engine in pkg/web/server.go.
6. pkg/web/handlers_flows.go: GET/PUT /api/flows, GET /api/flows/simulate,
   PUT /api/tiers/{id}/lifecycle (route inside existing handleTiers), using requireAdmin for
   mutations (from prompt A; if absent, create it per prompt A item 2). 422 body:
   {"errors":[{"edge_id","field","message"}]}. 409 on version conflict. Add all to swagger.go.

Tests (table-driven): Validate covers every rejection rule in spec §3.2; DefaultPolicy reproduces
current ingest order; PlanPlacement honours a custom order and falls through on full tiers;
SavePolicy version conflict and expire confirm phrase.
```

---

## Prompt D: Relocation primitive & executor

```
Phase 2 of .agents/plans/tier-lifecycle-flows.md (spec §2 invariants, §3.4, §5.3, §6 agent endpoints
+ /api/lifecycle/tasks*, /api/v1/files/{key}/move). Prereq: Phase 1 merged.

Build:
1. Agent (pkg/cluster/agent.go — put new handlers in a NEW file pkg/cluster/agent_relocate.go to avoid
   growing the god object; register them from the existing mux setup):
   - POST /api/v1/relocate {task_id, key, src_dir, final_node_id, final_dir, final_addr,
     progress_url}: verifySecret, validKey, resolveTargetDir on both dirs, reject "..". Locate the
     source folder <resolved src_dir>/<key> (404 if missing). Respond 202, then run async with a
     cancellable context kept in a map by task_id:
       * final_node_id == a.NodeID → local copy (os.Rename if same device, else recursive copy with
         fsync), registerRoot(final dir).
       * else → reuse a.transferFolder(ctx, key, srcFolder, &TransferTarget{...}, report) where
         report posts JSON {state, progress_percent, speed, error, transferred_bytes, total_bytes}
         to progress_url with X-Cluster-Secret. NEVER delete the source on the agent side.
   - POST /api/v1/relocate/cancel?task_id=: cancels the context.
   - GET /api/v1/media-size?key=&dir=: {bytes, files} of <dir>/<key>.
   Check that the transfer receiver path (ingest-receive / ingest-complete / rclone webdav) already
   reserves quota and registers the root; if not, add it.
2. Coordinator pkg/lifecycle/executor.go per spec §5.3: persisted state machine
   queued→copying→verifying→switching→cleanup→done|failed|cancelled, global concurrency 4, per-node 2,
   per-task timeout 6h, 3 attempts with backoff 1m/5m/15m, restart recovery exactly as specified,
   cancel semantics. Run(ctx) loop; Enqueue(task) enforces one active task per key via lcactive.
   Switching must re-read the FileJob and compare Placement to task.From before writing.
   After switch, update lcstate TierSince + History (cap 20).
3. Endpoints: GET /api/lifecycle/tasks (?state=active|done|failed&limit=), POST
   /api/lifecycle/tasks/{id}/cancel, POST /api/lifecycle/tasks/{id}/progress (secret required),
   POST /api/v1/files/{key}/move {"tier_id":N} (validates completed state, target tier exists and
   differs, picks block with fileapi.PickBlockInTier + ReceiverCandidates). Register the fileapi
   route without importing lifecycle into fileapi (register from pkg/web or via an interface hook).
   Swagger entries.
4. Wire engine.Run(ctx) / executor.Run(ctx) from cmd/stream/main.go coordinator mode.

Tests: executor against httptest fake agents — happy path cross-node; same-node; verify size
mismatch → retry then failed with source intact; placement changed during copy → failed, no delete;
simulated restart in each state (construct a new executor over the same store) → converges to
exactly one copy and Placement == disk; cancel during copying deletes partial destination only.
```

---

## Prompt E: Heat, demand API & evaluator

```
Phase 3 of .agents/plans/tier-lifecycle-flows.md (spec §3.3, §5.2 steps 1-4 and 6, §5.4, §6 rows
/preview, /placement, /demand). Prereq: Phases 1-2 merged. Do NOT implement expire (Phase 4) —
leave a clearly marked hook in Evaluate.

Build:
1. pkg/lifecycle/state.go HitRecorder: RecordHit(key) increments an in-memory buffer (mutex); a
   Run(ctx) loop flushes every 10s: load lcstate, decay to now, add hits, set LastHitAt, save. Decay
   uses policy.HalfLifeHours.
2. pkg/web/handlers_stream.go: on the redirect branch of handleStreamManifest call RecordHit only for
   the root key path or manifests (.m3u8, .mpd) or a whole-file .mp4, deduped per (client IP, key)
   for 30 min with a bounded LRU (container/list + map, cap 50k). Client IP: r.RemoteAddr host only
   (do not trust X-Forwarded-For unless a trusted-proxy setting already exists).
   Add additive field `play_url` (= coordinator-relative "/stream/{key}") to fileapi StatusResponse
   and CreateResponse.
3. pkg/lifecycle/planner.go: pure Evaluate(snapshot) (tasks, skipped) implementing spec §5.2 steps
   1-4 and 6 exactly (pins, promote with make-room demotes, idle demote with HeatBelow hysteresis,
   watermark demote until projected ≤ low), projecting capacity changes within a tick. Each task gets
   a human Reason string; each skipped candidate gets {key, edge_id, reason}.
4. Engine.Run(ctx): ticker every policy.TickSeconds + Kick() channel; builds the snapshot (tiers
   resolved with usage, completed FileJobs, lcstate, lcactive), calls Evaluate, enqueues to the
   executor. Engine.Preview() returns Evaluate output without enqueueing.
5. Endpoints: GET /api/flows/preview; GET /api/v1/files/{key}/placement (spec §6, include the
   "next" action computed by running Evaluate for that single file); POST /api/v1/files/{key}/demand
   per spec §5.4 (validate ttl ≤ 720h; tier must exist). Swagger entries; add an "Auto-tiering"
   section to web/templates/pages/docs.html explaining play_url, demand API and rules.

Tests (table-driven, fake clock): decay half-life math; promote threshold; hysteresis gap prevents
flapping over 10 simulated ticks; make-room demotes coldest first and never pinned files; watermark
stops at low; idle uses max(LastHitAt, TierSince); MaxTasksPerTick priority order; skipped reasons
for "destination full"; dedupe LRU counts one play per IP per 30 min.
```

---

## Prompt F: Opt-in expire rule

```
Phase 4 of .agents/plans/tier-lifecycle-flows.md (spec §2 invariant 3, §3.5, §5.2 step 5, §5.3
expire branch, §6 /api/lifecycle/audit). Prereq: Phases 1-3 merged. The user explicitly approved
this as the ONLY path that deletes user media; be conservative.

Build:
1. Evaluate step 5 at the hook left in Phase 3: only when policy.ExpireAcknowledged; AgeDays uses
   now − TierSince, IdleDays uses now − max(LastHitAt, TierSince); OnWatermark variant only while
   From ≥ HighWatermark and no demote edge from From has room; oldest first; never pinned; never a
   file with an active task; lowest priority in MaxTasksPerTick.
2. Executor expire branch: write lcaudit row FIRST (fail task if the write fails), then agent
   ingest-delete on the current Placement, then delete FileJob, FileRecord, lcstate, lcactive.
   If the agent delete fails, keep DB rows and retry (never leave DB pointing at nothing silently;
   never delete DB rows while bytes still exist unless the node is permanently gone — then fail the
   task and surface it).
3. GET /api/lifecycle/audit?limit= (newest first). Swagger.
4. Server-side guard: SavePolicy already requires confirm "DELETE FILES" to enable expire; also
   require it whenever an expire edge's trigger is made more aggressive (smaller AgeDays/IdleDays).

Tests: no expire tasks without acknowledgement; pinned and active-task files excluded; audit row
precedes delete (fake store records call order); agent failure keeps DB rows; OnWatermark variant
only fires under pressure; tightening a trigger without confirm → 422.
```

---

## Prompt G: Data Flow UI

```
Phase 5 of .agents/plans/tier-lifecycle-flows.md (spec §7; APIs in §6). Prereq: Phases 1-4 merged
and the frontend split (prompt B) done — follow its module pattern (App.pages.<name> with
init/destroy, shared helpers in core.js, reusable pieces in components.js / window.UI).

Build:
1. Sidebar link "Data Flow" between Tiers and Media in web/templates/components/sidebar.html
   (icon: simple git-branch/flow SVG, same stroke style), page route in the dashboard handler like
   /tiers, template web/templates/pages/flows.html, script web/static/js/flows.js, styles in
   web/static/css/app.css using existing tokens (amber accent, radius scale, tabular-nums).
2. components.js: UI.flowCanvas(el, model, handlers) — inline SVG, no libraries: Source node left,
   tier nodes ordered by SpeedRank (fastest top), Trash node only when an expire edge exists or
   while dragging; tier card = name, capacity bar with high/low watermark ticks, file count, hot
   count, online-block badge; edges styled per kind (ingest solid with priority numbers, promote
   accent ↑, demote dashed ↓, expire red) with a one-line trigger label. Drag from an output port
   to a node creates an edge (kind inferred: faster target=promote, slower=demote, Trash=expire,
   from Source=ingest entry). Click selects; Delete key removes; Tab/Enter keyboard navigation and
   aria-labels on nodes and edges. Also UI.ruleEditor, UI.taskRow, UI.capacityBar.
3. Right panel tabs: Rule (edge editor / tier watermark + SpeedRank editor), Preview
   (/api/flows/preview — tasks + skipped with reasons), Queue (/api/lifecycle/tasks?state=active,
   poll 3s, progress + speed + cancel), Hot files (top 50 by score from /api/flows stats or a
   dedicated query — add GET /api/lifecycle/hot?limit= if needed, minimal backend), Simulate
   (size input → /api/flows/simulate), Audit (/api/lifecycle/audit).
4. Save bar: "Unsaved changes · Preview · Save"; PUT /api/flows with version; 409 → reload prompt;
   422 → mark offending edges inline with messages.
5. Expire safety modal: when the user adds/enables an expire edge, show a modal listing how many files
   are currently eligible (run preview with the draft policy — add POST /api/flows/preview accepting a
   draft policy if needed) and require typing DELETE FILES; send it as expire_confirm.
6. Media page: add a "Lifecycle" row action opening a drawer: /api/v1/files/{key}/placement (tier,
   node, path, heat score, pin, next action + reason, move history) with buttons "Move to tier…"
   (POST /move) and "Mark high demand" (POST /demand).
7. Narrow screens (< 768px): replace the canvas with a vertical rule list (same data, same editor).

Verify in a browser with a running coordinator: create ingest/promote/demote edges, save, reload
(persists), invalid edge shows 422 inline, preview renders, simulate works, queue updates during a
manual move, expire modal blocks without the phrase, zero console errors, 375px and 1440px widths.
```
