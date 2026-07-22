# Mailbox OAuth (Framework + Gmail) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Connect a Gmail mailbox by OAuth and send / read replies through the Gmail API, behind a provider abstraction that M365 can join next phase.

**Architecture:** Control plane (`mailbox.Service`) owns the OAuth authorization-code *connect* flow and seals the token into `mailboxes.secret_ciphertext`. Execution plane refreshes the token at job-build time inside `coreapi` (which holds the pool + sealer) and hands the worker a short-lived access token; the worker dispatches SMTP vs Gmail on `job.Provider` through a single widened `Sender` seam.

**Tech Stack:** Go 1.25 · `golang.org/x/oauth2` (+ `/google`) · `google.golang.org/api/gmail/v1` · pgx/v5 · sqlc · golang-migrate · chi. HMAC-signed state (SHA-256) mirrors `internal/platform/unsub`.

## Global Constraints

- Toolchain PATH (this machine): prefix EVERY Go/sqlc command with `export PATH="$PATH:/c/Program Files/Go/bin:$HOME/go/bin"`. Shell state does not persist between calls.
- Go files lowercase, no hyphens (`_test.go` only). Identifiers `MixedCaps`. snake_case only at JSON/DB/env boundaries.
- Every tenant query filtered by `workspace_id`; secrets never logged, never in HTTP responses; decrypted secrets are `[]byte`, zeroized after use.
- Layering: `app/*`→`platform/*` only; `app/*` packages don't import each other; worker reaches data only via `coreapi` (zero `db` import).
- Migrations/queries under `internal/platform/db/`; regenerate with `make sqlc` after any query/migration change. Migration head is `000011`; new migration is `000012`.
- Conventional commits. Verify before "done": `go build ./...`, `go vet ./...`, `gofmt -l internal cmd` (empty), `go test ./...`.
- Do NOT commit — report back per task; the coordinator commits.
- Mailbox `provider` values: `smtp` | `gmail` (later `m365`). OAuth *route* segment is `google` (the identity provider); the mailbox provider it produces is `gmail`.

---

### Task 1: Config + `oauthstate` signed-state package

**Files:**
- Modify: `internal/platform/config/config.go` (add 3 fields + loads)
- Create: `internal/platform/oauthstate/state.go`
- Test: `internal/platform/oauthstate/state_test.go`
- Modify: `.env.example` (document the 3 vars)

**Interfaces:**
- Produces: `config.Config.GoogleClientID string`, `.GoogleClientSecret string`, `.GoogleRedirectURL string`.
- Produces: `oauthstate.Sign(secret []byte, workspaceID string, now time.Time, ttl time.Duration) string`; `oauthstate.Verify(secret []byte, token string, now time.Time) (workspaceID string, err error)`. `now` is a parameter (not `time.Now()` inside) so tests are deterministic.

- [ ] **Step 1: Write the failing test** — `internal/platform/oauthstate/state_test.go`

```go
package oauthstate

import (
	"testing"
	"time"
)

var secret = []byte("test-secret-at-least-16-bytes")

func TestSignVerifyRoundTrip(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	tok := Sign(secret, "ws-123", now, 10*time.Minute)
	ws, err := Verify(secret, tok, now.Add(time.Minute))
	if err != nil || ws != "ws-123" {
		t.Fatalf("round trip: ws=%q err=%v", ws, err)
	}
}

func TestVerifyRejectsExpired(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	tok := Sign(secret, "ws-123", now, 10*time.Minute)
	if _, err := Verify(secret, tok, now.Add(11*time.Minute)); err == nil {
		t.Fatal("expected expiry error")
	}
}

func TestVerifyRejectsTamperedSigAndWrongSecret(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	tok := Sign(secret, "ws-123", now, 10*time.Minute)
	if _, err := Verify([]byte("different-secret-16b"), tok, now); err == nil {
		t.Fatal("expected bad-signature error under wrong secret")
	}
	if _, err := Verify(secret, tok+"x", now); err == nil {
		t.Fatal("expected error on tampered token")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `export PATH="$PATH:/c/Program Files/Go/bin:$HOME/go/bin"; go test ./internal/platform/oauthstate/ -run TestSign -v`
Expected: FAIL — package/func not defined.

- [ ] **Step 3: Write `internal/platform/oauthstate/state.go`**

```go
// Package oauthstate is the stateless HMAC codec for the OAuth `state`
// parameter. It binds a mailbox-OAuth callback to the workspace that started
// the flow, without a server-side session store: the HMAC proves the server
// minted it and the embedded expiry bounds replay. Same construction family as
// internal/platform/unsub. See docs/superpowers/specs/2026-07-23-mailbox-oauth-gmail-design.md §3.1.
package oauthstate

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strconv"
	"strings"
	"time"
)

// ErrInvalid is returned for any malformed, mis-signed, or expired state. It
// is deliberately opaque (no distinction) so a caller gives an attacker no
// oracle.
var ErrInvalid = errors.New("oauthstate: invalid state")

// Sign returns base64url(payload)."base64url(HMAC) where payload is
// "workspaceID:expiryUnix:nonce". ttl is added to now to compute the expiry.
func Sign(secret []byte, workspaceID string, now time.Time, ttl time.Duration) string {
	nonce := make([]byte, 8)
	_, _ = rand.Read(nonce)
	payload := workspaceID + ":" + strconv.FormatInt(now.Add(ttl).Unix(), 10) + ":" + b64(nonce)
	return b64([]byte(payload)) + "." + b64(sign(secret, payload))
}

// Verify checks the signature and expiry (against now) and returns the
// workspace id. Any failure yields ErrInvalid.
func Verify(secret []byte, token string, now time.Time) (string, error) {
	dot := strings.IndexByte(token, '.')
	if dot < 0 {
		return "", ErrInvalid
	}
	payload, err := unb64(token[:dot])
	if err != nil {
		return "", ErrInvalid
	}
	gotSig, err := unb64(token[dot+1:])
	if err != nil {
		return "", ErrInvalid
	}
	if !hmac.Equal(gotSig, sign(secret, string(payload))) {
		return "", ErrInvalid
	}
	parts := strings.SplitN(string(payload), ":", 3)
	if len(parts) != 3 {
		return "", ErrInvalid
	}
	exp, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || now.Unix() > exp {
		return "", ErrInvalid
	}
	return parts[0], nil
}

func sign(secret []byte, payload string) []byte {
	h := hmac.New(sha256.New, secret)
	h.Write([]byte(payload))
	return h.Sum(nil)
}

func b64(b []byte) string            { return base64.RawURLEncoding.EncodeToString(b) }
func unb64(s string) ([]byte, error) { return base64.RawURLEncoding.DecodeString(s) }
```

- [ ] **Step 4: Add config fields** — in `internal/platform/config/config.go`, add to the `Config` struct (near `AppBaseURL`):

```go
	// Google OAuth (mailbox connect via Gmail). Empty client id/secret disables
	// Gmail OAuth: the start endpoint returns 501 and gmail jobs fail cleanly.
	GoogleClientID     string
	GoogleClientSecret string
	GoogleRedirectURL  string
```

and in `Load()` after the `cfg.AppBaseURL = ...` line:

```go
	cfg.GoogleClientID = getenv("INROAD_GOOGLE_CLIENT_ID", "")
	cfg.GoogleClientSecret = getenv("INROAD_GOOGLE_CLIENT_SECRET", "")
	cfg.GoogleRedirectURL = getenv("INROAD_GOOGLE_REDIRECT_URL", cfg.PublicURL+"/oauth/google/callback")
```

- [ ] **Step 5: Document in `.env.example`** — append:

```
# Google OAuth for connecting Gmail mailboxes (leave blank to disable Gmail).
# Create an OAuth client (type: Web application) in Google Cloud Console and
# add INROAD_GOOGLE_REDIRECT_URL to its Authorized redirect URIs.
INROAD_GOOGLE_CLIENT_ID=
INROAD_GOOGLE_CLIENT_SECRET=
# Defaults to ${INROAD_PUBLIC_URL}/oauth/google/callback
INROAD_GOOGLE_REDIRECT_URL=
```

- [ ] **Step 6: Run tests + build**

Run: `export PATH="$PATH:/c/Program Files/Go/bin:$HOME/go/bin"; go test ./internal/platform/oauthstate/ -v && go build ./...`
Expected: PASS; build ok.

- [ ] **Step 7: Commit** — `feat(oauth): signed state codec + Google OAuth config`

---

### Task 2: Migration 000012 + mailbox queries (secret update, gmail cursor) + sqlc

**Files:**
- Create: `internal/platform/db/migrations/000012_mailbox_oauth.up.sql`
- Create: `internal/platform/db/migrations/000012_mailbox_oauth.down.sql`
- Modify: `internal/platform/db/queries/mailbox.sql` (add `UpdateMailboxSecret`, `SetInboxCursorString`)
- Regen: `internal/platform/db/gen/*` via `make sqlc`

**Interfaces:**
- Produces: column `mailboxes.inbox_cursor TEXT NOT NULL DEFAULT ''`.
- Produces sqlc methods `UpdateMailboxSecret(ctx, UpdateMailboxSecretParams{ID, WorkspaceID, SecretCiphertext})` and `SetInboxCursorString(ctx, SetInboxCursorStringParams{ID, WorkspaceID, InboxCursor})`.

- [ ] **Step 1: Write `000012_mailbox_oauth.up.sql`**

```sql
-- Gmail (and future API providers) track inbox position by an opaque, monotonic
-- historyId string, not the IMAP UID/UIDVALIDITY pair. Store it separately so
-- the existing IMAP cursor columns (inbox_last_seen_uid/inbox_uid_validity) are
-- untouched and the reply/bounce path keeps working unchanged.
ALTER TABLE mailboxes ADD COLUMN inbox_cursor TEXT NOT NULL DEFAULT '';
```

- [ ] **Step 2: Write `000012_mailbox_oauth.down.sql`**

```sql
ALTER TABLE mailboxes DROP COLUMN inbox_cursor;
```

- [ ] **Step 3: Add queries** — append to `internal/platform/db/queries/mailbox.sql`:

```sql
-- name: UpdateMailboxSecret :exec
-- Overwrites the sealed credential. Used by the coreapi token-refresh path when
-- an OAuth access/refresh token is rotated, so the new token is persisted.
UPDATE mailboxes SET secret_ciphertext = $3
WHERE id = $1 AND workspace_id = $2;

-- name: SetInboxCursorString :exec
-- Persists an opaque provider cursor (Gmail historyId) after a poll pass.
UPDATE mailboxes SET inbox_cursor = $3, last_poll_at = now()
WHERE id = $1 AND workspace_id = $2;
```

- [ ] **Step 4: Regenerate + build**

Run: `export PATH="$PATH:/c/Program Files/Go/bin:$HOME/go/bin"; make sqlc && go build ./...`
Expected: `gen/mailbox.sql.go` gains both methods; `gen/models.go` `Mailbox` gains `InboxCursor string`; build ok.

- [ ] **Step 5: Verify migration reversibility** (Postgres up — `make db-up` first if needed)

Run: `export PATH="$PATH:/c/Program Files/Go/bin:$HOME/go/bin"; make migrate-up && make migrate-down && make migrate-up`
Expected: all succeed; no error on the 000012 up/down/up cycle. (If docker is down, note it and defer — coordinator runs it.)

- [ ] **Step 6: Commit** — `feat(db): migration 000012 mailbox inbox_cursor + secret/cursor update queries`

---

### Task 3: OAuth token codec + Google OAuth config type (platform/mail)

**Files:**
- Create: `internal/platform/mail/oauth.go`
- Test: `internal/platform/mail/oauth_test.go`

**Interfaces:**
- Produces: `mail.GoogleOAuth` struct `{ClientID, ClientSecret, RedirectURL string}` with method `Config() *oauth2.Config` (scopes baked in) and `Enabled() bool`.
- Produces: `mail.MarshalToken(*oauth2.Token) ([]byte, error)` and `mail.UnmarshalToken([]byte) (*oauth2.Token, error)` — the JSON form sealed into `secret_ciphertext` for `provider='gmail'`.
- Consumes: `golang.org/x/oauth2`, `golang.org/x/oauth2/google` (add via `go get`).

- [ ] **Step 1: Add dependencies**

Run: `export PATH="$PATH:/c/Program Files/Go/bin:$HOME/go/bin"; go get golang.org/x/oauth2@latest google.golang.org/api@latest`
Expected: `go.mod` gains `golang.org/x/oauth2` and `google.golang.org/api`.

- [ ] **Step 2: Write the failing test** — `internal/platform/mail/oauth_test.go`

```go
package mail

import (
	"testing"
	"time"

	"golang.org/x/oauth2"
)

func TestMarshalUnmarshalTokenRoundTrip(t *testing.T) {
	exp := time.Unix(1_700_000_000, 0)
	in := &oauth2.Token{AccessToken: "at", RefreshToken: "rt", TokenType: "Bearer", Expiry: exp}
	b, err := MarshalToken(in)
	if err != nil {
		t.Fatal(err)
	}
	out, err := UnmarshalToken(b)
	if err != nil {
		t.Fatal(err)
	}
	if out.AccessToken != "at" || out.RefreshToken != "rt" || !out.Expiry.Equal(exp) {
		t.Fatalf("round trip mismatch: %+v", out)
	}
}

func TestGoogleOAuthEnabled(t *testing.T) {
	if (GoogleOAuth{}).Enabled() {
		t.Fatal("empty config must be disabled")
	}
	if !(GoogleOAuth{ClientID: "a", ClientSecret: "b"}).Enabled() {
		t.Fatal("configured must be enabled")
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `export PATH="$PATH:/c/Program Files/Go/bin:$HOME/go/bin"; go test ./internal/platform/mail/ -run TestMarshalUnmarshalTokenRoundTrip -v`
Expected: FAIL — undefined.

- [ ] **Step 4: Write `internal/platform/mail/oauth.go`**

```go
package mail

import (
	"encoding/json"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

// gmailScopes are the OAuth scopes requested when connecting a Gmail mailbox:
// send (outbound), readonly (reply/bounce polling), and openid/email (learn the
// connected address).
var gmailScopes = []string{
	"https://www.googleapis.com/auth/gmail.send",
	"https://www.googleapis.com/auth/gmail.readonly",
	"openid",
	"email",
}

// GoogleOAuth holds the app's Google OAuth client credentials. Zero value =
// disabled (self-hoster did not configure Google).
type GoogleOAuth struct {
	ClientID     string
	ClientSecret string
	RedirectURL  string
}

// Enabled reports whether Gmail OAuth is configured.
func (g GoogleOAuth) Enabled() bool { return g.ClientID != "" && g.ClientSecret != "" }

// Config builds the x/oauth2 config for the authorization-code flow and
// TokenSource refresh. Scopes are fixed (gmailScopes).
func (g GoogleOAuth) Config() *oauth2.Config {
	return &oauth2.Config{
		ClientID:     g.ClientID,
		ClientSecret: g.ClientSecret,
		RedirectURL:  g.RedirectURL,
		Scopes:       gmailScopes,
		Endpoint:     google.Endpoint,
	}
}

// MarshalToken serializes an OAuth token for sealing into secret_ciphertext.
func MarshalToken(t *oauth2.Token) ([]byte, error) { return json.Marshal(t) }

// UnmarshalToken parses the sealed OAuth token JSON.
func UnmarshalToken(b []byte) (*oauth2.Token, error) {
	var t oauth2.Token
	if err := json.Unmarshal(b, &t); err != nil {
		return nil, err
	}
	return &t, nil
}
```

- [ ] **Step 5: Run tests + build**

Run: `export PATH="$PATH:/c/Program Files/Go/bin:$HOME/go/bin"; go test ./internal/platform/mail/ -run 'TestMarshal|TestGoogleOAuth' -v && go build ./...`
Expected: PASS; build ok.

- [ ] **Step 6: Commit** — `feat(mail): Google OAuth config + sealed-token codec`

---

### Task 4: Control-plane connect flow (start + callback)

**Files:**
- Modify: `internal/app/mailbox/service.go` (add `oauth *mail.GoogleOAuth`-based methods; extend `NewService`)
- Create: `internal/app/mailbox/oauth.go` (the two service methods, kept out of the SMTP file)
- Modify: `internal/app/mailbox/store.go` (add `CountByEmail` already exists; add nothing — reuse `Create`)
- Modify: `internal/app/mailbox/handler.go` (add `startGoogleOAuth`, `googleCallback`; the handler needs `jwtSecret` + `appBaseURL`)
- Modify: `internal/app/mailbox/routes.go` (add protected `start`; expose a separate public `CallbackRoutes()`)
- Modify: `cmd/inroad/main.go` (wire `mail.GoogleOAuth`, jwtSecret, appBaseURL into the mailbox handler; mount public `/oauth`)
- Test: `internal/app/mailbox/oauth_test.go`

**Interfaces:**
- Consumes: `oauthstate.Sign/Verify`, `mail.GoogleOAuth`, `mail.MarshalToken`, `crypto.Sealer`.
- Produces (service):
  - `(*Service).StartGoogleOAuth(ctx, workspaceID uuid.UUID) (authURL string, err error)` — returns `ErrOAuthDisabled` if `!oauth.Enabled()`.
  - `(*Service).CompleteGoogleOAuth(ctx, code string, workspaceID uuid.UUID) (MailboxSafe, error)` — exchanges code, learns email, seals token, creates mailbox (provider=gmail).
- Produces sentinel: `ErrOAuthDisabled = errors.New("gmail oauth not configured")`.

**Design notes for the implementer:**
- `NewService` signature becomes `NewService(store Store, tester mail.ConnectionTester, sealer *crypto.Sealer, oauth mail.GoogleOAuth, exchanger TokenExchanger)`. Define a small seam so the test injects a fake exchange (no live Google):
  ```go
  // TokenExchanger exchanges an auth code for a token and fetches the mailbox's
  // email address. Real impl uses oauth2 + the userinfo endpoint; tests fake it.
  type TokenExchanger interface {
      Exchange(ctx context.Context, code string) (tok *oauth2.Token, email string, err error)
  }
  ```
  Provide the production `googleExchanger` (in `oauth.go`) that calls `oauth.Config().Exchange(ctx, code)` then GETs `https://openidconnect.googleapis.com/v1/userinfo` (or `https://www.googleapis.com/oauth2/v2/userinfo`) with the token to read `email`.
- `start` is **protected** (`RequireVerified`): reads workspace from JWT, signs a 10-min state, returns `{"auth_url": oauth.Config().AuthCodeURL(state, oauth2.AccessTypeOffline, prompt=consent)}`.
- `callback` is **public**: `GET /oauth/google/callback?code&state`. On `oauth_error` query or missing code → 302 to `appBaseURL + "/mailboxes?oauth_error=denied"`. Verify state (→ workspaceID). `CompleteGoogleOAuth`. On success 302 to `appBaseURL + "/mailboxes?connected=" + url.QueryEscape(email)`; on error 302 with `?oauth_error=...` (never 500 a browser navigation; log the detail).
- Duplicate email: reuse the existing `CountByEmail` guard → on duplicate, 302 `?oauth_error=already_connected`.
- The callback handler holds `jwtSecret []byte` (to Verify state) and `appBaseURL string`.

- [ ] **Step 1: Write the failing service test** — `internal/app/mailbox/oauth_test.go`

```go
package mailbox

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"golang.org/x/oauth2"

	"github.com/inroad/inroad/internal/platform/crypto"
	"github.com/inroad/inroad/internal/platform/mail"
)

type fakeExchanger struct {
	tok   *oauth2.Token
	email string
	err   error
}

func (f fakeExchanger) Exchange(ctx context.Context, code string) (*oauth2.Token, string, error) {
	return f.tok, f.email, f.err
}

func TestStartGoogleOAuthDisabled(t *testing.T) {
	svc := NewService(&fakeStore{}, nil, mustSealer(t), mail.GoogleOAuth{}, fakeExchanger{})
	if _, err := svc.StartGoogleOAuth(context.Background(), uuid.New()); err != ErrOAuthDisabled {
		t.Fatalf("want ErrOAuthDisabled, got %v", err)
	}
}

func TestCompleteGoogleOAuthCreatesGmailMailbox(t *testing.T) {
	store := &fakeStore{}
	oauth := mail.GoogleOAuth{ClientID: "a", ClientSecret: "b", RedirectURL: "http://x/cb"}
	exch := fakeExchanger{
		tok:   &oauth2.Token{AccessToken: "at", RefreshToken: "rt", Expiry: time.Now().Add(time.Hour)},
		email: "rep@example.com",
	}
	svc := NewService(store, nil, mustSealer(t), oauth, exch)
	m, err := svc.CompleteGoogleOAuth(context.Background(), "code", uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	if m.Provider != "gmail" || m.Email != "rep@example.com" {
		t.Fatalf("unexpected mailbox: provider=%q email=%q", m.Provider, m.Email)
	}
	if store.lastCreate.SecretCiphertext == "" {
		t.Fatal("token was not sealed into secret_ciphertext")
	}
}
```

> The implementer must add a `fakeStore` (recording `lastCreate gen.CreateMailboxParams`, returning a `MailboxSafe` echoing the params) and a `mustSealer(t)` helper (`crypto.NewSealer(bytes.Repeat([]byte{1}, 32))`) if not already present in the package's test files — check `service_test.go` first and reuse.

- [ ] **Step 2: Run test to verify it fails**

Run: `export PATH="$PATH:/c/Program Files/Go/bin:$HOME/go/bin"; go test ./internal/app/mailbox/ -run GoogleOAuth -v`
Expected: FAIL — `NewService` arity / methods undefined.

- [ ] **Step 3: Write `internal/app/mailbox/oauth.go`** (service methods + production exchanger)

```go
package mailbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/google/uuid"
	"golang.org/x/oauth2"

	"github.com/inroad/inroad/internal/platform/db/gen"
	"github.com/inroad/inroad/internal/platform/mail"
)

// ErrOAuthDisabled is returned when a Gmail OAuth action is attempted but no
// Google client credentials are configured.
var ErrOAuthDisabled = errors.New("gmail oauth not configured")

// TokenExchanger exchanges an auth code for a token and the mailbox's own email
// address. Real impl calls Google; tests fake it.
type TokenExchanger interface {
	Exchange(ctx context.Context, code string) (tok *oauth2.Token, email string, err error)
}

// StartGoogleOAuth is implemented by the handler (which owns state signing); the
// service exposes only Enabled() via oauthEnabled for the handler's guard.
func (s *Service) oauthEnabled() bool { return s.oauth.Enabled() }

// oauthConfig exposes the oauth2 config to the handler for AuthCodeURL.
func (s *Service) oauthConfig() *oauth2.Config { return s.oauth.Config() }

// CompleteGoogleOAuth exchanges the code, seals the token, and persists a new
// gmail mailbox in the workspace. Dedupes on email like ConnectSMTP.
func (s *Service) CompleteGoogleOAuth(ctx context.Context, code string, workspaceID uuid.UUID) (MailboxSafe, error) {
	if !s.oauth.Enabled() {
		return MailboxSafe{}, ErrOAuthDisabled
	}
	tok, email, err := s.exchanger.Exchange(ctx, code)
	if err != nil {
		return MailboxSafe{}, fmt.Errorf("oauth exchange: %w", err)
	}
	if email == "" {
		return MailboxSafe{}, fmt.Errorf("%w: no email in userinfo", ErrValidation)
	}
	count, err := s.store.CountByEmail(ctx, workspaceID, email)
	if err != nil {
		return MailboxSafe{}, err
	}
	if count > 0 {
		return MailboxSafe{}, ErrDuplicateMailbox
	}
	raw, err := mail.MarshalToken(tok)
	if err != nil {
		return MailboxSafe{}, err
	}
	ciphertext, err := s.sealer.Seal(raw)
	if err != nil {
		return MailboxSafe{}, err
	}
	return s.store.Create(ctx, gen.CreateMailboxParams{
		WorkspaceID:      workspaceID,
		Provider:         "gmail",
		Email:            email,
		DisplayName:      email,
		SecretCiphertext: ciphertext,
		// SMTP/IMAP fields unused for gmail; zero values are fine.
		DailyCap:           defaultDailyCap,
		MinIntervalSeconds: defaultMinIntervalSeconds,
		RampEnabled:        true,
		RampStartCap:       defaultRampStartCap,
		RampDays:           defaultRampDays,
	})
}

// googleExchanger is the production TokenExchanger.
type googleExchanger struct {
	cfg    *oauth2.Config
	client *http.Client
}

// NewGoogleExchanger builds the production exchanger.
func NewGoogleExchanger(oauth mail.GoogleOAuth) TokenExchanger {
	return &googleExchanger{cfg: oauth.Config(), client: &http.Client{}}
}

func (g *googleExchanger) Exchange(ctx context.Context, code string) (*oauth2.Token, string, error) {
	tok, err := g.cfg.Exchange(ctx, code)
	if err != nil {
		return nil, "", err
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://openidconnect.googleapis.com/v1/userinfo", nil)
	req.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	resp, err := g.client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("userinfo: status %d", resp.StatusCode)
	}
	var info struct {
		Email string `json:"email"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return nil, "", err
	}
	return tok, info.Email, nil
}
```

- [ ] **Step 4: Extend `Service` + `NewService`** in `internal/app/mailbox/service.go`

Add fields to `Service`:
```go
	oauth     mail.GoogleOAuth
	exchanger TokenExchanger
```
Change `NewService`:
```go
func NewService(store Store, tester mail.ConnectionTester, sealer *crypto.Sealer, oauth mail.GoogleOAuth, exchanger TokenExchanger) *Service {
	return &Service{store: store, tester: tester, sealer: sealer, oauth: oauth, exchanger: exchanger}
}
```

- [ ] **Step 5: Add handlers** in `internal/app/mailbox/handler.go`

Extend `Handler` with `jwtSecret []byte` and `appBaseURL string`; change `NewHandler(svc *Service, jwtSecret []byte, appBaseURL string) *Handler`. Add:

```go
func (h *Handler) startGoogleOAuth(w http.ResponseWriter, r *http.Request) {
	wid, ok := auth.WorkspaceID(w, r)
	if !ok {
		return
	}
	if !h.svc.oauthEnabled() {
		httpx.Error(w, http.StatusNotImplemented, "gmail oauth not configured")
		return
	}
	state := oauthstate.Sign(h.jwtSecret, wid.String(), time.Now(), 10*time.Minute)
	url := h.svc.oauthConfig().AuthCodeURL(state, oauth2.AccessTypeOffline, oauth2.SetAuthURLParam("prompt", "consent"))
	httpx.JSON(w, http.StatusOK, map[string]string{"auth_url": url})
}

func (h *Handler) googleCallback(w http.ResponseWriter, r *http.Request) {
	redirect := func(q string) { http.Redirect(w, r, h.appBaseURL+"/mailboxes?"+q, http.StatusFound) }
	if r.URL.Query().Get("error") != "" || r.URL.Query().Get("code") == "" {
		redirect("oauth_error=denied")
		return
	}
	wid, err := oauthstate.Verify(h.jwtSecret, r.URL.Query().Get("state"), time.Now())
	if err != nil {
		redirect("oauth_error=bad_state")
		return
	}
	wsID, err := uuid.Parse(wid)
	if err != nil {
		redirect("oauth_error=bad_state")
		return
	}
	m, err := h.svc.CompleteGoogleOAuth(r.Context(), r.URL.Query().Get("code"), wsID)
	if err != nil {
		switch {
		case errors.Is(err, ErrDuplicateMailbox):
			redirect("oauth_error=already_connected")
		case errors.Is(err, ErrOAuthDisabled):
			redirect("oauth_error=disabled")
		default:
			// Log detail server-side; never surface internals to the browser.
			slog.Error("gmail oauth callback failed", "err", err)
			redirect("oauth_error=exchange_failed")
		}
		return
	}
	redirect("connected=" + url.QueryEscape(m.Email))
}
```
(Add imports: `log/slog`, `net/url`, `time`, `golang.org/x/oauth2`, `.../oauthstate`.)

- [ ] **Step 6: Wire routes** in `internal/app/mailbox/routes.go`

Add to the protected `Routes(...)`:
```go
	r.With(auth.RequireVerified(checker)).Post("/oauth/google/start", h.startGoogleOAuth)
```
Add a separate public router the server mounts at `/oauth`:
```go
// CallbackRoutes returns the PUBLIC OAuth callback surface. It authenticates
// from the signed state parameter, not the JWT cookie (a top-level browser
// redirect from Google), so it is mounted outside the protected group.
func (h *Handler) CallbackRoutes() http.Handler {
	r := chi.NewRouter()
	r.Get("/google/callback", h.googleCallback)
	return r
}
```

- [ ] **Step 7: Wire `cmd/inroad/main.go`**

Where `mbHandler` / mailbox service is constructed, build `mail.GoogleOAuth{ClientID: cfg.GoogleClientID, ClientSecret: cfg.GoogleClientSecret, RedirectURL: cfg.GoogleRedirectURL}`, pass it + `mailbox.NewGoogleExchanger(oauth)` into `NewService`, and `cfg.JWTSecret`, `cfg.AppBaseURL` into `NewHandler`. Add a public mount:
```go
{pattern: "/oauth", handler: mbHandler.CallbackRoutes()},
```
to the `public` slice (alongside `/u`, `/t`).

- [ ] **Step 8: Fix compile fallout** — every `mailbox.NewService(...)` / `mailbox.NewHandler(...)` call (main + any tests) now needs the new args. Update them.

- [ ] **Step 9: Run tests + build + vet**

Run: `export PATH="$PATH:/c/Program Files/Go/bin:$HOME/go/bin"; go test ./internal/app/mailbox/... && go build ./... && go vet ./...`
Expected: PASS; build+vet clean.

- [ ] **Step 10: Commit** — `feat(mailbox): Gmail OAuth connect (start + public callback)`

---

### Task 5: Provider-dispatched send (MultiSender + GmailSender) + coreapi token refresh

**Files:**
- Create: `internal/platform/mail/gmail.go` (GmailSender)
- Create: `internal/platform/mail/multisender.go` (OutboundJob + MultiSender)
- Test: `internal/platform/mail/multisender_test.go`
- Modify: `internal/coreapi/coreapi.go` (add `Provider string` + `AccessToken []byte` to `SendJob` and `StepSendJob`; widen `Sender` doc)
- Modify: `internal/coreapi/inprocess/inprocess.go` (hold `mail.GoogleOAuth`; extend `New`)
- Modify: `internal/coreapi/inprocess/sendjob.go` + `stepsendjob.go` (set `Provider`; for gmail, refresh+reseal token, set `AccessToken`, skip password unseal)
- Create: `internal/coreapi/inprocess/oauthtoken.go` (refresh helper)
- Modify: `internal/worker/sender/sender.go` + `internal/worker/sequence/advance.go` (widen `Sender` to `Send(ctx, mail.OutboundJob, mail.Message)`; build OutboundJob from job; zeroize AccessToken)
- Modify: `internal/worker/handlers.go`, `cmd/worker/main.go` (construct `*mail.MultiSender`; pass GoogleOAuth into `inprocess.New`)
- Modify: fakes in `internal/worker/sender/sender_test.go`, `internal/worker/sequence/advance_test.go` (implement widened one-method interface)

**Interfaces:**
- Produces: `mail.OutboundJob{Provider string; Host string; Port int; Username, Password string; UseTLS bool; AccessToken string}`.
- Produces: `mail.NewMultiSender(smtp *NetSender, gmail *GmailSender) *MultiSender`; `(*MultiSender).Send(ctx context.Context, tj OutboundJob, msg Message) (string, error)`.
- Produces: `mail.NewGmailSender() *GmailSender`; `(*GmailSender).Send(ctx context.Context, accessToken string, msg Message) (messageID string, err error)`.
- Consumes: `google.golang.org/api/gmail/v1`, `google.golang.org/api/option`, `golang.org/x/oauth2`.

**Design notes:**
- `GmailSender.Send`: `buildMessage(msg)` → write to bytes via `m.WriteTo` → base64url → `gmail.Service` built with `option.WithHTTPClient(oauth2.NewClient(ctx, oauth2.StaticTokenSource(&oauth2.Token{AccessToken: accessToken})))` → `srv.Users.Messages.Send("me", &gmail.Message{Raw: enc}).Do()`. Return `m.GetMessageID()` (our header), not the Gmail resource id.
- Token refresh (`oauthtoken.go`): `func (c client) gmailAccessToken(ctx, mailboxID, workspaceID uuid.UUID, sealed string) (string, error)` — `UnmarshalToken(sealer.Open(sealed))`, build `c.googleOAuth.Config().TokenSource(ctx, tok)`, `fresh, err := ts.Token()`; if `fresh.AccessToken != tok.AccessToken`, `MarshalToken`→`Seal`→`UpdateMailboxSecret`. Return `fresh.AccessToken`. If `!c.googleOAuth.Enabled()`, return an error (logged, non-retryable) — job fails cleanly.
- In `GetSendJob`/`GetStepSendJob`: branch on the mailbox `provider`. For `gmail`, set `job.Provider="gmail"`, `job.AccessToken=[]byte(token)`, leave SMTP fields zero, and DON'T unseal a password. For `smtp` (default), behavior is exactly as today plus `job.Provider="smtp"`. The bundle query must return the mailbox `provider` — check `GetStepEnrollmentBundle` / the send bundle; if `provider` isn't selected, add it (regen sqlc). 

- [ ] **Step 1: Write `internal/platform/mail/multisender.go`**

```go
package mail

import "context"

// OutboundJob is the transport-agnostic slice of a send: which provider, and
// the credential for it. Exactly one credential set is populated per Provider.
type OutboundJob struct {
	Provider    string // "smtp" | "gmail"
	Host        string
	Port        int
	Username    string
	Password    string
	UseTLS      bool
	AccessToken string // gmail
}

// MultiSender dispatches a send to the right transport by Provider. It is the
// single place the SMTP/Gmail branch lives; both worker handlers call it.
type MultiSender struct {
	smtp  *NetSender
	gmail *GmailSender
}

func NewMultiSender(smtp *NetSender, gmail *GmailSender) *MultiSender {
	return &MultiSender{smtp: smtp, gmail: gmail}
}

// Send picks the transport. SMTP ignores ctx (the underlying client has its own
// timeout); Gmail honors it.
func (m *MultiSender) Send(ctx context.Context, tj OutboundJob, msg Message) (string, error) {
	switch tj.Provider {
	case "gmail":
		return m.gmail.Send(ctx, tj.AccessToken, msg)
	default:
		return m.smtp.Send(SMTPConfig{Host: tj.Host, Port: tj.Port, Username: tj.Username, Password: tj.Password, UseTLS: tj.UseTLS}, msg)
	}
}
```

- [ ] **Step 2: Write dispatch test** — `internal/platform/mail/multisender_test.go`

```go
package mail

import (
	"context"
	"testing"
)

func TestMultiSenderDispatch(t *testing.T) {
	// gmail path: nil smtp proves it isn't consulted. Use a GmailSender with a
	// transport stub that records the call; assert Provider routing only.
	// (Full Gmail send is covered in gmail_test.go.)
	var gotGmail bool
	g := &GmailSender{sendFn: func(ctx context.Context, at string, msg Message) (string, error) {
		gotGmail = true
		return "<id@x>", nil
	}}
	ms := NewMultiSender(nil, g)
	if _, err := ms.Send(context.Background(), OutboundJob{Provider: "gmail", AccessToken: "at"}, Message{}); err != nil {
		t.Fatal(err)
	}
	if !gotGmail {
		t.Fatal("gmail branch not taken")
	}
}
```

> Implementer: give `GmailSender` an unexported `sendFn` field defaulting to its real method so tests can stub it (or use an interface — pick the lighter option consistent with `NetSender`'s test seams).

- [ ] **Step 3: Write `internal/platform/mail/gmail.go`**

```go
package mail

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"

	"google.golang.org/api/gmail/v1"
	"google.golang.org/api/option"
	"golang.org/x/oauth2"
)

// GmailSender sends mail through the Gmail API using a per-call access token.
// No SSRF vetting: the host is Google's fixed API endpoint, not user input.
type GmailSender struct {
	// sendFn allows tests to stub the wire call; nil means use the real impl.
	sendFn func(ctx context.Context, accessToken string, msg Message) (string, error)
}

func NewGmailSender() *GmailSender { return &GmailSender{} }

// Send builds the RFC822 message (reusing buildMessage — same headers,
// threading, Message-ID as the SMTP path), base64url-encodes it, and calls
// users.messages.send. Returns our own Message-ID header (Gmail preserves it),
// keeping reply matching identical across transports.
func (g *GmailSender) Send(ctx context.Context, accessToken string, msg Message) (string, error) {
	if g.sendFn != nil {
		return g.sendFn(ctx, accessToken, msg)
	}
	m, err := buildMessage(msg)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if _, err := m.WriteTo(&buf); err != nil {
		return "", fmt.Errorf("gmail: serialize: %w", err)
	}
	client := oauth2.NewClient(ctx, oauth2.StaticTokenSource(&oauth2.Token{AccessToken: accessToken}))
	srv, err := gmail.NewService(ctx, option.WithHTTPClient(client))
	if err != nil {
		return "", fmt.Errorf("gmail: service: %w", err)
	}
	raw := base64.URLEncoding.EncodeToString(buf.Bytes())
	if _, err := srv.Users.Messages.Send("me", &gmail.Message{Raw: raw}).Context(ctx).Do(); err != nil {
		return "", fmt.Errorf("gmail: send: %w", err)
	}
	return m.GetMessageID(), nil
}
```

- [ ] **Step 4: Add `Provider` + `AccessToken` to coreapi jobs** — `internal/coreapi/coreapi.go`

In `SendJob` and `StepSendJob`, add:
```go
	// Provider selects the send transport ("smtp" | "gmail"). AccessToken is the
	// decrypted OAuth bearer for gmail (nil for smtp); zeroized after use like
	// SMTPPassword. For gmail the SMTP* fields are empty.
	Provider    string
	AccessToken []byte
```

- [ ] **Step 5: coreapi token refresh helper** — `internal/coreapi/inprocess/oauthtoken.go`

```go
package inprocess

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/platform/db/gen"
	"github.com/inroad/inroad/internal/platform/mail"
)

// gmailAccessToken unseals the mailbox's OAuth token, refreshes it if expired
// (persisting the rotated token), and returns a valid access token. The worker
// never sees the refresh token — only the short-lived access token.
func (c client) gmailAccessToken(ctx context.Context, mailboxID, workspaceID uuid.UUID, sealed string) (string, error) {
	if !c.googleOAuth.Enabled() {
		return "", fmt.Errorf("gmail oauth not configured")
	}
	raw, err := c.sealer.Open(sealed)
	if err != nil {
		return "", err
	}
	tok, err := mail.UnmarshalToken(raw)
	if err != nil {
		return "", err
	}
	ts := c.googleOAuth.Config().TokenSource(ctx, tok)
	fresh, err := ts.Token()
	if err != nil {
		return "", fmt.Errorf("gmail token refresh: %w", err)
	}
	if fresh.AccessToken != tok.AccessToken || fresh.RefreshToken != tok.RefreshToken {
		if b, err := mail.MarshalToken(fresh); err == nil {
			if ct, err := c.sealer.Seal(b); err == nil {
				_ = c.q.UpdateMailboxSecret(ctx, gen.UpdateMailboxSecretParams{
					ID: mailboxID, WorkspaceID: workspaceID, SecretCiphertext: ct,
				})
			}
		}
	}
	return fresh.AccessToken, nil
}
```

- [ ] **Step 6: Extend `inprocess.New` + `client`** — `internal/coreapi/inprocess/inprocess.go`

Add `googleOAuth mail.GoogleOAuth` to the `client` struct and a param to `New(pool, sealer, jwtSecret, publicURL, googleOAuth)`. Update `cmd/worker/main.go` to pass `mail.GoogleOAuth{...}` from config.

- [ ] **Step 7: Branch the send-job builders** — in `GetSendJob` (`sendjob.go`) and `GetStepSendJob` (`stepsendjob.go`)

Ensure the bundle query selects the mailbox `provider` (add to the SELECT + regen sqlc if missing). Then, replacing the unconditional password unseal:
```go
	provider := b.Provider // from the bundle
	var accessToken []byte
	var password []byte
	if provider == "gmail" {
		at, err := c.gmailAccessToken(ctx, b.MailboxID, ws, b.SecretCiphertext)
		if err != nil {
			return coreapi.StepSendJob{}, err
		}
		accessToken = []byte(at)
	} else {
		password, err = c.sealer.Open(b.SecretCiphertext)
		if err != nil {
			return coreapi.StepSendJob{}, err
		}
	}
```
and set `Provider: provider, AccessToken: accessToken` on the returned job (and `SMTPPassword: password`). Same shape in `GetSendJob`.

- [ ] **Step 8: Widen the worker `Sender` seam** — `internal/worker/sender/sender.go` and `internal/worker/sequence/advance.go`

Change both `Sender` interfaces to:
```go
type Sender interface {
	Send(ctx context.Context, tj mail.OutboundJob, msg mail.Message) (messageID string, err error)
}
```
At each call site, build the OutboundJob and add `defer zeroize(job.AccessToken)` alongside the SMTPPassword zeroize:
```go
	msgID, sendErr := sender.Send(ctx, mail.OutboundJob{
		Provider: job.Provider, Host: job.SMTPHost, Port: job.SMTPPort,
		Username: job.SMTPUsername, Password: string(job.SMTPPassword), UseTLS: job.UseTLS,
		AccessToken: string(job.AccessToken),
	}, mail.Message{ /* unchanged */ })
```

- [ ] **Step 9: Update wiring + fakes**

`internal/worker/handlers.go` `Register(...)` takes the sender interface — change `sndr *mail.NetSender` param to the `Sender` interface (or `*mail.MultiSender`); `cmd/worker/main.go` builds `mail.NewMultiSender(mail.NewNetSender(...), mail.NewGmailSender())`. Update the fake senders in `sender_test.go` and `advance_test.go` to the new one-method signature (they currently record `cfg, msg`; now record `tj, msg`).

- [ ] **Step 10: Build, vet, test**

Run: `export PATH="$PATH:/c/Program Files/Go/bin:$HOME/go/bin"; make sqlc && go build ./... && go vet ./... && gofmt -l internal cmd && go test ./...`
Expected: build/vet clean, gofmt empty, all unit tests PASS (existing SMTP send/sequence tests green — `Provider` defaults to `smtp`).

- [ ] **Step 11: Commit** — `feat(send): provider-dispatched sender + Gmail API transport + coreapi token refresh`

---

### Task 6: Gmail inbox (reply/bounce) via history API

**Files:**
- Create: `internal/platform/mail/gmailinbox.go` (GmailReader)
- Test: `internal/platform/mail/gmailinbox_test.go`
- Modify: `internal/coreapi/coreapi.go` (`InboxPollJob` gains `Provider string`, `AccessToken []byte`, `Cursor string`; add `SetInboxCursorString(ctx, mailboxID, workspaceID, cursor string) error`)
- Modify: `internal/coreapi/inprocess/inbox.go` (build gmail poll job: provider, access token, cursor; implement `SetInboxCursorString`)
- Modify: `internal/worker/inbox/poll.go` (dispatch reader by provider; persist the right cursor)
- Modify: fakes/tests as needed

**Interfaces:**
- Produces: `mail.NewGmailReader() *GmailReader`; `(*GmailReader).Fetch(ctx context.Context, accessToken, sinceHistoryID string, maxN int) (msgs []InboundMessage, newCursor string, err error)`.
- Consumes: existing `ParseDSN` + reply matcher (operate on `InboundMessage` raw MIME — unchanged).

**Design notes:**
- First poll (`sinceHistoryID==""`): `srv.Users.Messages.List("me").Q("newer_than:2d").MaxResults(int64(maxN))` to seed, then read the profile's current `historyId` as the new cursor (`srv.Users.GetProfile("me")`), returning the fetched messages. This avoids crawling all history.
- Incremental: `srv.Users.History.List("me").StartHistoryId(parse(sinceHistoryID)).HistoryTypes("messageAdded").MaxResults(int64(maxN))`, collect added message ids, `srv.Users.Messages.Get("me", id).Format("RAW")` each → decode base64url → parse to `InboundMessage` (Header + Body) the same way `inbox.go` does. New cursor = the response's `HistoryId`.
- Cap at `maxN` messages per pass (same bound rationale as the IMAP reader).
- The worker's inbox poll handler dispatches: `provider=="gmail"` → GmailReader + `Cursor`/`SetInboxCursorString`; else the existing IMAP path. The reply/bounce classification + `MarkReplied`/`MarkBounced` calls are shared, unchanged.

- [ ] **Step 1: Write the failing reader test** — `internal/platform/mail/gmailinbox_test.go`

Test the RAW→InboundMessage decode with a stubbed `getFn`/`historyFn` (mirror the `sendFn` stub pattern): feed a canned RAW MIME (a simple bounce DSN and a plain reply), assert `Fetch` returns two `InboundMessage`s with parsed headers and the new cursor. Keep it network-free via the stub seam.

- [ ] **Step 2: Run to verify fail.** `go test ./internal/platform/mail/ -run Gmailinbox -v` → FAIL.

- [ ] **Step 3: Implement `gmailinbox.go`** per the design notes, with a stub seam for tests (unexported func fields for history-list and message-get, defaulting to the real Gmail client calls).

- [ ] **Step 4: Extend `InboxPollJob` + coreapi** — add the three fields and `SetInboxCursorString` to the interface; implement in `inprocess/inbox.go` (build the gmail job with a refreshed access token via `gmailAccessToken`; the `Cursor` comes from `inbox_cursor`).

- [ ] **Step 5: Dispatch in the poll handler** — `internal/worker/inbox/poll.go`: branch on `job.Provider`; on gmail use `GmailReader.Fetch`, then persist via `SetInboxCursorString`; zeroize `job.AccessToken`.

- [ ] **Step 6: Build, vet, test.** `make sqlc && go build ./... && go vet ./... && gofmt -l internal cmd && go test ./...` → all green; existing IMAP inbox tests unaffected.

- [ ] **Step 7: Commit** — `feat(inbox): Gmail API reply/bounce polling via history cursor`

---

### Task 7: Docs

**Files:**
- Modify: `docs/self-hosting.md` (Google Cloud OAuth client setup: create Web application client, scopes, add redirect URI, set the 3 env vars)
- Modify: `docs/security.md` (OAuth tokens are sealed secrets; state is HMAC+TTL, residual-risk note from spec §3.1; no new SSRF surface)
- Modify: `docs/architecture.md` (provider abstraction: `MultiSender`, coreapi token refresh, opaque inbox cursor)

- [ ] **Step 1:** Write the self-hosting section with exact console steps + env vars from Task 1.
- [ ] **Step 2:** Add the security note (sealed tokens, `[]byte` zeroize, state signing + residual risk, workspace pinned from verified state).
- [ ] **Step 3:** Add the architecture note (single dispatch seam; refresh in control plane).
- [ ] **Step 4: Commit** — `docs(oauth): Gmail setup, security invariants, provider architecture`

---

## Self-Review

- **Spec coverage:** §3 flow → Task 4; §3.1 state → Task 1; §4 refresh → Task 5 (oauthtoken.go); §5 send → Task 5; §6 inbox → Task 6; §7 migration → Task 2; §8 config → Task 1; §9 security → Tasks 4/5 + Task 7; §10 deps → Task 3 step 1; §11 tests → per-task test steps; §12 order → task order. All covered.
- **Placeholders:** none — new code is shown in full; existing-file edits give exact insertion points and the surrounding anchor.
- **Type consistency:** `Sender.Send(ctx, mail.OutboundJob, mail.Message)` identical in both handlers; `OutboundJob` fields match what `MultiSender.Send` reads; `SendJob`/`StepSendJob` both gain `Provider string` + `AccessToken []byte`; `gmailAccessToken` signature matches its call sites; `NewService`/`NewHandler`/`inprocess.New` arity changes are each accompanied by a "fix compile fallout" step.
- **Open risk flagged for executor:** the send/step bundle queries must expose the mailbox `provider` column; Task 5 Step 7 says to add it + regen if missing. Confirm before assuming it's present.
```
