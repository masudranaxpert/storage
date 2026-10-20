# Review Fixes: Agent Prompt

Copy everything below the line into the implementing agent.

---

You are working in the Go repo at `/home/masud/work/storage` (Stream: VPS cluster manager and media streaming coordinator). First read `AGENTS.md` and `.agents/INDEX.md` and follow them strictly: idiomatic Go, standard library only, no new external dependencies, concise why-focused comments, flat error handling, never ignore errors, never commit secrets. Code and git state beat docs.

The working tree already has uncommitted changes (`pkg/cluster/agent.go`, `pkg/cluster/token.go` (new), `pkg/fileapi/api.go`, `pkg/fileapi/token.go`, `pkg/web/handlers_flows_test.go`, `pkg/web/handlers_stream.go`). They move stream-token HMAC helpers into `pkg/cluster`, add token checks to the agent streaming server, and add a multi-track `/streams` response. Keep that work; build on it. Do not `git reset`, `git checkout`, or `git stash` it away. Do not commit or push. Leave everything uncommitted for review.

## Tasks (do them in this order)

### 1. Make tests compile and pass
- `pkg/web/handlers_flows_test.go:408` uses `fmt` without importing it. Fix the import.
- `go build ./... && go vet ./... && go test ./...` must be fully green.

### 2. Stream and agent auth must fail closed
- `pkg/web/handlers_stream.go` `handleStreamManifest`: today the token check runs only when `secret != ""`, so an empty secret leaves `/stream/*` open. Change it so playback is allowed only for an authenticated caller (`s.isCallerAuthenticated(r)`) or a valid stream token. If the secret is empty and the caller is not authenticated, return 403.
- `pkg/cluster/agent.go` streaming server (around line 1281): same rule. An empty `a.Secret` must not mean open access. Keep `verifySecret` or valid-token as the only ways in.
- Check every other path touched by this diff and by `requireSecret` / `fileapi` `handleToken` (`pkg/fileapi/api.go` around line 286) for the same empty-secret bypass, and fix it.
- Add or adjust tests: empty secret + no token gives 403 on the coordinator and on the agent; valid token gives 200 or 307; expired token gives 403; authenticated session gives 200.

### 3. Admin cookie hardening (`pkg/web/auth.go`)
- Today `adminCookieToken` is a static HMAC of a constant, so it never expires or rotates. Replace it with a per-login random value (crypto/rand, at least 32 bytes) bound to an expiry, validated server-side (reuse the existing session store in `pkg/db/auth.go` if it fits; otherwise a small in-memory or Badger-backed store). Do not modify BadgerDB files directly; use the existing store APIs.
- Set `Secure` when the request is HTTPS (`r.TLS != nil` or `X-Forwarded-Proto == https`), keep `HttpOnly` and `SameSite=Strict`, and set `MaxAge`.
- Keep the bootstrap flow (no users yet) working. Keep existing tests passing and add tests for expiry and tampering.

### 4. SSH host key and TLS verification
- `pkg/provision/provision.go` uses `ssh.InsecureIgnoreHostKey()` in 6 places (lines ~88, 264, 327, 386, 437, 531). Replace it with one shared helper that does trust-on-first-use: store the host key fingerprint per host (use the existing DB store or a `known_hosts`-style file under the data dir) on first connect, and reject a mismatch on later connects with a clear error. Add a way to explicitly re-trust a host (an explicit parameter or option, not a silent default).
- `pkg/download/downloader.go:147` and `pkg/download/parallel.go:34` set `InsecureSkipVerify: true`. Default to verified TLS. If skipping is needed for a specific use, make it an explicit functional option that is off by default.
- Do not change public HTTP or gRPC wire formats. If a fix would need a breaking API or wire change, stop and write the question in the handoff instead of doing it.

### 5. Break up the God files (behavior-preserving only)
Split by responsibility into new files in the same package. Do not rename exported symbols or change behavior. Move code only.
- `pkg/cluster/agent.go` (~2240 lines): e.g. `agent_stream.go`, `agent_tools.go`, `agent_heartbeat.go`, based on what you find.
- `pkg/lifecycle/executor.go` and `pkg/lifecycle/engine.go`: split along clear seams.
- Run `go test ./...` after each split. If a split gets risky, stop and leave it.

### 6. Hygiene
- `gofmt -w` on every file listed by `gofmt -l .`, then confirm `gofmt -l .` prints nothing.
- Add basic unit tests for pure helpers in `pkg/media` (no real ffmpeg needed) only if cheap. Skip if it needs binaries.
- `bash -c` install scripts (`tools.InstallShell` and friends) must only ever receive values from a fixed allow-list of tool names. Add a validation guard and a test so an arbitrary string is rejected before reaching the shell.
- `isDirectLoopback` (`pkg/web/auth.go:94`) trusts loopback when no proxy headers exist. Add a comment documenting the reverse-proxy assumption, and an opt-out config or flag only if it is trivial. Do not redesign it.

## Rules
- Follow the "Meaningful change" workflow in `AGENTS.md`: write findings to `.agents/research/review-fixes/research.md`, tick the checklist in this file, and write `.agents/sessions/<date>-review-fixes/HANDOFF.md` with git state, completed tasks, skipped tasks and why, and next steps. Link it from `.agents/INDEX.md`.
- Never put secrets or real credentials in any file or test (use obvious dummy values like `test-secret`).
- Do not add dependencies.

## Definition of done (report each as pass/fail with the actual command output)
```
go build ./...
go vet ./...
go test ./...
gofmt -l .        # empty output
git status --short
git diff --stat
```
Finish with a short report: what changed per task, anything skipped, and any decision that needs the user.

## Checklist
- [ ] 1 tests compile and pass
- [ ] 2 fail-closed stream/agent auth + tests
- [ ] 3 admin cookie hardening + tests
- [ ] 4 SSH TOFU host key + TLS verify default
- [ ] 5 file splits, behavior preserved
- [ ] 6 gofmt, install-shell guard, docs
- [ ] handoff written and linked
