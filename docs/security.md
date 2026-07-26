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
   This includes the send-delivery claim/finalize/release
   (`ClaimStepSend`/`ClaimSend`/`SetSendResult`/`ReleaseSend`): each is
   `workspace_id`-pinned, so a foreign/cross-tenant id claims, finalizes, or
   releases zero rows, and the step finalize + enrollment cursor advance commit
   in one transaction.

4a. **Email delivery is idempotent (claim-before-send).** Both send handlers
   claim the `sends` row (`queued`/fresh → `sending`, with a `claimed_at` lease)
   BEFORE the real SMTP call, and only finalize (`sent`/`failed`) after. A retried
   asynq job or a raced sweeper-vs-lazy-chain advance that finds the row already
   `sending`/`sent` loses the claim and skips the send, so the same email is never
   delivered twice. A crashed worker's `sending` row is reclaimed only once its
   lease expires. A transient send error releases the claim (retry); an ambiguous
   or permanent one fails forward — "never double, occasionally drop a rare
   ambiguous send".

## Outbound network (SSRF)
5. **User-supplied hosts are dialed only through the SSRF guard** (`mail.vetAddr`):
   - Always blocked: loopback, link-local (incl. cloud metadata `169.254.169.254`),
     unspecified, multicast.
   - Private RFC1918/ULA: blocked unless `INROAD_MAIL_ALLOW_PRIVATE_HOSTS=true`
     (default true for self-hosted Core; set **false** for multi-tenant Cloud).
   - Port allowlist: SMTP {25,465,587,2525}, IMAP {143,993}.
   - Dial the resolved IP (hostname kept only as TLS ServerName) — closes the
     DNS-rebinding window.
6. **TLS is enforced for SMTP/IMAP by default.** The SMTP send and connection
   test dial STARTTLS-mandatory on 25/587/2525 and implicit TLS on 465 — an
   omitted/false request field can no longer silently downgrade to cleartext
   auth (the pre-Phase-A bug). Cleartext requires an explicit `allow_plaintext`
   opt-out, which is now **persisted** on the mailbox (`mailboxes.allow_plaintext
   BOOLEAN NOT NULL DEFAULT false`) and read back into the send job, so the
   connect-test and every send apply the SAME policy (a plaintext relay that
   passed the test no longer fails on every send). The dead `use_tls` column and
   its request/job plumbing were removed (it was never consulted by the send
   path and contradicted `allow_plaintext`). IMAP is unchanged (already
   TLS-by-default).

## Auth
7. **JWT is HS256 and the signing method is verified on parse** (`auth.ParseToken`
   rejects non-HMAC alg). Tokens carry `sub` (user) and `wid` (workspace) only.

## OAuth (mailbox connect)
8. **OAuth tokens are secrets, treated exactly like SMTP passwords.** The Gmail
   and M365 `oauth2.Token` (access + refresh) is sealed at rest via
   `crypto.Sealer` into `mailboxes.secret_ciphertext`, never logged, and never
   returned in an API response (`mailboxResponse` omits it, same as SMTP creds).
   On a job, the access token is a `[]byte` and is zeroized after the send/poll —
   like `SMTPPassword`. The worker never receives the refresh token, only a
   short-lived access token for one API call. M365 joins under the identical
   invariants: same sealed-token codec, same control-plane-only refresh
   (invariant 9), same workspace-from-verified-state (invariant 10).
9. **Token refresh + reseal + persist happen ONLY in the control plane**
   (`coreapi` inprocess `oauthAccessToken`, the provider-neutral refresh shared
   by Gmail and M365 — resolved to the Google or Azure AD `oauth2.Config` by the
   mailbox's `provider`), which holds the pool and sealer. The worker never
   refreshes, re-seals, or writes a token. A rotated refresh token is re-sealed
   and persisted at job-build time so it is not silently lost.
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
    all go to fixed Google hosts; the M365 code exchange, Graph `/me`,
    `sendMail`/draft, and `/$value` calls all go to fixed Microsoft hosts
    (`login.microsoftonline.com`, `graph.microsoft.com`). None are
    user-controlled input, so they do not go through (and do not need) the
    `mail.vetAddr` guard. The one M365 exception — the opaque, persisted Graph
    delta cursor — is host-pinned before use (invariant 13).

12. **M365 send stores Exchange's authoritative `internetMessageId`.** The Graph
    send path is draft-then-send: it creates a draft (Graph parses the MIME and
    may rewrite the `Message-Id` header we supplied), reads back the
    `internetMessageId` Exchange actually assigned, then sends that draft. That
    authoritative id — not our requested header — is stored as
    `sends.message_id`, so inbound reply/bounce matching (`FindSendByMessageID`)
    keys on the value Exchange used and stays robust. A draft created without an
    `internetMessageId` fails the send rather than persisting an unmatchable value.

13. **The incremental Graph delta cursor is host-pinned before the bearer is
    attached.** The inbox delta cursor is an opaque URL persisted from a prior
    Graph response and re-dialed with the mailbox's access token attached.
    Before every incremental fetch it must parse to an absolute `https` URL whose
    host is exactly `graph.microsoft.com` (`graphHostPinned`); a corrupted or
    hostile stored value (wrong host, plain http, embedded userinfo, unparseable)
    is treated as an expired cursor and re-baselined, so the access token can
    never be exfiltrated off-host. Cursor values are never logged verbatim — only
    their length.

## Field encryption keys (per-workspace DEK)
14. **Field secrets are sealed under a per-workspace data-encryption key (DEK),
    not the master key directly.** SMTP passwords and Gmail/M365 OAuth tokens are
    AES-256-GCM-sealed under a random 32-byte DEK unique to the workspace. Each
    DEK is itself wrapped by the key-encryption key (KEK) via the `KeyProvider`
    seam (`internal/platform/crypto/keyprovider.go`). `LocalKeyProvider` (the
    default, `Name() == "local"`) wraps under an HKDF-SHA256-derived subkey of
    `INROAD_MASTER_KEY` (info label `inroad-kek-v1`), so the KEK never shares a
    key/nonce domain with the legacy raw master key; a cloud KMS is a future
    drop-in behind the same interface. Plaintext DEKs live ONLY in a short-TTL
    (5-minute) in-memory cache that zeroizes the bytes on eviction — never on
    disk, never in a response. Only the wrapped DEK is persisted
    (`workspace_deks.wrapped_dek`).
15. **Every field ciphertext is AAD-bound to its `workspace_id`, and so is the
    wrapped DEK.** The same additional-authenticated-data value (`ws:<uuid>`)
    binds both layers: the DEK sealer's field ciphertext AND the KEK's wrap of
    that DEK. A blob or wrapped DEK minted for workspace A therefore fails
    authentication under workspace B's context — cross-tenant decrypt fails
    closed, not silently returning garbage.
16. **A DEK is never overwritten (fail-if-exists) and its wrapping provider is
    recorded read-only.** `workspace_deks.workspace_id` is the primary key and
    `CreateWorkspaceDEK` never upserts, so a second write (including a race) is
    rejected rather than replacing a live DEK — overwriting would silently
    invalidate every prior ciphertext. On a losing race the Keyring re-reads and
    unwraps the winner. `key_provider` records which KeyProvider wrapped the DEK
    for read-back; rotation is an explicit re-encrypt path, not an in-place
    replace (a backfill/rotation CLI is a deferred follow-up).
17. **`INROAD_MASTER_KEY` is now the KEK.** It wraps DEKs (via the HKDF-derived
    subkey); the raw master key additionally remains the legacy-v1 field key,
    decrypt-only, for pre-DEK blobs. Because the HKDF label domain-separates the
    two roles they never collide. Blast radius is unchanged: losing
    `INROAD_MASTER_KEY` loses every DEK and therefore every sealed secret, exactly
    as before this change.
18. **Crypto-shredding on workspace deletion.** `workspace_deks.workspace_id`
    references `workspaces(id)` `ON DELETE CASCADE` (migration `000013`), so
    deleting a workspace destroys its DEK and renders all of that workspace's
    sealed data (mailbox creds, OAuth tokens) permanently unrecoverable — the
    ciphertext survives only as undecryptable bytes. This is the GDPR-erasure
    primitive.
19. **Legacy v1 blobs stay decryptable and migrate lazily.** The `Sealer`
    envelope is versioned and self-describing: v2 = `base64(0x02 || nonce ||
    aes-gcm(dek, nonce, plaintext, aad))`; pre-DEK v1 = `base64(nonce || ct)`
    under the master key with nil AAD, detected by the absence of the `0x02`
    prefix. A workspace sealer opens either — falling back to the legacy
    master-key sealer for v1 — and always writes v2, so a v1 secret re-seals to a
    per-workspace DEK on its next write (OAuth mailboxes on token refresh). An
    eager backfill CLI is deferred, so SMTP-only secrets sealed before this
    change remain v1 until they are rewritten; they stay workspace-isolated by the
    `workspace_id` SQL filter (invariant 4) in the meantime.

## Reply classification & opt-out compliance
20. **A reply-classified unsubscribe suppresses the address via the existing
    workspace-scoped, idempotent suppression path.** When `processMessage`
    (`internal/worker/inbox/poll.go`) classifies a matched reply as `unsubscribe`
    — or detects an explicit opt-out inside an otherwise-automated reply
    (`LooksLikeUnsubscribe`, where compliance wins over automation) — it calls
    `coreapi.MarkUnsubscribed`, which inserts into the SAME workspace-scoped,
    `ON CONFLICT DO NOTHING` suppression table `MarkBounced` uses (only the reason
    literal differs: `unsubscribe`). The suppression is the load-bearing write and
    runs BEFORE the enrollment stop/tag, so a downstream failure can never skip it,
    and it fires EVEN WHEN there is no enrollment (a legacy direct-send opt-out
    must still suppress the address). Opt-outs are honored in the fail-safe
    direction: the accepted trade-off is occasionally suppressing an out-of-office
    whose footer says "unsubscribe" — over-honoring an opt-out is compliant;
    under-honoring one is not.
21. **Classification is pure, offline, and side-effect-free.** Layers 1–2
    (`internal/platform/replyclassify`) are deterministic header/lexicon scans with
    no database access, no network/outbound dial, and no per-message global state;
    the optional Layer-3 model seam is injected via `New` and is currently UNWIRED
    (`New(nil)`), so the shipped path adds no AI dependency and no new SSRF surface.
    The classifier reads only the reply's headers/subject/body projection and never
    logs PII or secrets; it emits a class/source/confidence, not message content.
    Automated replies (`auto_reply`/`out_of_office`) are tagged via
    `RecordReplyClass` but keep the enrollment ACTIVE — they never over-suppress an
    address nor wrongly stop a sequence (the OOO-trap fix). Every reply-driven write
    (`MarkReplied` / `MarkUnsubscribed` / `RecordReplyClass`) is `workspace_id`-pinned
    from the poll job, upholding invariant 4.

## Worker routing (per-IP send distribution)
22. **Binding a worker's outbound source IP never bypasses the SSRF guard.** The
    optional `LocalAddr` on the mail dialer (from `INROAD_WORKER_EGRESS_IP`) sets
    only the *source* address. `mail.vetAddr` still runs FIRST on every send/poll
    dial — resolving the destination and rejecting loopback/link-local/metadata
    (`169.254.169.254`)/private/multicast — and the dial still targets the vetted
    destination IP. Source-bind can only choose the egress interface; it can never
    reach a destination the guard blocked (`internal/platform/mail/{sender,inbox,
    net_tester}.go`, proven by `TestSendSourceBindDoesNotBypassSSRF` /
    `TestInboxSourceBindDoesNotBypassSSRF`).
23. **A job's routing destination is derived server-side, never from the client.**
    The target queue (`w:<worker_id>` or the shared default `""`) comes from the
    persisted `mailbox_worker_assignments` row via `AssignMailboxWorker`
    (`internal/coreapi/inprocess/workerrouting.go`), computed from the worker
    registry — no request field influences which worker/IP a mailbox's mail
    egresses through, so a caller can't pin or divert traffic.
24. **Assignment is tenant-scoped; the worker registry is not tenant data.**
    `mailbox_worker_assignments` is `workspace_id`-pinned (invariant 4): a mailbox
    is assigned/read only within its own workspace, and the assignment is
    idempotent (`ON CONFLICT` keeps a single race winner). The `workers` heartbeat
    registry is global infrastructure state — it holds no tenant rows and is never
    returned on a tenant-facing API.

## Deferred (documented, not yet built)
- Cloud KMS as a second `KeyProvider` (KEK) behind the existing seam — today only
  `LocalKeyProvider` (wraps DEKs under `INROAD_MASTER_KEY`) is implemented.
- Eager re-seal/rotation CLI: backfill pre-DEK v1 blobs to v2 and re-encrypt DEKs
  under a rotated KEK (today v1→v2 migration is lazy, on next write).
- Rate limiting / abuse controls on auth and connect endpoints.
- Audit log for sensitive actions (mailbox connect/disconnect, settings changes).
- Server-side single-use nonce store for the OAuth `state` (see invariant 10).
- Rate limiting + an audit log on reply-driven suppression/stop. Reply-driven
  actions (`MarkReplied`/`MarkUnsubscribed`) are workspace-bounded (invariant 21)
  but are spoofable WITHIN a workspace: anyone who knows a target contact email
  and a real `Message-ID` of a send could forge an inbound reply that suppresses
  the contact or stops its enrollment. This is bounded (no cross-tenant effect, no
  data read) but unthrottled and unlogged today; a rate limit + audit trail on
  reply-driven state changes is the intended hardening.

## Checklist for a security-sensitive change
- [ ] New stored credential? → sealed via a workspace Sealer from
      `Keyring.SealerFor(ctx, ws)` (per-workspace DEK, AAD-bound), absent from
      responses/logs.
- [ ] New outbound dial to a user-supplied host? → routed through the SSRF guard.
- [ ] New tenant-scoped query? → filtered by `workspace_id` from the JWT.
- [ ] New secret/config? → env-loaded, fail-closed in compose, in `.env.example`.
- [ ] New OAuth/state-authenticated flow? → `state` HMAC-signed + TTL; tenant
      derived from the verified state, not a request param; token refresh stays
      in the control plane.
