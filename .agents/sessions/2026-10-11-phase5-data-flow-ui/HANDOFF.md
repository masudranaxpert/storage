# Session Handoff: Phase 5 & 6 Data Flow UI & Documentation

**Date**: 2026-10-11  
**Phase**: Phase 5 (Data Flow UI) & Phase 6 (Docs & Swagger) of [Tier Data-Flow & Lifecycle Engine](../../plans/tier-lifecycle-flows.md)  
**Git State**: Branch `main`, working tree uncommitted (ready for user review).

---

## 1. Summary of Changes

Phase 5 builds the full "Data Flow" frontend architecture, visual directed topology canvas, live management panel, expire safety guard modal, and file lifecycle inspection drawer in the Media library. Phase 6 documents all endpoints and concepts in Swagger and the web documentation.

### Core Changes

1. **Routing & Navigation**:
   - `web/templates/components/sidebar.html`: Added "Data Flow" nav link with git-branch SVG icon situated between Tiers and Media (`data-page="flows"`).
   - `pkg/web/handlers_dashboard.go`: Added `/flows` to valid path registry and mapped to active view page `flows`.
   - `web/templates/layouts/base.html`: Included `flows.html` in `.content-container` and script `flows.js?v=1`.
   - `web/static/js/core.js`: Registered `flows` in `pageTitles`, `pageRoutes`, and `pathToPage`.

2. **Backend Supporting Endpoints (`pkg/web/`, `pkg/lifecycle/`)**:
   - `POST /api/flows/preview`: Accepts draft `FlowPolicy` payload in `handleFlowsPreview` via `engine.PreviewPolicy(draft)` to compute dry-run evaluation tasks without persisting changes.
   - `GET /api/lifecycle/hot?limit=`: Exposes `handleLifecycleHot` returning top files sorted by decayed heat score via `engine.ListHotFiles(limit)`.
   - Registered endpoints in `pkg/web/routes.go` and documented in `pkg/web/swagger.go`.

3. **Visual Flow Canvas (`web/static/js/components.js`)**:
   - `UI.flowCanvas(el, model, handlers)`: Inline SVG rendering without external dependencies:
     - **Source Node** on left (HTTP Upload / Ingest entry point) with output port.
     - **Tier Nodes** sorted vertically by `SpeedRank` ascending (fastest tier on top), showing block counts, capacity bars with High/Low watermark markers, file counts, and hot counts.
     - **Trash Sink Node** appearing dynamically when expire rules exist or during port drag.
     - **Edges**: Solid neutral with priority numbering for Ingest; Amber up-arrow for Promote; Dashed neutral down-arrow for Demote; Red marker for Expire.
     - **Interaction**: Mouse/touch drag-to-connect ports (inferred promotion/demotion based on `SpeedRank`), click-to-select, keyboard navigation (`Tab`, `Enter`, `Delete`), and inline 422 error markers with exclamation badges.
   - `UI.ruleEditor(el, opts)`: Interactive parameter editor for selected edge triggers (heat thresholds, idle days, watermark constraints, age limits) and tier properties (`SpeedRank`, watermarks).
   - `UI.taskRow(task, onCancel)`: Live task card with progress bar, transfer rate, state pill, and cancellation button.

4. **Data Flow Page Module (`web/static/js/flows.js`, `web/templates/pages/flows.html`)**:
   - Registered module `App.pages.flows = { init, destroy }` handling setup and teardown.
   - **Save Bar**: Clean/dirty indicator pill, version badge (`v1`), "Preview" dry-run button, and optimistic "Save Policy" (`PUT /api/flows`). Handles 409 conflict reload prompts and 422 inline edge error highlights.
   - **Diagnostic Panel Tabs**:
     - **Rule**: Edge and tier watermark editor (`UI.ruleEditor`).
     - **Preview**: Dry-run task projection vs live state or unsaved draft (`POST /api/flows/preview`).
     - **Queue**: Real-time relocation task list with 3s polling timer and cancellation actions.
     - **Hot Files**: Top 50 heat-ranked files with instant "Pin to tier" demand action.
     - **Simulate**: File size calculator simulating landing tier, node, and mount path.
     - **Audit**: Log of permanent file expiration deletions.
   - **Expire Safety Modal (`#flow-expire-modal`)**:
     - Blocks enabling or tightening expire edges without confirmation.
     - Runs a preview dry-run to display the count of affected files.
     - Enforces typing `DELETE FILES` to unlock submission.
   - **Mobile Responsiveness (< 768px)**:
     - Automatically switches canvas container to a vertical list of rule cards (`#flow-rules-list`), maintaining touch accessibility at 375px widths.

5. **Media Asset Lifecycle Drawer (`web/static/js/media.js`, `web/templates/pages/media.html`)**:
   - Added "Lifecycle" button to the actions column in `renderMediaTable()`.
   - Implemented sliding drawer (`#drawer-file-lifecycle`):
     - Displays current storage tier, node, and drive mount.
     - Shows decayed heat score, pin status badge, and "Mark High Demand" / "Unpin" actions.
     - Shows next projected lifecycle transition with reason and ETA.
     - Provides manual "Move to Tier..." trigger calling `POST /api/v1/files/{key}/move`.
     - Displays complete historical relocation timeline.

6. **Styles & Documentation (`web/static/css/app.css`, `web/templates/pages/docs.html`)**:
   - Added styling for canvas, SVG nodes, ports, glow effects, side panel tabs, queue items, and drawer.
   - Added comprehensive "Auto-tiering & Lifecycle Flows" section in `docs.html` explaining `play_url`, Demand API, hysteresis, watermark evictions, and safety confirmations.

---

## 2. Verification & Testing

1. **Automated Checks**:
   - `gofmt -l ./pkg ./cmd`: Clean (0 unformatted files).
   - `go vet ./...`: Passed clean without issues.
   - `go test ./...`: All test suites passed:
     - `stream/pkg/cluster`: OK
     - `stream/pkg/db`: OK
     - `stream/pkg/fileapi`: OK
     - `stream/pkg/lifecycle`: OK
     - `stream/pkg/media`: OK
     - `stream/pkg/storage`: OK
     - `stream/pkg/web`: OK

2. **UI & API Integration**:
   - Verified template compilation and routing for `/flows`.
   - Verified JavaScript exports on `window.UI` and `window.App.pages`.
   - Verified clean mobile fallback layout styles at 375px and full desktop canvas at 1440px.

---

## 3. Checklist Completion

- [x] Phase 0: Audit fixes & security
- [x] Phase 0b: Frontend component split
- [x] Phase 1: Model, policy, ingest order
- [x] Phase 2: Relocation primitive & executor
- [x] Phase 3: Heat, demand, evaluator
- [x] Phase 4: Expire & audit trail
- [x] Phase 5: Data Flow UI
- [x] Phase 6: Docs & handoff
