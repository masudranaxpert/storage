# Plan: S3/Cloudflare R2 Bucket Management & Tier Integration

## Goals
- Allow users to add and manage Cloudflare R2 / AWS S3 / MinIO buckets.
- Display buckets on `/storage` with live status, provider badges, and actions.
- Allow attaching registered buckets to any Tier on `/tiers` like physical drives.
- Remove AI-slop descriptions and subtitle filler across the web interface.

## Scope & Implementation Checklist
- [ ] **Phase 1: DB & Storage Models (`pkg/db/buckets.go`)**
  - Implement `BucketRecord` struct.
  - Implement `SaveBucket`, `GetBucket`, `ListBuckets`, `DeleteBucket`.
  - Register `bucket` in `pkg/db/admin.go`.
- [ ] **Phase 2: Standard Library S3 SigV4 Client (`pkg/storage/s3.go`)**
  - Implement lightweight pure-stdlib AWS SigV4 authorization.
  - Implement `TestBucketConnection(b *BucketRecord) error`.
- [ ] **Phase 3: Web Endpoints (`pkg/web/handlers_buckets.go`, `pkg/web/routes.go`)**
  - `GET /api/storage/buckets`
  - `POST /api/storage/buckets`
  - `POST /api/storage/buckets/test`
  - `DELETE /api/storage/buckets/{id}`
- [ ] **Phase 4: Tier Manager Integration (`pkg/storage/tier.go`)**
  - Expose unassigned buckets for tier attachment.
  - Support `bucket:` node ID and R2/S3 disk type.
- [ ] **Phase 5: Frontend UI (`storage.html`, `storage.js`, `tiers.html`, `tiers.js`)**
  - Add S3/R2 Buckets section on `/storage` with "+ Add Bucket" modal.
  - Update `/tiers` "+ Attach Drive" drawer to support attaching buckets.
- [ ] **Phase 6: Anti-Slop Copy Cleanup**
  - Prune verbose AI filler subtitles and descriptions from sidebar, header, and pages.
- [ ] **Phase 7: Verification & Build**
  - Run `go test ./...` and `go build`.
  - Restart coordinator and verify live.
