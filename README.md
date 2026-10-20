# Stream

Distributed VPS cluster manager and byte-range media streaming coordinator written in Go.

Ingests video from URLs or file uploads, validates codecs, remuxes to fast-start MP4/CMAF, extracts WebVTT subtitles, places media across tiered storage (NVMe, SSD, HDD, Cloudflare R2 / S3), and coordinates direct HTTP 206 byte-range streaming.

## Quickstart

```bash
# 1. Build single binary
go build -o stream ./cmd/stream

# 2. Start Coordinator (Dashboard: http://localhost:1212)
./stream coordinator --port=1212

# 3. Join a Worker Node
./stream agent --join=http://127.0.0.1:1212 --port=2052
```

## Architecture

- **Coordinator (`./stream coordinator`)**: Control plane running a web UI on `:1212`, gRPC hub on `:9090`, BadgerDB persistence, storage tiering planner, and automatic heat-based lifecycle engine.
- **Worker Agent (`./stream agent`)**: Self-contained edge runner on port `:2052`. Handles downloads (`aria2c`/`http`), FFmpeg remuxing, thumbnail extraction, and direct HTTP 206 partial-content streaming.
- **Storage Tiering**: Organizes drives and remote buckets into tiers:
  - **Tier 0 (Hot)**: High-speed NVMe/SSD or low-latency cloud buckets.
  - **Tier 1 / 2 (Warm/Cold)**: High-capacity HDDs or archive buckets.
  - **Cloud Buckets (S3/R2)**: Standard library AWS SigV4 client with direct CDN redirect (`pub-...r2.dev`) and automated CORS management.

## Key Capabilities

- **Zero-Dependency S3/R2 Client**: Built-in AWS SigV4 signing using standard library `net/http` and `crypto` packages — no external AWS SDK.
- **Decayed Heat Relocation**: Tracks real-time playback demand with half-life decay, automatically promoting hot videos to fast tiers and demoting cold files.
- **Ghost Record Reconciliation**: Real-time verification of files on disk/buckets with instant purge via `files-sync?force=true`.
- **Single-Binary SSH Provisioning**: Automatically bootstraps remote Ubuntu/Debian VPS workers over SSH via `./stream add-vps`.

## CLI Reference

| Command | Description | Default Port |
|---------|-------------|--------------|
| `./stream coordinator` | Start cluster control plane and web dashboard | `:1212` |
| `./stream agent` | Start worker daemon and join cluster | `:2052` |
| `./stream add-vps` | Provision and install remote worker over SSH | — |
| `./stream status` | Check status of connected mesh nodes | — |
| `./stream check` | Audit local dependencies (`ffmpeg`, `aria2c`) | — |

## Core HTTP API

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/api/v1/files` | `POST` | Ingest media by URL (`{"url": "https://..."}`) |
| `/api/v1/files` | `GET` | List all tracked media packages and progress |
| `/api/v1/files-sync` | `POST` | Audit and reconcile missing disk files (`?force=true`) |
| `/stream/{key}` | `GET` | Stream media (HTTP 206 range or HTTP 307 CDN redirect) |
| `/api/pool` | `GET` | Cluster telemetry (CPU, RAM, disk quotas, nodes) |
| `/api/storage/clean` | `POST` | Clean scratch files or purge media across drives/buckets |

## Requirements

- **Coordinator**: Go 1.22+, Linux or macOS.
- **Worker Nodes**: Linux (Ubuntu/Debian recommended), `ffmpeg` for media remuxing, `rclone` for S3/R2 multipart transfers.

## Development

```bash
go test ./...    # Run all tests
go vet ./...     # Lint and typecheck
```
