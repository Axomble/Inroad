---
title: Security Invariants
description: Mandatory security rules — multi-tenancy pinning, envelope encryption (DEK/KEK), SSRF guards, OAuth handling, warm-up isolation, and the checklist for security-sensitive changes.
---

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
   (`ClaimStepSend`/`SetSendResult`/`ReleaseStepSend`): each is
   `workspace_id`-pinned, so a foreign/cross-tenant id claims, finalizes, or
   releases zero rows, and the step finalize + enrollment cursor advance commit
   in one transaction.

4a. **Email delivery is idempotent (claim-before-send).** The send handlers
   (sequence step and warm-up) claim the `sends` row (`queued`/fresh → `sending`,
   with a `claimed_at` lease)
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

   The **transactional** sender (`platform/notify`, system email: verification,
   password reset, login codes, invites) follows the same rule with its own
   explicit opt-out, `INROAD_SYSTEM_SMTP_ALLOW_PLAINTEXT` (default **false**).
   Unset and empty keep TLS mandatory, and a value `config.Load` does not
   recognise is a **startup error** rather than a silent false — the reasoning is
   identical to the per-mailbox flag above: a configuration mistake must never be
   able to downgrade a send to cleartext, so cleartext has to be *chosen*, never
   defaulted into, and never mistyped into. It exists so the dev stack can reach a local mail catcher
   (Mailpit, plaintext and no AUTH); it must never be set in production. Auth is
   offered only when a system SMTP username is configured, which is orthogonal:
   omitting credentials never relaxes transport security.

   The SAME rule covers the one other operator-supplied outbound host in this
   codebase: a custom `INROAD_S3_ENDPOINT` (`platform/storage.FromEnv`) must be
   `https://` or the binary refuses to start (`ErrInsecureEndpoint`). An
   `http://` endpoint would put SigV4-signed requests, object bodies AND the
   presigned GET/PUT URLs this provider hands out — the ones that leave the
   deployment — in the clear. A missing scheme is refused rather than assumed
   https, because assuming is how a typo becomes a silent downgrade, and the
   opt-out (`INROAD_S3_ALLOW_PLAINTEXT_ENDPOINT`, default false, dev-only for a
   MinIO on a trusted private network) has to be *chosen* exactly like the two
   above. An empty endpoint is real AWS S3, whose URL the SDK builds itself, so
   there is nothing to check.

   Related: transactional email bodies carry single-use bearer credentials
   (verify/reset links, login codes), so **no driver logs a message body**. The
   console driver logs the recipient and subject only. Reading a link in
   development is what the mail catcher is for — do not add body or token
   logging as a debugging shortcut.

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
    write is possible. The signed payload also carries a **purpose**
    (`mailbox_connect`), checked on verify, so a SIGN-IN state can never be
    replayed here and vice versa (invariant 50). **Residual risk:** mailbox connect
    still has no server-side single-use nonce store, so a `state` URL leaked within
    its 10-minute window would let an attacker bind *their own* Gmail mailbox into
    the victim's workspace (low value, bounded, no data read). The LOGIN flow does
    have one (invariant 50), because there a replay is account access; extending it
    to mailbox connect remains the phase-2 hardening.
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

## OAuth provider (token endpoint)
The `/oauth2/token`, `/oauth2/introspect`, and `/oauth2/revoke` endpoints (Inroad acting
as an OAuth 2.1 PROVIDER, distinct from the mailbox-connect client above) are
CLIENT-credentialed — a public client authenticates by `client_id` + PKCE, a confidential
client by `client_secret_basic`/`_post` — and are NOT IP- or account-rate-limited. This is
the intended, accepted posture: PKCE brute force is defeated by consume-first single-use
(a wrong `code_verifier` attempt still burns the authorization code) combined with 256-bit
S256 challenge entropy, and refresh-token theft is bounded by single-use rotation with
reuse detection that revokes the ENTIRE rotation family — both the refresh tokens AND the
access tokens minted in it (shared `family_id`), so a detected reuse invalidates access on
the next request rather than after the ~1h access TTL. A client may introspect/revoke ONLY
its own tokens (own-client policy; a foreign token is inactive/no-op, no cross-client
oracle), and a client may only exercise the grant types it registered for. Adding a rate
limit / abuse control here is tracked in the Deferred list below.

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
    `TestInboxSourceBindDoesNotBypassSSRF`). The Gmail and Microsoft Graph API
    legs bind the same source address on their HTTP transport
    (`internal/platform/mail/apiclient.go`) and are likewise source-only: their
    destinations are fixed provider-host constants, never user input, so there is
    no vet for a source bind to relax.
23. **A job's routing destination is derived server-side, never from the client.**
    The target queue (`w:<worker_id>`, or the shared `send` queue when the
    mailbox has no live assignment) comes from the persisted
    `mailbox_worker_assignments` row via `AssignMailboxWorker`
    (`internal/coreapi/inprocess/workerrouting.go`), computed from the worker
    registry — no request field influences which worker/IP a mailbox's mail
    egresses through, so a caller can't pin or divert traffic.
24. **Assignment is tenant-scoped; the worker registry is not tenant data.**
    `mailbox_worker_assignments` is `workspace_id`-pinned (invariant 4): a mailbox
    is assigned/read only within its own workspace. The assignment is idempotent
    while its worker stays live (`ON CONFLICT` keeps a single race winner); an
    assignment whose worker stopped heartbeating is treated as absent and
    reassigned, and the rotation tick (`RotateMailboxWorkerAssignment`,
    invariant 71) moves one deliberately — so "the pin never moves" is NOT a
    guarantee to rely on. What does
    not move is the tenant: `DO UPDATE` never writes `workspace_id`, and a
    mismatched (mailbox, workspace) pair yields zero source rows from the
    `INSERT … SELECT`, so it never reaches the conflict clause at all. The
    `workers` heartbeat registry is global infrastructure state — it holds no
    tenant rows, and neither does `worker_provider_signals`, which records how a
    provider is treating one egress IP. Scored placement (fleet F4) reads both
    fleet-wide on purpose: `ListPlacementCandidates` answers "what is each worker
    carrying", which is a question about infrastructure and has no per-tenant
    answer. It returns only worker ids and counts — never a mailbox, an address
    or any tenant row — and the single `workspace_id`-filtered aggregate in it
    measures the CALLING workspace's own footprint, so it is a per-tenant number
    computed for that tenant rather than a pin that could be forgotten.

    **Neither is reachable by a machine credential.** Both are readable on
    exactly one surface, `internal/app/fleet` (`/api/v1/fleet`), which is mounted
    in `cmd/inroad`'s SESSION-ONLY group and additionally wraps its whole router
    in `auth.RequireRole("admin")`. Those are two independent barriers: the group
    rejects an `inrd_` API key or an `inoa_` OAuth token at authentication, and
    RequireRole rejects them again on role, because both machine verifiers assign
    an EMPTY role by construction and an empty role ranks below every gate.
    `TestNoScopeGrantReachesTheFleetSurface` drives a principal holding every
    scope in `auth.AllScopes` through the real router and asserts 403.

    This narrows an earlier, stricter reading of this invariant ("never returned
    on a tenant-facing API"), and the narrowing is deliberate rather than
    incidental. A worker id CANNOT be withheld from the placement decision log:
    `fleetdecision.Chose` renders "chose w-1 (score 0.82) over w-2 (score 0.31)",
    and serving that prose unaltered is the whole point of the decision endpoint
    (the constructors exist so a forced decision never claims a comparison nobody
    computed — reformatting the text is how that guarantee is lost). Stripping a
    `worker_id` field while printing worker ids inside a sentence would be
    redaction theatre.

    What the surface withholds instead is more than the identifier: it is not a
    fleet census. `ListWorkspaceFleetWorkers` INNER JOINs through the caller's own
    `mailbox_worker_assignments`, so a worker carrying none of that workspace's
    mailboxes does not appear and the deployment's size is not discoverable;
    every count returned is that workspace's own footprint, never fleet-wide
    occupancy. `RollupWorkspaceFleetProviderSignals` is scoped to the same worker
    set by a subquery inside the statement. The counters it returns are
    shared-fate aggregates — they include events other tenants' mailboxes on the
    same worker caused — which is the fact being reported rather than a leak
    around one: the provider's verdict is about the address, nothing per-tenant
    is recoverable from a sum, and the UI says so on screen.

    The redaction on `GET /dead-letters` (`deadletter.workerQueueLabel`, which
    collapses a per-worker queue to the literal `"worker"`) is NOT superseded by
    any of this and must stay. That route is gated on `campaigns:read`, which IS
    OAuth-grantable, so a delegated third-party client could otherwise enumerate
    fleet hostnames. The gate is what differs between the two surfaces, not the
    sensitivity of the field. See also invariant 70 for the one field the fleet
    read surface withholds outright.

## Warm-up engine
25. **Warm-up mail is strictly isolated from campaign reply/bounce handling.** The
    inbox poller detects a warm-up message (invariant 26) and records it BEFORE
    campaign classification, then returns — a warm-up message never reaches
    `MarkReplied`/`MarkBounced`/`MarkUnsubscribed`/`RecordReplyClass`, even when its
    `In-Reply-To`/`References` match a real prior send. So warm-up traffic can never
    stop, suppress, or bounce a campaign enrollment (`internal/worker/inbox/poll.go`,
    proven by `TestPollInboxWarmupRecordedEngagedAndIsolated` — same message, opposite
    outcome, gated only on token validity).
26. **A message is treated as warm-up only after its HMAC token verifies.** Every
    warm-up send carries an `X-Inroad-Warmup` header signed with a per-deployment
    secret (`INROAD_WARMUP_SECRET`, ≥16 bytes, falling back to `JWTSecret`);
    `warmup.Verify` is a constant-time HMAC check AND the signed payload's
    `workspace_id` must equal the polled mailbox's workspace. An absent, unsigned,
    forged, or cross-workspace header falls through to normal classification —
    an external party without the secret cannot forge one, so cannot swallow a real
    reply as "warm-up" or inject a cross-tenant receipt.
27. **Every warm-up query is `workspace_id`-pinned, every insert self-enforces
    tenancy, and delivery is claim-idempotent.** Partner selection stays in-workspace
    (a mailbox never warms a foreign peer); `warmup_threads`/`warmup_sends`/
    `warmup_receipts` inserts are `INSERT … SELECT … FROM mailboxes WHERE id=$ AND
    workspace_id=$` so a mismatched pair writes zero rows (upholds invariant 4);
    the warm-up send + reply reuse the same claim-before-send lease as campaign sends
    (deterministic id → a retry recovers-forward, never double-sends), and a receipt
    is idempotent on `(warmup_send_id, recipient_mailbox)`.
28. **Recipient-side engagement acts only on the recipient's own mailbox, through the
    same guarded dials, and never builds raw IMAP commands from message content.**
    The rescue-from-spam / mark-read / reply transport is resolved by a
    `workspace_id`-pinned lookup for that receipt's recipient — an attacker cannot
    steer it to another mailbox or tenant. IMAP dials go through the same `vetAddr`
    SSRF guard + source-IP bind as the poller (Gmail/Graph use fixed hosts). The
    attacker-influenceable `Message-ID` and source-folder are passed as go-imap
    quoted/literal protocol arguments (`SEARCH HEADER`, `SELECT`), never
    string-concatenated — no IMAP command injection (`TestSearchByMessageIDInjectionSafe`).
29. **Inbox-vs-spam placement is attributed to the SENDER, not the observing
    recipient.** A warm-up message landing in spam degrades the *sender's* health
    (its outbound mail is not landing), resolved via `warmup_sends.from_mailbox`; the
    recipient only observes it. Attributing spam to the recipient would invert the
    health signal — punishing the innocent inbox owner and never flagging the bad
    sender.

## Sender pools & rotation
30. **A campaign can only ever send from a mailbox in its own workspace.**
    `campaign_senders` carries composite tenant foreign keys
    (`(campaign_id, workspace_id)` → `campaigns`, `(mailbox_id, workspace_id)` →
    `mailboxes`, the migration-000028 pattern), so a cross-tenant pool member is
    unrepresentable rather than merely rejected in Go — the service's 422 is the
    friendly message, the constraint is the guarantee. Every pool query
    (list/replace/counter-bump) and the enrollment assignment claim/read are
    `workspace_id`-pinned (invariant 4), and the mailbox joins are pinned too, so a
    pool row can never surface or select another tenant's mailbox.
31. **Credentials are opened for the RESOLVED sending mailbox, not the campaign's.**
    `GetStepSendJob` resolves the mailbox first (the enrollment's pinned mailbox →
    a rotation selection → `campaigns.mailbox_id`) and only then unseals the SMTP
    password or refreshes the OAuth token, through the same per-workspace
    `Keyring.SealerFor` / control-plane refresh path as before. No new secret is
    stored, no secret is added to a response DTO (the sender-pool DTO carries
    mailbox id/email/provider/status only), and no new outbound dial is
    introduced — the resolved mailbox's transport goes through the identical
    `mail.vetAddr`-guarded send path.
32. **Assignment is write-once, so two workers can never disagree about a thread's
    mailbox.** The claim is
    `UPDATE sequence_enrollments SET mailbox_id = $3 WHERE id = $1 AND workspace_id = $2 AND mailbox_id IS NULL`;
    zero rows means another worker won, and the loser RE-READS the stored value
    rather than using its own selection. The rotation counter bump commits in the
    same transaction. An exhausted pool pins nothing and defers through the
    existing cap-deferral path.

## Contact search & keyset pagination
33. **Every contact-search statement is workspace-pinned as its FIRST bound
    argument, and the pin is inside the index.** The search SQL is assembled per
    access path (`internal/app/contact/search.go`) rather than written as static
    sqlc queries, so the tenant filter is enforced structurally: `where()` emits
    `c.workspace_id = $1` before any optional clause, and every generated shape —
    page and capped count, all three sorts, both travel directions, with and
    without the text/list filters — is asserted to carry it
    (`TestEveryStatementPinsWorkspaceFirst`). The workspace comes from
    `auth.WorkspaceID` (the JWT), never a query param. `idx_contacts_search` is a
    composite `gin (workspace_id, search_text gin_trgm_ops)`, so a search does not
    even read another tenant's index entries to discard them.
34. **The cursor is opaque and carries no authority.** A cursor encodes a sort,
    a direction, a row id and a sort key — never a workspace. Replaying a cursor
    minted in workspace A against workspace B therefore cannot widen the query;
    it only names a position within B's own workspace-pinned scan
    (`TestCrossWorkspaceContactsNeverAppear`). A malformed cursor, or one from a
    different sort, is a typed error mapped to 400 — never a silent reset to the
    first page, which would hide the fault.
35. **A `list` filter is ownership-checked before any read.** The service calls
    `ListChecker.ListExists(ctx, ws, listID)` first, so another tenant's list id
    is a 404 rather than a silently empty page, and the id never reaches a query.
36. **Search work is bounded by construction.** The page fetches `limit + 1`
    rows (limit ≤ 100, rejected outside 1..100 rather than clamped) and the total
    counts through a subquery capped at 10,001 rows, so neither a broad query nor
    a deep page can become an unbounded scan. `q` shorter than 2 characters is
    rejected instead of answered with a non-selective pattern, and LIKE
    metacharacters in `q` are escaped so a caller cannot turn a keystroke into a
    wildcard that defeats the trigram index. Measured on 200,000 contacts: see
    `perf_integration_test.go`.

## Inbox full-text search
82. **Inbox search is workspace-pinned on every table and bounded by
    construction.** `GET /inbox/search` (`internal/app/inbox/search.go`,
    `queries/inboxsearch.sql`) is the inbox's counterpart to invariants 33–36,
    and its inputs are more hostile than a contact list: inbound bodies are
    written by anyone who can email the workspace. The rules:
    - **Tenancy.** Every table the search reads — `inbox_messages`,
      `sequence_steps`, `sequence_step_variants`, `sends`, `inbox_threads` and
      the joined contacts/labels/snoozes — is filtered on the JWT's
      `workspace_id`, never a request value, and each GIN index leads with
      `workspace_id` so another tenant's postings are never even read
      (`TestSearchIsWorkspaceScopedOnBothLegsAgainstPostgres`). The opaque
      cursor carries a position, never a workspace (as invariant 34).
    - **Nothing unselective reaches the index.** `q` is trimmed, ≤ 256
      characters, valid UTF-8 without NUL, and parsed by
      `websearch_to_tsquery`, which has no syntax errors. A query whose
      `querytree` is `T` (only exclusions: `-foo`, `foo OR -bar`) or empty
      (only stop words) is refused with 400 before any index is touched,
      because it matches nearly everything and can only be answered by scanning
      the whole index.
    - **Work is capped, and a capped answer is refused rather than served.** A
      precheck counts matching messages and matching campaign copy, each
      through a `LIMIT 10,001`; more than 10,000 in either is a 422 ("add words
      to narrow it"), never results drawn from an arbitrary subset. The search
      itself repeats the same `LIMIT`s as a backstop and reads one REPEATABLE
      READ snapshot with the precheck, so the backstop never truncates. Text is
      matched once, through the indexes; the thread walk and the snippet reuse
      those candidate sets and never recompute a tsvector. `ts_headline` runs
      only on the ≤ 51 returned rows, over capped input (subject 1,000, body
      20,000 characters), and the indexed document itself is capped at 100,000
      characters so an enormous inbound body can neither fail its insert nor
      cost more to search.
    - **Address matching is index-served and capped the same way.** A query of
      3+ characters is also matched, lower-cased and LIKE-escaped (so `%`/`_`
      are literals), as a substring of the linked contact's email (through
      `idx_contacts_search`, re-checked against `lower(email)` so a name or
      company is not an address match) and of inbound messages' From address
      (through the trigram `idx_inbox_messages_from_email_search`, inbound
      only — an outbound From is the workspace's own mailbox). Both sets are
      workspace-pinned, counted in the precheck and refused over 10,000 like
      the text sets. Shorter queries skip address matching rather than scan
      every contact.
    - **Time is capped.** The search transaction runs under a transaction-local
      `statement_timeout` (3 s); a cancelled statement is a 503, not a 500, and
      the pooled connection's own timeout is untouched.
    - **Rate is capped.** The route is throttled per IP and per workspace
      (`INROAD_RATELIMIT_INBOX_SEARCH_{IP,WORKSPACE}`, failing closed on a
      limiter outage), after the scope check.
    - **Output cannot carry markup.** Snippets are returned as plain-text
      segments with a `match` flag, never an HTML string; the highlight markers
      are private-use code points stripped from the input before highlighting,
      so a message cannot forge one.

## Sending-domain authentication (DNS)
37. **The resolver only ever answers about domains the workspace already sends
    from.** `POST /sending-domains/{domain}/check` takes a caller-controlled path
    parameter, so the service resolves it against the workspace's OWN mailboxes
    first (`Store.Get`, `workspace_id` from the JWT — invariant 4) and returns
    404 BEFORE any lookup happens; the sweep's domain list comes from
    `mailboxes.email` the same way. Without that ordering the endpoint would be
    an open resolver proxy — an authenticated caller could probe arbitrary names
    from our egress and read the answers. Proven by
    `TestCheckRejectsAForeignDomainBeforeAnyLookup` /
    `TestCheckForeignDomainIs404AndNeverResolves`, which assert the resolver was
    called ZERO times, and by the integration test's foreign-domain 404.
38. **A DNS TXT lookup is not an outbound dial to a user-supplied host, so it
    does not go through (and does not need) `mail.vetAddr`.** It reaches the
    host's configured nameservers, never an address from the request, and returns
    text — there is no connection to a caller-chosen endpoint to redirect. What
    protects the seam is the ownership gate above (which names are asked about)
    plus the per-lookup timeout and the whole-probe budget in
    `internal/platform/dnsauth` (how much work one request can cause).
39. **The domain check is informational and workspace-pinned.** Nothing on the
    send path reads `sending_domains` — an advisory that turns out to be wrong
    must not be able to stop a campaign. Warmup's `pending_auth` lane is derived
    from it and therefore WARNS rather than failing campaign preflight, and
    contributes no capacity reduction; it withholds only warmup traffic, which is
    Inroad's own mail. A spoofed or un-swept DNS answer can pause warmup, never a
    campaign (`TestComputePreflightPendingAuthWarnsRatherThanBlocking`). Every row is
    `workspace_id`-scoped, so two tenants sending from the same domain keep
    separate verdicts (`TestSendingDomainsAreWorkspacePinned`). The response DTO
    carries only public DNS data (records already published to the world), and no
    credential or ciphertext field exists on it by construction.

## Deliverability guardrails (circuit breaker + event ingest)
40. **The event-ingest endpoint derives its tenant from the credential, never the
    body, and carries its own least-privilege scope.** `POST /deliverability/events`
    is a MACHINE endpoint on the api-key-accepting data plane; the workspace comes
    from `auth.WorkspaceID` (the authenticated principal — invariant 4) and the
    request has no workspace field to send, so an event can only ever be recorded
    against the tenant whose credential presented it. It requires
    `deliverability:write` rather than `campaigns:write`: an external bounce
    pipeline needs to report events and nothing else, so an ingest credential does
    not also carry the authority to mutate campaigns.

    That last clause is enforced, not merely intended. `bounce_class='hard'` feeds
    warmup's reputation engine, and warmup lanes can withhold new campaign leads —
    so an uncapped ingest arm WOULD have let this scope quarantine a mailbox and,
    through the domain gate, withhold every sibling on its sending domain for 72h,
    with no way for the tenant to clear it. Feed-reported bounces are therefore
    counted separately from the ones Inroad's own DSN parser observed
    (`warmup_signal_snapshots.campaign_asserted_hard_bounces`) and capped at
    `watch`: they reduce volume and surface to an operator, they never contain.
    Self-observed evidence is unaffected. This is the same posture invariant 39
    gives the DNS advisory — an assertion Inroad did not make itself may advise but
    may not veto (`TestAssertedBouncesAdviseButCannotContain`). Asserted evidence
    still blocks PROMOTION, because being unable to punish is not the same as
    vouching. The scope is deliberately
    ABSENT from `OAuthGrantableScopes` — an ingested complaint suppresses an
    address workspace-wide and can trip a campaign's breaker, so a third party who
    could forge complaints could suppress a workspace's contacts and stop its
    campaigns.
41. **A caller-supplied `send_id` is resolved by a workspace-pinned SELECT, not
    trusted.** `InsertDeliverabilityEvent` inserts
    `(SELECT s.id FROM sends s WHERE s.id = $send_id AND s.workspace_id = $ws)`, so
    a send id belonging to another tenant (or to nothing) stores NULL rather than
    failing the FK or attributing to the foreign campaign: the event still counts
    at workspace scope but reaches no other tenant's breaker
    (`TestIngestWithAForeignSendIDAttributesToNoCampaign`). Ingest is idempotent on
    `(workspace_id, provider_event_id)` — a redelivered webhook writes nothing and
    therefore causes nothing: no second suppression, no second evaluation, so a
    replay cannot inflate the rate a breaker acts on
    (`TestReplayedIngestDoesNotInflateTheRate`).
42. **A complaint suppresses through the existing suppression path; an ingested
    bounce does not suppress at all.** A complaint calls the SAME workspace-scoped,
    `ON CONFLICT DO NOTHING` suppression insert `MarkBounced` and `MarkUnsubscribed`
    use, under its own reason literal `complaint` (migration 000037 widens the
    CHECK). The suppression is the load-bearing write and runs BEFORE any scoring,
    so a failure to score can never skip honouring the opt-out — the same ordering
    as invariant 20. An ingested `bounce` is counted but NOT suppressed: provider
    bounce feeds include soft bounces (full mailbox, greylisting), and suppressing
    an address forever on a temporary failure is not recoverable by the operator;
    hard bounces are still suppressed where they are actually classified, in the
    inbox poller. The caller's own `bounce_class` (`hard`/`soft`/`unknown`) is
    validated against that enum at the service boundary — a value outside it is a
    422, never coerced — and forced to `unknown` for a `complaint`, where the
    concept does not apply. Only `hard` feeds warmup's hard-bounce rate; an
    omitted or unclassified value is EXCLUDED from it rather than assumed
    permanent, because over-counting pauses a healthy sender for 72 hours while
    under-counting only delays a true signal
    (`TestOnlyAHardIngestedBounceFeedsTheWarmupHardBounceRate`).
43. **The breaker runs outside the send transaction and can only ever pause, never
    fail a delivery.** Evaluation is a separate `deliverability:evaluate` task
    enqueued AFTER `MarkStepDelivered` commits, so it reads committed state only;
    the enqueue's failure is logged, not returned, so a scoring or Redis fault
    cannot turn a delivered send into a retried task. The pause itself is
    `UPDATE campaigns SET status='paused' … AND status='running'` plus the pause
    event in ONE transaction, so a paused campaign always has its recorded reason
    and repeated evaluation flips nothing twice. Every guardrail query is
    `workspace_id`-pinned and the campaign id is paired with the workspace from the
    JWT or the task payload, so a foreign campaign id reads, scores and pauses zero
    rows (`TestScoresAndEventsAreWorkspacePinned`). The breaker cannot fire below
    50 delivered at any ratio, which is what makes it safe to ship on by default.

## Reply-label taxonomy & automation
44. **Reply-label routes are workspace-pinned and campaign-scope-gated.** Every
    `internal/app/replylabel` handler derives `workspace_id` from
    `auth.WorkspaceID` (never the body or path), and the routes are gated on
    `campaigns:read`/`campaigns:write` — deliberately NOT the narrower
    `inbox:write` OAuth scope, because label role-flags drive campaign
    automation (stop/suppress/capture/defer), which is campaign-level authority.
    Builtin labels are renameable but never deletable, and a label's `key` is
    immutable after create (it is the value stored on enrollments). The two
    unsafe flag combinations (`is_automated && stops_enrollment`,
    `defers_enrollment && !is_automated`) are rejected server-side, not just in
    the UI.
45. **Compliance dispatch runs BEFORE label automation.** In
    `internal/worker/inbox/dispatch.go`, the unsubscribe/compliance check is
    evaluated before any label-driven automation, in BOTH the label path
    (`byLabel`) and the legacy class fallback (`byClass`) — the same
    suppression-first ordering as invariants 20 and 42. The `workspace_id`
    threaded through `replyDispatch` comes from the polled mailbox, never from
    message content, so a hostile inbound message cannot steer automation into
    another tenant.

## ESP-matched sender selection
46. **Recipient-ESP matching never adds a hot-path dial and never gates a send.**
    The recipient-domain MX lookup runs ONLY in the off-hot-path sweep
    (`internal/worker/recipientesp/sweep.go`) via `net.Resolver.LookupMX` — a
    read-only DNS answer; the resolved host is never dialed, so there is no SSRF
    surface despite user-influenced domains driving the lookup. On the send path,
    `resolveSender` (`internal/coreapi/inprocess/senderpool.go`) reads only the
    cache table and fails closed to `Unknown` on any error (parse failure, DB
    error, cache miss). ESP matching only NARROWS the candidate sender pool —
    an exhausted matched subset falls back to the full pool, so a wrong or
    missing classification can delay nothing and block nothing. Provider
    matching uses label-boundary suffix checks (`hasHostSuffix`), so
    `notgoogle.com` never matches `google.com`.

## Manual reply from the unified inbox
47. **A manual reply is workspace-pinned end-to-end, suppression-checked
    twice, and its scope is deliberately not delegable.** `POST
    /inbox/threads/{id}/reply` (`internal/app/inbox.Service.Reply`) resolves
    the thread via `Store.GetThread`, which pins `workspace_id` from
    `auth.WorkspaceID` (never the body or path) the same way every other
    inbox read does; a cross-tenant or unknown thread id is indistinguishable
    404, per the "never leak a foreign row's existence" rule. Suppression is
    checked BEFORE enqueuing (`inbox.SuppressionChecker`, backed by
    `suppression.Store`) and re-checked by
    `internal/worker/inbox.ReplySendHandler` immediately before dialing (the
    same defense-in-depth the test-send/warmup paths use against a race with
    an incoming unsubscribe) — suppression is never bypassed, in either
    direction. The credential is decrypted ONLY in the execution plane
    (`ReplyCore.ResolveSenderTransport`, invariant 1): the control plane only
    ever enqueues a task carrying ids, never a credential — and, since the
    correspondence fix, never the body either. Both reply routes now create an
    `inbox_pending_replies` row and enqueue `inbox:pending_reply_send` carrying
    `{pending_id, workspace_id}`, so the reply text never leaves Postgres
    (invariant 64). The earlier `inbox:reply_send` payload carried the free-text
    body, and because a terminal failure captures a payload verbatim into
    `task_dead_letters`, that body was readable under `campaigns:read` — a scope
    this file deliberately grants to OAuth clients while withholding
    `inbox:read`. The new `inbox:send` scope is in
    `auth.AllScopes` (a workspace-minted API key or a session may send a
    reply) but deliberately absent from `OAuthGrantableScopes` — sending mail
    is never delegable to a third-party client, the same rule
    `campaigns:send` follows. A manual reply bypasses the mailbox's daily cap
    and minimum send interval (product decision: an operator's own reply is
    not automation) but is still counted toward the mailbox's daily sent
    volume — `queries/send.sql`'s `CountSentToday` sums both `sends` and
    same-day outbound `inbox_messages` for the mailbox, so campaign
    scheduling sees true volume even though no `sends` row is ever written
    for a reply (`sends.campaign_id`/`contact_id` are `NOT NULL` with a
    `UNIQUE(campaign_id, contact_id)`, which a legacy direct-send thread's
    reply cannot satisfy and a threaded one would collide with).

    **Claim-before-send.** Both reply routes now go through
    `PendingReplySendHandler`, whose claim is the row itself: a status-guarded
    `scheduled` -> `sending` UPDATE plus a lease, which is both the claim and the
    operator's cancel handle. The paragraph below describes the claim used by the
    legacy `inbox:reply_send` drain handler, which survives only until the last
    pre-fix task is drained (invariant 64) and is deleted with it. It is kept
    documented because the drain path is still registered and still sends mail.

    `ReplySendHandler` claims the task (workspace-
    pinned, keyed on the enqueue-time `asynq.TaskID` — stable across every
    retry/redelivery of that ONE enqueued task, carried in
    `InboxReplySendPayload.TaskID`) immediately before `ResolveSenderTransport`,
    the same claim-before-dial discipline `ClaimStepSend`/`ClaimWarmupSend`
    apply to a sequence/warmup send — without it, a worker crashing between
    the provider ACK and the handler returning would leave asynq's lease to
    expire and redeliver the identical task to another worker as a full
    re-run, re-dialing and double-sending. The claim reuses the EXISTING
    generic Idempotency-Key replay cache (`idempotency_keys`, migration
    000045) rather than a new table — a namespaced key
    (`"inbox-reply:" + taskID`) inserted via the same atomic
    `InsertIdempotencyKey`/`DeleteIdempotencyKey` pair the HTTP layer uses,
    aging out via the SAME 24h maintenance sweep, no dedicated retention job.
    A failed claim (`claimed=false`) means a prior attempt at this exact task
    already reached the dial: skip, never send again — "never double,
    occasionally drop a rare ambiguous send" (the same accepted posture as
    invariant 4a). A claim taken but never dialed (a transient
    transport-resolve or send failure) is released BEFORE the handler
    returns its error, so the retry's own claim attempt can re-claim rather
    than seeing its own abandoned claim and dropping the reply forever; if
    the release itself fails, the original error is still returned and the
    retry simply skips (drop, never double — the fail-safe direction, not
    the failure-avoiding one). Post-ACK bookkeeping (`RecordInboxReply`) is
    unaffected by any of this: its own failure is logged, never retried, for
    the reason above the claim exists in the first place.

## AI-drafted replies
48. **Drafting a reply can never send one, and it reads only the caller's own
    workspace.** `POST /inbox/threads/{id}/draft-reply`
    (`internal/app/inbox.Service.DraftReply`) returns suggested text and nothing
    else: it enqueues no task, opens no mailbox credential, and dials no mail
    provider. Sending stays a separate call a human makes deliberately
    (`POST /inbox/threads/{id}/reply`, invariant 47), so a draft is inert until
    someone acts on it — and because drafting is not a send, no agent or MCP
    tool gains send authority by being able to draft. The suppression checks of
    invariant 47 are unchanged and still the only gate on actually sending; the
    draft path deliberately does not consult them (nothing is going anywhere
    yet).

    **Tenancy.** The transcript is built ONLY from `Store.GetThread`, which pins
    `workspace_id` from `auth.WorkspaceID` (never the body or a path param), so
    the prompt can only ever contain the caller's own conversation; an unknown
    or cross-tenant thread id is the same indistinguishable 404 every other
    inbox read returns. The workspace id handed to the drafter is that same
    JWT-derived value, so a draft cannot be generated against another tenant's
    AI configuration.

    **Scope.** Gated on `inbox:send`, NOT `inbox:read`. It exposes no more
    thread content than `inbox:read` already does, but every call spends the
    workspace's AI budget and is useful only as a step toward replying, so it
    belongs behind the same authority as the send. `inbox:send` is absent from
    `OAuthGrantableScopes`, so a delegated third-party integration cannot burn a
    workspace's tokens. Spend is additionally bounded by a per-IP and
    per-WORKSPACE rate limit (`throttle.Config` with an `AcctKey` resolving the
    principal's workspace — the budget owner — rather than the pre-auth body
    `email`), configured via `INROAD_RATELIMIT_DRAFT_REPLY_IP` /
    `INROAD_RATELIMIT_DRAFT_REPLY_WORKSPACE` and fail-closed on a Redis outage
    like every other throttle.

    **No prompt or message content is ever logged.** `Runtime.DraftReply` emits
    only ids, the resolved model id, turn count, token counts, draft LENGTH and
    duration — extending the discipline invariant 21 states for the reply
    classifier ("never logs PII or secrets; it emits a class/source/confidence,
    not message content") to prompts. `agentrun/manager.go` follows the same
    habit for runs, logging error values rather than message content. The
    provider error text is likewise never returned to the caller: the 502
    response carries only the domain's own sentinel message, because an upstream
    string can echo request detail back.

    A FAILED draft (`inbox.logDraftFailure`) logs ids plus a stable reason token
    — `no_model_configured`, `provider_timeout`, `provider_failed`,
    `drafter_not_wired`, `empty_draft` — so an operator can ask "is anyone
    hitting this, and why" without reading correspondence. For the same reason
    the 502 body is a sentinel, a PROVIDER's error text is not logged either:
    that class contributes machine facts only (kind, HTTP status and
    retryability off `*ai.ProviderStatusError`, which carries no body by
    construction; a Go type name for any other shape, since a provider SDK error
    may embed a response body). Our own errors — model resolution, and the two
    that carry no error at all — keep their full value, which is what makes a
    line actionable. This is what keeps this invariant's claim literal rather
    than approximate.

49. **AI provider credentials are a different class from mailbox credentials,
    with a deliberately different posture.** This is documented here for the
    first time; it describes the design the agent platform has always had, which
    the draft endpoint reuses without changing.

    Invariant 1 governs MAILBOX credentials and says to open them only in the
    worker/send path. That rule exists because their consumer is an outbound
    SMTP/IMAP/API dial that delivers mail on a tenant's behalf, so the blast
    radius of a leak is a tenant's mail being sent or read, and the execution
    plane is where that dial belongs.

    AI provider credentials have no execution-plane consumer at all. The agent
    run loop runs IN the API server process — `agentrun.Manager.Start` simply
    does `go m.run(...)`; there is no worker hop and no `internal/worker/agent*`
    package — so `agentchat.PgModelResolver.Resolve` unseals the provider blob in
    the control plane via `keyring.SealerFor`, hands the plaintext to the
    streamer factory, and lets it go out of scope within that call. Nothing above
    that seam can hold or log a provider key: `ResolvedModel` has no credential
    field, by construction (see its doc comment). No mail is sent and no
    SMTP/IMAP connection is opened on this path, so none of the reasoning behind
    invariant 1's execution-plane rule applies. This is a distinct rule for a
    distinct credential class, not an exception carved out of invariant 1.

    The draft endpoint adds no new credential handling and no new SSRF surface:
    it receives an already-built streamer from that same resolver, whose
    user-supplied `base_url`/endpoint hosts were vetted at write time by the mail
    package's SSRF classifier and again by the guarded transport at dial time.

## Federated sign-in (Google)

Google SIGN-IN (Inroad as an OAuth client to Google, for LOGIN) is a separate
surface from the mailbox-connect flow of invariants 8–11, with its own redirect URL,
its own routes (`GET`/`POST /api/v1/auth/oauth/google/start`,
`GET /api/v1/auth/oauth/google/callback`, all unauthenticated), and its own scopes.
Nothing about it is reachable from, or reuses state from, mailbox connect.

It MAY, however, share the same Google OAuth *client*:
`INROAD_GOOGLE_SIGNIN_CLIENT_ID` falls back to `INROAD_GOOGLE_CLIENT_ID` so one
registered client makes both features work (a dedicated pair is available, and is
worth setting, because the mailbox client carries restricted Gmail scopes subject to
Google's verification review — the ability to log in should not be blocked by that
review). Sharing a client is safe and does not weaken invariant 50, for two reasons
worth stating since they are what a reader will reasonably worry about:

- **A mailbox state still cannot be replayed as a login state.** Purpose separation is
  enforced by the `oauthstate.Purpose` inside the HMAC-signed payload, not by which
  OAuth client minted the state, so a shared client changes nothing about it.
- **A login still never obtains Gmail authority.** Scopes are requested per
  authorization, not granted per client: the sign-in flow requests only
  `openid email profile`, so the token it receives cannot read or send mail even
  though that client is *capable* of requesting those scopes on the other flow. And
  sign-in never persists its token at all — it reads the ID token and drops it.

The redirect URL never falls back, because the two flows have different callback
paths even on a single shared client.

50. **The workspace a federated session lands in is derived server-side, and the
    provider identity is keyed on `sub`, never email.**

    *Workspace from the server, never the callback.* The callback URL carries no
    workspace at all — unlike a mailbox-connect state, a login state's subject is
    empty. The workspace comes from resolving the provider identity: an existing
    `user_identities` row, an existing account for the verified email, a pending
    invite, or a brand-new workspace created in the same transaction. There is no
    query parameter a caller can set to steer a session into a tenant.

    *Identity keyed on `sub`.* `user_identities` is `UNIQUE (provider,
    provider_subject)` on Google's immutable subject id. Email is used only to
    find an existing account to LINK the first time. Matching on email instead
    would silently mint a second account for the same person after they change
    their Google address — and, worse, would let a re-registered address inherit an
    account.

    *`email_verified` gates BOTH signup and linking.* A Google `email_verified` of
    false is refused outright. The tempting objection — that refusing to link
    protects nothing, since whoever controls the mailbox could take the account
    over via password reset — has it backwards: the claim being false is Google
    telling us they may NOT control it. Without the check, anyone able to create a
    Google account asserting `victim@corp.example` would be handed the existing
    Inroad account for it.

    *An invite must match the authenticated address.* An invite token is a bearer
    credential granting workspace membership, so the federated accept path requires
    the provider-verified email to equal the invite's address
    (`ErrIdentityEmailMismatch`, checked inside the transaction that reads the
    invite). Otherwise whoever obtained a link addressed to alice@ could present it
    while authenticating as bob@ and join a workspace nobody invited them to. The
    invite token also never travels to Google: only its SHA-256 is stashed
    server-side against the state nonce, because `state` passes through Google's
    servers, browser history, and any `Referer`.

    *State is purpose-scoped, single-use, and PKCE-bound.* The signed `state`
    (`internal/platform/oauthstate`) now carries a **purpose** inside the HMAC'd
    payload, so a mailbox-connect state can never be replayed at the sign-in
    callback or the reverse. Its nonce is additionally backed by a **server-side
    single-use store** (`oauth_login_states`, consumed by a guarded `UPDATE ...
    WHERE consumed_at IS NULL AND expires_at > now()`), so a leaked state URL is
    usable at most once. That store was listed as deferred hardening under
    invariant 10; it matters more here, because replaying a LOGIN state is account
    access rather than a stray mailbox binding. Only the nonce's hash is stored (it
    rides in a URL, so it is treated as a bearer credential like
    `sessions.token_hash`). The code exchange is **PKCE S256**; the verifier lives
    in that same row because the callback has no cookie to carry it. Mailbox
    connect keeps its TTL-only replay window (invariant 10's residual risk) — it
    gained the purpose tag but not the nonce store.

    *The post-sign-in destination cannot become an open redirect.* A caller may ask
    to land on an in-app path after sign-in (`return_to`). It is validated by an
    ALLOWLIST, not sanitized (`identity.safeReturnTo`): only a single-leading-slash
    same-origin path survives, and absolute URLs, scheme-relative `//host`,
    backslash forms some browsers normalize to `/`, control characters and
    whitespace that could split a `Location` header, and anything over 512 bytes are
    all dropped in favor of the SPA's default landing route. It is stored in the
    login-state row rather than in `state` or a redirect URL, so the destination
    cannot be swapped by editing a URL mid-flow, and it is query-escaped when handed
    back so it stays one parameter value.

    *The unauthenticated write is rate-limited.* Each `/start` call inserts an
    `oauth_login_states` row before any credential exists, so both start routes are
    throttled per-IP through the shared Redis limiter
    (`INROAD_RATELIMIT_SENSITIVE_IP`, fail-closed). Rows are small, expire in ten
    minutes, and are swept by the maintenance job, so the exposure was bounded
    already — but an unauthenticated endpoint that writes without a cap is worth
    capping. The CALLBACK is deliberately NOT throttled: it is the provider
    redirecting a real user's browser back, so shedding it would break a legitimate
    sign-in, and its guard is stronger anyway (the state must be signed, unexpired,
    purpose-matched, and unconsumed).

    *Login scopes deliberately exclude Gmail.* `openid email profile` only. Gmail
    scopes are Google-restricted, they would push every sign-in through a scarier
    consent screen for permissions login does not need, and a mailbox token
    obtained at sign-up would have no per-workspace DEK to be sealed under —
    at that moment the workspace does not exist yet. Connecting a mailbox stays the
    separate authenticated flow (invariants 8–11).

    *No new SSRF surface.* The consent URL, token endpoint, and ID-token
    verification all target fixed Google hosts; only server-minted values (state,
    PKCE challenge) are interpolated. The ID token's signature is not verified,
    and that is safe for one specific reason: it arrives in the body of a direct
    server-to-server TLS call WE made to Google's token endpoint with our client
    secret, so there is no party in between to have substituted it. Issuer,
    audience (must be our client id), expiry, and subject ARE checked. If that
    token ever starts arriving from a client instead, JWKS signature verification
    becomes mandatory — the code comment on `parseGoogleIDToken` says so.

51. **A NULL `password_hash` means "no password", and can never authenticate.**
    `users.password_hash` became nullable so a federated account can exist at all
    (migration 000051; before it, `AcceptInviteTx` refused to create a user without
    one). `Service.Authenticate` checks for `nil` explicitly and rejects — never a
    comparison against `""`, never a coalesce, never a nil dereference — and burns
    the same dummy argon2 cost as a wrong password so response time does not reveal
    which kind of account an address is. `UpdatePasswordHash` takes a non-pointer
    string on purpose: setting a password is not the same operation as clearing
    one, and no path should be able to strip a password. A password reset on a
    federated account legitimately GIVES it a password — that is account recovery
    for an address the provider already verified.

## Warmup pool lanes and evidence

Warmup carries two independent axes. `health_state` is sender reputation — how the
mailbox's outbound mail performs. `lane` is pool eligibility — who it may exchange
traffic with, and whether it may take new campaign leads. They are decided in one
pass and written in one CAS statement guarded on both `from_state` and `from_lane`,
so "quarantined but healthy" is unrepresentable and two racing evaluators cannot
write history that never happened.

52. **Evidence is never attributed on an attacker's say-so.** An inbound message can
    claim anything, so every warmup observation must be bound to something the
    attacker does not control before it can gate health.
    - `invalid_token` rows are recorded against `observer_mailbox_id` only, never a
      claimed sender, with `attribution_trusted = false`. Two CHECK constraints
      (`warmup_observations_invalid_token_untrusted`,
      `..._unattributed`) make this structural — the database refuses the unsafe row
      rather than the writer remembering to omit it. Otherwise anyone able to email a
      connected mailbox could throttle a mailbox they do not own.
    - `hard_bounce` rows from an inbound DSN additionally require
      `warmup_sends.from_mailbox = <the mailbox that observed the DSN>`.
      `Original-Message-ID` is parsed from the DSN body and is fully
      attacker-controlled; without the binding a forged DSN to any connected mailbox
      wrote a trusted bounce against a different one
      (`TestRecordWarmupHardBounceRequiresTheObservingMailboxToBeTheSender`).
    - `placement` requires a DB-proven send→recipient binding, reached by ONE of
      two routes. The first is a verified signed token. The second exists because
      Microsoft strips unknown custom headers, so warmup mail to an M365 mailbox
      carries no token at all and was being counted as no observation at all:
      `FindWarmupSendByMessageID`
      (`internal/coreapi/inprocess/warmupsendlookup.go`) matches the inbound
      `Message-ID` against `warmup_sends.message_id` for a `sent` row in THIS
      workspace whose `to_mailbox` is THE POLLED MAILBOX.
      **The second route's key is an inbound claim, and this is the one place in
      invariant 52 where that is true.** The predicate is our own data, but
      `Message-ID` is read off unauthenticated mail — the same class of value the
      `hard_bounce` bullet above calls fully attacker-controlled. Nothing
      downstream narrows it: the receipt `INSERT` re-proves exactly the four facts
      the lookup already matched, and `RecordWarmupPlacementObservation` writes
      `attribution_trusted = true` with no CHECK behind it, unlike the two that
      make the `invalid_token` rule structural. So the honest bar for minting a
      placement observation is **already knowing a real `Message-ID` of a real
      warmup send addressed to this mailbox** — the same bar invariant 65 states
      for a mail-borne complaint.
      The residual, named rather than implied: whoever holds such an id can
      deliver a tokenless message carrying it to that mailbox and mint an
      observation. Because placement is monotone-worsening (below), one delivered
      into the mailbox's junk folder DOWNGRADES a real `inbox` sample to `spam`,
      and placement feeds the health state machine. It is accepted, and it is
      sized by the id: on the smtp and gmail paths a warmup send's `Message-ID` is
      CSPRNG-generated (go-mail `SetMessageID`, 22 chars from `crypto/rand`) and
      never leaves the workspace, so holding one means having already seen the
      message; on m365 it is whatever Exchange assigned, which is worth noting
      because m365 is the provider the route exists for. The pool is
      intra-workspace, so the adversary is the in-path or intra-tenant one
      invariant 62 says this whole invariant rests on — and the alternative was
      not a stronger signal but NO signal for every M365 participant.
      Because the key is an inbound claim, it is also refused before it reaches
      the database when it is not a usable identifier at all — see
      `usableMessageID` in invariant 65, which closes the same
      cursor-wedge denial of service on this route as on the complaint one.
      Only a genuinely ABSENT header takes the second route
      (`TestPollForgedWarmupTokenNeverFallsBackToMessageID`). That is NOT an
      access control and must not be read as one — anyone who simply omits the
      header reaches the fallback, which is the entire point of it. It is there so
      one message produces ONE record of itself: a present-but-invalid token is
      recorded as `invalid_token` attack evidence, and letting the same message
      also mint a trusted receipt would put two contradictory claims about it in
      the trail an operator reads to tell an attack from header loss.
      A later observation of the SAME receipt may only make the placement worse
      (`inbox`/`tabbed`/`other` → `spam`), superseding the row rather than adding
      one, so one message is always one sample: a re-poll cannot inflate the
      evidence, and the engager's own rescue of a spam message back into the inbox
      cannot erase the evidence that the rescue was needed
      (`TestPlacementReclassificationIsMonotoneAndCountsOnce`).
    - `tabbed` is recorded only when a provider POSITIVELY identifies a tab, and
      `tab_capable` records whether the reading path could have identified one. Both
      are written from the poller that read the message, never derived from
      `mailboxes.provider` afterwards: a mailbox migrated between providers would
      otherwise make historical observations claim a capability the reader never had.
      `('tabbed', tab_capable = false)` is refused by a CHECK, because that row makes
      the tabbed rate's numerator exceed its denominator and the snapshot's own CHECK
      then aborts the refresh for a whole WORKSPACE — one bad row would stop
      promotions for every participant in the tenant. The tabbed rate gates NOTHING:
      the signal is undetectable on an entire provider class, so a threshold on it
      would make promotion unreachable for every SMTP mailbox or demand assuming
      primary placement where we cannot see
      (`TestWideningThePlacementVocabularyChangesNoHealthStateAndNoLane`).

53. **Containment cannot be cleared by the tenant.** `quarantine` and `blocked` are
    carried across a disable/re-enable: the participant row is deleted on disable,
    but `warmup_state_transitions` survives and the last sealed lane is restored on
    re-entry (`TestReEnablingWarmupDoesNotClearContainment`). The quarantine cooldown
    is derived only from transitions that MOVED a participant into quarantine, so
    health-only rows written while quarantined cannot restart the clock. An auth
    regression cannot launder a quarantine into probation.

54. **Lane isolation holds on every outbound path.** Partner selection, the due-send
    job, the campaign rotation's `availableToday`, and the engagement REPLY all
    enforce it — the reply re-checks at engage time because the lane can move between
    a send and its answer. A healthy mailbox never exchanges warmup traffic with a
    probation, recovery, watch, quarantined, blocked or unauthenticated peer.

    New CAMPAIGN leads are gated on the mailbox AND its organizational domain, and
    both halves go through one predicate (`warmup.NewLeadsWithheld`) that the
    preflight check, the senders panel's `cap_today` and the rotation all call, so a
    displayed warning and an enforced block cannot drift. Domain scope is an
    aggregate read — the worst lane among the workspace's ENABLED participants on
    the same organizational domain — not a second state machine and not a second
    table. Only `quarantine` and `blocked` withhold. `pending_auth` never does, on
    either half, because it is derived from the advisory DNS check that invariant 39
    forbids from stopping a campaign, and an empty lane means the mailbox is not
    warming up at all (`TestQuarantinedSiblingWithholdsItsWholeDomain`,
    `TestComputePreflightNonContainingDomainLanesDoNotBlock`). Replies to a human who
    already wrote back are exempt throughout.

    "Organizational domain" means the registrable domain (eTLD+1), so a quarantined
    `a@example.com` withholds `b@mail.example.com`: providers largely inherit
    reputation across subdomains. It is derived in exactly ONE place,
    `warmup.OrganizationalDomain` (Go, at read time) — SQL has no public-suffix data
    and a stored column would freeze the answer at the list version in force when
    the mailbox was connected. A host the list cannot resolve falls back to the
    exact host: narrower, never wider
    (`TestQuarantineOnASubdomainWithholdsTheParentDomainOnTheSendPath`,
    `TestQuarantineOnAnUnrelatedDomainDoesNotWithhold`). This is deliberately NOT
    the scope `sending_domains` uses — SPF/DKIM/DMARC are published per host, so the
    DNS advisory stays keyed on the exact host.

    The domain half is workspace-pinned like everything else: two tenants sending
    from the same domain — a shared parent company, a reseller, the same public
    provider — never see each other's containment, and one tenant cannot stop
    another's campaigns by quarantining a mailbox
    (`TestForeignQuarantineOnTheSameDomainWithholdsNothing`,
    `TestForeignQuarantineDoesNotWithholdTheFallbackSender`).

    KNOWN LIMITATION: eTLD+1 does not help the free-provider case, and slightly
    widens it. `gmail.com` is its own registrable domain, so a workspace sending
    entirely from `@gmail.com` shares one organizational domain and quarantining one
    of its mailboxes withholds the rest. An exclusion list of "public" providers is
    not the fix — any list maintained here would be incomplete, would go stale, and
    would be a second answer to "which mailboxes share a reputation". The blast
    radius is bounded to one workspace's own mailboxes, and containment failing
    CLOSED is the direction this subsystem prefers.

55. **Warmup evidence is bounded and retained.** `warmup_observations` is append-only
    and reachable by external senders, so the invalid-token idempotency key buckets on
    (mailbox, UTC date, reason) rather than hashing an attacker-controlled header, and
    a 90-day purge runs in the maintenance sweep — comfortably beyond the widest
    30-day read window.

56. **A receiver's authentication verdict is believed only from a header that
    receiver demonstrably wrote.** `warmup_observations` records the SPF/DKIM/DMARC
    results the receiving provider reached (`spf_result`, `dkim_result`,
    `dmarc_result`) plus the sending identity (`dkim_domain`, `return_path_domain`).
    All five come from headers INSIDE a message, so the trust rule is the control
    (`warmup.ExtractIdentity`, RFC 8601 §5):
    - **Only the topmost `Authentication-Results` is considered.** A receiving MTA
      prepends its own, so a sender's forgery is necessarily below it. Scanning for
      the first *trusted* header instead was exploitable: wherever the receiver's own
      stamp failed the check, the scan walked past it to a forged header below.
    - **The authserv-id must match a domain derived from our own database** — the
      provider we connected the mailbox as, or the mailbox's organizational domain
      but ONLY where `SharesDomainReputation` says the workspace plausibly controls
      it. A shared consumer domain is not a trust unit: nobody with an `@gmail.com`
      mailbox owns `gmail.com`'s MTAs, and Gmail's genuine stamp says
      `mx.google.com`, so accepting `gmail.com` believed a forgery *instead of* the
      real verdict.
    - **Exchange Online omits the authserv-id**, so an id-less header is accepted
      only for a mailbox we know is on `m365`. For anyone else it is unattributable.
    - **A structurally damaged header is refused entirely**, not parsed as far as it
      goes. Closing the receiver's own comment early with a `)` spills attacker text
      into the methodspec stream ahead of the genuine verdict; refusing any header
      with an unbalanced close, unterminated comment or unterminated quote catches it.
    - No match yields `unknown` on all three. Absence of a verdict is never a pass
      and never a fail.
    **None of these five columns gates anything** — no threshold, lane, health state
    or promotion decision reads them. That is deliberate and load-bearing: the
    verdicts are `unknown` for every provider that stamps none, so gating would
    penalise a whole provider class for our inability to observe it, and it would
    make attacker-influenced headers reach pool eligibility. Authentication posture
    is gated separately by `sending_domains` and the `pending_auth` lane, from DNS we
    resolve ourselves. Before wiring a threshold to `dmarc_result`, re-read this.
    **Note which two of the five this rule does NOT protect.** It gates the three
    verdicts only. `dkim_domain` and `return_path_domain` are read off the message
    before it runs, so they are sender-written and unverified — and since slice D they
    feed `warmup.DetectIncidents`, whose output is rendered on the pulse card. That
    inference gates nothing (invariant 58), but the two columns are influenceable by
    anyone with read/write on one warmup recipient mailbox, which is weaker than
    invariant 57's MX controller.

57. **The warmup destination route is influenceable within a workspace, and must
    not be treated as attacker-independent evidence.** `warmup_observations.destination_esp`
    records where a warmup message was delivered, resolved from the recipient
    mailbox's provider and the `recipient_domains` MX cache — never from the
    message, so no header or envelope value can set it. But warmup partners are
    the workspace's own mailboxes, so whoever controls a mailbox domain's MX
    controls which route that mailbox's observations file under: point the MX at
    Google, junk everything that arrives, and the `google` route's spam rate for
    that workspace's senders drops.
    This is safe **only** while nothing reads a per-route rate. No threshold, lane,
    health state or promotion decision does today, and the route columns appear in
    exactly four statements: `RecordWarmupPlacementObservation` writes them,
    `ListWarmupRoutes` aggregates the matrix, `ListWarmupIncidentParticipants` feeds
    `warmup.DetectIncidents`, and `ListWarmupObserverStats` feeds
    `warmup.DiscountObservers`. The fourth was very nearly the breach: observer trust
    shipped as a gate in review and was cut back to disclosure precisely because this
    rule says a route-derived rate may not decide a health state (invariant 59). **Keep this enumeration current** — it is the
    tripwire by which a later reviewer finds every route consumer, and it is the
    third entry, the correlated-incident fold, that first turns route data into a
    derived claim rendered outside the warmup page (the pulse attention row).
    The design's stated reason for not gating is that no calibration data exists
    yet — **that reason expires and this one does not.** A slice that starts gating
    on route rates needs the same treatment invariant 52 gives the placement axis:
    the evidence must be bound to something the attacker does not control. Read
    this before wiring an exposure budget.

58. **A correlated incident is an inference over influenceable inputs, and gates
    nothing.** `warmup.DetectIncidents` groups degraded participants by a shared
    fault dimension — destination route, DKIM signing domain, return-path domain,
    sender organizational domain, or observed relay address — and reports
    concentration as a lift over the rest of the pool. It is computed at read time, persists nothing, and no threshold,
    lane, health state or promotion decision reads it.
    **Four of the five dimensions are steerable inside a workspace, and two of them
    by a weaker actor than invariant 57 describes.** `destination_esp` needs MX
    control. `dkim_domain` and `return_path_domain` do not: `ExtractIdentity` reads
    them straight off `DKIM-Signature d=` and `Return-Path` *before* the invariant-56
    trust rule, which gates only the SPF/DKIM/DMARC verdicts — so read/write on one
    warmup recipient mailbox is enough to deliver a crafted copy of a token-carrying
    message and choose the value recorded against every sender that mails it.
    `observed_relay_ip` is steerable only by SUPPRESSION, and the distinction is the
    reason it is admissible as a dimension at all: `ObservedRelayIP` trusts nothing
    but receiver-attributable hops and every failure direction returns `''`, so an
    attacker can blank a participant out of a cohort and can never place a chosen
    value into one. A relay cohort is therefore always real, and only ever
    incomplete — which is a different failure from the identity dimensions, where the
    cohort itself can be manufactured.
    What that cannot do is fabricate a member: membership comes only from
    participants the evaluator already marked degraded, over evidence invariant 52
    binds. What it can do is decide which correlation ranks highest, and the pulse
    card names only the strongest — so an influenced dimension can displace a true
    finding from that line. Survivable only because the warmup overview lists every
    finding with its arithmetic and discloses its cap. Do not make the pulse row the
    only place a correlation is reported, and do not gate on any of this without
    binding the identity dimensions the way invariant 52 binds placement.

59. **Observer trust is measured and published; it removes nothing.** Placement is
    sender-attributed but recipient-observed, so a mailbox that reports everything it
    receives as spam degrades every sender that mails it. `warmup.DiscountObservers`
    names those observers — 20+ observations, a 30% absolute floor, and 3x the peer
    rate within the observer's own provider cohort — and the overview publishes each
    verdict with its arithmetic. **Nothing acts on it.** The hole stays open.
    The reason is that the cohort is dilutable: an attacker who adds clean volume to a
    cohort drags the peer baseline down until an HONEST observer clears the multiple,
    silencing the mailbox that would have reported their spam. Reproduced — 150 clean
    observations discounted an honest 35/100 observer beside a strict 25/100 peer.
    Peer floors (≥2 peer mailboxes, and peers clearing the same sample minimum) raise
    the price without closing it.
    Applying it anyway would trade a hole that makes senders look WORSE than they are
    — visible, self-limiting, costing only sending — for one that makes them look
    BETTER, silently. Under-containment is the dangerous direction here.
    **Before this can gate:** the cohort key must be bound to something the attacker
    does not control, and invariant 57's rule against a route-derived rate reaching
    policy must be satisfied or consciously retired. Two known evasions survive
    regardless and should not be read as closed: an observer parked just under the 30%
    floor is untouchable, and one that junks a single victim rather than everything
    sits near the pool average at any volume.

60. **A warmup lane now reaches campaign selection by a SECOND path, and that path
    cannot withhold a send.** Invariant 54 says a lane's effect on campaign leads runs
    through `warmup.NewLeadsWithheld`, and that only `quarantine` and `blocked`
    withhold. Both are still true and are no longer the whole picture: since exposure
    budgets, `watch` and `recovery` also change WHICH mailbox a new lead is assigned
    to, through `exposureCeilings` → `rotation.WithinExposureBudgetFor`, which does not
    go through `NewLeadsWithheld` at all. A reviewer auditing "what can a lane do to a
    campaign" from invariant 54 alone would miss it.
    Two properties bound it, and both are load-bearing rather than incidental.
    **It never empties the candidate set:** if every candidate is over budget — the
    ordinary single-domain workspace — the whole set is returned unchanged, so the
    budget can shift volume but never reduce it. A concentration limit able to
    withhold mail would be a worse failure than the concentration it prevents.
    **A ceiling of 0 means "no opinion", never "may not send":** containment stays
    `LaneMaySend`'s decision, because a second implementation of "may this send" is the
    shape every repeated defect here has taken. `pending_auth` gets 0 deliberately, so
    the DNS advisory invariant 39 forbids from stopping a campaign does not narrow one
    either.
    It reads a LANE, not a rate, grouped by the sender's organizational domain from
    `mailboxes.email` — our own record, and immutable after insert. Nothing
    route-derived or message-derived reaches it, which is why it is outside what
    invariants 57, 58 and 59 constrain. Keep it that way.
    **Known asymmetry:** consumer-provider mailboxes are never grouped
    (`SharesDomainReputation`), so the budget can push volume ONTO them but never off.
    Those are the same mailboxes the domain half of the containment gate never covers.
    Each mailbox's own lane still applies before the budget, so containment is not
    weakened — but new leads drift toward the least domain-contained class of mailbox.

61. **The observed relay IP and the content version are reported and gate nothing.**
    The relay IP is now also a correlation dimension (invariant 58) and the content
    version is rendered on the warmup pool page; neither changes what reads them,
    which is nothing that decides whether a mailbox may send.
    `warmup_observations.observed_relay_ip` is the address the RECEIVER saw its peer
    connect from, taken from the topmost `Received` hop the receiving infrastructure
    wrote. `content_version` fingerprints the library template a send carried, copied
    onto the observation inside `RecordWarmupPlacementObservation`'s own
    `INSERT…SELECT` so it cannot disagree with the send.
    **Neither gates anything**, and the reasons differ. The relay IP is derived from a
    `Received` chain a sender partly controls, so only hops attributable to the
    receiver are trusted at all, and letting an attacker-influenceable relay identity
    reach pool eligibility is the escalation path invariants 57–60 describe. The
    content version is a calibration problem twice over: the sample per version is
    small by construction, because a shared library spreads thin across a pool, and a
    template's apparent spam rate is confounded with whichever mailboxes happened to
    draw it.
    Private, loopback, link-local and CGNAT ranges are refused as relay identities —
    an attacker can name those freely and they identify nothing. `''` means "nothing
    trustworthy was observed", which is also every pre-column row, and is never to be
    read as a relay.
    **ASN resolution is deliberately absent.** It needs a MaxMind-class dataset: a new
    dependency, a licence question, and a refresh story for a self-hostable product.
    The IP alone is what could be recorded without taking that on, and naming a relay
    by ASN is the more useful grouping — so this is deferred for a supply-chain reason,
    not an effort one.

62. **A shared warmup pool removes the premise every preceding invariant rests on.**
    Invariants 52 and 56–61 all bound their residual risk the same way: the attacker
    is a customer, acting inside their own tenant, damaging only their own reads, with
    something to lose. A cross-workspace pool (design §9, Phase 3) removes all three
    at once and adds a motive that did not exist — degrading a competitor.
    **Nothing here is weakened, and nothing is pre-approved.** What follows is what
    would have to become true.
    **The coordinator issues instructions, never evidence.** No output of a
    coordinator — no peer's advertisement, no reported outcome, no lease term — may be
    an input to a lane, health-state or promotion decision. That is the single rule
    that keeps invariants 57–61 intact under a shared pool: they say route-, incident-,
    observer- and content-derived signals gate nothing, and a peer-supplied signal is
    strictly less trustworthy than our own. It is also what protects invariant 60's
    clean path into campaign selection.
    **A participant's mail address is never published.** It may appear only in a single
    assignment, to the one peer about to send to it, for the life of one lease. An
    advertisement carrying addresses is a harvestable directory of every mailbox in the
    pool. `internal/platform/coordinator` makes this structural: the outbound payload
    has no address field.
    **"Give me a partner from another workspace" must be inexpressible, not merely
    refused.** A `PairRequest` names exactly one workspace — the caller's own — and the
    local implementation additionally rejects any candidate whose workspace differs.
    **What a coordinator must refuse while sentinel capacity is zero:** every
    cross-tenant assignment, consent on both sides included. Phase 3's own gate is
    "expand only when sentinel capacity and incident operations can protect the
    promised isolation"; with no sentinels, the only way to diagnose a degrading peer
    is to expose real customer mailboxes to it. Sentinels exist as a mechanism
    (`warmup.Pairable`, `warmup_participants.is_sentinel`) but a pool has capacity only
    once an operator designates one.
    **No threshold is asserted for peer-only versus sentinel-corroborated evidence.**
    `warmup.SentinelPoolShare` is an advisory upper cap, not a floor, and nobody has
    measured what a sentinel observation is worth relative to a peer one in this
    system. Until that exists the answer is binary refusal, not a discount — every
    slice here that guessed a threshold and acted on it had to be walked back.

## Open/click bot and prefetch classification

63. **A tracking hit's classification is a reporting signal, never a security
    boundary, and never an authorization input.** Every open/click is judged
    HUMAN or MACHINE at write time by the pure classifier
    (`internal/platform/botfilter`) and the verdict is stored on the event
    (`tracking_events.is_machine` / `machine_reason`, a CHECK keeping the pair
    consistent). Everything it reads except the send id — User-Agent, source
    address, arrival time — arrives on a PUBLIC, unauthenticated endpoint and is
    fully attacker-controllable, so a `human` verdict means "not obviously a
    machine", never proof a person was there. Nothing may be authorized,
    suppressed or granted on the strength of it.

    **Tenancy is unchanged.** The workspace and campaign still come from the
    `sends` row resolved server-side (invariant 4); a machine verdict is not a
    path around that pin, and no request field influences it. The two classifier
    lookups (`GetSendTrackingContext`, `CountRecentSendOpensFromSubnet`) are
    keyed by the HMAC-signed send id and are deliberately NOT workspace-scoped:
    there is no authenticated principal to scope by, they return a count and a
    timestamp about that one send rather than row data, and scoping them would
    mean trusting a workspace id supplied by an unauthenticated caller.

    **The event is STORED, never dropped.** Machine hits are recorded like any
    other and excluded from the headline rate by a FILTER on the stored column,
    so reporting can say "N opens, M of them machine". Silently discarding them
    would present a truncated count as the whole truth.

    **No new outbound surface, and none may be added.** Classification does no
    I/O beyond the two indexed reads above: no IP-intelligence provider, no DNS,
    no network call of any kind. This endpoint takes unauthenticated traffic at
    blast volume, so a per-hit lookup would put a third party on the hot path
    and leak every recipient's address to them. Cloud-provider range lists, if
    ever added, belong in a periodically-refreshed table read off this path.

    **A failure degrades, it does not condemn.** An unreadable event history
    classifies on the remaining signals rather than defaulting to machine: a
    database blip must not permanently zero a workspace's open rate. The same
    reasoning makes an absent User-Agent, an unresolvable IP, and a private or
    loopback address all NOT machine — behind a misconfigured proxy every hit
    carries a private address, and the fail-safe direction here is to
    over-count a rate an operator can question rather than silently delete a
    real person's engagement.

    **`client_ip` is recipient personal data.** It is retained solely as the
    burst rule's input, must never appear in an API response DTO, and is erased
    with its workspace through `tracking_events`' existing `ON DELETE CASCADE`.

    **A MACHINE verdict never changes what the endpoint DOES.** The pixel is
    still served and the click is still redirected — a scanner that got a 404
    would report the link broken and the human it protects would never reach the
    page. The verdict governs reporting only.

64. **A task payload is a pointer, never content.** Every payload in
    `internal/platform/queue` carries ids plus `workspace_id` and nothing else.
    Anything a handler needs that is not an id lives in a row the handler
    re-reads, so the row stays the single source of truth and the payload carries
    nothing worth disclosing.
    **The rule is structural, not stylistic.** `queue.DeadLetterErrorHandler`
    stores `task.Payload()` byte-for-byte into `task_dead_letters`, and
    `GET /dead-letters` serves it verbatim under `campaigns:read` — which IS in
    `auth.OAuthGrantableScopes`, while `ScopeInboxRead` is deliberately excluded
    from it because "reply bodies are free-text correspondence content, a
    materially more sensitive category". A payload carrying content therefore
    hands correspondence to a scope that was structurally denied it, and it does
    so only on failure, which is exactly when nobody is looking. That is how
    `inbox:reply_send.body_text` went unnoticed: the field had no doc comment of
    its own, and the disclosure needed a send to fail before it existed.
    **One permanent exception:** `TestSendPayload.To`, a single operator-typed
    recipient address. It is structured contact-class data that `contacts:read`
    already grants wholesale, not a third party's correspondence, and a row
    holding one address for thirty seconds would be the wrong abstraction. The
    acceptance is named in the payload's doc comment and in the test's allowlist,
    so it is a decision on record rather than an oversight repeated.
    **And one temporary one:** `InboxReplySendPayload.body_text` remains in the
    allowlist while that type survives as a drain for tasks enqueued before the
    fix. Nothing produces it; the worker refuses to capture it at all, and the
    control plane strips the body and files the row `discarded` — deliberately
    different, because the control plane must still leave the operator a record
    that a send was permanently lost, which is the whole reason the table exists.
    The type and its allowlist entry are deleted together in the release after
    the drain completes. Until then the allowlist has exactly two non-id entries.
    Both are keyed by the WIRE key (`TestSendPayload.to`,
    `InboxReplySendPayload.body_text`), not the Go field name, because the wire is
    what a dead letter stores: a field named `To` tagged `json:"body_text"`
    publishes a body while reading as an allowlisted address.
    **The same rule governs `last_error`**, which is stored verbatim and served
    in the same response. A provider's rejection string names the recipient and
    echoes the server's own text, so a handler returns a stable token to the queue
    and keeps the raw cause in its log line; the stored message is truncated on a
    rune boundary, because a byte cut through a multi-byte response is invalid
    UTF-8 and Postgres would reject the whole row.
    **Three gates, one predicate** (`queue.IsLegacyContentBearingTaskType`): the
    worker refuses capture, the control plane strips and files as `discarded`, and
    the read path redacts. The worker gate alone was not enough — it lives in the
    worker binary, so a pre-fix pod still running through a rolling deploy could
    write a fresh body-bearing row after the one-shot migration had already run,
    and the packaged Helm chart has no migration hook to order that for you.
    Enforced by `TestTaskPayloadsCarryNoContent`, which walks the JSON a payload
    actually emits — not its Go fields, so an embedded struct cannot smuggle a key
    past the allowlist — and parses the package so a brand-new payload type cannot
    slip past unlisted; and by `TestDeadLetterListNeverServesReplyBody`, which
    seeds an UNMIGRATED body-bearing row and asserts the sentinel appears nowhere
    in the bytes an API-key principal holding only `campaigns:read` receives. That
    test was vacuous on its first draft — every fixture was built without a body,
    so it searched bytes that could never have contained one and passed with the
    entire fix reverted.
    `task_dead_letters` is swept at 90 days by the maintenance job, for the
    reasoning invariant 55 gives: an append-only table reachable by outside input
    needs a horizon, or a single exposure becomes a permanent one.

## Inbound feedback reports (ARF complaints)
65. **A complaint that arrives as MAIL is resolved against our own send, never
    against the address the report names.** `ParseARF`
    (`internal/worker/inbox/arf.go`) reads an RFC 5965 feedback report, and
    `recordInboundComplaint` (`poll.go`) routes it to the SAME idempotent ingest
    `POST /deliverability/events` feeds — so the suppression, the score, the
    at-risk list and the campaign breaker consume it unchanged, and there is no
    second complaint path to drift.
    An ARF is ordinary, unauthenticated mail: anyone able to email a connected
    mailbox can deliver one, and an ingested complaint suppresses an address
    workspace-wide and can pause a campaign (invariants 40 and 42) — which is
    exactly why `deliverability:write` is withheld from OAuth grants. So the
    report's own `Original-Rcpt-To` is NEVER the address acted on. The
    `Message-ID` the report QUOTES is resolved with the workspace-pinned
    `FindSendByMessageID`, and the complaint is recorded against THAT send's
    contact — the same discipline the hard-bounce arm applies to
    `Final-Recipient`, for the same reason. A report that quotes nothing, quotes
    a send we do not have, or names a recipient the send did not go to is a
    LOGGED SKIP (`inbox_poll_complaint_declined`, with a stable reason token and
    no address in the log line), never an ingest. The idempotency key is
    `arf:<send id>`, so a re-poll or a redelivered report writes nothing and
    causes nothing.
    **The residual, named rather than compared.** The bar for reaching this path
    is "already knows a real `Message-ID` of a real send" — the same bar
    reply-driven suppression has. What the bar BUYS is not the same, so it is
    stated here directly rather than by pointing at that one. Trace `Ingest`
    (`app/deliverability/service.go`) → `evaluateAfterIngest` →
    `EvaluateBreaker`: a mail-borne complaint does three things a reply cannot.
    (1) It flips the workspace's complaint rate from UNMEASURED to MEASURED — the
    `Complained` pointer in `toInputs` is deliberately nil until a feed reports
    (invariant 4), so the first ingested complaint permanently changes what the
    deliverability score means. (2) It reaches the campaign circuit breaker and
    can AUTO-PAUSE a campaign (invariant 42). (3) It is not rate-bounded: each
    send is a distinct `arf:<send id>` key, so anyone holding N `Message-ID`s
    from one campaign can drive the numerator N times. The corroboration is one
    cross-check that can only ever REFUSE: `Original-Rcpt-To` is OPTIONAL in RFC
    5965 and the check is skipped entirely when it is absent
    (`TestPollARFWithoutAReportedRecipientStillIngestsOnTheResolvedSend`), which
    is correct — real FBLs redact the field and refusing those reports would drop
    genuine complaints — but it means the quoted `Message-ID` is the whole of the
    proof on that shape. Accepted for the reason invariant 52's placement bullet
    accepts its own: the alternative is not a stronger complaint signal but no
    mail-borne complaint signal at all, and a dropped complaint is a compliance
    failure.
    **The polled mailbox is deliberately NOT pinned, and that is an asymmetry
    with the sibling path.** `FindSendByMessageID(ctx, workspaceID, …)` is
    workspace-pinned only, although `SendRef.MailboxID` is available and IS used
    a few lines later on the reply path — so an ARF forger may deliver to ANY
    connected mailbox in the workspace, while a warmup forger must hit the exact
    mailbox the send was addressed to (`FindWarmupSendByMessageIDForRecipient`
    makes `to_mailbox` load-bearing; invariant 52). The pin is withheld because
    FBL reports commonly land on a designated `abuse@` mailbox rather than the
    sending one, so requiring them to arrive at the sending mailbox would refuse
    the majority of REAL reports. Whoever revisits this: the question to answer
    first is whether a workspace can nominate its complaint-receiving mailboxes,
    because that — not the send's own mailbox — is what the pin would have to be
    against.
    `Original-Mail-From` is deliberately NOT a fallback for the complained
    recipient: per RFC 5965 it is the offending message's envelope SENDER, i.e.
    our own mailbox, so falling back to it would name the wrong party entirely.
    Only `Feedback-Type: abuse|fraud` is a complaint. `not-spam` (RFC 6650) is
    the INVERSE signal — a recipient rescuing our mail out of spam — and
    suppressing on it would be backwards; `virus`, `other` and any unregistered
    or absent value are not actionable, so the default direction is never
    "suppress". A malformed report yields nothing and never fails the poll: the
    cursor it would hold back carries every other inbound signal too.
    For the same reason, an ingest the control plane rejects as PERMANENTLY
    invalid (`coreapi.ErrInvalidComplaint`, today reachable only via a send row
    with a blank contact address) is a logged skip, not a retry. Retrying one
    returns before `SetInboxCursor`, so a single unauthenticated inbound message
    would freeze that mailbox's cursor indefinitely and stop every campaign reply,
    bounce and warmup receipt behind it — a denial of service on the whole mailbox
    for the price of one email. Transient failures still retry, because the ingest
    is idempotent and dropping a real complaint is a compliance failure
    (`TestPollARFRejectedAsPermanentlyInvalidIsSkippedNotRetriedForever`).
    The same denial of service is reachable through the lookup KEY rather than
    the ingest verdict, so it is closed in one place for all four inbound
    ingest paths: `usableMessageID` (`internal/worker/inbox/reply.go`) refuses a
    `Message-ID` that is not printable US-ASCII or exceeds RFC 5322's 998-octet
    line bound BEFORE it becomes a Postgres text parameter. `textproto.
    ReadMIMEHeader` validates header KEYS only, so a raw `0xFF` or NUL in the
    VALUE survives verbatim through the IMAP, Gmail and Graph readers, and
    Postgres refuses such a parameter with SQLSTATE 22021 — neither
    `pgx.ErrNoRows`/`coreapi.ErrNoMatch` nor transient, so it propagates and
    wedges the cursor exactly as a retried permanent rejection would. RFC 5322's
    `msg-id` is `dot-atom "@" dot-atom`, US-ASCII by definition, so the guard
    refuses nothing legitimate; an unusable id gets the answer an absent one
    gets. Covered by the four tests in `poll_messageid_test.go` and
    `poll_warmupfallback_test.go` (one per ingest path), all of which model the
    store's SQLSTATE 22021 refusal rather than letting a fake absorb it.
    A feedback report found in a SPAM/junk folder is deliberately not ingested:
    the junk pass records warmup placement only, and letting a
    spam-filter-chosen folder trigger a contact suppression is a widening that
    needs its own design pass (`scanJunkForWarmup`).

## Fleet credential brokering (the master key and the execution plane)

66. **A `role=send` worker holds no master key, and refuses to start if it is
    given one.** `INROAD_MASTER_KEY` is the KEK: it unwraps every workspace's
    DEK (invariants 14–17) and therefore decrypts every stored SMTP password and
    every OAuth refresh token in the installation. Until this slice, `cmd/worker`
    called `keys.BuildKeyring` for EVERY role, and every compose file handed a
    worker that key — so a fleet host, the role introduced precisely because it
    may run on hardware we do not control, could decrypt the whole installation,
    offline and permanently. A README claim that the control plane was the only
    holder of the wrapping key was false and was corrected before this.

    `cmd/worker.resolveCredentialMode` now decides from the role and the
    configuration alone, and refuses the unsafe combinations at STARTUP:
    `role=send` with `INROAD_MASTER_KEY` set, `role=send` with no broker (it
    could not send, so it must not start), and ANY role holding the key while a
    broker is configured — because a process configured to broker has no
    business also being able to decrypt everything itself. The single-process
    self-host topology (`RoleAll`) is untouched: it sets `INROAD_MASTER_KEY` and
    nothing else, exactly as before, and none of the broker settings exist for
    it. `role=control` needs neither, because `registerScheduled` wires no
    handler that opens a credential.

67. **A broker request names a subject; it never carries a ciphertext.** The
    wire shape (`internal/platform/credbroker`) is `{workspace_id, mailbox_id}`
    — ids only, asserted both on the server's view and on the raw request bytes.
    If a worker could send the blob to open, the broker would be a
    general-purpose decryption oracle and moving the key would have bought
    nothing. The control plane re-reads the row itself, `workspace_id`-pinned
    (invariant 4), with the same belt-and-braces `ErrCrossTenant` check every
    other read has; the field ciphertext stays AAD-bound to its workspace
    (invariant 15), so a cross-tenant pair fails closed twice over.

    The opened value is produced by the SAME code path as the in-process one
    (`inprocess.NewCredentialOpener`), so a brokered credential and a locally
    opened one cannot drift. That is also what makes invariant 9 literally true
    for a fleet: token refresh, re-seal and persist happen only where the key is,
    and the worker receives a short-lived access token and never a refresh token.

68. **The broker is a separate listener, off by default, https by default, and
    fails closed.** It is never mounted on the public API router — the rule
    `httpx.MetricsMux` follows, for a sharper reason: these responses ARE
    credentials. `cmd/inroad` opens it only when `INROAD_FLEET_BROKER_ADDR` is
    set, so an installation that has not opted in serves no such route. The
    shared bearer token is at least 32 bytes (refused below that, at startup,
    like `INROAD_JWT_SECRET`) and compared in constant time; redirects are never
    followed (following one would replay the token and accept a credential from
    wherever it pointed); error bodies are fixed strings, so the endpoint is not
    a probe oracle for which ids exist; neither side logs a request or a response
    body. An `http://` broker URL is refused unless
    `INROAD_FLEET_BROKER_ALLOW_PLAINTEXT` is explicitly set — the identical rule
    and the identical reasoning `INROAD_S3_ENDPOINT` gets in invariant 6, because
    this channel carries both the bearer token and the plaintext credential. The
    broker URL is OPERATOR-supplied, not user-supplied, so it does not go through
    (and does not need) `mail.vetAddr`, for the same reason the S3 endpoint does
    not. A worker that cannot obtain a credential refuses to send
    (`credbroker.ErrNotConfigured`) — it never falls back to anything weaker.

69. **What brokering does NOT contain, stated so the claim is not overstated.**
    It removes the OFFLINE capability: a stolen worker disk, image or environment
    file decrypts nothing, access is revocable by rotating one token instead of
    re-encrypting every DEK, and every open is a request the control plane sees.
    It does NOT shrink what a LIVE compromised `send` worker can reach. Two
    reasons, both structural and both still true:
    - Every `send` worker consumes the SHARED `send` queue and may legitimately
      be handed a job for any mailbox, so the broker must answer for any mailbox
      the token names. Only the two task types whose payload NAMES a mailbox are
      routed by assignment (`warmup:tick`, `inbox:poll`); `sequence:advance` — a
      campaign send, and the highest-value credential in the set — is keyed on an
      enrollment whose mailbox is not resolved until the job is hydrated, so it
      stays on the shared queue and any worker may be handed it. Scoping the
      broker needs per-mailbox routing for THAT path first.
    - The token is shared across the fleet, so the broker cannot tell which host
      is asking; revoking one revokes all. Per-worker identity is only useful
      once the point above is fixed.

    The THIRD bullet this invariant used to carry — "`cmd/worker` still opens
    its own `pgxpool`, so a live worker still reads every workspace's rows" — is
    no longer true and has been removed rather than softened. Invariant 79 is
    what replaced it, and it restates the residual exposure without that clause.

    Treat a `send` host as able to obtain the CREDENTIAL of any mailbox in the
    installation while it is running, and restrict the fleet listener to the
    fleet network.

## Remote coreapi transport (the worker's database connection)
72. **The fleet listener serves two transports under one token, deliberately.**
    `INROAD_FLEET_BROKER_ADDR` now carries both the credential broker
    (`/internal/fleet/credentials/…`) and the coreapi remote transport
    (`/internal/fleet/coreapi/…`, `internal/coreapi/remote`), authenticated by
    the same `INROAD_FLEET_BROKER_TOKEN` through one shared constant-time check
    (`credbroker.RequireToken`), validated by one shared endpoint parser
    (`credbroker.ParseEndpoint`, which is where invariant 68's https rule and
    32-byte floor now live for BOTH).

    Sharing is the security decision, not the convenient one, and the argument
    is worth recording because the alternative is defensible. Separating would
    give blast-radius isolation between "unwrap a credential" and "read job
    data". It buys nothing here: there is no principal that holds one and not
    the other — a `role=send` worker brokers the credential it dials with AND
    checks suppression before every send — so two tokens partition no capability
    while doubling what must be rotated, and rotation that misses one leaves a
    live capability. The asymmetry also runs the wrong way: the credential token
    is strictly the more powerful, since it yields the ability to authenticate
    AS a customer's mailbox. What would change the answer is a role that needs
    coreapi data and not credentials; it does not exist, and both sides already
    take a base URL and token as parameters, so splitting later is additive.

    Everything invariant 68 states holds unchanged and now covers both: separate
    listener never mounted on the public API router (asserted by
    `TestThePublicAPIRouterServesNoFleetRoute`), off unless the address is set,
    https unless explicitly opted out, redirects never followed, fixed error
    strings, no request or response body logged.

73. **A coreapi request names one subject and can never express a query.** The
    wire shape is `{workspace_id, email}` — asserted on the raw request bytes,
    and unknown fields are refused rather than ignored, so a caller cannot add a
    parameter and believe the narrower answer it gets back. There is no filter,
    no pattern, no limit and no cursor, which is the same restriction invariant
    67 puts on the broker and for the same reason: a request that could express
    "rows matching X" would make this seam a general query engine over the tenant
    database, which is exactly the capability the plane split exists to remove.
    Every route a later slice adds answers to that rule.

    The address is not an id, and that is stated rather than glossed. The
    subject of a suppression record IS an email address and the worker already
    holds this one, from the job it was handed — so the request reveals nothing
    to the control plane, and the answer is one bit about an address the caller
    named. What it is not is enumerable: nothing lists the table, pages it, or
    matches a prefix.

    **Workspace-pinned, adding to the tenant pin rather than replacing it.** The
    workspace travels on the wire because there is no session here to derive it
    from — the caller is a machine holding the fleet token, not a user holding a
    JWT. The handler parses it and the reader applies the same `workspace_id`
    SQL filter the in-process path applies (invariant 4), over the SAME
    `app/suppression` store the HTTP API and the in-process coreapi path use, so
    there is one implementation of "is this address suppressed" behind all
    three. Naming a different workspace asks a question about that workspace,
    which is what the fleet token already authorises — one worker legitimately
    serves every tenant's sends. Proven against Postgres end to end by
    `TestARemoteCoreAPIClientWithNoPoolAnswersFromTheControlPlane`, whose client
    holds a nil pool: every answer it gives came off the wire, and the foreign
    workspace reads false.

74. **A worker that cannot reach the control plane refuses to send.** Every
    failure — unreachable host, 500, rejected token, malformed id — is
    `(false, err)`, and all four call sites (`worker/testsend`, and
    `worker/inbox`'s manual reply, pending reply and pending compose sends)
    already treat a non-nil error as "do not send". It never falls back to a
    local read, never retries into a guess, and never reopens a pool.
    `TestAnUnreachableControlPlaneRefusesRatherThanMissingASuppression` proves
    the direction that matters: the address IS suppressed in Postgres, the
    control plane is down, and the answer is an error rather than a false
    negative — which would be mail delivered to someone who opted out.

    **There is deliberately no cache.** A TTL on a suppression answer is a
    correctness knob, not a tuning knob, for exactly that reason; it gets its
    own decision when a measured cost justifies it.

    Timeouts are chosen and sit UNDER the asynq ceilings the call runs inside
    (`queue.sendTimeout` 2m, `pollTimeout` 5m), following the precedent
    `internal/platform/mail/apiclient.go` set: 10s whole request, 5s dial, 5s
    TLS handshake, 5s response header. A wedged call therefore fails the CALL —
    attributable and retried — rather than burning the task's budget and
    surfacing as a killed handler with no cause.

    A JOB read is sized separately, on its own `http.Client`, rather than by
    loosening those: a job build runs several indexed SELECTs and opens a mailbox
    credential, and an EXPIRED OAuth token makes one round trip to Google or
    Microsoft inside the handler. So 20s whole request and 15s response header,
    matching `credbroker`'s numbers for the identical work behind the identical
    listener, still ~17% of the tightest ceiling above it. The response cap is
    likewise sized for what a job actually is: a realistic step send encodes to
    ~4.8 KiB, but the API accepts up to httpx's 1 MiB JSON body for a step, and
    1 MiB of HTML encodes to ~2.8 MiB here because `encoding/json` escapes every
    `<`, `>` and `&`. The 8 MiB cap is therefore ~2.8x the largest job the
    product can legitimately produce — sizing it tighter would mean a campaign
    that silently never sent on a fleet worker, which is worse than the
    allocation it bounds.

75. **The transport is decided by the ROLE, and self-host never takes it.**
    *(Revised by slice 4; the original text described
    `INROAD_FLEET_COREAPI_REMOTE` as the switch, and it no longer is.)*

    `resolveCoreAPIMode` now derives the mode from `INROAD_WORKER_ROLE` alone:
    `role=send` reads coreapi remotely, `role=control` and `role=all` read
    in-process. The flag was made redundant rather than removed, because
    `role=send` already had to set `INROAD_FLEET_BROKER_URL` (invariant 66) and
    has no pool to read any other way — leaving it opt-in would have meant an
    operator could configure a fleet host that still held a database
    connection, which is the whole thing this is for.

    `INROAD_FLEET_COREAPI_REMOTE` is KEPT, still parsed strictly (an
    unrecognised value is a startup error, never a silent false), for the one
    thing it can still catch: set TRUE on a `control` or `all` worker it is
    refused, because an operator who set it there believes their worker stopped
    reading the tenant database and it has not. Deleting the field would turn
    that refusal into a silent ignore. On `role=send` it selects nothing.

    A self-hosted installation is untouched, and that is asserted through the
    real `config.Load` with a cleared environment rather than a struct literal:
    `TestSelfHostDefaultsToTheInProcessCoreAPI` (role=all and role=control
    resolve in-process and the wiring builds no remote client),
    `TestADatabaseURLIsFineOnEveryOtherRole`, and
    `TestTheDefaultSuppressionSourceReadsPostgres`.

    A `role=send` worker with no `INROAD_FLEET_BROKER_URL` refuses at startup
    (`TestASendWorkerWithNoFleetChannelRefusesToStart`) rather than falling back
    to opening a pool, which is the one outcome that would make the boundary a
    lie.

76. **A coreapi job response carries no credential. There is ONE channel in the
    installation that hands out a plaintext secret, and it is the credential
    broker.** The per-message job reads (`GetStepSendJob`,
    `GetInboxPollJob`, `GetWarmupSendJob`, `GetWarmupEngageJob`,
    `GetWebhookDeliveryJob`, `GetTestSendContent`, `ResolveSenderTransport`,
    `FindSendByMessageID`, and `NextWarmupDue` since slice 4 — nine)
    answer with everything about a piece of work EXCEPT
    its secret: a mailbox's host, port, username and TLS policy travel, the
    decrypted password or access token does not. The worker then opens that one
    secret through `credbroker` (invariants 67–69), naming the mailbox by id.

    Inlining it would have been one round trip instead of two and would have
    matched the in-process shape exactly. It was rejected because `credbroker`
    already exists FOR this, with a reviewed ids-in/values-out contract, and is
    already MANDATORY on the only role permitted to read coreapi remotely
    (`role=send` cannot start without a broker) — so a second plaintext channel
    would have added a second set of audit properties to keep in step, over the
    same listener, with the same token, for the same principal. Two channels
    that both hand out secrets is the duplication that drifts.

    **Omission is structural, not remembered.** Every secret field on the
    `coreapi` job types is tagged `json:"-"` at its declaration — the same
    "omission by construction" standard invariant 2 sets for API response DTOs —
    so no later change to a job build can start emitting one.
    `TestSecretJobFieldsNeverCrossTheWire` populates every secret field with a
    recognisable marker, drives all six credential-bearing routes through the
    real handler, and fails if the marker appears in any response body, raw or
    base64. It was verified against an untagged field and fails.

    **Zeroization is unchanged, and the honest statement is worth making.** The
    worker still wipes the `[]byte` it holds, and that still does not reach every
    copy: on a fleet host the plaintext has arrived through an HTTP read buffer
    and a base64 decode since brokering existed (invariant 69). Slice 2 does not
    change that in either direction, and the reason it does not is precisely that
    the secret does not travel on this wire. On the warm-up engage path the
    worker's single deferred wipe covers only the OUTER copy, so the transport
    installs the SAME slices on the nested reply rather than copies — an
    unaliased inner copy would survive the wipe intact
    (`TestTheEngageJobsReplyCredentialAliasesTheOuterOne`, verified against an
    un-aliased implementation).

    **Fail closed on both halves.** A job that fetched but whose credential could
    not be opened is discarded, never returned: a job with an empty password
    would dial without authenticating, and a job with an unset gate flag would
    send mail a gate meant to stop. Every read returns the ZERO value alongside
    its error (`TestEveryJobReadFailsClosedWhenTheControlPlaneDies`, which first
    proves every read works while the control plane is up, then kills it).

    **Workspace-pinned exactly as invariant 73 describes**, over the SAME
    in-process job builds the single-process topology runs — `cmd/inroad` serves
    them by type assertion on a real `inprocess` client, so a fleet worker's job
    and a local one come out of identical code including every tenant pin and
    every send gate, rather than two implementations that can drift.
    `TestJobReadsArePinnedToTheRequestedWorkspace` drives a foreign workspace id
    at six routes through the real handler and gets a refusal and a zero value
    from each.

    **Two sentinels cross as themselves**, because the execution plane branches
    on them and flattening either would change what a worker does:
    `coreapi.ErrNoMatch` (the ordinary answer for nearly every inbound message —
    a generic error would abort the poll before `SetInboxCursor` and stop the
    mailbox processing inbound mail at all) and `pgx.ErrNoRows` (a webhook
    delivery deleted after its task was queued must be dropped, not retried to
    exhaustion). A 404 naming NEITHER — an un-upgraded control plane answering
    `net/http`'s own plain-text 404 — maps to no sentinel and stays a plain
    error, because reading it as "row gone" would make a worker silently discard
    every webhook delivery (`TestAnUnrecognised404IsNotMistakenForAVanishedRow`,
    verified against an implementation that did).

77. **A lost response is never a double send. The claim decides what the retry
    does, not the transport.** The claim and outcome path now crosses the wire —
    the sequence step's claim/deliver/advance/release/finalize, the enrollment
    stop/defer/cap-deferral, the warm-up claim/sent/release/fail/engaged, the
    inbox reply/class/unsubscribe/bounce outcomes, and the three webhook delivery
    outcomes (`internal/coreapi/remote/outcomes.go`).

    A network adds a third outcome a function call does not have: the request
    arrived, the control plane COMMITTED, and the response was lost. A worker
    cannot tell that apart from a call that never happened, so nothing here tries
    to. Every method fails the CALL, the worker returns the error, asynq retries
    the whole task, and the retry passes through `ClaimStepSend` — the only thing
    in the system that knows what already happened. A worker that claimed, sent
    and lost the answer to `MarkStepDelivered` re-claims, is told
    `ClaimAlreadySent`, and advances the cursor instead of delivering again.
    Invariant 4a's posture is unchanged and now holds over HTTP: never double,
    occasionally drop a rare ambiguous send.

    That path was UNREACHABLE in tests before this slice, because in process a
    response cannot be lost. `remoteoutcomes_integration_test.go` makes it
    reachable with a fault injector that runs the REAL handler to completion
    against a throwaway recorder — so everything it committed is committed — then
    hijacks the connection and closes it without writing a byte. Three scenarios
    drive the real `sequence.AdvanceHandler` with a counting sender against real
    Postgres: the claim's response lost (the retry must not re-claim), the
    delivery's response lost (the retry must not re-send), and the delivery
    committed with the advance never run (the retry must recover forward). All
    three assert EXACTLY ONE SEND, and all three were verified to report two
    against an implementation whose wire loses the `ClaimAlreadySent` state, and
    against one whose claim re-claims without consulting the existing row.

    **Idempotent by KEY, never by attempt**, audited per method against its SQL
    rather than assumed (the audit is in `outcomes.go`): the two claims key on the
    deterministic send id that IS the row id; `MarkStepDelivered`,
    `AdvanceStepCursor` and `FinalizeStepSend` set their values absolutely;
    release/stop/fail/engaged/the webhook trio are status-guarded in SQL;
    `MarkUnsubscribed` and `MarkBounced` reuse the `ON CONFLICT DO NOTHING`
    suppression insert of invariants 20 and 42. The ONE exception is
    `IncrementEnrollmentCapDeferrals` (`cap_deferrals + 1`), left non-idempotent
    deliberately: its only consumer is a log threshold — the count-based ceiling
    that used to stop an enrollment was removed because a daily cap is
    self-clearing — and the same double-bump window already exists in process
    when its enqueue fails.

    **No method returns "unknown", and none invents a success.** A failed outcome
    returns the ZERO value alongside its error; a failed claim returns
    `ClaimSkip`, which is "do nothing" rather than "send". The four-state protocol
    crosses as a STRING enum, not the Go iota, so the enum's declaration order is
    not part of a network contract, and an unrecognised value is an ERROR on both
    sides rather than a default — defaulting to `ClaimSkip` would hide a version
    skew behind a fleet that quietly stopped sending.

    **The credential does not travel in this direction either.** These methods
    take the job the worker was handed, and that worker is HOLDING the decrypted
    SMTP password and OAuth token it just dialed with. The `json:"-"` tags of
    invariant 76 cover the request as well as the response, and
    `TestAnOutcomeRequestCarriesNoCredential` asserts it on the bytes of all nine
    job-carrying requests. The job travels WHOLE rather than as a subset for the
    reason invariant 76 gives for the responses: a hand-written mirror that
    forgot `NotDueUntil` would not fail to compile, it would send into a stated
    out-of-office absence.

    **Workspace-pinned twice on the job-carrying routes** — the envelope's
    `workspace_id` and the workspace inside the job must parse to the same uuid,
    or the request is a 400 before anything runs — and by the same `workspace_id`
    SQL filter everywhere else.
    `TestOutcomeWritesArePinnedToTheRequestedWorkspace` re-points a real job at
    another tenant and asserts the owning workspace's rows and cursor are
    untouched.

    **The control plane's coreapi client is now built WITH its optional wiring**
    (`cmd/inroad/main.go`): metrics, realtime and the webhook emitter. While it
    served only reads, omitting them was correct and documented. The outcome
    writes touch all three — `MarkBounced` publishes `send.bounced` and emits
    `email.bounced`, `MarkUnsubscribed` emits `contact.unsubscribed`, every claim
    reports `inroad_send_claims_total` — and omitting them would not have failed
    anything, it would have silently stopped a fleet deployment's webhooks firing.

78. **A lost response never duplicates a human's reply, and the transport prefers
    dropping an ambiguous manual send over risking a second one.** The manual
    reply and compose protocol now crosses the wire whole — the reply job read,
    the record, the legacy drain claim/release, and the claim families for
    deferred replies and deferred composes
    (`internal/coreapi/remote/inboxsends.go`, twelve methods).

    The bar is deliberately higher than invariant 77's. A duplicate sequence step
    is one extra marketing touch; a duplicate manual reply is the operator's own
    words arriving twice in a conversation a customer is watching. What holds it
    is the ROW, never this transport: a status-guarded `scheduled` -> `sending`
    UPDATE with a 300s lease (`queries/inbox.sql`), which is simultaneously the
    claim and the operator's undo handle.

    **`ClaimPendingInboxReply` loses the LEASE AND THE BODY together**, which has
    no analogue elsewhere on the seam — every other claim answers with an enum. A
    lost response means the row is `sending` and the content the worker was about
    to send is gone with the answer. The retry re-claims, the guarded UPDATE
    matches nothing, and the control plane answers
    `coreapi.ErrInboxPendingNotClaimable`, which `worker/inbox` treats as
    terminal: that attempt sends NOTHING. The reply is therefore deferred until
    the lease expires, at which point the row — which still holds the body —
    delivers it exactly once.

    **That last sentence was a promise nothing kept.** Delivery after the lease
    needed *a task attempt to happen to land* after it, so asynq's retry
    schedule — not the design — decided whether a human's reply was ever sent.
    Invariant 79 is what makes it true.

    That trade is chosen rather than inherited. Handing the body back to "the same
    worker" on a re-claim would need an attempt id on the wire, and therefore a
    change to the coreapi signature, every fake and both handlers — and any such
    mechanism weakens the mutual exclusion between two DIFFERENT workers, which is
    the only thing standing between a lost response and a duplicate reply. A
    deferred send the operator can see in their outbox beats a double send they
    cannot take back. The window is not new either: an in-process worker that
    claimed and then crashed leaves the identical row in the identical state
    (invariant 4a).

    `remoteinboxsends_integration_test.go` drives the real
    `PendingReplySendHandler` and `PendingComposeSendHandler` against real
    Postgres with slice 77's response dropper, and asserts EXACTLY ONE SEND across
    the lost claim, the lost completion and the lease expiry. Both were verified
    against a deliberately broken implementation — with the claim error ignored
    they report "THE DOUBLE SEND: sent 2 times for one manual reply" and "a
    cancelled reply was sent 1 times".

    **Two sentinels cross as 409, not 404, and the status is load-bearing.**
    `ErrInboxPendingNotClaimable` (cancelled, already sent, not yet due, or held
    by a live lease) and `ErrInboxNoInbound` (a thread with nothing to reply to)
    are both branched on by `worker/inbox`. 404 means "the row is gone"; these mean
    "the row is there and its state forbids this". Keeping them off 404 is not
    tidiness — a stray 404 from a mis-routed request or an intermediary must never
    be readable as "the operator cancelled this reply", because that reading makes
    a worker return nil, report the task done, and silently never send a reply the
    operator is watching for. An unrecognised 409 maps to no sentinel at all
    (`TestAnUnrecognisedConflictIsNotMistakenForAnUndo`).

    **The sentinel had to be made to exist first.** `internal/app/inbox` returns
    its own `inbox.ErrPendingNotClaimable` while `worker/inbox` checks
    `errors.Is(err, coreapi.ErrInboxPendingNotClaimable)` — two distinct
    `errors.New` values, so the check had NEVER matched. Every undone reply was
    returned to asynq as a failure, retried to exhaustion, and captured into
    `task_dead_letters` (served under `campaigns:read`) instead of ending the task
    quietly. `inprocess` now translates, wrapping rather than replacing.

    **This is the one place a tenant's own CORRESPONDENCE crosses the fleet wire**,
    and it is unavoidable: a worker cannot send a reply it has not been given the
    text of. Eleven of the twelve routes are ids-only; the twelfth
    (`inbox-reply/record`) carries the delivered body back because the thread's
    history is written from it, and the worker is by then holding that exact text
    because the control plane handed it over on the claim — so the wire reveals
    nothing the caller was not already given, and nothing on it can express "rows
    matching X". What follows is a rule rather than a mitigation: **no route logs a
    body, a subject or a recipient**, on either side. The reply body still never
    enters a task payload (invariant 64) — the task remains a pointer to a row.

    **Workspace-pinned like every other route**, and asserted on the BODY as well
    as the row: `TestTheManualSendProtocolIsPinnedToTheRequestedWorkspace` checks
    that a foreign workspace's claim receives an empty `BodyText`, because leaking
    correspondence is a different and worse failure than leaking a state
    transition.

79. **The sweep that rescues a stranded manual send can never cause a second
    one.** `inbox:pending_send_sweep`
    (`internal/worker/inbox.PendingSweepHandler`, the eighth periodic reconcile)
    finds replies and composed emails no live task will deliver — a row left
    `scheduled` because its enqueue was lost, and a row abandoned in `sending`
    past its lease — and re-enqueues the ordinary send task for each. It closes
    the gap invariant 78 named: delivery after a lost claim no longer depends on
    a retry happening to land after the lease.

    Three properties make a row safe to rescue, and they are independent:

    - **The scan's predicate is a strict subset of `ClaimInboxPendingReply`'s own
      guard** (`queries/inbox.sql`), and the **lease is added by the control
      plane**, not supplied by the worker — `ListStrandedPendingInboxSends` adds
      `PendingReplyLeaseSeconds` to the caller's grace itself. No configuration a
      worker can express nominates a row whose lease is still live, which matters
      because a live lease means another worker may be mid-dial and that mutual
      exclusion is the only thing between two workers and one duplicate reply.
    - **The sweep mutates nothing.** It does not release the row, clear
      `claimed_at` or move `send_after`. A row it rescues was already claimable;
      the sweep supplies the missing task, not a state change. Releasing a
      `sending` row here would destroy the very evidence the claim guard reads.
    - **The sweep never sends.** Its capability interface (`PendingSweepCore`) is
      one read method — no claim, no mark, no release, no sender — so it hands
      ids to the ordinary task whose first act is the same guarded claim. It
      cannot produce a delivery the claim would have refused, including one
      racing a worker that took the lease between the scan and the enqueue.

    **The residual window, stated rather than glossed.** A row abandoned in
    `sending` *after* its dial succeeded — the post-ACK
    `MarkPendingInboxReplySent` never committed — cannot be told apart from one
    abandoned before it, so a rescue re-sends it. That is invariant 4a's accepted
    posture reaching a path that previously had no attempt to reach it, and the
    same trade `sequence:sweep_stuck_enrollments` already makes for campaign
    sends. It is taken in this direction because a reply that never leaves is
    worse and the operator cannot see that it happened. Closing it would need a
    pre-dial marker on the row, which is a hot-path write and a new fleet route.

    **A rescue must not reuse the scheduled task's asynq id**, and this is
    operational rather than cosmetic: asynq's uniqueness check is `EXISTS` on a
    task key that outlives the run (24h of `Retention`, longer in the archive)
    and `queue.Client.enqueue` treats a `TaskID` conflict as success. A rescue
    keyed `inboxpending:<id>` would be swallowed silently — the metric and the
    job-run ledger would both report a clean sweep that delivered nothing.

    `pendingsweep_integration_test.go` asserts the pair against real Postgres: a
    row under a live lease is left untouched, and a row past its lease is rescued
    with **exactly one send** resulting. Both were verified against deliberately
    broken implementations — with the lease dropped from the scan the live-lease
    case reports the nominated row, and with the scan returning nothing every
    rescue case fails.
80. **A `role=send` worker opens no database connection, and refuses to start if
    it is given a DSN.** This is the invariant slices 72–78 were building
    toward, and until it existed all of them were plumbing: `cmd/worker` still
    called `db.ConnectSized` for every role, so a fleet host — the role
    introduced precisely because it may run on hardware we do not control —
    held a `pgxpool` and could read every workspace's contacts, message bodies,
    reply text and `secret_ciphertext`, the last of which it could not decrypt
    (invariant 66) but could exfiltrate.

    **What changed, structurally.** On `role=send`, `cmd/worker` opens no pool,
    builds no `*gen.Queries`, registers no pool metrics, constructs no webhook
    emitter and no realtime hub, and does not call `inprocess.New` at all. Its
    `coreapi.Client` IS `*coreapi/remote.Client`, a type with no
    `*pgxpool.Pool` field — so "this process cannot reach the tenant database"
    is a fact about the type rather than a claim about which code paths happen
    to be unreachable today. A `depguard` rule denies `internal/platform/db`
    and `pgxpool` inside `internal/coreapi/remote`, so the transport cannot
    regain database access without failing the build.

    **The refusal.** `ErrSendRoleHoldsDatabaseURL` stops a `role=send` worker
    that was handed `INROAD_DATABASE_URL`, at startup, before anything
    connects, with a message naming the variable and saying to unset it. It is
    shaped exactly like invariant 66's `ErrSendRoleHoldsMasterKey` and for the
    same reason: either the operator believes this host talks to the database
    (it does not, and nothing will use the value), or they have put production
    database credentials in the environment of a machine that has no use for
    them. It fires on the variable being PRESENT, not on the resolved value
    being non-empty — `INROAD_DATABASE_URL` has a local-development default, so
    a check on the value would pass vacuously for every worker
    (`config.Config.DatabaseURLSet`,
    `TestASendWorkerGivenADatabaseURLRefusesToStart`).

    **What moved to make it possible.** Fifteen methods, the ones slices 1–3b
    left behind: the ten inbound-mail calls an inbox poll makes
    (`SetInboxCursor`, `SetInboxCursorString`, `StoreInboundMessage`,
    `CaptureCRMReply`, `IngestComplaint`, `ResolveReplyLabel`,
    `RecordWarmupReceipt`, `FindWarmupSendByMessageID`,
    `RecordWarmupTokenFailure`, `RecordWarmupHardBounce`), `NextWarmupDue`, and
    the four worker-infrastructure calls (`UpsertWorkerHeartbeat`,
    `RecordWorkerProviderSignals`, `AssignMailboxWorker`, `RecordDeadLetter`).
    Every one answers to invariant 73's rule — one named subject, no filter, no
    pattern, no limit, no cursor — and every tenant-scoped one is
    `workspace_id`-pinned by the same SQL the in-process path uses.

    **`StoreInboundMessage` is the SECOND place a tenant's correspondence
    crosses this wire**, after invariant 78's `inbox-reply/record`, and it is
    unavoidable in the same way: the poller holds the mailbox connection now,
    so the control plane can only write the thread's history from what the
    poller read. `CaptureCRMReply` carries the reply's subject and the sender's
    display name for the same reason. The rule invariant 78 set extends
    unchanged and now covers thirteen more routes: **no route logs a body, a
    subject, a recipient, an address, a Message-ID or a cursor value**, on
    either side.

    **The cross-tenant sweeps have no remote transport and never will.**
    `ListDueEnrollments`, `ListActiveMailboxes`, `ListDueWarmupMailboxes`,
    `EvaluateWarmupHealth`, `ListStaleSendingDomains` and
    `RecordSendingDomainAuth` return `remote.ErrControlPlaneOnly` naming the
    method. A route that answered any of them would be a route that returns
    "rows matching X" over the tenant database, which is exactly the capability
    invariant 73 forbids. A `role=send` worker never reaches them —
    `worker.Register` gates `registerScheduled` on the role — and the refusal
    exists for the day that stops being true: a nil-returning stub would report
    "no work due" and the sweep would quietly do nothing.

    **`coreapi.ErrInvalidComplaint` crosses as itself, on 422.** The poller
    SKIPS a permanently-invalid abuse report and RETRIES everything else, and
    `recordInboundComplaint` returns BEFORE `SetInboxCursor` either way — so
    flattening the sentinel into a plain error would retry forever, wedge the
    mailbox's cursor, and stop every inbound signal for it (campaign replies
    and bounces included) on the strength of one unauthenticated inbound
    message. An unrecognised 422 stays a plain error, because reading one as
    "permanently invalid" would let an intermediary's response make a worker
    drop a real abuse report
    (`TestAnInvalidComplaintCrossesAsItselfAndAStray422DoesNot`).

    **`role=control` and `role=all` are untouched**, and that is the constraint
    this was built under rather than a side effect. `role=control` runs the
    cross-tenant sweeps and keeps its pool, its keyring and its in-process
    client; `role=all` is the single-process self-host topology every compose
    file, Helm chart and Terraform config in the repository runs, and it sets
    none of the fleet variables.

81. **What a compromised `role=send` host can STILL reach, stated so the claim
    is not overstated.** This is invariant 69's counterpart for the pool, and
    it is written in the same spirit: a README in this repository once claimed
    the control plane was the only holder of the wrapping key, which was false
    and needed a correction PR.

    The honest claim is: **a compromised `role=send` host cannot read the
    tenant database.** It cannot issue a query, enumerate a table, page a
    result set, or match a pattern, because the only thing it can do is name
    one subject at a time over a seam whose wire shapes cannot express a
    filter. What it CAN still do, while it is running:

    - **Obtain any mailbox's decrypted credential**, by naming the mailbox id
      to the credential broker. Unchanged by this slice, and unchanged for the
      reasons invariant 69 gives: the fleet token is shared, and
      `sequence:advance` stays on the shared queue, so the broker must answer
      for any mailbox the token names. That credential is the ability to send
      and read mail AS a customer — strictly more than a database read of the
      same mailbox's row.
    - **Fetch any JOB it can name an id for**, and a job carries real tenant
      content: a step send's subject and HTML body, a pending reply's text, an
      inbox poll's cursor. It cannot DISCOVER those ids — nothing lists
      enrollments, mailboxes or threads — so in practice it reaches the work it
      is handed, plus anything whose id it learns another way.
    - **Read and write the mail of every mailbox it is handed a job for**,
      which follows from the credential rather than from this seam.
    - **Report false fleet telemetry**: a heartbeat under any worker id, and
      provider-signal counters for any worker. Those tables hold no tenant data
      (invariant 24), and the blast radius is a wrong placement decision, not a
      tenant read.
    - **Suppress an address or stop an enrollment it can name**, through the
      outcome writes. Every one is workspace-pinned, so it must name a real
      (workspace, row) pair; none of them reads anything back that it could not
      already ask for.

    What it can no longer do, which is the whole point: read another tenant's
    contacts, message bodies or reply text that it was not handed a job for;
    enumerate anything; or carry a copy of `secret_ciphertext` off the host.

    The fleet listener remains the boundary that matters. **Restrict it to the
    fleet network**, treat the token as equivalent to every mailbox credential
    in the installation, and rotate it on any suspicion.

## Fleet operator read surface
70. **The scheduled-job ledger never serves its error text.** `scheduled_job_runs`
    has no `workspace_id` and cannot honestly have one — a periodic reconcile runs
    once per deployment, not once per tenant — while `error_message` stores
    `err.Error()` from eight handlers the ledger does not own, any of which may have
    wrapped a mailbox address or a recipient. The table's own migration
    (`20260908123022`) says exactly that; an earlier version of that comment
    claiming the column is "never tenant content" was wrong and was corrected.
    Serving the text on a workspace-scoped endpoint would therefore let one
    workspace's admin read an error produced while sweeping another's mailbox — a
    cross-tenant read arriving through a diagnostics field.

    So the read path does not carry it, twice over: `ListScheduledJobHealth`
    (`queries/jobrun.sql`) does not select the column, and `fleet`'s
    `scheduledJobResponse` has no field for it, so the text cannot reach a
    response even if the query were changed.
    `TestScheduledJobResponseWithholdsTheErrorTextButReportsTheFailure` fails if a
    field named `error_message` or `last_error` ever appears in that payload.

    Withholding the message costs an operator the CAUSE and nothing else: the
    failure itself is fully reported (`last_outcome`, `last_failure_at`,
    `failures_in_window` paired with `runs_in_window`), and the text is in the
    deployment's own logs, where whoever runs the deployment already has it. The
    UI says so where a reader looks for the message, rather than leaving the
    screen appearing to hide something.

## Fleet rotation
71. **A rotation moves a mailbox between workers; it can never move it between
    tenants, and it never writes a decision it did not make.**
    `RotateMailboxWorkerAssignment` is pinned to `(mailbox_id, workspace_id)` and
    additionally to the worker the decision was made FROM. It writes only
    `worker_id`, `band` and `assigned_at` — never `workspace_id` — so invariant 4
    holds and invariant 24's "the tenant does not move" is unchanged by the one
    path that deliberately moves the pin.

    The third predicate is a concurrency guard, not decoration. Between the scan
    and the write the send path may have re-placed the mailbox itself; a blind
    `UPDATE` would then undo a placement made on fresher facts. A mismatch matches
    zero rows, the caller skips the mailbox, and — this is the load-bearing part —
    **nothing is logged**. A decision-log entry saying a mailbox moved from a
    worker it had already left would be a lie in the one table an operator trusts
    to explain where mail is coming from.

    Rotation reuses `fleetscore.Candidate.Eligible` as its definition of health
    rather than introducing a second one. Two disagreeing definitions would let a
    worker be too sick to receive new mailboxes and well enough to keep the ones it
    has — the original bug with the sign flipped. The decision log entry is built
    through `fleetdecision`'s constructors like every other, so a FORCED move (an
    unreachable or blocked incumbent) renders its cause and nothing numeric: no
    score comparison is printed, because none was computed.

    The tick is fleet-scoped infrastructure work, not tenant work, and reads the
    whole fleet on purpose (invariant 24). It is reachable only from the `control`
    role's scheduler — there is no API surface that triggers a rotation, so no
    caller can steer a mailbox onto a worker of their choosing.

## Deferred (documented, not yet built)
- **Conditional branching on a sequence step must gate on HUMAN events only**
  (invariant 63). This is written down BEFORE the feature exists because getting
  it wrong is silent: a scanner's prefetch would fire an "if opened" branch and
  send the contact the wrong follow-up, with nothing in the UI to show that a bot
  rewrote a real sequence. A branch predicate must read the stored verdict —
  `... AND kind = 'open' AND NOT is_machine`, the same filter `CountHumanOpens`
  uses — and must never re-derive its own definition of an open, or the branch
  and the reported open rate will disagree about the same contact. Two further
  cautions: a `human` verdict is "not obviously a machine", so a branch whose
  wrong side is expensive or irreversible should prefer the negative path; and
  because the verdict is computed once at write time, rows recorded before that
  migration are all marked human and a branch reading old history will
  over-fire.
- Datacenter/cloud IP ranges as a refreshed table (AWS/GCP/Azure publish
  machine-readable lists; Apple's MPP relay egress likewise). `botfilter`'s
  compiled-in range list covers only what is knowable from the address itself,
  because invariant 63 forbids a lookup on the tracking hot path — a periodic
  sweep into a table read off that path is the intended shape.
- Re-classification backfill for `tracking_events` rows written before the
  classification column existed (they default to `is_machine = false`). Deliberately
  not a side effect of the migration: re-judging history is a decision an operator
  should make, not something a schema change does silently.
- Cloud KMS as a second `KeyProvider` (KEK) behind the existing seam — today only
  `LocalKeyProvider` (wraps DEKs under `INROAD_MASTER_KEY`) is implemented.
- Eager re-seal/rotation CLI: backfill pre-DEK v1 blobs to v2 and re-encrypt DEKs
  under a rotated KEK (today v1→v2 migration is lazy, on next write).
- Rate limiting / abuse controls on auth and connect endpoints.
- Audit log for sensitive actions (mailbox connect/disconnect, settings changes).
- Server-side single-use nonce store for the MAILBOX-CONNECT OAuth `state` (see
  invariant 10). The LOGIN flow has one (invariant 50); mailbox connect still relies
  on the 10-minute TTL alone, where the residual risk is an attacker binding their
  own mailbox into a victim's workspace rather than account access.
- Rate limiting + an audit log on reply-driven suppression/stop. Reply-driven
  actions (`MarkReplied`/`MarkUnsubscribed`) are workspace-bounded (invariant 21)
  but are spoofable WITHIN a workspace: anyone who knows a target contact email
  and a real `Message-ID` of a send could forge an inbound reply that suppresses
  the contact or stops its enrollment. This is bounded (no cross-tenant effect, no
  data read) but unthrottled and unlogged today; a rate limit + audit trail on
  reply-driven state changes is the intended hardening.
- Rate limiting + an audit log on warm-up engagement writes. Like the reply path
  (above), the receipt/engage enqueue is workspace-bounded but spoofable WITHIN a
  workspace by anyone holding the `INROAD_WARMUP_SECRET`. Bounded and low-value, but
  it should be included when the reply-driven abuse-controls work lands.
- Graph/M365 recipient-side engagement (rescue/mark-read). Warm-up SENDS work on all
  three transports; the `mail.Engager` seam implements IMAP + Gmail, and M365 returns
  `ErrEngageUnsupported` (a logged skip) until the Graph modify path is added.

## Checklist for a security-sensitive change
- [ ] New stored credential? → sealed via a workspace Sealer from
      `Keyring.SealerFor(ctx, ws)` (per-workspace DEK, AAD-bound), absent from
      responses/logs.
- [ ] New outbound dial to a user-supplied host? → routed through the SSRF guard.
- [ ] New tenant-scoped query? → filtered by `workspace_id` from the JWT.
- [ ] New task payload? → ids + `workspace_id` only; anything else lives in a row
      the handler re-reads (invariant 64). A payload is served verbatim to
      `campaigns:read` when the task finally fails.
- [ ] New secret/config? → env-loaded, fail-closed in compose, in `.env.example`.
- [ ] New OAuth/state-authenticated flow? → `state` HMAC-signed + TTL + a
      `oauthstate.Purpose` of its own (so it cannot be replayed at another flow's
      callback); tenant derived server-side, not from a request param; token refresh
      stays in the control plane. If the flow grants a SESSION rather than binding a
      resource, it also needs a single-use nonce store and PKCE (invariant 50).
- [ ] New federated identity provider? → keyed on the provider's immutable subject,
      never email; the provider's email-verified claim gates both signup and
      linking; no credential (invite token, session token) placed in `state` or a
      redirect URL.
