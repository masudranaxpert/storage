# Media DRM & Download Protection Architecture Plan

## Overview
This plan specifies the architecture and implementation roadmap for enterprise-grade video download protection and media encryption (ClearKey AES-128 and FairPlay/Widevine DRM) for the Stream distributed media cluster.

## Protection Layers

### Layer 1: Dynamic Opaque Tickets & IP Pinning (Implemented in Phase 1)
- Mask internal BadgerDB media keys (`HELH5QjXyETgEwUW`) with ephemeral HMAC-AES session tickets (`/stream/t_<token>`).
- Bind session tickets to client IP and 4-hour expiration window.
- Fail closed: unauthenticated requests and expired tickets return HTTP 403 Forbidden.

### Layer 2: Real-time Byte-Range Throttling
- Prevent fast-grab download managers (IDM, FDM, browser extensions) from pulling the full asset at wire speed.
- Rate limit the HTTP range responses to ~1.25x the video bitrate.
- Browser HTML5 `<video>` buffers smoothly, while automated scrapers take hours to download, discouraging batch ripping.

### Layer 3: AES-128 Envelope / ClearKey Encryption (Future Phase)
1. **Packaging**:
   - Ingest pipeline remuxes media into fragmented MP4 / CMAF with AES-128 encryption.
   - Video frames are encrypted using a symmetric 16-byte key generated per asset.
2. **Key Acquisition**:
   - Web player (Shaka Player / Video.js / Plyr with EME) requests the key via `/api/v1/drm/key?ticket=...`.
   - Key server validates caller session and returns the decryption key directly to the browser's protected Encrypted Media Extensions (EME) memory pipeline.
3. **Decryption Guarantee**:
   - If an unauthorized user downloads the raw file, it is completely unplayable (scrambled ciphertext) in VLC, QuickTime, or desktop players without the dynamic key.

### Layer 4: Multi-DRM (Widevine & Apple FairPlay)
- For native mobile apps (iOS AVPlayer, Android ExoPlayer), integrate hardware-backed CDM (Content Decryption Module).
