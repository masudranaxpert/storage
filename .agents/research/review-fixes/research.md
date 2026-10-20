# Review Fixes & Key Masking Research

## Overview
Analysis of the current codebase for fail-closed stream authentication, opaque ticket generation (key masking), admin cookie hardening, SSH TOFU, TLS verification, and God file decomposition.

## Key Areas
1. **Opaque Playback Ticket**:
   - Stateless AES-GCM ticket format: `t_<base64url(nonce + ciphertext)>`.
   - Encrypts `{key}:{exp}` using SHA-256 derived key from cluster secret.
   - Hides `HELH5QjXyETgEwUW` completely from URLs.
2. **Fail-Closed Stream Auth**:
   - Coordinator (`pkg/web/handlers_stream.go`) and Agent (`pkg/cluster/agent.go`) must require either an authenticated caller/session or a valid, unexpired token/ticket.
   - Empty secret + unauthenticated request = 403 Forbidden.
3. **Admin Cookie Hardening**:
   - Use dynamic random tokens with expiry, server-side validated, with `Secure`, `HttpOnly`, `SameSite=Strict`.
4. **SSH TOFU & TLS Verification**:
   - Replace `InsecureIgnoreHostKey` with `known_hosts` TOFU verifier under `data/known_hosts`.
   - Default `InsecureSkipVerify` to false in downloaders.
5. **Decomposition**:
   - Extract stream server, heartbeat, and tool installers from `agent.go`.
