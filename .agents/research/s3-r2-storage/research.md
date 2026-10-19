# Research: S3 & Cloudflare R2 Remote Storage Integration

## 1. Problem & Architecture Context
Stream currently pools local POSIX storage disks (`/mnt/hdd`, `/data`, `/mnt/storage`) mounted on distributed VPS worker nodes. Tiers (`Tier 0 Hot NVMe`, `Tier 1 Warm SSD`, `Tier 2 Cold HDD`, etc.) organize these physical blocks for byte-range streaming, CMAF transcoding, and automated heat-based lifecycle relocations.

While VPS disks offer ultra-low latency, their capacity is bounded by VPS hosting limits. Cloudflare R2 and S3-compatible object storage offer:
- Zero egress bandwidth fees (Cloudflare R2).
- Unlimited scalable remote capacity.
- Direct-to-client CDN playback via public domain/workers.

## 2. Requirements
1. **First-class Storage Resource**:
   - S3/R2 buckets are managed as standalone storage entities (Bucket Name, Endpoint, Region, Access Key, Secret Key, Public CDN URL).
   - Displayed on `/storage` alongside physical volumes, with status, provider, and test connectivity actions.
2. **Flexible Tier Attachment**:
   - On `/tiers`, when attaching storage (`+ Attach Drive`), admins can attach any registered S3/R2 bucket to any tier (e.g. Tier 2 Cold or custom Tier 3 Archive).
   - Appears in the tier's attached blocks list with distinct R2/S3 badge, bucket path, and CDN streaming host.
3. **Zero Bloat (Ponytail Mode)**:
   - S3 authentication implemented via pure Go standard library (AWS Signature Version 4) in ~120 lines without bulky AWS SDK dependencies.
4. **Anti-Slop Copy Cleanup**:
   - Strip AI-generated marketing subtitles and filler paragraphs across the interface.

## 3. BadgerDB Key Schema
- `bucket:<id>`: JSON serialization of `BucketRecord`.
- Logical table: `bucket`.

## 4. REST Endpoints
- `GET /api/storage/buckets`: List registered buckets (secret masked).
- `POST /api/storage/buckets`: Save new bucket (with pre-flight connectivity check).
- `POST /api/storage/buckets/test`: Transient connection check.
- `DELETE /api/storage/buckets/{id}`: Remove bucket.
