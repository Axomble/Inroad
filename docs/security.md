# Security Invariants

Rules that MUST hold across every change. If a change would break one, stop and
reconsider — these are the failure modes that cause data loss, credential leaks,
or SSRF. (Not a full threat model; that's future work.)

## Credentials & secrets
1. **All mailbox credentials are envelope-encrypted at rest** via `crypto.Sealer`
   (AES-256-GCM). Raw SMTP/IMAP passwords or OAuth tokens NEVER hit Postgres or
   logs in plaintext. Seal before persist; open only in the worker/send path.
2. **`secret_ciphertext` is never returned in an API response.** Response DTOs
   (e.g. `mailboxResponse`) must not carry a ciphertext/secret field at all —
   omission by construction, not by remembering to strip it.
3. **Secrets come from the environment, never hardcoded.** The compose stack is
   fail-closed: `INROAD_JWT_SECRET` / `INROAD_MASTER_KEY` use `${VAR:?}` and
   refuse to start if unset.

## Multi-tenancy
4. **Every tenant-scoped query is filtered by `workspace_id`.** The id comes from
   the authenticated JWT (`auth.UserFromContext`), never from the request body or
   a path param the caller controls. Store methods take `workspaceID` explicitly.

## Outbound network (SSRF)
5. **User-supplied hosts are dialed only through the SSRF guard** (`mail.vetAddr`):
   - Always blocked: loopback, link-local (incl. cloud metadata `169.254.169.254`),
     unspecified, multicast.
   - Private RFC1918/ULA: blocked unless `INROAD_MAIL_ALLOW_PRIVATE_HOSTS=true`
     (default true for self-hosted Core; set **false** for multi-tenant Cloud).
   - Port allowlist: SMTP {25,465,587,2525}, IMAP {143,993}.
   - Dial the resolved IP (hostname kept only as TLS ServerName) — closes the
     DNS-rebinding window.
6. **TLS is enforced for SMTP/IMAP by default.** Plaintext auth requires an
   explicit opt-out (future: per-mailbox flag), never a silent fallback.

## Auth
7. **JWT is HS256 and the signing method is verified on parse** (`auth.ParseToken`
   rejects non-HMAC alg). Tokens carry `sub` (user) and `wid` (workspace) only.

## OAuth (mailbox connect)
8. **OAuth tokens are secrets, treated exactly like SMTP passwords.** The Gmail
   `oauth2.Token` (access + refresh) is sealed at rest via `crypto.Sealer` into
   `mailboxes.secret_ciphertext`, never logged, and never returned in an API
   response (`mailboxResponse` omits it, same as SMTP creds). On a job, the
   access token is a `[]byte` and is zeroized after the send/poll — like
   `SMTPPassword`. The worker never receives the refresh token, only a
   short-lived access token for one API call.
9. **Token refresh + reseal + persist happen ONLY in the control plane**
   (`coreapi` inprocess `gmailAccessToken`), which holds the pool and sealer.
   The worker never refreshes, re-seals, or writes a token. A rotated refresh
   token is re-sealed and persisted at job-build time so it is not silently
   lost.
10. **The callback derives `workspace_id` only from a verified signed `state`,
    never from a request param.** `state` is HMAC-signed (SHA-256, `JWTSecret`)
    with a 10-minute TTL (`internal/platform/oauthstate`). The HMAC proves the
    server minted it and the TTL bounds replay — the public callback carries no
    JWT cookie (top-level redirect from Google), so the state IS the auth. Every
    mailbox the callback creates is pinned to that workspace, so no cross-tenant
    write is possible. **Residual risk:** there is no server-side single-use
    nonce store yet, so a `state` URL leaked within its 10-minute window would
    let an attacker bind *their own* Gmail mailbox into the victim's workspace
    (low value, bounded, no data read). A single-use nonce store is the phase-2
    hardening.
11. **No new SSRF surface.** Gmail API, Google token, and OpenID userinfo calls
    all go to fixed Google hosts, not user-controlled input, so they do not go
    through (and do not need) the `mail.vetAddr` guard.

## Deferred (documented, not yet built)
- KMS-backed data-encryption keys (Cloud) — today a single local master key.
- Rate limiting / abuse controls on auth and connect endpoints.
- Audit log for sensitive actions (mailbox connect/disconnect, settings changes).
- Server-side single-use nonce store for the OAuth `state` (see invariant 10).

## Checklist for a security-sensitive change
- [ ] New stored credential? → sealed via `crypto.Sealer`, absent from responses/logs.
- [ ] New outbound dial to a user-supplied host? → routed through the SSRF guard.
- [ ] New tenant-scoped query? → filtered by `workspace_id` from the JWT.
- [ ] New secret/config? → env-loaded, fail-closed in compose, in `.env.example`.
- [ ] New OAuth/state-authenticated flow? → `state` HMAC-signed + TTL; tenant
      derived from the verified state, not a request param; token refresh stays
      in the control plane.
