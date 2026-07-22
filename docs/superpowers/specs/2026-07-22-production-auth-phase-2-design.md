# Production Auth — Phase 2 Design

**Goal:** Complete the account-security surface on top of Phase 1: a pluggable
transactional email sender, email verification (gating the send capability),
password reset, and workspace invites — with the architecture pre-cut for
future OAuth/SSO providers (no integrations built this phase).

**Builds on:** Phase 1 (multi-workspace accounts, access+refresh tokens with
family-based reuse detection, CSRF double-submit, argon2id, deny-by-default
router, `RequireRole`). Branch: `feature/production-auth-phase-2` off
`feature/production-auth-phase-1`.

**Tech stack:** Go 1.25, chi/v5, pgx/v5, sqlc, golang-jwt/v5, argon2id,
Postgres (citext). Frontend: React 19, RTK Query, TanStack Router.

---

## 0. Assumptions (confirmed with product owner)

| # | Decision | Rationale |
|---|---|---|
| A1 | **Scope = all three flows** (verify + reset + invites) on a shared transactional sender. | Full Phase 2 per the Phase 1 spec's deferred list. |
| A2 | **Verification gates sending, not login.** Unverified users log in and explore; `RequireVerified` blocks exactly **campaign launch** and **mailbox create** (the email-emitting actions). | Strongest anti-abuse posture for a cold-email tool without a dead-end UX. |
| A3 | **Invites: invite-any-email, accept-creates-or-links.** Existing email ⇒ add membership; new email ⇒ set password + create account. Accepting marks the email verified (token proves inbox ownership). `RequireRole(admin)` to invite. | Standard SaaS growth flow; the invite token is itself proof of email ownership. |
| A4 | **OAuth is a pre-cut seam, not built.** Email is the account anchor; password is the first of potentially many auth providers; session issuance is provider-agnostic. A future OAuth callback reuses the resolve-or-create-by-verified-email → issue-session path. Reserved (not created): `auth_identities` table, `/auth/{provider}/callback` route. | "Architecture in place" without speculative integration code (YAGNI). |
| A5 | **Token model:** opaque random token, SHA-256 hashed at rest, single-use, expiring — identical to Phase 1 refresh tokens. TTLs: **verify 24h, reset 1h, invite 72h**. | Reuse the proven Phase 1 pattern; no new crypto. |
| A6 | **No account-existence leak.** `POST /auth/password/forgot` always returns 200 regardless of whether the email exists. | Prevents user enumeration. |
| A7 | **Reset revokes all sessions** of the user (reuse family revocation). | A password reset implies possible compromise. |

**Non-goals (Phase 3):** OAuth/SSO integrations, 2FA/TOTP, session-management
UI, audit log. `users.password_hash` stays `NOT NULL` this phase (every account
has a password); it becomes nullable when password-less OAuth accounts arrive.

---

## 1. Transactional email sender — `internal/platform/notify`

New platform package (peer of `mail`/`crypto`), decoupled from campaign sending.

```go
// Message is one transactional email.
type Message struct { To, Subject, TextBody, HTMLBody string }

// Sender delivers transactional email. Consumers depend on this interface.
type Sender interface { Send(ctx context.Context, m Message) error }
```

Two drivers, selected by `INROAD_TRANSACTIONAL_DRIVER` (`console` default | `smtp`):
- **`consoleSender`** — renders the message to the structured logger; no network. Dev/test default.
- **`smtpSender`** — sends via an operator-configured **system mailbox**
  (`INROAD_SYSTEM_SMTP_HOST/PORT/USERNAME/PASSWORD`, `INROAD_SYSTEM_EMAIL_FROM`),
  reusing `platform/mail`'s dialer + TLS enforcement. Distinct from per-user
  campaign mailboxes.

Templates live in `notify/templates.go` (`text/template` + `html/template`),
one pair per email kind (verify, reset, invite). `notify.New(cfg) (Sender, error)`
is the composition-root factory. Unit tests use a `fakeSender` capturing messages.

**Why an interface, not concrete:** dependency inversion — `identity.Service`
depends on `notify.Sender`, so flows are unit-testable with no SMTP and the
driver is swappable. *Rejected: extending `platform/mail` (couples transactional
with campaign sends); a provider SDK (SMTP covers self-host; YAGNI).*

## 2. Data model — migration `000009_auth_phase2`

```sql
-- Single-use, expiring tokens for email verification and password reset.
CREATE TYPE user_token_kind AS ENUM ('email_verify', 'password_reset');
CREATE TABLE user_tokens (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id      UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    kind         user_token_kind NOT NULL,
    token_hash   BYTEA NOT NULL,          -- SHA-256 of the opaque token
    expires_at   TIMESTAMPTZ NOT NULL,
    consumed_at  TIMESTAMPTZ,             -- NULL until used (single-use)
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_user_tokens_hash ON user_tokens (token_hash);
CREATE INDEX idx_user_tokens_user_kind ON user_tokens (user_id, kind, created_at);

CREATE TYPE invite_status AS ENUM ('pending', 'accepted', 'revoked');
CREATE TABLE workspace_invites (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    email        CITEXT NOT NULL,
    role         member_role NOT NULL DEFAULT 'member',
    token_hash   BYTEA NOT NULL,
    invited_by   UUID NOT NULL REFERENCES users(id),
    status       invite_status NOT NULL DEFAULT 'pending',
    expires_at   TIMESTAMPTZ NOT NULL,
    accepted_at  TIMESTAMPTZ,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_invites_hash ON workspace_invites (token_hash);
CREATE UNIQUE INDEX idx_invites_pending_ws_email
    ON workspace_invites (workspace_id, email) WHERE status = 'pending';
```
`down`: drop tables + the two enum types. No `users` change (`email_verified_at`
exists from Phase 1).

**Migration numbering — CROSS-BRANCH HAZARD (resolve at implementation):** this
branch is off `main`, whose latest migration is `000006`. The *sequencing* branch
(unmerged) owns `000007` and `000008`. To avoid a filename collision at merge,
Phase 2 uses **`000009`**, deliberately leaving the `000007`/`000008` slots for
sequencing. Caveat: golang-migrate applies by version order and refuses to run a
*lower* version after a higher one is recorded — so `000009` is safe only if
sequencing merges to `main` **before or with** this branch. If auth Phase 2 would
merge first, renumber to `000007` at merge time. The implementer must confirm the
current `main` migration head before finalizing the number and flag the merge-order
dependency to the coordinator. (Pre-production, no data to preserve — per Phase 1.)

## 3. Endpoints

Public (no access token). These are **pre-authentication, token-based flows, so
they are NOT CSRF-gated** (unlike Phase 1 refresh/logout, which act on the ambient
session cookie): a logged-out caller has no CSRF cookie, and each endpoint's
protection comes from its own out-of-band single-use token (or, for forgot, the
absence of any ambient authority — it only acts on a body email):
- `POST /api/v1/auth/verify-email {token}` → set `email_verified_at`, consume.
- `POST /api/v1/auth/password/forgot {email}` → always 200; issue+email reset token if user exists.
- `POST /api/v1/auth/password/reset {token, new_password}` → set hash, consume, revoke all sessions.
- `POST /api/v1/auth/invites/accept {token, password?}` → link or create+membership, verify email, consume.

Protected (RequireAuth):
- `POST /api/v1/auth/verify-email/resend` → reissue verify token (rate-limited).
- `POST /api/v1/workspaces/{id}/invites {email, role}` — **RequireRole(admin)**.
- `GET /api/v1/workspaces/{id}/invites` — list pending (admin).
- `DELETE /api/v1/workspaces/{id}/invites/{inviteId}` — revoke (admin).

**Register (modify):** after creating the user, issue an `email_verify` token and
send the verify email. Login still succeeds unverified (A2).

## 4. Flows (service layer, `identity` domain)

- **Token issuance/consumption** is a small store surface: `IssueUserToken(userID, kind, ttl) → rawToken`, `ConsumeUserToken(rawToken, kind) → userID` (validates hash, unexpired, unconsumed; marks consumed atomically). Reuses `auth.NewRefreshToken`/`HashRefreshToken` (generalized to `auth.NewOpaqueToken`/`HashToken` if naming warrants; keep backward-compatible).
- **Verify:** consume token → `SET email_verified_at = now()`.
- **Forgot/Reset:** forgot issues a reset token (rate-limited, A6 no-leak); reset consumes it, `UpdatePasswordHash`, and `RevokeAllUserSessions` (A7).
- **Invite create:** insert pending invite (unique per pending (workspace,email)), email accept link.
- **Invite accept:** validate token; in ONE transaction — resolve user by email (create with password if absent, marking `email_verified_at`), upsert `workspace_members`, mark invite `accepted`. Return a session (provider-agnostic issuance — the OAuth seam, A4).

## 5. Verified-only guard

`auth.RequireVerified` middleware: reads `user_id` from the RequireAuth-populated
context, looks up `email_verified_at` (small store method; these routes are
low-frequency), 403 `email_not_verified` if null. Applied to exactly:
- campaign launch handler (`POST /campaigns/{id}/launch`)
- mailbox create handler (`POST /mailboxes`)

Freshness by DB lookup (not a token claim) so verifying takes effect immediately
without waiting for token refresh.

## 6. Rate limiting

`forgot` and `verify-email/resend` are enumeration / email-bomb vectors. Throttle
token issuance off `user_tokens` timestamps (no Redis): reject a new token of a
given kind if one was issued for that user **< 60s ago**, or if **≥ 5** were issued
in the last hour. `forgot` still returns 200 when throttled (no signal leak).

## 7. Frontend

- Routes/pages: `verify-email` (consumes `?token=`), `forgot-password`,
  `reset-password?token=`, `accept-invite?token=`.
- `settings/team`: pending-invites list + invite form (admin-gated by role in the session).
- An unverified banner with a "resend" action; 403 `email_not_verified` from a
  gated action surfaces a "verify your email" prompt.
- OpenAPI `/auth/*` + `/workspaces/{id}/invites` paths added; `web/src/store/api.ts`
  regenerated (never hand-edited).

## 8. Security invariants

No existence leak on forgot (A6). Tokens: opaque, SHA-256 at rest, single-use,
short TTL (A5), timing-safe lookup by hash. Reset revokes all sessions (A7).
Invite-accept verifies email and is workspace/role-scoped (add-only: accepting
never changes an existing member's role). The public token flows (verify/forgot/
reset/accept) are intentionally NOT CSRF-gated — their credential is the
out-of-band token, and there is no ambient cookie authority to protect; only the
cookie-session flows (refresh/logout) carry CSRF. `RequireRole(admin)` on invite
management, with `workspace_id` taken from the JWT (not the path). Every new
query workspace/user-scoped. Transactional system SMTP credentials from env,
never logged; the sender never logs token values (log the *event*, not the link).

## 9. OAuth seam (Phase 3 — reserved, not built)

Documented so Phase 3 is additive, not a rewrite:
- **Reserved table:** `auth_identities(user_id, provider text, provider_subject text, created_at, UNIQUE(provider, provider_subject))`. Password remains on `users.password_hash` (becomes nullable when password-less accounts exist).
- **Reserved route shape:** `GET /api/v1/auth/{provider}/start`, `GET /api/v1/auth/{provider}/callback`.
- **Reused path:** the callback resolves-or-creates a user by verified email and calls the SAME provider-agnostic session-issuance code (`issueSession`) that Register / Login / invite-accept use today. No change to the token/session/CSRF layer is anticipated.

## 10. Testing

- **Unit:** notify drivers (console captures; smtp via fake dialer); token issue/consume (expiry, single-use, wrong-kind); rate-limit thresholds; each service flow with a fake store + `fakeSender`; `RequireVerified` (verified 200 / unverified 403); invite accept both branches (existing vs new user).
- **Integration (`//go:build integration`):** full register→verify→login; forgot→reset→old-session-revoked; invite→accept (new + existing user)→membership; gated route 403 unverified then 200 after verify. Against dockerized Postgres.
- **Frontend:** Vitest for the four token pages + invite form; RTK Query hooks.

## 11. File structure

- `internal/platform/notify/{notify.go, console.go, smtp.go, templates.go, *_test.go}`
- `internal/platform/config/config.go` — transactional driver + system-SMTP + TTL settings.
- `internal/platform/db/migrations/000009_auth_phase2.{up,down}.sql`; `queries/{user_token,invite}.sql`; extend `queries/user.sql`,`session.sql` as needed → regen `gen/`.
- `internal/app/auth/{token.go (generalize),middleware.go (RequireVerified)}`.
- `internal/app/identity/{service.go,store.go,handler.go,routes.go}` — extend with the new flows; `invite.go` for invite-specific handlers if it keeps files focused.
- `internal/app/campaign/routes.go`, `internal/app/mailbox/routes.go` — apply `RequireVerified` to launch / create.
- `cmd/inroad/main.go` — construct `notify.Sender`, wire into identity service; mount invite routes under the protected group.
- `api/openapi.yaml` + `web/` pages/slice + regenerated types.
