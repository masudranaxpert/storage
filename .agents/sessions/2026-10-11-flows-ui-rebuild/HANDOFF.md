# Session Handoff: Data Flow Page UI Rebuild

## Date & Scope
- **Date**: 2026-10-11
- **Focus**: Rebuild `/flows` as a component-based, responsive page. Frontend only, plus a global header-title fix.

## Changed
- `web/static/js/flow-ui.js` (new): `UI.flow` components: `graph` (HTML nodes plus an SVG wire overlay, pointer-event drag that works with touch, ResizeObserver redraw), `pipeline` (narrow list), `inspector` (edge/tier/source/new-rule forms with live draft edits), and list rows (task, preview, skipped, hot, audit, stat tiles).
- `web/static/js/flows.js` (rewritten): state, API calls and polling only. Draft vs saved policy, a 409 keeps the draft, 422 errors map to edges, the expire modal mirrors the server's confirm rules, Ctrl/Cmd+S saves, an unsaved-changes guard, arrow-key tabs.
- `web/templates/pages/flows.html` (rewritten); `web/static/css/flows.css` (new, `.fl-*` scope).
- Removed the legacy `flowCanvas`/`ruleEditor`/`taskRow` from `components.js` and the old flows CSS block from `app.css` (the media lifecycle drawer CSS was kept).
- `web/templates/components/header.html`: the title id is now `header-page-title`. `core.js` expected that id, so every page's topbar was stuck on "Cluster Overview".

## Bugs fixed in the old page
- Capacity was always 0%: it read `t.used_percent`, which the tier API does not return.
- Queue and audit lists were always empty: the API returns `{tasks}`/`{audits}`, but the page expected arrays. Task progress (`progress`) and audit fields (`at`, `tier`) were also mismatched.
- A fixed 840px SVG was scaled down, which made text unreadable. Window listeners leaked on every render. Drag worked with the mouse only.
- "Add Rule" created a random demote edge. It now opens a From/To form with the rule kind inferred.
- Save sent a partial policy (dropping `tick_seconds` and others). It now sends the full policy.

## Verification
- Browser checks at 1440px (graph view), 900px and 375px (pipeline view): drag-to-connect for promote, demote and expire; the expire confirm modal; the mobile sticky save bar; every tab. No console errors, and no page-level horizontal overflow.
- Nothing was saved to the live policy during testing (all drafts were discarded).
- `go build`, `go vet` and `go test ./...` pass.

## Open
- The policy save round-trip was not exercised against the live DB (to avoid changing the user's policy).
