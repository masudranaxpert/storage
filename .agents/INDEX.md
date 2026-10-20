# Agents Documentation Index

## Plans
- [Review Fixes Agent Prompt](plans/review-fixes-prompt.md): Single prompt for fixing the 2026-10-20 review findings (auth fail-closed, cookie, SSH/TLS, file splits).
- [Cluster & Agent Architecture Redesign](plans/cluster-and-agent-redesign.md): Redesign of `pkg/cluster`, packaging bundle, quota engine, and security hardening.
- [Tier Data-Flow & Lifecycle Engine](plans/tier-lifecycle-flows.md): Flow canvas, promote/demote/expire rules, demand API ([agent prompts](plans/tier-lifecycle-prompts.md)).

## Research
- [Tier Lifecycle Research](research/tier-lifecycle/research.md): Codebase health check (2026-10-11) and gaps for tier data-flow.
- [Brutal Architecture & Code Quality Audit](research/architecture-audit.md): Deep flaw analysis across security, correctness, and codebase design.
- [Open-Source Alternatives & Modern System Redesign](research/open-source-alternatives-and-redesign.md): Comparative analysis of Garage S3, SeaweedFS, JuiceFS, PeerTube Runner, and Temporal.

## Sessions
- [Review Fixes, Opaque Playback Tickets & God File Deconstruction](sessions/2026-10-20-review-fixes-and-opaque-tickets/HANDOFF.md): Stateless AES-GCM playback tickets (`t_...`) masking DB keys, fail-closed auth, dynamic crypto/rand admin cookies, SSH TOFU & TLS defaults, and breaking up God files (`agent.go`, `executor.go`, `engine.go`).
- [S3/R2 Cloud Storage & Anti-Slop Cleanup](sessions/2026-10-19-s3-r2-bucket-storage-and-anti-slop/HANDOFF.md): Zero-dependency AWS SigV4 client in stdlib, BadgerDB bucket persistence, /storage buckets tab & modal, dynamic tier attachment, and anti-slop copy cleanup.
- [Self-Contained Tuned Static Binaries Architecture](sessions/2026-10-12-self-contained-static-binaries/HANDOFF.md): Bundle packaging of verified static ELF media binaries (ffmpeg, ffprobe, aria2c, rclone), autonomous bundle upgrades, distro independence, bumped to v2.7.0.
- [Global Cache Budget 50MB](sessions/2026-10-11-cache-budget/HANDOFF.md): `pkg/cache` constant + registry, PlayDeduper byte-eviction, `/api/system/cache`, Settings usage card.
- [Full Audit, CSS Split & Nil-Store Crash Fix](sessions/2026-10-11-css-split-and-audit/HANDOFF.md): app.css → 8 ordered files (concatenation-proven), fileapi nil-store watchdog fix, security/structure audit status.
- [Data Flow Page UI Rebuild](sessions/2026-10-11-flows-ui-rebuild/HANDOFF.md): Component-based responsive /flows, API shape fixes, header title fix.
- [Phase 5 & 6 Data Flow UI & Documentation](sessions/2026-10-11-phase5-data-flow-ui/HANDOFF.md): Data Flow visual SVG topology canvas, rule editor, preview/queue/hot/simulate/audit tabs, expire safety modal, and media lifecycle drawer.
- [Phase 4 Expire & Audit Trail](sessions/2026-10-11-phase4-expire/HANDOFF.md): Opt-in Expire rules, `ExpireAcknowledged` + confirmation phrase, audit logging before delete, atomic failure handling, and `/api/lifecycle/audit`.
- [Phase 3 Heat, Demand API & Evaluator](sessions/2026-10-11-phase3-heat-evaluator/HANDOFF.md): Hit tracking & deduping (`PlayDeduper`, `HitRecorder`), pure `planner.Evaluate` (pins, promote with make-room, watermarks, idle hysteresis), `/api/flows/preview`, `GET /placement`, and `POST /demand`.
- [Phase 2 Relocation Primitive & Executor](sessions/2026-10-11-phase2-relocation-executor/HANDOFF.md): Agent `/api/v1/relocate` + `/api/v1/media-size`, coordinator `Executor` state machine with crash recovery/cancel, and `/api/lifecycle/tasks*` + `/api/v1/files/{key}/move`.
- [Phase 1 Lifecycle Model, Policy & Ingest Order](sessions/2026-10-11-phase1-lifecycle-policy/HANDOFF.md): `TierLifecycle`, `pkg/lifecycle` policy/validation/engine, BadgerDB `flow`/`lc*` tables, `PlanPlacement` ingest order, and `/api/flows*` endpoints.
- [Phase 0b Frontend Component Split](sessions/2026-10-11-phase0b-frontend-split/HANDOFF.md): Split monolithic `app.js` into `core.js`, `components.js` (`window.UI`), and per-page lifecycle modules.
- [Phase 0 Security & Audit Fixes](sessions/2026-10-11-phase0-audit-fixes/HANDOFF.md): Authenticated progress callbacks, `requireAdmin` + HMAC cookie, context-managed watchdog, 3-strike reconcile, parallel cached media usage.
- [Codebase Health Check & Tier Lifecycle Design](sessions/2026-10-11-tier-lifecycle-design/HANDOFF.md): Usage-accounting fix, gofmt, auto-tiering plan + agent prompts.
- [Impeccable UI Redesign & Anti-Slop Clarification](sessions/2026-10-11-impeccable-ui-redesign-and-anti-slop/HANDOFF.md): Token refinement, typography polish, tabular numbers, anti-slop copy, and layout cleanup.
- [Node IP Sanitization & UI Polish](sessions/2026-10-10-node-modal-and-ip-sanitization/HANDOFF.md): Docker bridge IP filtering, WCAG AA status colors, tabular numbers, and modal enhancements.
