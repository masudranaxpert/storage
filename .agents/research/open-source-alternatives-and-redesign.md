# Open-Source Alternatives & Modern Multi-VPS System Redesign

**Date:** 2026-10-08  
**Scope:** Comparative Analysis of State-of-the-Art Solutions & Blueprint for System Redesign  
**Goal:** Replace Fragile Custom Code with High-Leverage, Battle-Tested Open-Source Primitives  

---

## 1. Industry State of the Art: How Distributed Video & Storage Are Solved

Building a distributed video storage and transcoding system across commodity VPS nodes is a well-studied problem. Instead of inventing custom ad-hoc protocols (HTTP tar streams, WebDAV rclone wrappers, and plain-text SSH provisioning), the open-source ecosystem provides proven primitives:

| Concern | What the Current Codebase Does | Industry Best Practice | Open-Source Tool |
| :--- | :--- | :--- | :--- |
| **Multi-VPS Storage** | Raw local disks + DIY tar/chunk HTTP sync | Geo-distributed, self-healing S3 object storage | **Garage S3** or **SeaweedFS** |
| **POSIX Filesystem Bridge** | Custom symlinks & hardcoded paths (`/mnt/hdd/stream`) | Transparent FUSE cache over object storage | **JuiceFS** |
| **Video Ingestion** | Custom `/api/v1/ingest` and raw HTTP streams | Resumable chunked upload protocol | **tus.io (`tusd`)** / S3 Presigned URLs |
| **Job Orchestration** | Custom gRPC push / synchronous agent HTTP | Resilient queue with retry, backoff, and dead-letters | **Temporal.io** or **Asynq / River (Redis/Postgres)** |
| **Transcoding Workers** | Synchronous FFmpeg child processes in agent | Independent runner workers (pull-based) | **PeerTube Runner** or **Dockerized FFmpeg Workers** |
| **Node Provisioning** | Plaintext SSH passwords in DB + `apt install` script | Declarative configuration management & containers | **Ansible / Docker Compose / Nomad** |
| **Streaming Delivery** | Single-file MP4 byte-range over custom Go server | Fragmented HLS / DASH segments with edge caching | **Nginx / Caddy / Cloudflare CDN** |

---

## 2. In-Depth Comparative Analysis: Storage Engines

### A. Garage S3 (Recommended for Geo-Distributed Multi-VPS)
* **What it is:** A lightweight, self-hosted, distributed S3-compatible object store written in Rust.
* **Why it fits Multi-VPS:**
  - Designed specifically for **unreliable WAN connections, heterogeneous VPS providers, and network partitions**.
  - Uses **CRDTs (Conflict-free Replicated Data Types)** instead of Raft quorum for metadata sync across high-latency links.
  - No single master server; true peer-to-peer architecture.
  - S3-compatible API: works out-of-the-box with AWS CLI, rclone, MinIO client, and video libraries.
* **Operational Trade-offs:**
  - Metadata requires fast storage (SSD/NVMe recommended for LMDB/SQLite metadata).
  - Designed for resiliency and eventual consistency rather than raw 100Gbps LAN throughput.

### B. SeaweedFS (Recommended for Maximum Raw Throughput & Built-in FUSE)
* **What it is:** A high-performance distributed blob/object store inspired by Facebook’s Haystack architecture, written in Go.
* **Why it fits Video:**
  - **O(1) disk lookups:** Large video blobs and small HLS chunks are stored in append-only volume files, eliminating filesystem inode exhaustion.
  - Native **POSIX FUSE mount**, WebDAV, and S3 API built into the same binary.
  - Direct volume streaming: Video requests bypass the master and stream directly from volume servers.
* **Operational Trade-offs:**
  - Multi-component topology (Master, Volume, Filer).
  - Cross-site WAN active-active replication requires designated master clusters and careful network tuning.

### C. JuiceFS (The Bridge: Object Storage + POSIX Mount)
* **What it is:** A distributed POSIX filesystem built on top of Redis/PostgreSQL (for metadata) and any S3 object store (for data chunks).
* **Why it fits Video Workflows:**
  - Allows existing software (FFmpeg, Jellyfin, Nginx) to access petabytes of media as a standard local folder (`/mnt/media/video.mp4`).
  - **Aggressive Local Caching:** Automatically caches metadata and data blocks on each VPS node's local NVMe/SSD. Subsequent reads for video streaming hit local NVMe speeds with zero WAN traffic.

---

## 3. In-Depth Comparative Analysis: Distributed Transcoding Architectures

### Pattern 1: PeerTube Remote Runner Architecture (Cleanest Model)
* **How it works:**
  - The central coordinator manages the catalog and exposes a standard Task API.
  - Independent `peertube-runner` agents run on worker VPS instances (or GPU nodes).
  - Workers use HTTP polling or WebSocket to claim transcoding jobs.
  - A worker downloads the source video from S3 storage, runs containerized FFmpeg, generates HLS segments + playlists, uploads the output back to S3, and notifies the coordinator upon completion.
* **Why it wins:**
  - **Zero shared filesystem dependency:** No NFS, no SSHFS, no node-to-node disk mounting.
  - Workers can join or disconnect at any time without destabilizing the cluster.
  - If a worker crashes, the coordinator's queue automatically retries the job on a different worker.

### Pattern 2: Tdarr Server-Node Architecture
* **How it works:**
  - `Tdarr_Server` manages the job queue, database, and web UI.
  - Multiple `Tdarr_Node` instances connect over HTTP/WebSockets to process video files.
  - Uses plugin pipelines for automated inspection, hardware transcoding (NVENC, QSV, VAAPI), and audio normalization.
* **Trade-off:** Typically relies on a shared media mount (NFS/SMB) across LAN, which makes it less ideal for WAN VPS networks unless paired with JuiceFS.

### Pattern 3: Livepeer Chunk-Level Transcoding
* **How it works:**
  - Video streams are split into 2-to-6 second GOP segments.
  - Segments are distributed via gRPC across a pool of orchestrators and transcoders in parallel.
  - Extremely fast (sub-realtime encoding), but introduces significant architectural complexity for keyframe alignment and manifest assembly.

---

## 4. Modern Production Architecture Blueprint for Multi-VPS

To transform the current fragile codebase into a robust, high-performance system, the entire platform should be restructured into **four decoupled layers**:

```
                              [ Viewers / Mobile / Web ]
                                          │
                                          ▼
                         [ Cloudflare / Edge CDN Caching ]
                                          │
                                          ▼
               ┌─────────────────────────────────────────────────────┐
               │         Layer 1: Edge Streaming & Ingest            │
               │   • Caddy / Nginx: HLS / DASH Segment Cache         │
               │   • tusd: Resumable chunked video uploads           │
               └──────────────────────────┬──────────────────────────┘
                                          │
                                          ▼
               ┌─────────────────────────────────────────────────────┐
               │        Layer 2: Core Coordinator & Queue            │
               │   • API Gateway & Metadata Store (PostgreSQL/SQLite)│
               │   • Task Queue: Asynq / River (Go on Redis/PG)      │
               │   • Web Dashboard: Admin, Telemetry, Health         │
               └───────────────┬─────────────────────┬───────────────┘
                               │                     │
               Job Dispatch    │                     │  S3 Metadata / State
                               ▼                     ▼
┌────────────────────────────────────────┐ ┌────────────────────────────────────────┐
│     Layer 3: Transcoding Workers       │ │      Layer 4: Distributed Storage      │
│  • Worker Nodes (Any CPU/GPU VPS)      │ │   • Garage S3 or SeaweedFS (Cluster)   │
│  • Pull-based job consumer (Go runner) │ │   • Geo-distributed multi-VPS replica  │
│  • Containerized FFmpeg / AV1 / H.264  │ │   • S3-compatible API                  │
│  • Source fetch -> HLS Package -> S3   │ │   • JuiceFS mount (optional bridge)    │
└────────────────────────────────────────┘ └────────────────────────────────────────┘
```

### Layer 1: Ingest & Edge Delivery
1. **Resumable Uploads (`tusd`):**
   - Implement the `tus.io` open protocol for file uploads.
   - If an upload from a browser or CLI drops after 90% completion, it resumes seamlessly from the exact byte offset.
2. **Edge Video Delivery:**
   - Instead of streaming huge single `.mp4` files via custom byte-range Go handlers, package videos into standard **HLS (fMP4 / CMAF) playlists (`master.m3u8` + `.m4s` segments)**.
   - Each `.m4s` chunk is an immutable 4-second file. Nginx/Caddy fronted by Cloudflare caches these chunks at edge POPs, reducing worker bandwidth by 95%+.

### Layer 2: Core Coordinator & Queue
1. **Persistent Metadata:**
   - Replace single-file BadgerDB with a relational store (PostgreSQL or clustered SQLite via Litestream/LiteFS).
   - Never store plain-text SSH passwords.
2. **Asynchronous Task Queue:**
   - Use **Asynq** (Go + Redis) or **Temporal.io**.
   - Jobs are modeled as stateful tasks:
     `IngestUploaded -> ProbeMetadata -> TranscodeHLS -> UploadS3 -> NotifyReady`.
   - Built-in exponential backoff, dead-letter queues, and worker heartbeat timeouts.

### Layer 3: Independent Transcode Runners
1. **Pull-based Worker Model:**
   - Worker agents run a lightweight Go runner that polls the coordinator's queue via secure mTLS or API token.
   - Worker claims task -> downloads source file from S3 bucket -> runs FFmpeg with proper multi-audio and subtitle extraction -> uploads HLS package to S3 -> marks task completed.
   - **Zero custom node-to-node transfers:** Worker nodes never connect to each other directly; all synchronization happens through the S3 storage layer.

### Layer 4: Multi-VPS Distributed Storage
1. **Deploy Garage S3 or SeaweedFS:**
   - Run Garage S3 across the VPS pool to form a single unified S3 namespace.
   - Files are automatically replicated across nodes with CRDT consistency.
   - A single disk failure causes zero data loss; the cluster re-replicates missing shards automatically.

---

## 5. Actionable Migration Roadmap

### Phase 1: Security Hardening & Bug Fixes (Immediate)
1. **Secure the Agent:** Add mandatory API token / HMAC authentication to all agent endpoints on port 2052.
2. **Eliminate Critical Vulnerabilities:**
   - Remove `/api/install-tools` and `/api/uninstall-tools` from the web server. OS package management should never be triggered via unauthenticated HTTP.
   - Add strict sanitization and path boundary enforcement to `/api/v1/ingest-delete` to prevent directory traversal.
   - Fix `/api/dbadmin/rows` and remove plain-text SSH credentials from database tables.
3. **Fix Catastrophic Data-Loss Bugs:**
   - Remove the eviction loop in `PlanPlacement` that deletes newest user files.
   - Remove the aggressive DB record deletion in `ReconcileGhostRecords`.
   - Fix `RemuxAndPackageCMAF` to preserve multi-audio tracks instead of passing `-an`.

### Phase 2: Decouple Transfers via Standard S3 (Short-Term)
1. Run **Garage S3** on the VPS nodes.
2. Deprecate the three competing custom transfer implementations (`activeTransferFiles`, tar over HTTP, and rclone rcd over WebDAV).
3. Allow workers and coordinator to read and write directly to the S3 bucket using standard AWS S3 Go SDK.

### Phase 3: Transition to Queue-Worker Architecture (Medium-Term)
1. Adopt **Asynq** or **PeerTube Runner** pattern for video processing.
2. Separate the God-object agent into two clean binaries:
   - `stream-storage` (runs Garage S3 node + edge cache).
   - `stream-worker` (pulls transcode jobs and executes FFmpeg in Docker).
3. Generate true adaptive bitrate HLS (1080p, 720p, 480p with fMP4 CMAF chunks) for modern web and mobile playback.

---

## Summary Verdict

The best code is the code never written. By retiring the ~6,000 lines of custom transfer engines, shell scripts, WebDAV wrappers, and ad-hoc storage heuristics, and replacing them with **Garage S3 + Asynq + HLS**, the system will eliminate 95% of its bugs and gain enterprise-grade durability, security, and scalability.
