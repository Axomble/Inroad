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

## Deferred (documented, not yet built)
- KMS-backed data-encryption keys (Cloud) — today a single local master key.
- Rate limiting / abuse controls on auth and connect endpoints.
- Audit log for sensitive actions (mailbox connect/disconnect, settings changes).

## Checklist for a security-sensitive change
- [ ] New stored credential? → sealed via `crypto.Sealer`, absent from responses/logs.
- [ ] New outbound dial to a user-supplied host? → routed through the SSRF guard.
- [ ] New tenant-scoped query? → filtered by `workspace_id` from the JWT.
- [ ] New secret/config? → env-loaded, fail-closed in compose, in `.env.example`.
