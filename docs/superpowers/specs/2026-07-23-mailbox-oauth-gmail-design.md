# Mailbox OAuth — Framework + Gmail Provider (Design)

**Date:** 2026-07-23
**Branch:** `feature/mailbox-oauth-gmail` (based on `origin/main` @ analytics-merged, migration head `000011`)
**Status:** Approved (brainstorm) → spec

## 1. Goal

Let a workspace connect a **Gmail** mailbox by OAuth instead of an app password,
and send / read replies through the **Gmail API** rather than SMTP/IMAP. Build the
provider abstraction so Microsoft 365 (Graph) slots in next phase without another
refactor. This is the difference between "paste an app password" (fragile, blocked
by default on Workspace accounts) and "Sign in with Google" (what Instantly/Smartlead
ship).

Non-goals this phase: M365/Graph provider (framework only), incremental Gmail push
(Pub/Sub watch) — we poll like the IMAP path, and any UI beyond the connect button +
callback landing.

## 2. Where it plugs in (seams that already exist)

| Concern | Existing seam | Change |
|---|---|---|
| Mailbox row | `mailboxes.provider` (`'smtp'` today), `secret_ciphertext` | store sealed OAuth token JSON when `provider='gmail'`; add `inbox_cursor TEXT` (opaque Gmail `historyId`) |
| Connect (control plane) | `mailbox.Service.ConnectSMTP` | add `StartGoogleOAuth` / `CompleteGoogleOAuth` |
| Send (execution plane) | `sender.Sender.Send(cfg, msg)`, called by `sender.Handler` + `sequence.AdvanceHandler` | widen to `Send(ctx, OutboundJob, Message)`; dispatch SMTP vs Gmail on `OutboundJob.Provider` |
| Inbox (execution plane) | `mail.InboxReader` (IMAP UID), `inbox` worker | add Gmail reader; dispatch on `provider`; Gmail uses `inbox_cursor` |
| Token refresh | `coreapi` inprocess (holds pool + sealer) | refresh+reseal+persist at job-build time; return fresh access token on the job |
| Job payloads | `coreapi.StepSendJob` / `SendJob` / `InboxPollJob` | add `Provider string` + `AccessToken []byte` (zeroized like `SMTPPassword`); Gmail poll job carries opaque `Cursor string` |
| Config | `config.Config` | `INROAD_GOOGLE_CLIENT_ID/SECRET/REDIRECT_URL` |
| Routing | public group (`/u`, `/t`) + protected group | `start` protected; `callback` **public**, state-authenticated |

## 3. OAuth authorization-code flow

```
Browser            API (control plane)                     Google
  │  POST /api/v1/mailboxes/oauth/google/start  (JWT)        │
  │─────────────────────────────────────────────▶           │
  │            { auth_url } (signed state)                    │
  │◀─────────────────────────────────────────────           │
  │  redirect to auth_url ──────────────────────────────────▶│  consent
  │  GET  {PUBLIC_URL}/oauth/google/callback?code&state ◀─────│
  │──────────▶  verify state (HMAC+TTL+workspace)             │
  │            exchange code → token                          │──▶ token endpoint
  │            GET userinfo email (profile)                   │──▶ gmail profile
  │            seal token, INSERT mailbox(provider=gmail)     │
  │  302 → {APP_BASE_URL}/mailboxes?connected=<email>         │
  │◀──────────                                                │
```

- **`start` (protected, `RequireVerified`):** `POST /api/v1/mailboxes/oauth/google/start`.
  Reads `workspace_id` from the JWT, builds the Google consent URL with
  `access_type=offline` + `prompt=consent` (forces a refresh token every time),
  scopes below, and a **signed `state`**. Returns `{ "auth_url": "..." }` (SPA does the
  redirect). If Google creds are unconfigured → `501 gmail oauth not configured`.
- **`callback` (public, mounted at `/oauth` like `/u` and `/t`):**
  `GET /oauth/google/callback?code&state`. It is a top-level browser navigation from
  Google, so it **cannot rely on the JWT cookie** (SameSite) — it authenticates from the
  signed `state`. Verifies HMAC + expiry + extracts `workspace_id`, exchanges `code`,
  fetches the mailbox's own email address, seals the token, creates the mailbox, then
  `302`s to `APP_BASE_URL/mailboxes?connected=<email>` (or `?oauth_error=<reason>`).
- **Scopes:** `https://www.googleapis.com/auth/gmail.send`,
  `https://www.googleapis.com/auth/gmail.readonly`, `openid`, `email`.
  (`gmail.readonly` for reply/bounce polling; `email`/`openid` to learn the address.)
- **Redirect URI:** `INROAD_GOOGLE_REDIRECT_URL`, default `PUBLIC_URL + /oauth/google/callback`.
  Must be registered verbatim in the Google Cloud console.

### 3.1 Signed state (new `internal/platform/oauthstate`)

Stateless, HMAC-signed (SHA-256, `JWTSecret`), same construction family as
`unsub.MakeToken` / the tracking tokens:

```
Sign(secret []byte, workspaceID string, ttl time.Duration) string
Verify(secret []byte, token string) (workspaceID string, err error)   // checks sig + expiry
```

Payload: `workspace_id | expiry-unix | nonce`, base64url, appended HMAC. **TTL 10 min.**
There is no server-side nonce store, so the HMAC + short TTL *are* the CSRF/replay
protection: only the server can mint a valid state, and it's bound to one workspace for
10 minutes. Residual risk: within that window a leaked state URL lets an attacker bind
**their own** Google mailbox into the victim's workspace — low value, bounded, documented.
(A server-side single-use nonce store is the phase-2 hardening if we add stateful sessions.)

## 4. Token lifecycle (the crux)

`secret_ciphertext` stores, sealed, the JSON of the `oauth2.Token`
(`access_token`, `refresh_token`, `token_type`, `expiry`) for `provider='gmail'`; it
still stores the raw password for `provider='smtp'`. The `provider` column is the
discriminator.

**Refresh happens in `coreapi` (inprocess), never the worker.** When building a
send/poll job for a gmail mailbox:

1. Unseal → `oauth2.Token`.
2. `ts := googleCfg.TokenSource(ctx, tok)` (`x/oauth2` `ReuseTokenSource` auto-refreshes
   when expired, using the refresh token).
3. `fresh, _ := ts.Token()`. If `fresh.AccessToken != tok.AccessToken` (or refresh token
   rotated), re-seal and `UpdateMailboxSecret` (new query) — so we don't refresh every hour
   and we capture Google's rotated refresh tokens.
4. Put `fresh.AccessToken` (as `[]byte`) on the job with `Provider="gmail"`; leave SMTP
   fields empty.

This keeps *all* secret material + persistence in the control plane; the worker receives
a short-lived bearer token it uses for one API call, then zeroizes it (same discipline as
`SMTPPassword`). `inprocess.New` gains a `mail.GoogleOAuth` config (client id/secret/scopes);
if unset, gmail jobs fail cleanly with a logged, non-retryable error.

## 5. Send via Gmail API

New `internal/platform/mail/gmail.go`:

```go
type GmailSender struct{ /* http.Client from oauth2 static token per call */ }
func (g *GmailSender) Send(ctx context.Context, accessToken string, msg Message) (messageID string, err error)
```

- Reuse `buildMessage(msg)` (already unit-tested: from/to/subject/bodies/threading/
  List-Unsubscribe/**Message-ID**) → serialize to RFC822 → base64url → Gmail
  `users.messages.send` (`google.golang.org/api/gmail/v1` with a static-token client).
- **Return our own `Message-ID` header** (from the built message), *not* Gmail's resource
  id — threading (`In-Reply-To`/`References`) and reply matching (`FindSendByMessageID`)
  already key on the header we set, and Gmail preserves supplied headers. Consistent with
  the SMTP path; zero change downstream.
- No SSRF vetting: the host is a fixed Google endpoint, not user input.

**Dispatch seam.** Widen the worker's `Sender` interface to transport-agnostic:

```go
// internal/platform/mail
type OutboundJob struct {
    Provider string // "smtp" | "gmail"
    Host string; Port int; Username, Password string; UseTLS bool // smtp
    AccessToken string                                             // api
}
type MultiSender struct{ smtp *NetSender; gmail *GmailSender }
func (m *MultiSender) Send(ctx context.Context, tj OutboundJob, msg Message) (messageID string, err error)
```

`sender.Handler` and `sequence.AdvanceHandler` build one `OutboundJob` from the coreapi
job and call `Send` — the SMTP/Gmail branch lives once, in `MultiSender`. Test fakes
implement the same one-method interface.

## 6. Reply/bounce via Gmail API

New `internal/platform/mail/gmailinbox.go` implementing a provider-parallel reader. Gmail
has no IMAP UID/UIDVALIDITY — it has a monotonic **`historyId`** (opaque string):

```go
func (g *GmailReader) Fetch(ctx context.Context, accessToken, sinceHistoryID string, maxN int)
    (msgs []InboundMessage, newCursor string, err error)
```

- First poll (`sinceHistoryID==""`): `users.messages.list` bounded by `maxN` + a recent
  `q` (e.g. `newer_than:2d`), establish baseline `historyId` — do **not** crawl all history.
- Subsequent: `users.history.list?startHistoryId=<cursor>&historyTypes=messageAdded`,
  paginate up to `maxN`, then `users.messages.get?format=RAW` per new message.
- RAW → parse the RFC822 with the **existing** `ParseDSN` (bounce) + reply matcher —
  they operate on raw MIME and are transport-agnostic, so they're reused unchanged.
- Cursor persisted in `inbox_cursor` (migration 000012); the IMAP path keeps using
  `inbox_last_seen_uid`/`inbox_uid_validity` untouched. The inbox worker dispatches reader
  + cursor column on `provider`.

`coreapi.InboxPollJob` gains `Provider string`, `AccessToken []byte`, `Cursor string`;
`SetInboxCursor` gets a gmail variant (persist the opaque string) or a second method
`SetInboxCursorString`.

## 7. Data model — migration 000012

```sql
-- up
ALTER TABLE mailboxes ADD COLUMN inbox_cursor TEXT NOT NULL DEFAULT '';
-- down
ALTER TABLE mailboxes DROP COLUMN inbox_cursor;
```

Reversible (`migrate-up && migrate-down && migrate-up`). No collision: analytics took
`000011`, this is `000012`. SMTP columns become nullable-in-practice for gmail rows (they
carry `''`/`0`); `secret_ciphertext` reused for the sealed token. New query
`UpdateMailboxSecret(id, workspace_id, secret_ciphertext)` for token-refresh persistence.

## 8. Config

```
INROAD_GOOGLE_CLIENT_ID       (default "")
INROAD_GOOGLE_CLIENT_SECRET   (default "")
INROAD_GOOGLE_REDIRECT_URL    (default PUBLIC_URL + "/oauth/google/callback")
```

Empty client id/secret ⇒ Gmail OAuth disabled: `start` → `501`, and any pre-existing gmail
job fails cleanly (logged, not retried into a hot loop). `.env.example` documents all three.

## 9. Security invariants (docs/security.md must hold)

- OAuth tokens are secrets: sealed at rest (`crypto.Sealer`), never logged, never in an
  HTTP response (`mailboxResponse` already omits the ciphertext). Access token is `[]byte`
  on jobs, zeroized after the send/poll like `SMTPPassword`.
- Every mailbox row created by the callback pins `workspace_id` from the **verified signed
  state**, not from a request body — no cross-tenant write.
- No SSRF surface added: Gmail API + Google token/userinfo endpoints are fixed hosts, not
  user-controlled. State construction reflects no unvalidated input into the redirect.
- Refresh-token rotation captured (re-seal on change) so a rotated token doesn't silently
  break sending.

## 10. Dependencies

- `golang.org/x/oauth2` + `golang.org/x/oauth2/google` — flow + auto-refreshing `TokenSource`.
- `google.golang.org/api/gmail/v1` + `google.golang.org/api/option` — send, history.list,
  messages.get(RAW). Official client handles pagination/retries; the API host is fixed so
  it adds no SSRF surface.

## 11. Testing strategy

- **Unit (no network):** `oauthstate` sign/verify (tamper, expiry, wrong-workspace);
  `GmailSender` message assembly (RAW round-trips through `buildMessage`, Message-ID
  preserved) with a stubbed HTTP transport; `MultiSender` dispatch picks the right
  transport per `Provider`; coreapi refresh path re-seals only when the token changed
  (fake `TokenSource`); Gmail `historyId` cursor advance with a stubbed history response.
- **Integration (Postgres, tagged):** callback creates a `provider='gmail'` mailbox with a
  sealed token (exchange stubbed); migration 000012 reversibility.
- **Backward-compat:** existing SMTP send/sequence/inbox tests stay green — `provider`
  defaults to `smtp`, `OutboundJob` built from the same fields.
- Live Google OAuth is manual/QA (real client id/secret), out of CI.

## 12. Delivery order (independently testable)

1. Config + `oauthstate` package (+ tests).
2. Migration 000012 + `UpdateMailboxSecret` query + sqlc.
3. Control-plane connect: `mailbox` OAuth service methods + `start`/`callback` handlers + routing.
4. `OutboundJob`/`MultiSender` + `GmailSender`; widen `Sender` seam; wire `coreapi` token
   refresh + `Provider`/`AccessToken` on `SendJob`/`StepSendJob`; both send handlers.
5. Gmail inbox reader + `InboxPollJob` provider fields + inbox worker dispatch + cursor.
6. `.env.example` + `docs/self-hosting.md` (Google Cloud setup) + `docs/security.md` note.

Send (1–4) is usable without inbox (5); reply/bounce for Gmail (5) is the second increment.
```
