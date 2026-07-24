# Microsoft 365 / Graph Provider Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. Steps use checkbox (`- [ ]`) syntax.

**Goal:** Connect an M365 mailbox by OAuth and send / read replies via Microsoft Graph, as a second provider behind the existing abstraction.

**Architecture:** Mirror the Gmail phase (now on `main`) for a Graph provider. Reuse `oauthstate`, the sealed-token codec, control-plane refresh, `MultiSender`/poll dispatch, and `ParseDSN`/reply matcher unchanged; add Graph-specific auth (`x/oauth2` + Azure AD endpoint), send (`sendMail` base64 MIME), and inbox (delta query).

**Tech Stack:** Go 1.25 · `golang.org/x/oauth2` + `/microsoft` · Graph REST (bounded `http.Client`) · pgx/sqlc · chi.

## Global Constraints

- Toolchain: prefix EVERY Go/sqlc command with `export PATH="$PATH:/c/Program Files/Go/bin:$HOME/go/bin"` in the SAME command. Do NOT `set -a && . ./.env` before tests (pollutes config-default tests; the integration harness defaults `INROAD_DATABASE_URL` to `localhost:5433`).
- Go files lowercase; identifiers `MixedCaps`; snake_case only at JSON/DB/env boundaries. Frontend kebab-case.
- Every tenant query `workspace_id`-pinned; secrets never logged / never in responses; decrypted secrets `[]byte`, zeroized after use.
- Worker reaches data only via `coreapi` (zero `db` import); refresh/reseal/persist happen ONLY in coreapi.
- `web/src/store/api.ts` generated — never hand-edit (extend via `injectEndpoints`). Apply React best practices on any frontend work (lazy routes already on via `autoCodeSplitting`; typed RTK errors via `lib/rtk-error`; one shared banner surface).
- Conventional commits; do NOT commit (coordinator commits per task). Verify before "done": `go build ./...`, `go vet ./...`, `gofmt -l internal cmd` (only the 4 known pre-existing files may appear), `go test ./...`.
- Provider values: `smtp` | `gmail` | `m365`. OAuth route segment: `microsoft`. NO new migration (opaque `inbox_cursor` + `provider` already cover m365).

**Reference implementations to mirror (read them first each task):** `internal/platform/mail/{oauth,gmail,gmailinbox,multisender}.go`, `internal/app/mailbox/oauth.go`, `internal/coreapi/inprocess/oauthtoken.go`, `internal/worker/inbox/poll.go`, `cmd/{inroad,worker}/main.go`, `internal/platform/config/config.go`, `web/src/features/mailboxes/*`.

---

### Task 1: Config + `mail.MicrosoftOAuth`

**Files:** Modify `internal/platform/config/config.go`; Create `internal/platform/mail/msoauth.go` + `msoauth_test.go`; Modify `.env.example`.

**Interfaces (Produces):**
- `config.Config.MSClientID/MSClientSecret/MSRedirectURL/MSTenant string`.
- `mail.MicrosoftOAuth{ClientID, ClientSecret, RedirectURL, Tenant string}` with `Enabled() bool` and `Config() *oauth2.Config` (Azure AD endpoint via `microsoft.AzureADEndpoint(tenant)`, tenant default `"common"`, scopes: `https://graph.microsoft.com/Mail.Send`, `.../Mail.Read`, `.../User.Read`, `offline_access`, `openid`, `email`). Token marshal/unmarshal reuse `mail.MarshalToken`/`UnmarshalToken` (already exist — do not duplicate).

- [ ] **Step 1:** `go get golang.org/x/oauth2/microsoft` (part of `golang.org/x/oauth2`, already a dep — confirm it resolves; `go mod tidy`).
- [ ] **Step 2:** Write `msoauth_test.go` mirroring `oauth_test.go`: `Enabled()` false on zero value, true when id+secret set; `Config()` has the 6 scopes and a non-nil Endpoint; tenant default `"common"` when blank.
- [ ] **Step 3:** Run it → FAIL.
- [ ] **Step 4:** Write `msoauth.go` (mirror `oauth.go`'s `GoogleOAuth`, substitute Azure AD endpoint + Graph scopes; `Config()` uses `microsoft.AzureADEndpoint(tenantOr("common"))`).
- [ ] **Step 5:** Add config fields + loads (`INROAD_MS_CLIENT_ID/SECRET`, `INROAD_MS_REDIRECT_URL` default `cfg.PublicURL+"/oauth/microsoft/callback"`, `INROAD_MS_TENANT` default `"common"`) after the Google block. Document the 4 vars in `.env.example`.
- [ ] **Step 6:** `go test ./internal/platform/mail/ -run 'Microsoft' -v && go build ./...` → PASS.
- [ ] **Step 7:** Commit `feat(mail): Microsoft OAuth config (Azure AD endpoint + Graph scopes)`.

---

### Task 2: Control-plane M365 connect (start + callback)

**Files:** Modify `internal/app/mailbox/oauth.go` (+ `service.go`, `handler.go`, `routes.go`), `cmd/inroad/main.go`, `internal/app/mailbox/oauth_test.go`.

**Interfaces (Produces):**
- `(*Service).MicrosoftAuthCodeURL(state string) (string, error)` (mirror `GoogleAuthCodeURL`; `offline_access` is in scopes so a refresh token is issued; add `prompt=consent`).
- `(*Service).CompleteMicrosoftOAuth(ctx, code string, workspaceID uuid.UUID) (MailboxSafe, error)` — Provider `"m365"`.
- `microsoftExchanger` (production `TokenExchanger`) — code exchange via bounded client, then `GET https://graph.microsoft.com/v1.0/me`, use `mail` else `userPrincipalName`.
- Handlers `startMicrosoftOAuth` / `microsoftCallback`; public route `GET /oauth/microsoft/callback` on `CallbackRoutes()`; protected `POST /oauth/microsoft/start`.
- Generalize `ErrOAuthDisabled` message to provider-neutral (or add `ErrMSOAuthDisabled`); update the Gmail 501 copy if you neutralize it.

**Design notes:** `Service` gains `msOAuth mail.MicrosoftOAuth` + `msExchanger TokenExchanger`; `NewService` extends (update ALL call sites: `cmd/inroad/main.go` + tests). Reuse `oauthstate` unchanged (state carries only workspace; provider known from the route path). The callback is byte-for-byte the Gmail callback with "microsoft"/"m365" substituted and `CompleteMicrosoftOAuth`.

- [ ] Steps mirror Gmail Task 4 (see the Gmail plan / the merged code). TDD: service test `CompleteMicrosoftOAuth` via a fake exchanger creates a `provider='m365'` mailbox with a sealed token + workspace from arg; httptest callback test asserts workspace-from-state + reason mapping + 302 targets. Reuse the existing `fakeStore`/`fakeExchanger`/`newCallbackHarness` helpers.
- [ ] Verify `go test ./internal/app/mailbox/... && go build ./... && go vet ./...`. Commit `feat(mailbox): M365 OAuth connect (start + public callback)`.

---

### Task 3: Send via Graph + provider dispatch + generalized token refresh

**Files:** Create `internal/platform/mail/graph.go` + `graph_test.go`; Modify `internal/platform/mail/multisender.go`, `internal/coreapi/inprocess/oauthtoken.go`, `sendjob.go`, `stepsendjob.go`, `inprocess.go`, `cmd/worker/main.go`, `internal/worker/handlers.go`.

**Interfaces (Produces):**
- `mail.NewGraphSender() *GraphSender`; `(*GraphSender).Send(ctx, accessToken string, msg Message) (messageID string, err error)` — `buildMessage`→MIME→base64→`POST https://graph.microsoft.com/v1.0/me/sendMail` (`Content-Type: text/plain`, body=base64 MIME); `202` = success; return `m.GetMessageID()`. Wire seam (`transmitFn`) for network-free tests, like `GmailSender`.
- `MultiSender` gains `graph *GraphSender` + `case "m365"`; `NewMultiSender(smtp, gmail, graph)`; `OutboundJob.Provider` doc adds `"m365"` (reuses `AccessToken`).
- coreapi: rename/generalize `gmailAccessToken` → `oauthAccessToken(ctx, mailboxID, workspaceID uuid.UUID, sealed string, cfg *oauth2.Config) (string, error)`; `inprocess.client` gains `msOAuth mail.MicrosoftOAuth`; `New(...)` extends. `GetSendJob`/`GetStepSendJob` pick cfg by `provider` (`gmail`→googleOAuth.Config(), `m365`→msOAuth.Config()).

**Design notes:** the SMTP + gmail send behavior must stay byte-identical. Update fakes/wiring (`Register`, `cmd/worker`) for the extra sender + the `New` arg. `go mod tidy`.

- [ ] TDD: `graph_test.go` asserts MIME assembly + Message-ID preserved via a stubbed transmit; `multisender_test.go` gains an `m365` dispatch case. Verify `make sqlc` (no-op expected), build, vet, `go test ./...` (SMTP+gmail green). Commit `feat(send): Graph API transport + m365 dispatch; generalize coreapi token refresh`.

---

### Task 4: Reply/bounce via Graph delta

**Files:** Create `internal/platform/mail/graphinbox.go` + `graphinbox_test.go`; Modify `internal/worker/inbox/poll.go` (+ `register.go` if needed).

**Interfaces (Produces):**
- `mail.NewGraphReader() *GraphReader`; `(*GraphReader).Fetch(ctx, accessToken, sinceCursor string, maxN int) (msgs []InboundMessage, newCursor string, err error)`.
  - First poll (`sinceCursor==""`): `GET /me/mailFolders('inbox')/messages/delta?$deltatoken=latest` → baseline `deltaLink`, zero messages.
  - Incremental: `GET <sinceCursor>` (opaque delta/next link); collect new message ids to `maxN`; consume-and-checkpoint (a pure helper like Gmail's `collectHistory`: stop at a page boundary, cursor = `@odata.nextLink` if truncated/more, else `@odata.deltaLink`); per id `GET /me/messages/{id}/$value` (raw MIME) via bounded errgroup(8) → `parseInbound`. Ignore deletes/moves.
  - `410 Gone`/resync error → re-baseline via `$deltatoken=latest`, `slog.Warn`, zero messages.
- Poll handler: `job.Provider=="m365"` → `GraphReader.Fetch(ctx, accessToken, job.Cursor, fetchBatchSize)` then `core.SetInboxCursorString`; zeroize `job.AccessToken`. Add a `GraphFetcher` interface param (mirror `GmailFetcher`); construct `mail.NewGraphReader()` in `inbox.Register`.

- [ ] TDD: `graphinbox_test.go` — first-poll baseline (zero msgs + deltaToken=latest link), incremental parses a reply + a DSN bounce + advances cursor, truncation checkpoints the nextLink, `410` re-baselines; the pure cursor helper directly tested. Poll test: an m365 job routes to the Graph fetcher + shared classification (bounce→MarkBounced). Verify build/vet/test. Commit `feat(inbox): Graph delta reply/bounce polling for m365`.

---

### Task 5: Frontend — Microsoft 365 in the connect dropdown

**Files:** Modify `web/src/features/mailboxes/mailboxes-page.tsx`, `api.ts`; Create `web/src/features/mailboxes/microsoft-icon.tsx`; extend tests.

**Interfaces:** `useStartMicrosoftOauthMutation` via `injectEndpoints` (POST `/mailboxes/oauth/microsoft/start` → `{auth_url}`), mirroring `startGoogleOauth`. Dropdown gains a "Microsoft 365" item (official 4-square Microsoft mark) → same start-flow helper (reuse `onConnectGmail`'s logic generically, or a parallel `onConnectMicrosoft`; keep the shared `BannerShell` + typed `rtk-error` handling). `ProviderTag` shows "Microsoft 365" for `provider==='m365'` (and its row line "Microsoft 365 · API"). Callback banner is already generic (reason codes shared).

- [ ] Apply React best practices (no eager import regressions, typed errors, shared banner, dropdown closes on error). Extend `mailboxes-page.test.tsx` for the m365 start error/success paths mirroring the gmail cases. Verify `cd web && npm run lint && npm run build && npx vitest run`. Commit `feat(web): connect Microsoft 365 mailbox via OAuth`.

---

### Task 6: Docs

**Files:** `.env.example` (done in Task 1 — verify), `docs/self-hosting.md`, `docs/security.md`, `docs/architecture.md`.

- [ ] `self-hosting.md`: "Connecting a Microsoft 365 mailbox (OAuth)" — Azure app registration (Entra ID), API permissions (delegated: Mail.Send, Mail.Read, User.Read, offline_access, openid, email), redirect URI = `INROAD_MS_REDIRECT_URL` (default `${INROAD_PUBLIC_URL}/oauth/microsoft/callback`), the 4 `INROAD_MS_*` vars, tenant `common` note, blank = disabled.
- [ ] `security.md` + `architecture.md`: note m365 joins the provider abstraction under the same invariants (nothing new). Commit `docs(oauth): M365 setup + provider notes`.

---

## Self-Review

- Spec coverage: §3 auth→T1/T2, §4 routes→T2, §5 send→T3, §6 inbox→T4, §7 refresh→T3, §8 config→T1, §9 deps→T1, §11 tests→per task, frontend→T5, docs→T6. Covered.
- No placeholders; each task names files + interfaces + the Gmail file it mirrors.
- Type consistency: `oauthAccessToken(...cfg *oauth2.Config)` used by both providers; `NewMultiSender(smtp, gmail, graph)` + `inprocess.New(...msOAuth)` + `NewService(...msOAuth, msExchanger)` arity changes each carry a "update all call sites" note.
- No migration (verified: `inbox_cursor` opaque + `provider` column already present at head `000012`).
