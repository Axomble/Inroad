# Mailbox OAuth — Microsoft 365 / Graph Provider (Design)

**Date:** 2026-07-24
**Branch:** `feature/mailbox-oauth-m365` (off `main`, migration head `000012`)
**Status:** Approved architecture (reuses the Gmail phase design) → spec

## 1. Goal

Connect a **Microsoft 365 / Outlook** mailbox by OAuth and send / read replies
through the **Microsoft Graph API**, as a second provider behind the abstraction
built in the Gmail phase. This is the third mailbox-connect path (SMTP → Gmail →
M365) and the last major provider for parity with Instantly/Smartlead.

This phase is a deliberate **mirror of the Gmail phase**
(`docs/superpowers/specs/2026-07-23-mailbox-oauth-gmail-design.md`); only the
Graph-specific mechanics differ. Where behavior isn't called out below, it is
identical to Gmail (sealed token in `secret_ciphertext`, control-plane-only
refresh, `[]byte` access token zeroized in the worker, signed 10-min `state`,
workspace from verified state, no new SSRF surface).

Non-goals: change/generalize the Gmail path beyond small shared-seam edits; MSAL
library; Graph change-notifications (webhooks) — we poll like Gmail/IMAP.

## 2. Reuse map (what's shared vs new)

| Concern | Reuse as-is | New for M365 |
|---|---|---|
| Signed state | `internal/platform/oauthstate` (provider-agnostic) | — |
| Token codec | `mail.MarshalToken`/`UnmarshalToken` | — |
| Token storage | `secret_ciphertext`, `provider='m365'`, opaque `inbox_cursor` | — (no migration) |
| OAuth config | pattern of `mail.GoogleOAuth` | `mail.MicrosoftOAuth` (Azure AD endpoint, Graph scopes) |
| Connect service | pattern of `CompleteGoogleOAuth`/`GoogleAuthCodeURL` | `CompleteMicrosoftOAuth`/`MicrosoftAuthCodeURL`, `microsoftExchanger` |
| Send | `buildMessage` (MIME) | `mail.GraphSender` (sendMail base64 MIME) |
| Inbox | `ParseDSN` + reply matcher + `parseInbound` | `mail.GraphReader` (delta query) |
| Dispatch | `MultiSender` / worker poll handler | add `"m365"` case + `GraphSender`/`GraphReader` |
| Token refresh | control-plane pattern (`gmailAccessToken`) | generalize to accept the provider's `*oauth2.Config` |
| Routes | public `/oauth` mount + protected start pattern | `/oauth/microsoft/callback`, `POST .../oauth/microsoft/start` |
| Config | `config.Config` load pattern | `INROAD_MS_*` |

## 3. Auth (Microsoft identity platform)

`mail.MicrosoftOAuth{ClientID, ClientSecret, RedirectURL, Tenant}`; `Enabled()` =
client id+secret set; `Config()` returns an `*oauth2.Config` with
`Endpoint: microsoft.AzureADEndpoint(tenant)` (`golang.org/x/oauth2/microsoft`),
`Tenant` default `"common"`. Scopes:
`https://graph.microsoft.com/Mail.Send`, `.../Mail.Read`,
`https://graph.microsoft.com/User.Read`, `offline_access`, `openid`, `email`.
`offline_access` is required to receive a refresh token (Microsoft's analogue of
Google's `access_type=offline`). The consent URL adds `prompt=consent`.

**Learn the address (exchanger):** after code exchange, `GET
https://graph.microsoft.com/v1.0/me` with the bearer token; use `mail`, falling
back to `userPrincipalName`. Fixed Graph host → no SSRF; bounded `http.Client`
timeout (mirrors `googleExchanger`).

## 4. Routes

- Protected: `POST /api/v1/mailboxes/oauth/microsoft/start` → `{auth_url}` bound to
  a signed 10-min state; `501` if `MicrosoftOAuth` disabled.
- Public: `GET /oauth/microsoft/callback?code&state` on the existing `/oauth`
  mount — verifies state (HMAC, workspace), exchanges code, seals token, creates
  a `provider='m365'` mailbox, then 302s to `${APP_BASE_URL}/mailboxes?connected=<email>`
  or `?oauth_error=<reason>`. Same reason codes as Gmail. The provider is known
  from the route path, so `state` still carries only the workspace.

`ErrOAuthDisabled`'s message ("gmail oauth not configured") is generalized to a
provider-neutral string (or a sibling `ErrMSOAuthDisabled`) so the M365 501 path
reads correctly.

## 5. Send via Graph (`internal/platform/mail/graph.go`)

```go
type GraphSender struct{ /* wire seam like GmailSender.transmitFn */ }
func (g *GraphSender) Send(ctx context.Context, accessToken string, msg Message) (messageID string, err error)
```

- `buildMessage(msg)` → serialize to RFC822 → base64 → `POST
  https://graph.microsoft.com/v1.0/me/sendMail` with `Content-Type: text/plain`
  and the base64 MIME as the body (Graph's "send MIME" mode). A `202 Accepted` is
  success.
- Return our own `Message-ID` header (Graph preserves supplied MIME headers), so
  threading + reply matching are identical to the SMTP/Gmail paths. No SSRF (fixed
  host). Bearer via `oauth2.StaticTokenSource` per call.

**Dispatch:** `mail.OutboundJob.Provider` gains `"m365"`; it reuses the existing
`AccessToken` field. `MultiSender` gets a `graph *GraphSender` and a `case "m365"`.

## 6. Reply/bounce via Graph delta (`internal/platform/mail/graphinbox.go`)

```go
func (g *GraphReader) Fetch(ctx context.Context, accessToken, sinceCursor string, maxN int)
    (msgs []InboundMessage, newCursor string, err error)
```

Graph tracks inbox position by an opaque **delta cursor** (a `deltaLink` URL when a
round is complete, or a `nextLink`/`skipToken` URL mid-round). Both are stored
verbatim in `inbox_cursor`.

- **First poll** (`sinceCursor==""`): `GET
  /me/mailFolders('inbox')/messages/delta?$deltatoken=latest` returns an empty set
  with a baseline `deltaLink` — establishes the cursor WITHOUT crawling the backlog
  (the Graph analogue of Gmail's `getProfile` baseline). Process nothing, matching
  the IMAP/Gmail first-poll semantics.
- **Incremental**: `GET <stored cursor URL>`. Collect changed message ids up to
  `maxN` (consume-and-checkpoint like the Gmail fix): if a page's `@odata.nextLink`
  is present and we stop at `maxN`, store that `nextLink` as the cursor so the next
  poll resumes; when the round drains, store the final `@odata.deltaLink`. Never
  advance past unconsumed messages.
- **Per message**: `GET /me/messages/{id}/$value` → raw MIME → `parseInbound` →
  shared `ParseDSN` + reply matcher (unchanged). Bounded-concurrent gets
  (errgroup, limit ~8) like `GmailReader`.
- **Expired/invalid cursor** (`410 Gone`, or Graph "resync required"): re-baseline
  via `$deltatoken=latest`, log a warning, return zero messages — mirrors the Gmail
  404 re-baseline so a lapsed cursor can't wedge the mailbox.
- Ignore `messages.delta` entries that are deletions/moves (only `messageAdded`-equivalent
  new items matter for reply/bounce).

`coreapi.InboxPollJob` already carries `Provider`/`AccessToken`/`Cursor`; the poll
handler gains an `"m365"` branch using `GraphReader` + `SetInboxCursorString`
(already provider-neutral). Access token zeroized after the pass.

## 7. Token refresh (control plane)

Generalize the existing `gmailAccessToken` (inprocess) to a provider-neutral
`oauthAccessToken(ctx, mailboxID, workspaceID, sealed, cfg *oauth2.Config)` that
unseals → `cfg.TokenSource(ctx, tok)` → refresh → reseal+persist on change →
return the access token. The job builders pick `cfg` by `provider`:
`googleOAuth.Config()` for gmail, `msOAuth.Config()` for m365. `inprocess.New`
gains a `mail.MicrosoftOAuth` param alongside the existing `googleOAuth`.

## 8. Config

```
INROAD_MS_CLIENT_ID       (default "")
INROAD_MS_CLIENT_SECRET   (default "")
INROAD_MS_REDIRECT_URL    (default PUBLIC_URL + "/oauth/microsoft/callback")
INROAD_MS_TENANT          (default "common")
```

Empty client id/secret ⇒ M365 disabled (start → 501; any stray m365 job fails
cleanly). `.env.example` documents all four.

## 9. Dependencies

- `golang.org/x/oauth2/microsoft` — Azure AD endpoint for the existing `x/oauth2`
  flow + auto-refreshing `TokenSource`. No new HTTP client lib; Graph send/read
  are plain bounded `http.Client` calls to fixed Graph hosts (no MSAL, no
  `google.golang.org/api`-equivalent needed).

## 10. Security invariants (unchanged from Gmail; docs/security.md)

Sealed tokens, never logged/returned; access token `[]byte` zeroized in the
worker; refresh+reseal+persist only in coreapi; workspace from verified state;
fixed Graph hosts (no SSRF); refresh-token rotation captured. Add M365 to the
security note as invariant coverage, not a new invariant.

## 11. Testing strategy

- **Unit (network-free via wire seams):** `MicrosoftOAuth.Config()`/`Enabled()`;
  `GraphSender` MIME assembly + Message-ID preserved (stub transmit); `MultiSender`
  routes `"m365"` → GraphSender; `GraphReader` first-poll baseline (`$deltatoken=latest`,
  zero messages) + incremental consume-and-checkpoint + `410` re-baseline (pure
  cursor logic directly tested, like Gmail's `collectHistory`); `microsoftExchanger`
  reads `mail`/`userPrincipalName` (stub Graph `/me`); coreapi `oauthAccessToken`
  resel-on-change with a fake TokenSource.
- **Integration (Postgres):** callback creates a `provider='m365'` mailbox with a
  sealed token (exchange stubbed).
- **Backward-compat:** all SMTP + Gmail send/sequence/inbox tests stay green;
  `MultiSender`/poll dispatch default + `"gmail"` branches unchanged.
- Live Graph OAuth is manual/QA (real Azure app registration), out of CI.

## 12. Delivery order (independently testable, mirrors Gmail)

1. Config (`INROAD_MS_*`) + `mail.MicrosoftOAuth` (config + codec reuse) + `.env.example`.
2. Control-plane connect: `microsoftExchanger`, `CompleteMicrosoftOAuth`,
   `MicrosoftAuthCodeURL`, `/oauth/microsoft/{start,callback}` routes, `cmd/inroad` wiring;
   generalize `ErrOAuthDisabled` message.
3. Send: `GraphSender` + `OutboundJob"m365"` + `MultiSender` case; generalize coreapi
   token refresh to take a `*oauth2.Config`; job builders pick cfg by provider;
   `inprocess.New`/`cmd/worker` wiring.
4. Inbox: `GraphReader` (delta) + poll-handler `"m365"` branch.
5. Frontend: add "Microsoft 365" to the connect dropdown (Microsoft mark) → `microsoft/start`;
   callback banner already generic. Provider badge shows "Microsoft 365".
6. Docs: `.env.example`, `docs/self-hosting.md` (Azure app registration + redirect URI + scopes),
   `docs/security.md` note.

Send (1–3) is usable without inbox (4); frontend (5) makes it reachable; both mirror
the Gmail phase's increments.
