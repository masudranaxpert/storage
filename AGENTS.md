# AGENTS.md

## Project
Stream: Distributed VPS cluster manager and byte-range media streaming coordinator written in Go (BadgerDB, gRPC control plane, HTTP Range streaming, self-contained worker bundles).

## Commands (source of truth)
- Install: `go mod download`
- Dev (Coordinator): `go run ./cmd/stream coordinator --port=1212`
- Dev (Worker Agent): `go run ./cmd/stream agent --join=http://127.0.0.1:1212 --port=2052`
- Test: `go test ./...`
- Lint/typecheck: `go vet ./...`
- Build: `go build -o stream ./cmd/stream`

## Code rules
- Idiomatic Go, standard library first, zero unrequested external dependencies (`ponytail` mode).
- Functional options & explicit constructors over global state; avoid `init()` mutations.
- Keep comments concise, formal, and why-focused in technical English.
- Handle errors early with flat code paths; never ignore errors or leak file descriptors/goroutines.

## Boundaries
- Never: commit plaintext credentials, modify BadgerDB disk files directly, or delete user files for cache eviction.
- Ask first: breaking API schema changes, public wire protocol changes, new external dependencies.

## Docs (.agents/)
Start by reading `.agents/INDEX.md`.
- `plans/`: [Cluster & Agent Architecture Redesign](plans/cluster-and-agent-redesign.md)
- `research/`: [Architecture Audit](research/architecture-audit.md), [Open-Source Alternatives](research/open-source-alternatives-and-redesign.md)
- `sessions/`: Session handoff notes with git state, completed tasks, and next steps.

## Workflow
**Small change** (1-2 files, clear bug/typo): inspect, fix root cause, run checks, done.

**Meaningful change** (3+ files, new feature, or architecture/DB/API impact):
1. Inspect relevant code; save findings in `.agents/research/<feature>/research.md`.
2. Write `.agents/plans/<task>.md` (requirements, scope, acceptance criteria, steps).
3. Get user approval when needed.
4. Implement, run checks, tick checklist.
5. Create/update session handoff in `.agents/sessions/<date-topic>/HANDOFF.md` and link from `INDEX.md`.

## Rules
- Code and Git state beat docs. If they disagree, trust the code and fix or flag the doc.
- Keep each fact in one place; link instead of duplicating.
- Keep `INDEX.md` short: links only, details live in plans.
- Never put secrets or credentials in any file.