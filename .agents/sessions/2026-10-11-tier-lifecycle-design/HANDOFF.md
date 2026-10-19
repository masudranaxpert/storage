# Session Handoff: Codebase Health Check & Tier Lifecycle Design

## Date & Scope
- **Date**: 2026-10-11
- **Focus**: Audit current codebase; design auto-tiering (Data Flow menu, promote/demote/expire, demand API). Design only — implementation delegated via agent prompts.

## Git State
- Branch `main`, uncommitted working tree (pre-existing changes from earlier sessions plus this session's edits). Nothing committed.

## Changed This Session
- `pkg/web/handlers_stream.go`: `mediaUsagePerNode` no longer overwrites per-block measured usage with the node-wide total (tiers on multi-disk nodes were over-charged).
- `gofmt -w` applied to 18 previously unformatted Go files (whitespace only).
- Docs: [research](../../research/tier-lifecycle/research.md), [plan](../../plans/tier-lifecycle-flows.md), [agent prompts](../../plans/tier-lifecycle-prompts.md), INDEX links.

## Verification
- `go build ./...`, `go vet ./...`, `go test ./...` pass; `gofmt -l` empty.

## User Decisions
- Promote = **move** (single copy). Expire = **opt-in** (typed confirm + audit). Agent heartbeat hit report (proto change) = **deferred**. `app.js` split = separate agent (prompt B).

## Next Steps
- Run prompts A and B (parallel), then C → D → E → F → G per [prompts](../../plans/tier-lifecycle-prompts.md).
- Security items 7a/7b in research (unauthenticated progress endpoint, spoofable `Sec-Fetch-Site` in dbadmin) are in prompt A — prioritise.
