# Production Auth Phase 2 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a pluggable transactional email sender, email verification (gating the send capability), password reset, and workspace invites on top of Phase 1 auth — with the architecture pre-cut for future OAuth providers.

**Architecture:** A new `internal/platform/notify` package (interface + console/smtp drivers) delivers transactional email. Single-use hashed tokens (Phase 1 pattern) drive verify/reset/invite flows in the `identity` domain. A `RequireVerified` middleware gates campaign-launch and mailbox-create. Session issuance stays provider-agnostic (the OAuth seam). Deny-by-default router from Phase 1 hosts the new public/protected routes.

**Tech Stack:** Go 1.25 · chi/v5 · pgx/v5 · sqlc · argon2id · golang-jwt/v5 · Postgres (citext). Frontend: React 19 · RTK Query · TanStack Router.

## Global Constraints

- Module `github.com/inroad/inroad`. Go files lowercase; frontend files kebab-case; identifiers idiomatic MixedCaps; snake_case only at JSON/DB/env boundaries.
- `app/*` imports `platform/*`, never the reverse; `app/*` packages don't import each other; workers use `coreapi` only.
- Each domain defines its own `Store` interface; services depend on the interface, not the sqlc struct. Handlers thin; `validate.Struct(req)` before the service.
- `store/api.ts` is generated from `api/openapi.yaml` — never hand-edited. redux-persist whitelists UI-only slices, never `api` or `auth`.
- Toolchain PATH (this machine): prefix EVERY Go/sqlc command with `export PATH="$PATH:/c/Program Files/Go/bin:$HOME/go/bin"`. Shell state does not persist between calls. Work in the worktree `C:/Users/Ahmed/OneDrive/Desktop/personal-projects/Inroad-auth` (branch `feature/production-auth-phase-2`).
- Response helpers: `httpx.JSON(w, status, v)`, `httpx.Error(w, status, msg)`. Access-token secret env `INROAD_JWT_SECRET`.
- Tokens: opaque random, SHA-256 hashed at rest (BYTEA), single-use, expiring. TTLs: verify 24h, reset 1h, invite 72h.
- Security invariants (never break): no account-existence leak on forgot (always 200); reset revokes all sessions; invite-accept marks email verified; CSRF on public state-changing endpoints; `RequireRole(admin)` on invite management; every query workspace/user-scoped; system SMTP creds from env, never logged; never log token values.
- Conventional commits (`feat:`,`test:`,`chore:`,`docs:`). Commit at the end of every task. Never commit to `main`.

---

## Task 1: Transactional email sender — `internal/platform/notify`

**Files:**
- Create: `internal/platform/notify/notify.go`, `console.go`, `smtp.go`, `templates.go`, `notify_test.go`
- Modify: `internal/platform/config/config.go`

**Interfaces:**
- Produces: `notify.Message{To,Subject,TextBody,HTMLBody string}`; `notify.Sender interface { Send(ctx, Message) error }`; `notify.New(cfg Config) (Sender, error)`; template renderers `notify.VerifyEmail(link string) Message`, `notify.ResetEmail(link string) Message`, `notify.InviteEmail(workspaceName, link string) Message`.
- Consumes: config transactional settings (below).

- [ ] **Step 1: Add config fields**

In `internal/platform/config/config.go` add to `Config`:
```go
TransactionalDriver string // "console" (default) | "smtp"
SystemSMTPHost      string
SystemSMTPPort      int
SystemSMTPUsername  string
SystemSMTPPassword  string
SystemEmailFrom     string
AppBaseURL          string // for building links, e.g. https://app.example.com
EmailVerifyTTL      time.Duration // 24h
PasswordResetTTL    time.Duration // 1h
InviteTTL           time.Duration // 72h
```
In `Load()` populate them (reuse the existing `getenv` helper; defaults: driver `console`, port `587`, TTLs `24h`/`1h`/`72h`, `AppBaseURL` default `http://localhost:5173`). Do not require system-SMTP vars when driver is `console`.

- [ ] **Step 2: Write the failing test for the console driver + templates**

`internal/platform/notify/notify_test.go`:
```go
package notify

import (
	"context"
	"strings"
	"testing"
)

func TestConsoleSenderCaptures(t *testing.T) {
	var got Message
	s := &consoleSender{sink: func(m Message) { got = m }}
	m := Message{To: "a@b.io", Subject: "Hi", TextBody: "body", HTMLBody: "<p>body</p>"}
	if err := s.Send(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	if got.To != "a@b.io" || got.Subject != "Hi" {
		t.Fatalf("not captured: %+v", got)
	}
}

func TestVerifyEmailRendersLink(t *testing.T) {
	m := VerifyEmail("https://app.test/verify-email?token=abc")
	if !strings.Contains(m.TextBody, "https://app.test/verify-email?token=abc") ||
		!strings.Contains(m.HTMLBody, "abc") || m.Subject == "" {
		t.Fatalf("verify template missing link/subject: %+v", m)
	}
}
```

- [ ] **Step 3: Run it, verify it fails**

Run: `cd "C:/Users/Ahmed/OneDrive/Desktop/personal-projects/Inroad-auth" && export PATH="$PATH:/c/Program Files/Go/bin:$HOME/go/bin" && go test ./internal/platform/notify/ -v`
Expected: FAIL (package/symbols undefined).

- [ ] **Step 4: Implement `notify.go` (interface + Config + factory)**

`internal/platform/notify/notify.go`:
```go
// Package notify delivers transactional (system-originated) email — distinct
// from per-user campaign mailboxes. Pluggable via Config.Driver.
package notify

import (
	"context"
	"fmt"
	"log/slog"
)

type Message struct{ To, Subject, TextBody, HTMLBody string }

// Sender delivers one transactional email. Consumers depend on this interface.
type Sender interface {
	Send(ctx context.Context, m Message) error
}

type Config struct {
	Driver       string // "console" | "smtp"
	SMTPHost     string
	SMTPPort     int
	SMTPUsername string
	SMTPPassword string
	From         string
	Logger       *slog.Logger
}

// New builds the configured Sender. console (default) logs; smtp dials the
// operator system mailbox.
func New(cfg Config) (Sender, error) {
	switch cfg.Driver {
	case "", "console":
		lg := cfg.Logger
		return &consoleSender{sink: func(m Message) {
			lg.Info("transactional email (console)", "to", m.To, "subject", m.Subject)
		}}, nil
	case "smtp":
		if cfg.SMTPHost == "" || cfg.From == "" {
			return nil, fmt.Errorf("smtp driver requires SMTP host and From")
		}
		return &smtpSender{cfg: cfg}, nil
	default:
		return nil, fmt.Errorf("unknown transactional driver %q", cfg.Driver)
	}
}
```

- [ ] **Step 5: Implement `console.go`**

`internal/platform/notify/console.go`:
```go
package notify

import "context"

// consoleSender renders the message via an injected sink (logger in prod,
// capture func in tests). No network. Dev/test default.
type consoleSender struct{ sink func(Message) }

func (c *consoleSender) Send(_ context.Context, m Message) error {
	c.sink(m)
	return nil
}
```

- [ ] **Step 6: Implement `smtp.go`**

`internal/platform/notify/smtp.go` — send via go-mail through the system mailbox, mirroring `platform/mail`'s dial/TLS usage. Read `internal/platform/mail/net_tester.go`/`mail.go` first to match the exact go-mail client construction and TLS policy already in the repo, then:
```go
package notify

import (
	"context"
	"fmt"

	"github.com/wneessen/go-mail"
)

type smtpSender struct{ cfg Config }

func (s *smtpSender) Send(ctx context.Context, m Message) error {
	msg := mail.NewMsg()
	if err := msg.From(s.cfg.From); err != nil {
		return fmt.Errorf("from: %w", err)
	}
	if err := msg.To(m.To); err != nil {
		return fmt.Errorf("to: %w", err)
	}
	msg.Subject(m.Subject)
	msg.SetBodyString(mail.TypeTextPlain, m.TextBody)
	if m.HTMLBody != "" {
		msg.AddAlternativeString(mail.TypeTextHTML, m.HTMLBody)
	}
	c, err := mail.NewClient(s.cfg.SMTPHost,
		mail.WithPort(s.cfg.SMTPPort),
		mail.WithSMTPAuth(mail.SMTPAuthPlain),
		mail.WithUsername(s.cfg.SMTPUsername),
		mail.WithPassword(s.cfg.SMTPPassword),
		mail.WithTLSPolicy(mail.TLSMandatory),
	)
	if err != nil {
		return fmt.Errorf("smtp client: %w", err)
	}
	return c.DialAndSendWithContext(ctx, msg)
}
```
(Adjust option names to match the go-mail version already in `go.mod` — copy the pattern from `platform/mail`.)

- [ ] **Step 7: Implement `templates.go`**

`internal/platform/notify/templates.go` — three `Message` builders with plain+HTML bodies. Keep copy short; put the link verbatim in text and as an `<a>` in HTML. Example:
```go
package notify

import "fmt"

func VerifyEmail(link string) Message {
	return Message{
		Subject:  "Verify your email",
		TextBody: fmt.Sprintf("Confirm your email address:\n\n%s\n\nThis link expires in 24 hours.", link),
		HTMLBody: fmt.Sprintf(`<p>Confirm your email address:</p><p><a href="%s">Verify email</a></p><p>This link expires in 24 hours.</p>`, link),
	}
}

func ResetEmail(link string) Message {
	return Message{
		Subject:  "Reset your password",
		TextBody: fmt.Sprintf("Reset your password:\n\n%s\n\nThis link expires in 1 hour. If you didn't request this, ignore this email.", link),
		HTMLBody: fmt.Sprintf(`<p>Reset your password:</p><p><a href="%s">Reset password</a></p><p>Expires in 1 hour. If you didn't request this, ignore this email.</p>`, link),
	}
}

func InviteEmail(workspaceName, link string) Message {
	return Message{
		Subject:  fmt.Sprintf("You're invited to %s", workspaceName),
		TextBody: fmt.Sprintf("You've been invited to join %s:\n\n%s\n\nThis link expires in 72 hours.", workspaceName, link),
		HTMLBody: fmt.Sprintf(`<p>You've been invited to join <b>%s</b>:</p><p><a href="%s">Accept invite</a></p><p>Expires in 72 hours.</p>`, workspaceName, link),
	}
}
```

- [ ] **Step 8: Run tests + build**

Run: `cd "C:/Users/Ahmed/OneDrive/Desktop/personal-projects/Inroad-auth" && export PATH="$PATH:/c/Program Files/Go/bin:$HOME/go/bin" && go test ./internal/platform/notify/ -v && go build ./...`
Expected: PASS; build OK.

- [ ] **Step 9: Commit**

```bash
git add internal/platform/notify/ internal/platform/config/config.go
git commit -m "feat(notify): pluggable transactional email sender (console/smtp) + templates"
```

---

## Task 2: Migration 000009 + token/invite queries

**Files:**
- Create: `internal/platform/db/migrations/000009_auth_phase2.up.sql`, `.down.sql`
- Create: `internal/platform/db/queries/user_token.sql`, `queries/invite.sql`
- Generated: `internal/platform/db/gen/*`

**Interfaces:**
- Produces sqlc methods: `CreateUserToken`, `GetUserTokenByHash`, `ConsumeUserToken`, `CountRecentUserTokens`; `CreateInvite`, `GetInviteByHash`, `ListPendingInvites`, `RevokeInvite`, `MarkInviteAccepted`, `GetPendingInviteForEmail`; and (extend user.sql) `SetEmailVerified`, `UpdatePasswordHash`, `GetUserByEmailForAuth` if not present.

- [ ] **Step 1: Confirm migration head, then write the up migration**

Run `ls internal/platform/db/migrations/` and confirm the highest number is `000006` on this branch (sequencing owns 000007/000008 on its own branch — see spec §2 hazard note). Use `000009`. If the head differs, STOP and flag the coordinator.
`internal/platform/db/migrations/000009_auth_phase2.up.sql`:
```sql
CREATE TYPE user_token_kind AS ENUM ('email_verify', 'password_reset');
CREATE TABLE user_tokens (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id     UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    kind        user_token_kind NOT NULL,
    token_hash  BYTEA NOT NULL,
    expires_at  TIMESTAMPTZ NOT NULL,
    consumed_at TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
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

- [ ] **Step 2: Write the down migration**

`.down.sql`:
```sql
DROP TABLE IF EXISTS workspace_invites;
DROP TYPE IF EXISTS invite_status;
DROP TABLE IF EXISTS user_tokens;
DROP TYPE IF EXISTS user_token_kind;
```

- [ ] **Step 3: Prove reversibility (needs dev DB)**

Run: `cd "C:/Users/Ahmed/OneDrive/Desktop/personal-projects/Inroad-auth" && export PATH="$PATH:/c/Program Files/Go/bin:$HOME/go/bin" && make migrate-up && make migrate-down && make migrate-up`
Expected: all succeed. If Docker/DB unavailable, report NOT-RUN and flag the coordinator.

- [ ] **Step 4: Write `queries/user_token.sql`**

```sql
-- name: CreateUserToken :one
INSERT INTO user_tokens (user_id, kind, token_hash, expires_at)
VALUES ($1,$2,$3,$4) RETURNING *;
-- name: GetUserTokenByHash :one
SELECT * FROM user_tokens WHERE token_hash = $1 AND kind = $2;
-- name: ConsumeUserToken :one
-- Single-use: only succeeds if not already consumed and not expired.
UPDATE user_tokens SET consumed_at = now()
WHERE token_hash = $1 AND kind = $2 AND consumed_at IS NULL AND expires_at > now()
RETURNING user_id;
-- name: CountRecentUserTokens :one
-- Rate-limit support: how many of this kind issued to this user since $3.
SELECT count(*) FROM user_tokens
WHERE user_id = $1 AND kind = $2 AND created_at > $3;
```

- [ ] **Step 5: Write `queries/invite.sql`**

```sql
-- name: CreateInvite :one
INSERT INTO workspace_invites (workspace_id, email, role, token_hash, invited_by, expires_at)
VALUES ($1,$2,$3,$4,$5,$6) RETURNING *;
-- name: GetInviteByHash :one
SELECT * FROM workspace_invites WHERE token_hash = $1;
-- name: ListPendingInvites :many
SELECT * FROM workspace_invites WHERE workspace_id = $1 AND status = 'pending' ORDER BY created_at DESC;
-- name: RevokeInvite :exec
UPDATE workspace_invites SET status = 'revoked'
WHERE id = $1 AND workspace_id = $2 AND status = 'pending';
-- name: MarkInviteAccepted :exec
UPDATE workspace_invites SET status = 'accepted', accepted_at = now() WHERE id = $1;
-- name: GetPendingInviteForEmail :one
SELECT * FROM workspace_invites WHERE workspace_id = $1 AND email = $2 AND status = 'pending';
```

- [ ] **Step 6: Extend `queries/user.sql` (add only if absent — check first)**

```sql
-- name: SetEmailVerified :exec
UPDATE users SET email_verified_at = now() WHERE id = $1;
-- name: UpdatePasswordHash :exec
UPDATE users SET password_hash = $2 WHERE id = $1;
```
(`GetUserByEmail` for auth likely already exists from Phase 1 — reuse it; do not duplicate.)

- [ ] **Step 7: Regenerate sqlc + build**

Run: `cd "C:/Users/Ahmed/OneDrive/Desktop/personal-projects/Inroad-auth" && export PATH="$PATH:/c/Program Files/Go/bin:$HOME/go/bin" && make sqlc && go build ./...`
Expected: generation succeeds; build passes.

- [ ] **Step 8: Commit**

```bash
git add internal/platform/db/migrations/000009_auth_phase2.up.sql internal/platform/db/migrations/000009_auth_phase2.down.sql internal/platform/db/queries/ internal/platform/db/gen/
git commit -m "feat(db): 000009 user_tokens + workspace_invites schema and queries"
```

---

## Task 3: Opaque token helpers + user-token store (issue/consume/rate-limit)

**Files:**
- Modify: `internal/app/auth/token.go` (generalize helpers, keep backward-compatible)
- Modify: `internal/app/identity/store.go` (add user-token methods)
- Test: `internal/app/identity/token_test.go` (or extend existing service test)

**Interfaces:**
- Consumes: Task 2 sqlc methods; existing `auth` refresh-token helpers.
- Produces: `auth.NewOpaqueToken() (raw string, hash []byte, err error)`, `auth.HashToken(raw string) []byte` (alias/refactor of the refresh-token pattern); store methods `IssueUserToken(ctx, userID uuid.UUID, kind string, ttl time.Duration) (rawToken string, err error)`, `ConsumeUserToken(ctx, raw, kind string) (userID uuid.UUID, err error)` (returns `ErrTokenInvalid` on miss/expired/consumed), `CountRecentUserTokens(ctx, userID uuid.UUID, kind string, since time.Time) (int64, error)`.

- [ ] **Step 1: Read the current refresh-token helpers**

Run: `cd "C:/Users/Ahmed/OneDrive/Desktop/personal-projects/Inroad-auth" && export PATH="$PATH:/c/Program Files/Go/bin:$HOME/go/bin" && sed -n '1,80p' internal/app/auth/token.go`
Expected: see `NewRefreshToken`/`HashRefreshToken`. Generalize them to `NewOpaqueToken`/`HashToken` and keep the refresh names as thin aliases (no breaking change to Phase 1 callers).

- [ ] **Step 2: Write failing store test**

`internal/app/identity/token_test.go` — a fake over the new sqlc methods asserting: issue returns a raw token whose SHA-256 matches what was stored; consume of a valid token returns the user id; consume of a wrong/again token returns `ErrTokenInvalid`. Use the existing fake-store pattern from `service_test.go`.

- [ ] **Step 3: Add `ErrTokenInvalid` + store methods**

In `internal/app/identity/store.go`:
```go
var ErrTokenInvalid = errors.New("token invalid or expired")

func (s *Store) IssueUserToken(ctx context.Context, userID uuid.UUID, kind string, ttl time.Duration) (string, error) {
	raw, hash, err := auth.NewOpaqueToken()
	if err != nil {
		return "", err
	}
	_, err = s.q.CreateUserToken(ctx, gen.CreateUserTokenParams{
		UserID: userID, Kind: gen.UserTokenKind(kind), TokenHash: hash,
		ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(ttl), Valid: true},
	})
	return raw, err
}

func (s *Store) ConsumeUserToken(ctx context.Context, raw, kind string) (uuid.UUID, error) {
	uid, err := s.q.ConsumeUserToken(ctx, gen.ConsumeUserTokenParams{
		TokenHash: auth.HashToken(raw), Kind: gen.UserTokenKind(kind),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, ErrTokenInvalid
	}
	return uid, err
}

func (s *Store) CountRecentUserTokens(ctx context.Context, userID uuid.UUID, kind string, since time.Time) (int64, error) {
	return s.q.CountRecentUserTokens(ctx, gen.CountRecentUserTokensParams{
		UserID: userID, Kind: gen.UserTokenKind(kind),
		CreatedAt: pgtype.Timestamptz{Time: since, Valid: true},
	})
}
```
(Match the real `Store` receiver name + `storeIface` — add the three methods to the identity store interface consumed by the service.)

- [ ] **Step 4: Run tests + build**

Run: `... && go test ./internal/app/identity/ ./internal/app/auth/ -v && go build ./...`
Expected: PASS; build OK.

- [ ] **Step 5: Commit**

```bash
git add internal/app/auth/token.go internal/app/identity/store.go internal/app/identity/token_test.go
git commit -m "feat(auth): generalized opaque-token helpers + user-token store (issue/consume/count)"
```

---

## Task 4: Email verification flow

**Files:**
- Modify: `internal/app/identity/service.go` (Register issues+sends verify token; add `VerifyEmail`, `ResendVerification`)
- Modify: `internal/app/identity/handler.go`, `routes.go`
- Test: `internal/app/identity/service_test.go`

**Interfaces:**
- Consumes: Task 1 `notify.Sender`, Task 3 store methods, config TTLs + `AppBaseURL`.
- Produces: `Service.VerifyEmail(ctx, rawToken string) error`; `Service.ResendVerification(ctx, userID uuid.UUID) error` (rate-limited); handlers `verifyEmail`, `resendVerification`.

- [ ] **Step 1: Inject `notify.Sender` + config into the identity Service**

Extend `NewService` (and `NewHandler` where TTLs/base URL are needed) to take `notify.Sender`, `appBaseURL string`, and the three TTLs. Update the single constructor call in `cmd/inroad/main.go` (do this now so the build stays green; full wiring in Task 8).

- [ ] **Step 2: Write failing tests**

In `service_test.go` add: `TestRegisterSendsVerifyEmail` (fake sender captures a message whose body contains a `/verify-email?token=` link; user created with `email_verified_at` null), `TestVerifyEmailMarksVerified` (issue a token via fake store, VerifyEmail consumes it and calls `SetEmailVerified`), `TestResendVerificationRateLimited` (second call within 60s returns `ErrRateLimited`). Use the fake store + a `fakeSender{ last Message }`.

- [ ] **Step 3: Implement Register change + VerifyEmail + ResendVerification**

```go
var ErrRateLimited = errors.New("too many requests")

// in Register(), after the user row is created and before returning the session:
raw, err := s.store.IssueUserToken(ctx, userID, "email_verify", s.verifyTTL)
if err == nil {
	link := s.appBaseURL + "/verify-email?token=" + url.QueryEscape(raw)
	_ = s.sender.Send(ctx, notify.VerifyEmail(link)) // best-effort; log on error, don't fail registration
}

func (s *Service) VerifyEmail(ctx context.Context, raw string) error {
	uid, err := s.store.ConsumeUserToken(ctx, raw, "email_verify")
	if err != nil {
		return err // ErrTokenInvalid
	}
	return s.store.SetEmailVerified(ctx, uid)
}

func (s *Service) ResendVerification(ctx context.Context, userID uuid.UUID) error {
	n, err := s.store.CountRecentUserTokens(ctx, userID, "email_verify", time.Now().Add(-time.Minute))
	if err != nil { return err }
	if n > 0 { return ErrRateLimited }
	raw, err := s.store.IssueUserToken(ctx, userID, "email_verify", s.verifyTTL)
	if err != nil { return err }
	link := s.appBaseURL + "/verify-email?token=" + url.QueryEscape(raw)
	return s.sender.Send(ctx, notify.VerifyEmail(link))
}
```
Add `SetEmailVerified` to the store interface + `Store` impl (calls `q.SetEmailVerified`).

- [ ] **Step 4: Handlers + routes**

`verifyEmail` (public, `POST /verify-email {token}`): decode, `validate.Struct`, call `VerifyEmail`, map `ErrTokenInvalid`→400 `invalid or expired token`, else 204. `resendVerification` (protected, `POST /verify-email/resend`): read `userID` from `auth` context, call `ResendVerification`, map `ErrRateLimited`→429. Mount verify in the public sub-router, resend in the protected group (`routes.go`).

- [ ] **Step 5: Run tests + build**

Run: `... && go test ./internal/app/identity/ -v && go build ./...`
Expected: PASS; build OK.

- [ ] **Step 6: Commit**

```bash
git add internal/app/identity/ internal/coreapi/ cmd/inroad/main.go
git commit -m "feat(identity): email verification (register sends token, verify + rate-limited resend)"
```

---

## Task 5: Password reset flow

**Files:**
- Modify: `internal/app/identity/service.go` (`ForgotPassword`, `ResetPassword`), `handler.go`, `routes.go`
- Test: `internal/app/identity/service_test.go`

**Interfaces:**
- Consumes: Task 3 store methods, `notify.ResetEmail`, existing password hashing (`auth.HashPassword`), session-revocation store method (Phase 1 `RevokeAllUserSessions` / `LogoutAll` path — reuse; confirm the exact name).
- Produces: `Service.ForgotPassword(ctx, email string) error` (always nil to caller on unknown email — no leak; rate-limited), `Service.ResetPassword(ctx, rawToken, newPassword string) error`.

- [ ] **Step 1: Write failing tests**

`TestForgotPasswordUnknownEmailNoLeakNoSend` (unknown email ⇒ returns nil, sender NOT called), `TestForgotPasswordKnownSendsReset` (known email ⇒ reset link emailed), `TestResetPasswordSetsHashAndRevokesSessions` (valid token ⇒ `UpdatePasswordHash` + `RevokeAllUserSessions` called; invalid ⇒ `ErrTokenInvalid`), `TestForgotPasswordRateLimited`.

- [ ] **Step 2: Implement**

```go
func (s *Service) ForgotPassword(ctx context.Context, email string) error {
	u, err := s.store.GetUserByEmail(ctx, email)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil // no account-existence leak
	}
	if err != nil { return err }
	n, _ := s.store.CountRecentUserTokens(ctx, u.ID, "password_reset", time.Now().Add(-time.Minute))
	if n > 0 { return nil } // silently throttle; still no signal
	raw, err := s.store.IssueUserToken(ctx, u.ID, "password_reset", s.resetTTL)
	if err != nil { return err }
	link := s.appBaseURL + "/reset-password?token=" + url.QueryEscape(raw)
	return s.sender.Send(ctx, notify.ResetEmail(link))
}

func (s *Service) ResetPassword(ctx context.Context, raw, newPassword string) error {
	uid, err := s.store.ConsumeUserToken(ctx, raw, "password_reset")
	if err != nil { return err } // ErrTokenInvalid
	hash, err := auth.HashPassword(newPassword)
	if err != nil { return err }
	if err := s.store.UpdatePasswordHash(ctx, uid, hash); err != nil { return err }
	return s.store.RevokeAllUserSessions(ctx, uid)
}
```
Add `GetUserByEmail`, `UpdatePasswordHash`, `RevokeAllUserSessions` to the store interface if not already present (confirm Phase 1 names; reuse LogoutAll's underlying query for revocation).

- [ ] **Step 3: Handlers + routes (both public, CSRF-gated)**

`POST /password/forgot {email}` → always 204 (even on error paths that would leak); validate email format. `POST /password/reset {token,new_password}` → `validate:"min=8"` on password; map `ErrTokenInvalid`→400. Mount both in the public sub-router with `RequireCSRF` (matching Phase 1 refresh/logout).

- [ ] **Step 4: Run tests + build**

Run: `... && go test ./internal/app/identity/ -v && go build ./...`
Expected: PASS; build OK.

- [ ] **Step 5: Commit**

```bash
git add internal/app/identity/
git commit -m "feat(identity): password reset (no-leak forgot, reset revokes all sessions)"
```

---

## Task 6: `RequireVerified` middleware + gate sending routes

**Files:**
- Modify: `internal/app/auth/middleware.go` (add `RequireVerified`)
- Modify: `internal/app/campaign/routes.go`, `internal/app/mailbox/routes.go`
- Test: `internal/app/auth/middleware_test.go`

**Interfaces:**
- Consumes: a store lookup for `email_verified_at` by user id — define a tiny interface `VerifiedChecker interface { IsEmailVerified(ctx, userID uuid.UUID) (bool, error) }` satisfied by the identity store; injected where routes are built.
- Produces: `auth.RequireVerified(checker VerifiedChecker) func(http.Handler) http.Handler` → 403 `email_not_verified` when unverified.

- [ ] **Step 1: Write failing middleware test**

Table test with a fake checker: verified user ⇒ next handler runs (200); unverified ⇒ 403 with body `email_not_verified`; missing user in context ⇒ 401. Use `auth.RequireAuth`-populated context (reuse the test helper pattern from Phase 1 middleware tests).

- [ ] **Step 2: Implement `RequireVerified`**

```go
type VerifiedChecker interface {
	IsEmailVerified(ctx context.Context, userID uuid.UUID) (bool, error)
}

func RequireVerified(c VerifiedChecker) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims, ok := ClaimsFromContext(r.Context())
			if !ok { httpx.Error(w, http.StatusUnauthorized, "unauthorized"); return }
			uid, err := uuid.Parse(claims.UserID)
			if err != nil { httpx.Error(w, http.StatusUnauthorized, "unauthorized"); return }
			ok, err = c.IsEmailVerified(r.Context(), uid)
			if err != nil { httpx.Error(w, http.StatusInternalServerError, "verify check failed"); return }
			if !ok { httpx.Error(w, http.StatusForbidden, "email_not_verified"); return }
			next.ServeHTTP(w, r)
		})
	}
}
```
Add `IsEmailVerified` to the identity store (`SELECT email_verified_at IS NOT NULL FROM users WHERE id=$1`).

- [ ] **Step 3: Apply to launch + mailbox-create only**

In `campaign/routes.go`, wrap only the launch route: `r.With(auth.RequireVerified(checker)).Post("/{id}/launch", h.launch)`. In `mailbox/routes.go`, wrap only create: `r.With(auth.RequireVerified(checker)).Post("/", h.create)`. Thread the `checker` (identity store) into these handlers' `Routes()` from `main.go` (Task 8 finalizes wiring; add the param now).

- [ ] **Step 4: Run tests + build**

Run: `... && go test ./internal/app/auth/ ./internal/app/campaign/ ./internal/app/mailbox/ -v && go build ./...`
Expected: PASS; build OK.

- [ ] **Step 5: Commit**

```bash
git add internal/app/auth/middleware.go internal/app/campaign/ internal/app/mailbox/
git commit -m "feat(auth): RequireVerified middleware gating campaign launch + mailbox create"
```

---

## Task 7: Workspace invites (create/list/revoke + accept)

**Files:**
- Create: `internal/app/identity/invite.go` (invite service methods), `invite_handler.go` (handlers)
- Modify: `internal/app/identity/store.go`, `routes.go`
- Test: `internal/app/identity/invite_test.go`

**Interfaces:**
- Consumes: Task 2 invite queries, `notify.InviteEmail`, Phase 1 membership upsert + `RegisterTx`-style account creation, workspace lookup for the name.
- Produces: `Service.CreateInvite(ctx, ws, invitedBy uuid.UUID, email, role string) error` (unique pending per (ws,email)); `Service.ListInvites(ctx, ws uuid.UUID) ([]Invite, error)`; `Service.RevokeInvite(ctx, ws, inviteID uuid.UUID) error`; `Service.AcceptInvite(ctx, rawToken string, password *string) (Session, error)`.

- [ ] **Step 1: Write failing tests**

`TestCreateInviteEmails` (invite persisted pending + email sent with `/accept-invite?token=`), `TestAcceptInviteExistingUserAddsMembership` (email already a user ⇒ membership upserted, invite accepted, no password needed, session returned, email marked verified), `TestAcceptInviteNewUserCreatesAccount` (no user + password given ⇒ user created verified + membership + session), `TestAcceptInviteNewUserRequiresPassword` (no user + nil password ⇒ `ErrPasswordRequired`), `TestAcceptInviteInvalidToken` (⇒ `ErrTokenInvalid`).

- [ ] **Step 2: Implement invite service (`invite.go`)**

`AcceptInvite` runs in ONE transaction: validate token (hash lookup, pending, unexpired) → resolve user by email → if absent require password and create user (argon2id) with `email_verified_at=now()` → upsert `workspace_members(workspace_id, user_id, role)` → `MarkInviteAccepted` → mark existing user's email verified too → issue+return a Session for that workspace. Reuse the Phase 1 session-issuance helper (`newSessionRow`) — this is the provider-agnostic path the OAuth seam reuses. Errors: `ErrPasswordRequired`, `ErrTokenInvalid`. `CreateInvite` maps a unique-violation (existing pending invite) to `ErrInviteExists`→409.

- [ ] **Step 3: Handlers + routes**

Under the PROTECTED group, mounted at `/api/v1/workspaces/{id}/invites`:
`POST` create (`RequireRole(RoleAdmin)`, body `{email, role}`, validate), `GET` list (admin), `DELETE /{inviteId}` revoke (admin). Under the PUBLIC group (CSRF-gated): `POST /api/v1/auth/invites/accept {token, password?}`. Map errors to status codes (409 exists, 404 not-found/invalid, 400 password-required→ actually 422). Add `RequireRole` usage consistent with Phase 1.

- [ ] **Step 4: Run tests + build**

Run: `... && go test ./internal/app/identity/ -v && go build ./...`
Expected: PASS; build OK.

- [ ] **Step 5: Commit**

```bash
git add internal/app/identity/
git commit -m "feat(identity): workspace invites (create/list/revoke + accept-creates-or-links)"
```

---

## Task 8: OpenAPI + wiring + regenerate frontend types

**Files:**
- Modify: `api/openapi.yaml`, `cmd/inroad/main.go`
- Generated: `web/src/store/api.ts`

**Interfaces:**
- Consumes: all prior tasks' handlers.
- Produces: mounted routes + generated RTK Query hooks for the new endpoints.

- [ ] **Step 1: Add OpenAPI paths + schemas**

Add to `api/openapi.yaml`: `/auth/verify-email`, `/auth/verify-email/resend`, `/auth/password/forgot`, `/auth/password/reset`, `/auth/invites/accept`, `/workspaces/{id}/invites` (POST/GET), `/workspaces/{id}/invites/{inviteId}` (DELETE) — with request/response schemas (Invite, session response reuse). Match the existing spec's style + security schemes (bearer / CSRF where applicable).

- [ ] **Step 2: Wire everything in `cmd/inroad/main.go`**

Construct `notify.New(...)` from config; pass the sender + TTLs + base URL into `identity.NewService`/`NewHandler`; thread the identity store as the `RequireVerified` checker into campaign + mailbox `Routes()`; mount the invite routes under the protected group and accept under public. Keep the public/protected split from Phase 1 Task 13.

- [ ] **Step 3: Regenerate FE types + build both**

Run: `cd "C:/Users/Ahmed/OneDrive/Desktop/personal-projects/Inroad-auth" && export PATH="$PATH:/c/Program Files/Go/bin:$HOME/go/bin" && go build ./... && cd web && npm install && npm run gen:api && npx tsc -b --noEmit`
Expected: build OK; codegen adds the new hooks; typecheck clean.

- [ ] **Step 4: Commit**

```bash
git add api/openapi.yaml cmd/inroad/main.go web/src/store/api.ts
git commit -m "feat(api): Phase 2 auth endpoints (verify/reset/invites) + wiring + regen types"
```

---

## Task 9: Frontend — token pages, invites UI, unverified banner

**Files:**
- Create: `web/src/features/auth/verify-email-page.tsx`, `forgot-password-page.tsx`, `reset-password-page.tsx`, `accept-invite-page.tsx`, `features/team/invites-panel.tsx`, `features/auth/unverified-banner.tsx`
- Modify: `web/src/routes/*` (register the four public token routes + team settings), relevant tests

**Interfaces:**
- Consumes: generated RTK Query hooks from Task 8.
- Produces: user-facing flows. Follow existing feature/route conventions; components PascalCase, files kebab-case; `features/*` never import each other.

- [ ] **Step 1: Write failing Vitest for one page (TDD anchor)**

`verify-email-page.test.tsx`: renders, reads `?token=`, calls the verify mutation on mount, shows success/failure states. Mirror the existing `login-form.test.tsx` setup.

- [ ] **Step 2: Implement the four token pages**

Each reads its `?token=` (TanStack Router search param), calls its mutation, renders loading/success/error. `forgot-password` posts an email and always shows "check your inbox". `accept-invite` shows a password field only when the API indicates a new account (or always allow setting one; backend ignores for existing users).

- [ ] **Step 3: Invites panel + unverified banner**

`invites-panel.tsx` (in team settings): list pending invites (admin-only via session role), invite form (email + role), revoke button — using the generated hooks. `unverified-banner.tsx`: shown when the session's user is unverified; "Resend" calls the resend mutation; a 403 `email_not_verified` from a gated action routes the user here.

- [ ] **Step 4: Register routes + run FE checks**

Add the four public routes and the team settings section. Run: `cd web && npx oxlint && npx tsc -b --noEmit && npx vitest run`. Expected: all pass.

- [ ] **Step 5: Commit**

```bash
git add web/src/
git commit -m "feat(web): verify/forgot/reset/accept pages, invites panel, unverified banner"
```

---

## Task 10: Backend integration tests (full flows)

**Files:**
- Create: `internal/app/identity/phase2_integration_test.go` (`//go:build integration`)

- [ ] **Step 1: Write the integration tests**

Against dockerized Postgres (use the existing Phase 1 integration harness/`connect` helper): (a) register → email captured via a real `notify.Sender` fake wired into the service → verify-email → login still works, `email_verified_at` set; (b) forgot → reset → the pre-reset refresh token is now revoked (reuse fails, family revoked); (c) invite → accept new user → `workspace_members` row exists + email verified; invite → accept existing user → membership added; (d) a gated route (`launch`/mailbox create) returns 403 for an unverified user and 200 after verify.

- [ ] **Step 2: Run integration (needs dev DB)**

Run: `cd "C:/Users/Ahmed/OneDrive/Desktop/personal-projects/Inroad-auth" && export PATH="$PATH:/c/Program Files/Go/bin:$HOME/go/bin" && make db-up && go test -tags=integration ./internal/app/identity/... -v`
Expected: PASS. If Docker unavailable, report NOT-RUN and flag the coordinator.

- [ ] **Step 3: Full verification + commit**

Run: `go build ./... && go vet ./... && gofmt -l internal cmd && go test ./... && cd web && npx vitest run`.
```bash
git add internal/app/identity/phase2_integration_test.go
git commit -m "test(identity): Phase 2 integration — verify/reset/invite/gated-route flows"
```
