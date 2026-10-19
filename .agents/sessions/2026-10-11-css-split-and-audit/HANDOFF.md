# HANDOFF — Full Audit, CSS Split & Nil-Store Crash Fix

**Date:** 2026-10-11

## What was done

1. **Full system audit** (build/vet/test/security/structure) — see findings below.
2. **`app.css` split into 8 files** (user-requested). `flows.css` untouched (already scoped/sectioned).
3. **Fixed a nil-store startup crash** surfaced by the audit smoke run.
4. **Authored the missing Tier/Processing/Modal CSS (deep fix).** The 2026-10-11 morning rewrite of `tiers.js`/`tiers.html` introduced ~82 classes (tier card v2, block cards, quota/CDN controls, attach drawer, processing worker cards, iOS toggle, allocation panel, action modal, storage segmented tabs) that had **no CSS anywhere** (never in git history — `git log -S` empty). Tiers page rendered with native unstyled controls and the action modal leaked inline at the page bottom. Fix: ~600 lines of component CSS appended to `tiers.css`, segmented-tab + maintenance-button styles to `storage.css`, shared `.tabular-num`/`.ui-badge`/`.btn-sm` to `base.css`. Verified live on :1212 (fresh Chromium): tier cards, CDN drawer (animated open), create-tier modal, storage tabs all styled; dashboard/nodes/media/database/flows/docs regression-checked (shell + hero intact). Cache versions bumped `?v=6` → `?v=7` in `base.html`; a restart is needed for the new template links (static CSS itself is served live from disk).

## CSS split

| File | Lines | Content |
|---|---|---|
| `base.css` | 438 | tokens, scrollbars, sidebar, workspace, quick join |
| `dashboard.css` | 789 | stat cards, hero band, KPI rings, fleet strip |
| `layout.css` | 337 | mobile menu/shell breakpoints, section headers, page visibility, mobile bottom nav |
| `nodes.css` | 548 | nodes grid, node detail & quota modal |
| `storage.css` | 105 | storage pool table, dropzone, pagination |
| `tiers.css` | 583 | tier storage & processing VPS |
| `database.css` | 103 | database studio |
| `media.css` | 731 | cinema player, media badges, lifecycle drawer |

- Method: order-preserving range extraction; `layout.css` holds two blocks relocated from the file tail (Page Visibility, Mobile Bottom Nav). Selector isolation verified (no rule between old/new cascade positions targets `.page-view` / `.mobile-bottom-nav`).
- Proof: concatenation of the 8 files in link order is byte-identical to the reordered original (3627 lines, `diff` clean). `app.css` deleted; `base.html` links 9 files in cascade order.
- Verified in headless Chromium: all 9 files 200 + parsed, dashboard/nodes screenshots match pre-split design, page visibility & mobile nav behave.

## Crash fix (found by smoke run)

`pkg/fileapi/service.go` `sweepStaleJobs`: with BadgerDB unavailable (lock held / in-memory fallback), the watchdog goroutine dereferenced the nil `store` and killed the coordinator ~31s after start (`SIGSEGV`, exit 1). Fix: early return when `s.store == nil`, matching the existing nil-store convention in `pkg/lifecycle/engine.go`. Reproduced before (panic at 31s), verified after (survives 2+ ticks; `go test ./...` all pass).

## Audit findings (2026-10-11)

Pass: `go build`, `go vet`, `go test ./...`, `gofmt -l` clean. Fixed vs older audit: agent tool endpoints + `ingest-delete` authenticated, `validKey` traversal guard, dbadmin mutations behind `requireAdmin`, `ssh_password` masked in `rows`, usage fan-out cached (5s), 3-strike ghost reconcile.

Still open:
- `ssh.InsecureIgnoreHostKey` ×5 in `pkg/provision/provision.go`; `InsecureSkipVerify` + `--check-certificate=false` in `pkg/download` (`downloader.go:72,147`, `parallel.go:34`).
- `/api/v1/storage-folders` on the agent has no `verifySecret` (drive-layout disclosure on port 2052).
- dbadmin GET endpoints unauthenticated (values masked, but table/row shapes readable); `SSHPassword` still stored plaintext in Badger.
- Degraded (no-DB) mode: fileapi HTTP routes deref nil `store` on request (watchdog fixed; request paths not).
- Dead CSS: `.tiers-page-header` etc. defined but unused by templates (template uses `.tiers-header-bar` — now styled).
- Structure: `pkg/cluster/agent.go` 2138 lines (tracked in cluster redesign plan); `web/static/js/media.js` 1758 lines is now the largest JS monolith; test gaps in `cmd/stream`, `pkg/download`, `pkg/media`, `pkg/provision`, `pkg/telemetry`, `pkg/tools`.

## Git state

Modified: `web/templates/layouts/base.html`, `pkg/fileapi/service.go`, `.agents/INDEX.md`. Deleted: `web/static/css/app.css`. Added: 8 css files + this handoff.

## Dashboard & Nodes CSS Resolution (2026-10-11)

Following the split, UI regressions occurred on the Dashboard (`/`) and Nodes (`/nodes`) pages due to class mismatches:
1. **Dashboard BoardUI (`.bui-*`) system missing:** `dashboard.html` and `dashboard.js` utilize BoardUI double-surface classes (`.bui-tray-card`, `.bui-score-grid`, `.bui-score-card`, `.bui-tray-head`, `.bui-tray-titles`, `.bui-tray-eyebrow`, `.bui-tray-headline`, `.bui-head-chip`, `.bui-score-visual`, `.bui-ring-large`, `.bui-inner-card`, `.bui-divided-list`, `.bui-metric-row`, `.bui-swatch-sq`, `.bui-stage-pill-*`, `.bui-squircle-icon`, `.composition-segment`, etc.). These classes were absent from `dashboard.css`.
2. **Nodes Card & Drawer classes missing:** `nodes.js` was generating new card markup (`.node-card-head`, `.node-card-identity`, `.node-icon-box`, `.node-status-pip`, `.node-identity-meta`, `.node-name-row`, `.node-status-pill`, `.node-sub-row`, `.node-ip-copy`, `.node-toolbar`, `.node-metrics-bar`, `.node-metric-cell`, `.cell-label`, `.cell-value`, `.cell-progress`, `.node-drawer`, `.drawer-tools-grid`, `.engine-badge`, `.node-drives-list`, `.node-drive-item`), whereas `nodes.css` only retained legacy classes (`.node-header-row`, etc.).
3. **Missing CSS Tokens & Badge Variants:** `:root` in `base.css` lacked `--chart-1` through `--chart-5` and `.ui-badge-blue`, `.ui-badge-dot`, and `.ui-badge-mono`.

**Fix applied:**
- Added complete BoardUI rules to `dashboard.css` (4-column score grid on desktop, double-surface concentric cards, circular gauges, breakdown bars).
- Refactored `nodes.css` with clean rules for the active card markup, metrics bar (Drive Storage, CPU, RAM), collapsible drawer, tools badges (`aria2c`, `ffmpeg`, `rclone`), and mounted partitions.
- Added chart color variables and missing badge variants to `base.css`.
- Bumped CSS cache query string in `base.html` from `?v=7` to `?v=8`.
- Rebuilt `stream` and restarted coordinator daemon on port 1212.

## UI polish pass (better-ui, database + settings)

- Removed duplicate "Database Studio" page heading (topbar title already carries it); actions right-aligned like the media page convention. `web/templates/pages/database.html`
- Named transition properties instead of `transition: all` in `database.css` and the storage/tiers blocks authored this session.
- `overscroll-behavior: contain` on `.modal-overlay`, `.db-table-list-inner`, `.db-rows-scroll`, `.bui-json-viewer-card`.
- Global `prefers-reduced-motion: reduce` guard appended to `base.css`.
- Verified live: database page starts flush under the topbar (the user-reported gap matched a mid-churn stale state), pagination reachable via app-main scroll; settings cache card fully reachable at 768px viewport. Cache versions bumped to `?v=19`.

## Next steps

1. Split `media.js` per page-module (same pattern as phase0b).
2. Decide TLS/SSH verification policy for `pkg/download` + `pkg/provision` (config flag vs strict).
3. Add `verifySecret` to agent `/api/v1/storage-folders`.

