# Handoff: Cluster & Agent Redesign + VPS Provisioning Investigation

**Date:** 2026-10-08  
**Topic:** Cluster Redesign, Worker Packaging, Quota Engine, Security Hardening, and VPS Provisioning Diagnostics  
**Git Branch:** `main`  

---

## 1. Completed Work

### Phase 1: Security & Critical Bug Fixes
- **Deleted Dead Code:** Dropped unused `pkg/cluster/transfer_native_backup.go` (402 lines).
- **Authentication Middleware:** Added `verifySecret()` checking `X-Cluster-Secret` / Bearer token across sensitive agent endpoints (`/api/install-tools`, `/api/uninstall-tools`, `/api/v1/ingest`, `/api/v1/ingest-upload`, `/api/v1/ingest-delete`, `/api/v1/storage-clean`).
- **Path Traversal Fix:** Enforced strict `validKey()` alphanumeric sanitization on `/api/v1/ingest-delete` to eliminate path traversal vulnerabilities.
- **Data-Loss Prevention:** Removed destructive eviction loop (`newestHDDFile`) in `pkg/fileapi/service.go`. System now returns clean `ErrInsufficientStorage` when tiers are saturated without deleting existing files.
- **Ghost Record Watchdog Hardening:** Disabled automated periodic database drops in `startWatchdogLoop`.
- **Audio Stripping Fix:** Fixed FFmpeg remuxing arguments in `pkg/media/cmaf.go` to preserve audio tracks (`-map 0:v:0?`, `-map 0:a:0?`) instead of using `-an`.
- **Credential Masking:** Masked `SSHPassword` in `pkg/web/handlers_nodes.go` and `pkg/web/dbadmin.go`. Protected DB admin mutations with cluster secret.
- **Resource & FD Leak Prevention:** Replaced package-global map with `activeTransferEntry` tracking `lastAccess` time; added background reaper in `startStreamingServer` syncing and closing descriptors idle for > 10m.
- **Host Mount Isolation:** Restricted `autoSelectBestMediaDir` in `pkg/cluster/agent.go` to only trigger on production paths (`/var/lib/stream/media`), fixing `TestResolveTargetDir`.

### Phase 2: Worker Packaging Pipeline (`/opt/stream`)
- **Self-Contained Bundle Generator:** Created `pkg/provision/bundle.go` with `BuildOrGetWorkerBundle()`, generating `stream-bundle-linux-amd64.tar.gz` containing `stream`, static media utilities (`aria2c`, `ffmpeg`, `ffprobe`, `rclone`), and `/opt/stream` structure.
- **Bundle Download Endpoint:** Added `GET /download/stream-bundle-linux-amd64.tar.gz` to `pkg/web/routes.go`.
- **Unprivileged Deployment:** Updated `pkg/provision/provision.go` to unpack bundle to `/opt/stream` and run `stream-agent` systemd service under unprivileged `User=stream`.

### Phase 3: Quota Engine (`QuotaGuard`)
- **Engine Implementation:** Created `pkg/cluster/quota.go` and unit tests in `pkg/cluster/quota_test.go`. Features atomic `Reserve()`, `Commit()`, and `Reclaim()`.
- **Pre-flight Write Guards:** Added quota checks to `/api/v1/ingest`, `/api/v1/ingest-upload`, and `/api/v1/ingest-chunk`, returning `HTTP 507 Insufficient Storage` when exceeded.
- **CLI Flag:** Added `--quota-bytes` flag to `stream agent` in `cmd/stream/main.go`.

---

## 2. Diagnostics: "Add New VPS" Modal Hiding & Missing Node

### Symptoms
When submitting the "Add New VPS to Cluster" modal with valid credentials for remote node `204.136.10.31`:
1. Modal displays progress, then hides after ~1.5s.
2. The node does not appear on the dashboard or `/api/pool`.

### Root Cause Analysis
1. **Network Reachability Trap (RFC 1918 Private IP):**
   - The coordinator was running on a local WiFi network at `192.168.0.189:1212`.
   - The dashboard sent `coordinator_url: http://localhost:1212`.
   - `ResolveCoordinatorURL()` automatically translated `localhost` to `http://192.168.0.189:1212` using the local LAN interface (`wlp1s0`).
   - The provisioner wrote the remote VPS systemd service with:
     `ExecStart=/opt/stream/bin/stream agent --join=http://192.168.0.189:1212 ...`
   - A remote public VPS (`204.136.10.31`) cannot route to private RFC 1918 address `192.168.0.189`. The agent repeatedly fails to dial the coordinator and cannot send its telemetry heartbeat.
2. **Premature Modal Auto-Close in Frontend (`app.js`):**
   - `POST /api/nodes/provision` returns HTTP 200 as soon as the SSH commands complete (`systemctl restart stream-agent`).
   - The frontend immediately calls `finishSuccess()` and runs `setTimeout(() => closeAddVPSModal(), 1500)`.
   - The modal closes before verifying whether the agent's first heartbeat actually arrived at the coordinator.
3. **Missing Pending State in Coordinator:**
   - `coord.nodes` only populates when a live heartbeat is received. Before the heartbeat arrives, `coord.GetNodes()` and `/api/pool` return an empty list (`nodes: []`), showing nothing on the dashboard.

---

---

## 3. Implemented Fixes (`golang-design-patterns` & `better-interface`)

1. **Explicit Node Lifecycle State Machine (`StatusConnecting`):**
   - Added `StatusConnecting` to `NodeStatus` in `pkg/cluster/types.go`.
   - Added `RegisterProvisionedNode(nodeID, host string)` in `pkg/cluster/coordinator.go` and wired it into `pkg/web/handlers_nodes.go:handleProvisionNode`.
   - Newly provisioned nodes now immediately appear in memory and BadgerDB, rendering directly on the dashboard with a pulsating `Connecting` badge and status pill (`⏳ Provisioned • Waiting for telemetry heartbeat`).
2. **Network Reachability Guard & Warnings:**
   - Added `isPrivateOrLoopback()` in `pkg/provision/provision.go`.
   - When provisioning detects a public VPS paired with a private/loopback coordinator target, it surfaces an explicit warning directly in the real-time provisioning log.
3. **Coordinator Public Peering / Tunnel URL Field:**
   - Added `vps-input-coordinator-url` in `web/templates/components/modal_add_vps.html`.
   - Added real-time reachability detection in `web/static/js/app.js` (`checkCoordUrlSafety`): alerts the user with an orange warning banner whenever a private LAN or loopback IP is detected, prompting for a public IP, domain, or Cloudflare Tunnel / ngrok URL.
4. **Resilient Provisioning Lifecycle in UI:**
   - Updated `web/static/js/app.js:handleProvisionSubmit` to await `waitForProvisionedNode` before declaring success.
   - Modal no longer auto-closes prematurely. If the first heartbeat arrives, it celebrates and closes cleanly. If it times out after 30s, the modal remains open with explicit diagnostic steps (reachability, firewall, `journalctl` instructions).
   - Rebuilt binary and reloaded background coordinator (`task-868` on port 1212). All tests pass (`go test ./...`).

---

## 4. VPS Edit Management & Bundle Tools Cleanup (`better-layout` & `better-accessibility`)

1. **Full VPS Node Edit Feature:**
   - Added `POST/PUT /api/nodes/{id}/config` in `pkg/web/handlers_nodes.go` allowing direct updates to node alias/label, storage contribution cap (quota), active/disabled state, and SSH credentials without re-provisioning.
   - Added `CustomLabel` field to `NodeRecord` (`pkg/cluster/types.go`) and `SetNodeCustomLabel()` in `pkg/cluster/coordinator.go` for immediate in-memory reflection on the dashboard.
   - Replaced single-purpose Quota button on node cards with an accessible **Edit** button (`<button class="btn-quota-gear" id="btn-edit-...">`).
   - Overhauled `modal_node_detail.html` into a complete, structured Edit Modal: logical grouping (Identity, Quota, SSH, Hardware), native form submission, explicit `<label for="...">` associations, keyboard focus trapping on open, and `Escape` key dismissal.
2. **Media Worker Tools Cleanup (Badges over Clunky Buttons):**
   - Eliminated the obsolete 90-line `toolsPanel` and manual "Install / Uninstall / Install All / Remove All" buttons that were remnants of runtime `apt-get` reliance.
   - Replaced with a compact, modern capability status bar (`bundleToolsRow`) showcasing `/opt/stream/bin` self-contained tools (`aria2c`, `ffmpeg`, `rclone`) with clean ready/missing status badges.

---

## 5. Modal Quota Pruning & Dynamic Coordinator Peering Reconfiguration

1. **Pruned Redundant Quota UI (`modal_node_detail.html`):**
   - Stripped away redundant range slider, duplicate text badges ("Unlimited (0 GB)"), and bulky paragraph text.
   - Replaced with a clean, single-responsibility numeric input (`Storage Allocation Cap`) with GB unit badge and dynamic state pill (`Unlimited (Full Drive)` vs `Capped at X GB`).
2. **Coordinator Peering URL in Node Edit Modal:**
   - Added editable `modal-edit-coord-url` input in `web/templates/components/modal_node_detail.html` with real-time LAN/public reachability detection (`renderCoordSafetyBadge`).
   - Persisted `CoordinatorURL` in `db.NodeConfig` (`pkg/db/badger.go`).
   - Added `provision.UpdateServiceOverSSH` in `pkg/provision/provision.go`: updates `/etc/systemd/system/stream-agent.service` on the remote VPS over SSH and triggers `systemctl daemon-reload && systemctl restart stream-agent`.
   - Wired asynchronous reconfiguration in `handleNodeConfig` (`pkg/web/handlers_nodes.go`): changes to coordinator URL or quota automatically update the live agent daemon on the remote VPS without manual SSH intervention.
3. **Live Verification:**
   - Reconfigured remote node `VPS-01` (`204.136.10.31`) with Cloudflare Tunnel URL `https://sufficient-prior-wish-cents.trycloudflare.com`.
   - Background SSH reconfiguration succeeded; `VPS-01` immediately started dialing the coordinator via Cloudflare Tunnel.
   - Cluster status verified: `VPS-01` transitioned to `online`, actively reporting 8 AMD Ryzen CPU cores, 16 GB RAM, and 310 GB SSD disk telemetry.

