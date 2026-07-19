# Production Auth — Phase 1 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace v1 stateless-JWT auth with a production-grade identity system: multi-workspace accounts, in-memory access tokens + rotating/revocable refresh cookies, CSRF protection, and deny-by-default route security.

**Architecture:** Go control-plane API (chi + pgx/v5 + sqlc). Access tokens are short-lived HS256 JWTs sent as Bearer headers (CSRF-immune) and held in memory on the SPA. Refresh tokens are opaque random values stored hashed in a `sessions` table, delivered via an httpOnly cookie, rotated on every use with family-based reuse detection. Every route is behind `RequireAuth` except a tiny explicit public group. Frontend is React 19 + RTK Query with silent refresh + reauth-on-401.

**Tech Stack:** Go 1.25, chi/v5, pgx/v5, sqlc, golang-jwt/v5, golang.org/x/crypto/argon2, Postgres (citext extension). Frontend: React 19, Redux Toolkit / RTK Query, TanStack Router.

## Global Constraints

- Module path: `github.com/inroad/inroad`. Go files lowercase; frontend files kebab-case.
- `app/*` may import `platform/*`, never the reverse; domains don't import each other; workers use `coreapi` only.
- Data access lives in each domain's `store.go` over sqlc; services depend on it.
- `store/api.ts` is generated from `api/openapi.yaml` — never hand-edited. redux-persist whitelists UI-only slices; never the `api` reducer, and (new this phase) never `auth`.
- Response helpers: `httpx.JSON(w, status, v)` and `httpx.Error(w, status, msg)`.
- Access token signing secret: `INROAD_JWT_SECRET` (existing, ≥16 bytes). Refresh tokens are opaque (no secret).
- Prefix Go tooling with `export PATH="$PATH:/c/Program Files/Go/bin:$HOME/go/bin"` (this machine).
- Backend tests: `make test` (unit) · `make test-integration` (needs `make db-up`). Frontend: `cd web && npx vitest run`.
- Conventional commits (`feat:`, `test:`, `chore:`, `docs:`).

---

## File Structure

**Backend — created**
- `internal/platform/db/migrations/000002_auth_multiworkspace.up.sql` / `.down.sql` — new identity schema.
- `internal/platform/db/queries/member.sql`, `session.sql` — sqlc queries for membership + sessions.
- `internal/app/auth/token.go` — opaque refresh-token generation + hashing.
- `internal/app/auth/csrf.go` — double-submit CSRF helpers + middleware.
- `internal/app/identity/{service.go,store.go,handler.go,routes.go,cookies.go}` — new auth domain (register/login/refresh/logout/switch/me). Replaces `internal/app/workspace` auth responsibilities.
- Test files alongside each (`*_test.go`), plus `internal/app/identity/service_integration_test.go`.

**Backend — modified**
- `internal/app/auth/password.go` — argon2id.
- `internal/app/auth/jwt.go` — access-token claims gain `role`, `sid`.
- `internal/app/auth/middleware.go` — richer context claims + `RequireRole`.
- `internal/platform/config/config.go` — token TTLs + cookie settings.
- `internal/platform/db/queries/{user,workspace}.sql` — reshaped for the new schema.
- `internal/platform/httpx/router.go` — public/protected group helper.
- `cmd/inroad/main.go` — wire identity handler + deny-by-default groups.
- `internal/app/mailbox/routes.go` — drop local `RequireAuth` (guarded at group level).
- `api/openapi.yaml` — `/auth/*` paths + schemas.

**Frontend — created**
- `web/src/features/auth/workspace-switcher.tsx`.
- `web/src/features/auth/use-auth-bootstrap.ts` — silent refresh on load.

**Frontend — modified**
- `web/src/store/slices/auth.ts` — memory-only session shape (+ memberships/role).
- `web/src/store/index.ts` — drop `auth` from persist whitelist.
- `web/src/store/empty-api.ts` — `baseQueryWithReauth` + CSRF header.
- `web/src/features/auth/{login-form,register-form}.tsx` — consume new response shape.
- `web/src/components/layout/app-header.tsx` — switcher + logout wired to endpoints.
- `web/src/routes/app.tsx` — guard awaits bootstrap.

---

## Task 1: Identity schema migration

**Files:**
- Create: `internal/platform/db/migrations/000002_auth_multiworkspace.up.sql`
- Create: `internal/platform/db/migrations/000002_auth_multiworkspace.down.sql`

**Interfaces:**
- Produces: tables `users(id,email,password_hash,email_verified_at,created_at)`, `workspaces(id,name,created_at)`, `workspace_members(id,workspace_id,user_id,role,created_at,last_seen_at)`, `sessions(id,user_id,workspace_id,token_hash,family_id,expires_at,revoked_at,user_agent,ip,created_at)`; enum `member_role`.

- [ ] **Step 1: Write the up migration**

`internal/platform/db/migrations/000002_auth_multiworkspace.up.sql`:
```sql
CREATE EXTENSION IF NOT EXISTS citext;

-- v1 embedded workspace_id/role on users; recreate for multi-workspace (pre-prod, no data to keep).
DROP TABLE IF EXISTS users;

CREATE TABLE users (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    email             CITEXT NOT NULL UNIQUE,
    password_hash     TEXT NOT NULL,
    email_verified_at TIMESTAMPTZ,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TYPE member_role AS ENUM ('owner', 'admin', 'member');

CREATE TABLE workspace_members (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    user_id      UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role         member_role NOT NULL DEFAULT 'member',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at TIMESTAMPTZ,
    UNIQUE (workspace_id, user_id)
);
CREATE INDEX idx_members_user ON workspace_members (user_id);

CREATE TABLE sessions (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id      UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    workspace_id UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    token_hash   BYTEA NOT NULL UNIQUE,
    family_id    UUID NOT NULL,
    expires_at   TIMESTAMPTZ NOT NULL,
    revoked_at   TIMESTAMPTZ,
    user_agent   TEXT,
    ip           INET,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_sessions_user ON sessions (user_id);
CREATE INDEX idx_sessions_family ON sessions (family_id);
```

- [ ] **Step 2: Write the down migration**

`internal/platform/db/migrations/000002_auth_multiworkspace.down.sql`:
```sql
DROP TABLE IF EXISTS sessions;
DROP TABLE IF EXISTS workspace_members;
DROP TYPE IF EXISTS member_role;
DROP TABLE IF EXISTS users;

-- restore v1 users shape so 000001 down still composes
CREATE TABLE users (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id  UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    email         TEXT NOT NULL,
    password_hash TEXT NOT NULL,
    role          TEXT NOT NULL DEFAULT 'owner',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (workspace_id, email)
);
CREATE INDEX idx_users_email ON users (email);
```

- [ ] **Step 3: Apply and verify**

Run: `export PATH="$PATH:/c/Program Files/Go/bin:$HOME/go/bin" && make db-up && make migrate-up`
Expected: migrations apply with no error; `002` recorded. Then `make migrate-down` once and `make migrate-up` again to confirm reversibility, or inspect with `psql` that tables exist.

- [ ] **Step 4: Commit**

```bash
git add internal/platform/db/migrations/000002_auth_multiworkspace.*.sql
git commit -m "feat(db): multi-workspace identity + sessions schema"
```

---

## Task 2: sqlc queries + regenerate

**Files:**
- Modify: `internal/platform/db/queries/user.sql`
- Modify: `internal/platform/db/queries/workspace.sql`
- Create: `internal/platform/db/queries/member.sql`
- Create: `internal/platform/db/queries/session.sql`
- Regenerate: `internal/platform/db/gen/*`

**Interfaces:**
- Produces (sqlc `gen.Queries` methods): `CreateUser`, `GetUserByEmail`, `GetUserByID`, `CreateWorkspace`, `CreateMember`, `ListMembersByUser`, `GetMember`, `TouchMemberLastSeen`, `CreateSession`, `GetSessionByHash`, `RevokeSession`, `RevokeFamily`, `RevokeAllForUser`. Exact Go signatures come from generation; later tasks call them via `identity.Store` wrappers.

- [ ] **Step 1: Rewrite `user.sql`**

```sql
-- name: CreateUser :one
INSERT INTO users (email, password_hash) VALUES ($1, $2) RETURNING *;

-- name: GetUserByEmail :one
SELECT * FROM users WHERE email = $1;

-- name: GetUserByID :one
SELECT * FROM users WHERE id = $1;
```

- [ ] **Step 2: Extend `workspace.sql`**

```sql
-- name: CreateWorkspace :one
INSERT INTO workspaces (name) VALUES ($1) RETURNING *;

-- name: GetWorkspace :one
SELECT * FROM workspaces WHERE id = $1;
```

- [ ] **Step 3: Create `member.sql`**

```sql
-- name: CreateMember :one
INSERT INTO workspace_members (workspace_id, user_id, role)
VALUES ($1, $2, $3) RETURNING *;

-- name: GetMember :one
SELECT * FROM workspace_members WHERE workspace_id = $1 AND user_id = $2;

-- name: ListMembersByUser :many
SELECT m.*, w.name AS workspace_name
FROM workspace_members m
JOIN workspaces w ON w.id = m.workspace_id
WHERE m.user_id = $1
ORDER BY m.last_seen_at DESC NULLS LAST, m.created_at ASC;

-- name: TouchMemberLastSeen :exec
UPDATE workspace_members SET last_seen_at = now()
WHERE workspace_id = $1 AND user_id = $2;
```

- [ ] **Step 4: Create `session.sql`**

```sql
-- name: CreateSession :one
INSERT INTO sessions (user_id, workspace_id, token_hash, family_id, expires_at, user_agent, ip)
VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING *;

-- name: GetSessionByHash :one
SELECT * FROM sessions WHERE token_hash = $1;

-- name: RevokeSession :exec
UPDATE sessions SET revoked_at = now() WHERE id = $1 AND revoked_at IS NULL;

-- name: RevokeFamily :exec
UPDATE sessions SET revoked_at = now() WHERE family_id = $1 AND revoked_at IS NULL;

-- name: RevokeAllForUser :exec
UPDATE sessions SET revoked_at = now() WHERE user_id = $1 AND revoked_at IS NULL;

-- name: RepointSessionWorkspace :exec
UPDATE sessions SET workspace_id = $2 WHERE id = $1;
```

- [ ] **Step 5: Regenerate and build**

Run: `export PATH="$PATH:/c/Program Files/Go/bin:$HOME/go/bin" && sqlc generate && go build ./...`
Expected: `internal/platform/db/gen` updates; `go build` fails only in `internal/app/workspace` (old `CreateUserParams{WorkspaceID,Role}` no longer exists) — that package is replaced in Task 6, so it's expected to break here. Confirm `gen` compiles: `go build ./internal/platform/db/...` passes.

- [ ] **Step 6: Commit**

```bash
git add internal/platform/db/queries sqlc.yaml internal/platform/db/gen
git commit -m "feat(db): sqlc queries for users, members, sessions"
```

---

## Task 3: argon2id password hashing

**Files:**
- Modify: `internal/app/auth/password.go`
- Test: `internal/app/auth/password_test.go`

**Interfaces:**
- Produces: `HashPassword(pw string) (string, error)` → encoded `$argon2id$...` string; `CheckPassword(encoded, pw string) bool` (constant-time, parses params from the encoded string).

- [ ] **Step 1: Write failing tests**

`internal/app/auth/password_test.go`:
```go
package auth

import (
	"strings"
	"testing"
)

func TestHashAndCheckPassword(t *testing.T) {
	hash, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if !strings.HasPrefix(hash, "$argon2id$") {
		t.Fatalf("expected argon2id encoding, got %q", hash)
	}
	if !CheckPassword(hash, "correct horse battery staple") {
		t.Fatal("correct password rejected")
	}
	if CheckPassword(hash, "wrong password") {
		t.Fatal("wrong password accepted")
	}
}

func TestCheckPasswordRejectsGarbage(t *testing.T) {
	if CheckPassword("not-a-real-hash", "x") {
		t.Fatal("garbage hash accepted")
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `export PATH="$PATH:/c/Program Files/Go/bin:$HOME/go/bin" && go test ./internal/app/auth/ -run TestHashAndCheckPassword -v`
Expected: FAIL (still bcrypt, no `$argon2id$` prefix).

- [ ] **Step 3: Implement argon2id**

`internal/app/auth/password.go`:
```go
// Package auth handles registration, login, password hashing, and JWT sessions.
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

const (
	argonTime    = 1
	argonMemory  = 64 * 1024 // 64 MiB
	argonThreads = 4
	argonKeyLen  = 32
	argonSaltLen = 16
)

func HashPassword(pw string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(pw), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	), nil
}

func CheckPassword(encoded, pw string) bool {
	salt, key, t, m, p, err := decodeArgon(encoded)
	if err != nil {
		return false
	}
	got := argon2.IDKey([]byte(pw), salt, t, m, p, uint32(len(key)))
	return subtle.ConstantTimeCompare(got, key) == 1
}

func decodeArgon(encoded string) (salt, key []byte, t, m uint32, p uint8, err error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return nil, nil, 0, 0, 0, errors.New("bad argon2 hash")
	}
	var version int
	if _, err = fmt.Sscanf(parts[2], "v=%d", &version); err != nil {
		return nil, nil, 0, 0, 0, err
	}
	if _, err = fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil {
		return nil, nil, 0, 0, 0, err
	}
	if salt, err = base64.RawStdEncoding.DecodeString(parts[4]); err != nil {
		return nil, nil, 0, 0, 0, err
	}
	if key, err = base64.RawStdEncoding.DecodeString(parts[5]); err != nil {
		return nil, nil, 0, 0, 0, err
	}
	return salt, key, t, m, p, nil
}
```

- [ ] **Step 4: Tidy modules, run tests**

Run: `export PATH="$PATH:/c/Program Files/Go/bin:$HOME/go/bin" && go mod tidy && go test ./internal/app/auth/ -run TestHashAndCheckPassword -v && go test ./internal/app/auth/ -run TestCheckPasswordRejectsGarbage -v`
Expected: PASS (both).

- [ ] **Step 5: Commit**

```bash
git add internal/app/auth/password.go internal/app/auth/password_test.go go.mod go.sum
git commit -m "feat(auth): argon2id password hashing"
```

---

## Task 4: Access-token claims (role + session id)

**Files:**
- Modify: `internal/app/auth/jwt.go`
- Modify: `internal/app/auth/jwt_test.go`

**Interfaces:**
- Produces: `Claims{UserID, WorkspaceID, Role, SessionID string}`; `IssueToken(secret []byte, c Claims, ttl time.Duration) (string, error)`; `ParseToken(secret []byte, token string) (Claims, error)`.

- [ ] **Step 1: Write failing test**

Append to `internal/app/auth/jwt_test.go`:
```go
func TestIssueParseRoundTripWithRoleAndSession(t *testing.T) {
	secret := []byte("0123456789abcdef")
	in := Claims{UserID: "u1", WorkspaceID: "w1", Role: "admin", SessionID: "s1"}
	tok, err := IssueToken(secret, in, time.Minute)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	out, err := ParseToken(secret, tok)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if out != in {
		t.Fatalf("round-trip mismatch: %+v != %+v", out, in)
	}
}

func TestParseRejectsExpired(t *testing.T) {
	secret := []byte("0123456789abcdef")
	tok, _ := IssueToken(secret, Claims{UserID: "u1"}, -time.Minute)
	if _, err := ParseToken(secret, tok); err == nil {
		t.Fatal("expected expired token to be rejected")
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `export PATH="$PATH:/c/Program Files/Go/bin:$HOME/go/bin" && go test ./internal/app/auth/ -run RoundTrip -v`
Expected: FAIL to compile (`IssueToken` signature is the old positional one; `Claims` has no `Role`/`SessionID`).

- [ ] **Step 3: Update `jwt.go`**

```go
package auth

import (
	"errors"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

type Claims struct {
	UserID      string
	WorkspaceID string
	Role        string
	SessionID   string
}

type jwtClaims struct {
	WorkspaceID string `json:"wid"`
	Role        string `json:"role"`
	SessionID   string `json:"sid"`
	jwt.RegisteredClaims
}

func IssueToken(secret []byte, c Claims, ttl time.Duration) (string, error) {
	now := time.Now()
	claims := jwtClaims{
		WorkspaceID: c.WorkspaceID,
		Role:        c.Role,
		SessionID:   c.SessionID,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   c.UserID,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
		},
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(secret)
}

func ParseToken(secret []byte, token string) (Claims, error) {
	var c jwtClaims
	_, err := jwt.ParseWithClaims(token, &c, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, errors.New("unexpected signing method")
		}
		return secret, nil
	})
	if err != nil {
		return Claims{}, err
	}
	return Claims{UserID: c.Subject, WorkspaceID: c.WorkspaceID, Role: c.Role, SessionID: c.SessionID}, nil
}
```

- [ ] **Step 4: Run tests**

Run: `export PATH="$PATH:/c/Program Files/Go/bin:$HOME/go/bin" && go test ./internal/app/auth/ -run "RoundTrip|Expired" -v`
Expected: PASS. (Old positional `IssueToken` callers in `internal/app/workspace` will break — replaced in Task 6.)

- [ ] **Step 5: Commit**

```bash
git add internal/app/auth/jwt.go internal/app/auth/jwt_test.go
git commit -m "feat(auth): access-token claims carry role and session id"
```

---

## Task 5: Opaque refresh-token module

**Files:**
- Create: `internal/app/auth/token.go`
- Test: `internal/app/auth/token_test.go`

**Interfaces:**
- Produces: `NewRefreshToken() (raw string, hash []byte, err error)` (32 random bytes, base64url raw; hash = SHA-256 of raw); `HashRefreshToken(raw string) []byte`.

- [ ] **Step 1: Write failing test**

`internal/app/auth/token_test.go`:
```go
package auth

import (
	"bytes"
	"testing"
)

func TestNewRefreshTokenIsHashStable(t *testing.T) {
	raw, hash, err := NewRefreshToken()
	if err != nil {
		t.Fatalf("gen: %v", err)
	}
	if raw == "" || len(hash) != 32 {
		t.Fatalf("bad token/hash: %q / %d bytes", raw, len(hash))
	}
	if !bytes.Equal(hash, HashRefreshToken(raw)) {
		t.Fatal("hash of raw token is not stable")
	}
}

func TestRefreshTokensAreUnique(t *testing.T) {
	a, _, _ := NewRefreshToken()
	b, _, _ := NewRefreshToken()
	if a == b {
		t.Fatal("tokens should be unique")
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `export PATH="$PATH:/c/Program Files/Go/bin:$HOME/go/bin" && go test ./internal/app/auth/ -run Refresh -v`
Expected: FAIL (undefined `NewRefreshToken`).

- [ ] **Step 3: Implement `token.go`**

```go
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
)

// NewRefreshToken returns a new opaque refresh token and its SHA-256 hash.
// Only the hash is persisted; the raw value lives solely in the client cookie.
func NewRefreshToken() (raw string, hash []byte, err error) {
	b := make([]byte, 32)
	if _, err = rand.Read(b); err != nil {
		return "", nil, err
	}
	raw = base64.RawURLEncoding.EncodeToString(b)
	return raw, HashRefreshToken(raw), nil
}

// HashRefreshToken hashes a raw refresh token for storage/lookup.
func HashRefreshToken(raw string) []byte {
	sum := sha256.Sum256([]byte(raw))
	return sum[:]
}
```

- [ ] **Step 4: Run tests**

Run: `export PATH="$PATH:/c/Program Files/Go/bin:$HOME/go/bin" && go test ./internal/app/auth/ -run Refresh -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/app/auth/token.go internal/app/auth/token_test.go
git commit -m "feat(auth): opaque refresh-token generation and hashing"
```

---

## Task 6: Config — token TTLs and cookie settings

**Files:**
- Modify: `internal/platform/config/config.go`
- Modify: `internal/platform/config/config_test.go`
- Modify: `.env.example`

**Interfaces:**
- Produces: `Config` gains `AccessTokenTTL time.Duration`, `RefreshTokenTTL time.Duration`, `CookieSecure bool`, `CookieDomain string`.

- [ ] **Step 1: Write failing test**

Append to `internal/platform/config/config_test.go` (set required env in the test):
```go
func TestLoadTokenDefaults(t *testing.T) {
	t.Setenv("INROAD_JWT_SECRET", "0123456789abcdef")
	t.Setenv("INROAD_MASTER_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	cfg, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.AccessTokenTTL != 15*time.Minute {
		t.Fatalf("access ttl = %v", cfg.AccessTokenTTL)
	}
	if cfg.RefreshTokenTTL != 720*time.Hour {
		t.Fatalf("refresh ttl = %v", cfg.RefreshTokenTTL)
	}
	if !cfg.CookieSecure {
		t.Fatal("cookie secure should default true")
	}
}
```
(Add `"encoding/base64"` and `"time"` imports to the test file if missing.)

- [ ] **Step 2: Run to verify failure**

Run: `export PATH="$PATH:/c/Program Files/Go/bin:$HOME/go/bin" && go test ./internal/platform/config/ -run TokenDefaults -v`
Expected: FAIL (fields undefined).

- [ ] **Step 3: Add fields + parsing**

In `config.go` add imports `"time"`; add struct fields and a duration helper, and set them in `Load()`:
```go
// in struct Config
	AccessTokenTTL  time.Duration
	RefreshTokenTTL time.Duration
	CookieSecure    bool
	CookieDomain    string
```
```go
// in Load(), before `return cfg, nil`
	cfg.AccessTokenTTL = getenvDuration("INROAD_ACCESS_TOKEN_TTL", 15*time.Minute)
	cfg.RefreshTokenTTL = getenvDuration("INROAD_REFRESH_TOKEN_TTL", 720*time.Hour)
	cfg.CookieSecure = getenvBool("INROAD_COOKIE_SECURE", true)
	cfg.CookieDomain = getenv("INROAD_COOKIE_DOMAIN", "")
```
```go
// new helper
func getenvDuration(key string, fallback time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return fallback
}
```

- [ ] **Step 4: Update `.env.example`**

Add:
```
INROAD_ACCESS_TOKEN_TTL=15m
INROAD_REFRESH_TOKEN_TTL=720h
INROAD_COOKIE_SECURE=false
INROAD_COOKIE_DOMAIN=
```

- [ ] **Step 5: Run tests**

Run: `export PATH="$PATH:/c/Program Files/Go/bin:$HOME/go/bin" && go test ./internal/platform/config/ -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/platform/config/config.go internal/platform/config/config_test.go .env.example
git commit -m "feat(config): access/refresh token TTLs and cookie settings"
```

---

## Task 7: CSRF double-submit helper

**Files:**
- Create: `internal/app/auth/csrf.go`
- Test: `internal/app/auth/csrf_test.go`

**Interfaces:**
- Produces: `NewCSRFToken() (string, error)`; `RequireCSRF(next http.Handler) http.Handler` — rejects (403) when the `X-CSRF-Token` header doesn't equal the `csrf_token` cookie (constant-time). Cookie name constant `CSRFCookieName = "csrf_token"`, header `X-CSRF-Token`.

- [ ] **Step 1: Write failing tests**

`internal/app/auth/csrf_test.go`:
```go
package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRequireCSRFMatch(t *testing.T) {
	h := RequireCSRF(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) }))
	r := httptest.NewRequest("POST", "/auth/refresh", nil)
	r.AddCookie(&http.Cookie{Name: CSRFCookieName, Value: "tok123"})
	r.Header.Set("X-CSRF-Token", "tok123")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("want 200, got %d", w.Code)
	}
}

func TestRequireCSRFMismatch(t *testing.T) {
	h := RequireCSRF(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) }))
	r := httptest.NewRequest("POST", "/auth/refresh", nil)
	r.AddCookie(&http.Cookie{Name: CSRFCookieName, Value: "tok123"})
	r.Header.Set("X-CSRF-Token", "different")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("want 403, got %d", w.Code)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `export PATH="$PATH:/c/Program Files/Go/bin:$HOME/go/bin" && go test ./internal/app/auth/ -run CSRF -v`
Expected: FAIL (undefined).

- [ ] **Step 3: Implement `csrf.go`**

```go
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"net/http"
)

const CSRFCookieName = "csrf_token"
const CSRFHeaderName = "X-CSRF-Token"

func NewCSRFToken() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// RequireCSRF enforces the double-submit pattern on cookie-authenticated endpoints.
func RequireCSRF(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(CSRFCookieName)
		header := r.Header.Get(CSRFHeaderName)
		if err != nil || header == "" || subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(header)) != 1 {
			http.Error(w, `{"error":"csrf token mismatch"}`, http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}
```

- [ ] **Step 4: Run tests**

Run: `export PATH="$PATH:/c/Program Files/Go/bin:$HOME/go/bin" && go test ./internal/app/auth/ -run CSRF -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/app/auth/csrf.go internal/app/auth/csrf_test.go
git commit -m "feat(auth): double-submit CSRF helper for cookie endpoints"
```

---

## Task 8: Auth middleware — rich claims + RequireRole

**Files:**
- Modify: `internal/app/auth/middleware.go`
- Test: `internal/app/auth/middleware_test.go` (create)

**Interfaces:**
- Consumes: `ParseToken`, `Claims` (Task 4).
- Produces: `RequireAuth(secret []byte) func(http.Handler) http.Handler` (unchanged signature; now stores full `Claims`); `UserFromContext(ctx) (Claims, bool)` (unchanged); `RequireRole(min string) func(http.Handler) http.Handler` where role rank is `member<admin<owner`.

- [ ] **Step 1: Write failing test**

`internal/app/auth/middleware_test.go`:
```go
package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRequireRoleAllowsSufficient(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) })
	h := RequireRole("admin")(next)
	r := httptest.NewRequest("GET", "/x", nil).WithContext(
		context.WithValue(context.Background(), ctxKey{}, Claims{Role: "owner"}))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("owner should satisfy admin, got %d", w.Code)
	}
}

func TestRequireRoleRejectsInsufficient(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) })
	h := RequireRole("admin")(next)
	r := httptest.NewRequest("GET", "/x", nil).WithContext(
		context.WithValue(context.Background(), ctxKey{}, Claims{Role: "member"}))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("member should not satisfy admin, got %d", w.Code)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `export PATH="$PATH:/c/Program Files/Go/bin:$HOME/go/bin" && go test ./internal/app/auth/ -run RequireRole -v`
Expected: FAIL (undefined `RequireRole`).

- [ ] **Step 3: Add `RequireRole` to `middleware.go`**

Append:
```go
var roleRank = map[string]int{"member": 1, "admin": 2, "owner": 3}

// RequireRole rejects (403) callers whose workspace role ranks below min.
// Must run after RequireAuth.
func RequireRole(min string) func(http.Handler) http.Handler {
	want := roleRank[min]
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			c, ok := UserFromContext(r.Context())
			if !ok || roleRank[c.Role] < want {
				http.Error(w, `{"error":"insufficient role"}`, http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
```
(`RequireAuth` already stores full `Claims`; no change needed there beyond Task 4's richer struct.)

- [ ] **Step 4: Run tests**

Run: `export PATH="$PATH:/c/Program Files/Go/bin:$HOME/go/bin" && go test ./internal/app/auth/ -run RequireRole -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/app/auth/middleware.go internal/app/auth/middleware_test.go
git commit -m "feat(auth): RequireRole workspace-role middleware"
```

---

## Task 9: Identity store

**Files:**
- Create: `internal/app/identity/store.go`
- (No new test file; covered by service tests in Task 10/11.)

**Interfaces:**
- Consumes: `gen.Queries` methods from Task 2; a `*pgxpool.Pool` for transactions.
- Produces: `identity.Store` with methods wrapping sqlc: `CreateUser`, `GetUserByEmail`, `GetUserByID`, `CreateMember`, `GetMember`, `ListMembersByUser`, `TouchMemberLastSeen`, `CreateSession`, `GetSessionByHash`, `RevokeSession`, `RevokeFamily`, `RevokeAllForUser`, `RepointSessionWorkspace`, and `RegisterTx(ctx, name, email, hash) (workspaceID, userID uuid.UUID, err error)` running user+workspace+member in one transaction.

- [ ] **Step 1: Implement `store.go`**

```go
// Package identity owns authentication: users, workspace membership, and sessions.
package identity

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inroad/inroad/internal/platform/db/gen"
)

type Store struct {
	pool *pgxpool.Pool
	q    *gen.Queries
}

func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool, q: gen.New(pool)}
}

// RegisterTx creates workspace + user + owner membership atomically.
func (s *Store) RegisterTx(ctx context.Context, wsName, email, hash string) (wsID, userID uuid.UUID, err error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	defer tx.Rollback(ctx)
	qtx := s.q.WithTx(tx)

	ws, err := qtx.CreateWorkspace(ctx, wsName)
	if err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	user, err := qtx.CreateUser(ctx, gen.CreateUserParams{Email: email, PasswordHash: hash})
	if err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	if _, err = qtx.CreateMember(ctx, gen.CreateMemberParams{
		WorkspaceID: ws.ID, UserID: user.ID, Role: gen.MemberRoleOwner,
	}); err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	return ws.ID, user.ID, nil
}

// Thin pass-throughs (align arg/return types with generated `gen` after sqlc generate):
func (s *Store) GetUserByEmail(ctx context.Context, email string) (gen.User, error) {
	return s.q.GetUserByEmail(ctx, email)
}
func (s *Store) ListMembersByUser(ctx context.Context, userID uuid.UUID) ([]gen.ListMembersByUserRow, error) {
	return s.q.ListMembersByUser(ctx, userID)
}
func (s *Store) GetMember(ctx context.Context, wsID, userID uuid.UUID) (gen.WorkspaceMember, error) {
	return s.q.GetMember(ctx, gen.GetMemberParams{WorkspaceID: wsID, UserID: userID})
}
func (s *Store) TouchMemberLastSeen(ctx context.Context, wsID, userID uuid.UUID) error {
	return s.q.TouchMemberLastSeen(ctx, gen.TouchMemberLastSeenParams{WorkspaceID: wsID, UserID: userID})
}
func (s *Store) CreateSession(ctx context.Context, arg gen.CreateSessionParams) (gen.Session, error) {
	return s.q.CreateSession(ctx, arg)
}
func (s *Store) GetSessionByHash(ctx context.Context, hash []byte) (gen.Session, error) {
	return s.q.GetSessionByHash(ctx, hash)
}
func (s *Store) RevokeSession(ctx context.Context, id uuid.UUID) error { return s.q.RevokeSession(ctx, id) }
func (s *Store) RevokeFamily(ctx context.Context, familyID uuid.UUID) error { return s.q.RevokeFamily(ctx, familyID) }
func (s *Store) RevokeAllForUser(ctx context.Context, userID uuid.UUID) error { return s.q.RevokeAllForUser(ctx, userID) }
func (s *Store) RepointSessionWorkspace(ctx context.Context, id, wsID uuid.UUID) error {
	return s.q.RepointSessionWorkspace(ctx, gen.RepointSessionWorkspaceParams{ID: id, WorkspaceID: wsID})
}

var _ = pgx.ErrNoRows
```

- [ ] **Step 2: Build**

Run: `export PATH="$PATH:/c/Program Files/Go/bin:$HOME/go/bin" && go build ./internal/app/identity/`
Expected: compiles (adjust any field/param names to exactly match `gen` output from Task 2 — e.g. `gen.MemberRoleOwner` enum constant name).

- [ ] **Step 3: Commit**

```bash
git add internal/app/identity/store.go
git commit -m "feat(identity): store with transactional register + session queries"
```

---

## Task 10: Identity service

**Files:**
- Create: `internal/app/identity/service.go`
- Test: `internal/app/identity/service_test.go` (unit tests with a fake store)

**Interfaces:**
- Consumes: `identity.Store` (Task 9), `auth.HashPassword/CheckPassword`, `auth.NewRefreshToken/HashRefreshToken`.
- Produces: `Service` with:
  - `Register(ctx, RegisterInput) (Session, error)`
  - `Login(ctx, email, pw, ua, ip string) (Session, error)`
  - `Refresh(ctx, rawRefresh, ua, ip string) (Session, error)`
  - `Logout(ctx, rawRefresh string) error`
  - `LogoutAll(ctx, userID uuid.UUID) error`
  - `SwitchWorkspace(ctx, sessionID, userID, targetWS uuid.UUID) (activeWS uuid.UUID, role string, err error)`
  - `Memberships(ctx, userID uuid.UUID) ([]Membership, error)`
  - `Session` = `{UserID, WorkspaceID uuid.UUID; Role string; SessionID uuid.UUID; RawRefresh string; Memberships []Membership}`.
- Depends on a small `Store` interface (defined in this file) so the service is unit-testable with a fake — per SOLID/dependency-inversion in CLAUDE.md.

- [ ] **Step 1: Write the service with a Store interface**

`internal/app/identity/service.go` (key logic; the `storeIface` lists exactly the methods used):
```go
package identity

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/app/auth"
	"github.com/inroad/inroad/internal/platform/db/gen"
)

var (
	ErrInvalidCredentials = errors.New("invalid credentials")
	ErrNoWorkspace        = errors.New("user has no workspace")
	ErrRefreshInvalid     = errors.New("refresh token invalid")
	ErrNotMember          = errors.New("not a member of target workspace")
)

type Membership struct {
	WorkspaceID   uuid.UUID
	WorkspaceName string
	Role          string
}

type Session struct {
	UserID      uuid.UUID
	WorkspaceID uuid.UUID
	Role        string
	SessionID   uuid.UUID
	RawRefresh  string
	Memberships []Membership
}

type RegisterInput struct {
	WorkspaceName, Email, Password, UserAgent, IP string
}

type storeIface interface {
	RegisterTx(ctx context.Context, wsName, email, hash string) (uuid.UUID, uuid.UUID, error)
	GetUserByEmail(ctx context.Context, email string) (gen.User, error)
	ListMembersByUser(ctx context.Context, userID uuid.UUID) ([]gen.ListMembersByUserRow, error)
	GetMember(ctx context.Context, wsID, userID uuid.UUID) (gen.WorkspaceMember, error)
	TouchMemberLastSeen(ctx context.Context, wsID, userID uuid.UUID) error
	CreateSession(ctx context.Context, arg gen.CreateSessionParams) (gen.Session, error)
	GetSessionByHash(ctx context.Context, hash []byte) (gen.Session, error)
	RevokeSession(ctx context.Context, id uuid.UUID) error
	RevokeFamily(ctx context.Context, familyID uuid.UUID) error
	RevokeAllForUser(ctx context.Context, userID uuid.UUID) error
	RepointSessionWorkspace(ctx context.Context, id, wsID uuid.UUID) error
}

type Service struct {
	store      storeIface
	refreshTTL time.Duration
}

func NewService(store storeIface, refreshTTL time.Duration) *Service {
	return &Service{store: store, refreshTTL: refreshTTL}
}

func (s *Service) newSessionRow(ctx context.Context, userID, wsID, familyID uuid.UUID, ua, ip string) (uuid.UUID, string, error) {
	raw, hash, err := auth.NewRefreshToken()
	if err != nil {
		return uuid.Nil, "", err
	}
	row, err := s.store.CreateSession(ctx, gen.CreateSessionParams{
		UserID: userID, WorkspaceID: wsID, TokenHash: hash, FamilyID: familyID,
		ExpiresAt: pgxTimestamp(time.Now().Add(s.refreshTTL)),
		UserAgent: ptr(ua), Ip: parseIP(ip),
	})
	if err != nil {
		return uuid.Nil, "", err
	}
	return row.ID, raw, nil
}

func (s *Service) Register(ctx context.Context, in RegisterInput) (Session, error) {
	hash, err := auth.HashPassword(in.Password)
	if err != nil {
		return Session{}, err
	}
	wsID, userID, err := s.store.RegisterTx(ctx, in.WorkspaceName, in.Email, hash)
	if err != nil {
		return Session{}, err // handler maps unique-violation -> 409
	}
	fam := uuid.New()
	sid, raw, err := s.newSessionRow(ctx, userID, wsID, fam, in.UserAgent, in.IP)
	if err != nil {
		return Session{}, err
	}
	mems, _ := s.memberships(ctx, userID)
	return Session{UserID: userID, WorkspaceID: wsID, Role: "owner", SessionID: sid, RawRefresh: raw, Memberships: mems}, nil
}

func (s *Service) Login(ctx context.Context, email, pw, ua, ip string) (Session, error) {
	user, err := s.store.GetUserByEmail(ctx, email)
	if err != nil || !auth.CheckPassword(user.PasswordHash, pw) {
		return Session{}, ErrInvalidCredentials
	}
	mems, err := s.memberships(ctx, user.ID)
	if err != nil || len(mems) == 0 {
		return Session{}, ErrNoWorkspace
	}
	active := mems[0] // ListMembersByUser orders by last_seen desc, created asc
	_ = s.store.TouchMemberLastSeen(ctx, active.WorkspaceID, user.ID)
	fam := uuid.New()
	sid, raw, err := s.newSessionRow(ctx, user.ID, active.WorkspaceID, fam, ua, ip)
	if err != nil {
		return Session{}, err
	}
	return Session{UserID: user.ID, WorkspaceID: active.WorkspaceID, Role: active.Role, SessionID: sid, RawRefresh: raw, Memberships: mems}, nil
}

func (s *Service) Refresh(ctx context.Context, raw, ua, ip string) (Session, error) {
	row, err := s.store.GetSessionByHash(ctx, auth.HashRefreshToken(raw))
	if err != nil {
		return Session{}, ErrRefreshInvalid
	}
	// Reuse detection: a revoked or expired token kills the whole family.
	if row.RevokedAt != nil || time.Now().After(pgxTime(row.ExpiresAt)) {
		_ = s.store.RevokeFamily(ctx, row.FamilyID)
		return Session{}, ErrRefreshInvalid
	}
	if err := s.store.RevokeSession(ctx, row.ID); err != nil {
		return Session{}, err
	}
	member, err := s.store.GetMember(ctx, row.WorkspaceID, row.UserID)
	if err != nil {
		return Session{}, ErrRefreshInvalid
	}
	sid, newRaw, err := s.newSessionRow(ctx, row.UserID, row.WorkspaceID, row.FamilyID, ua, ip)
	if err != nil {
		return Session{}, err
	}
	mems, _ := s.memberships(ctx, row.UserID)
	return Session{UserID: row.UserID, WorkspaceID: row.WorkspaceID, Role: string(member.Role), SessionID: sid, RawRefresh: newRaw, Memberships: mems}, nil
}

func (s *Service) Logout(ctx context.Context, raw string) error {
	row, err := s.store.GetSessionByHash(ctx, auth.HashRefreshToken(raw))
	if err != nil {
		return nil // already gone; idempotent
	}
	return s.store.RevokeFamily(ctx, row.FamilyID)
}

func (s *Service) LogoutAll(ctx context.Context, userID uuid.UUID) error {
	return s.store.RevokeAllForUser(ctx, userID)
}

func (s *Service) SwitchWorkspace(ctx context.Context, sessionID, userID, target uuid.UUID) (uuid.UUID, string, error) {
	m, err := s.store.GetMember(ctx, target, userID)
	if err != nil {
		return uuid.Nil, "", ErrNotMember
	}
	if err := s.store.RepointSessionWorkspace(ctx, sessionID, target); err != nil {
		return uuid.Nil, "", err
	}
	_ = s.store.TouchMemberLastSeen(ctx, target, userID)
	return target, string(m.Role), nil
}

func (s *Service) Memberships(ctx context.Context, userID uuid.UUID) ([]Membership, error) {
	return s.memberships(ctx, userID)
}

func (s *Service) memberships(ctx context.Context, userID uuid.UUID) ([]Membership, error) {
	rows, err := s.store.ListMembersByUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := make([]Membership, len(rows))
	for i, r := range rows {
		out[i] = Membership{WorkspaceID: r.WorkspaceID, WorkspaceName: r.WorkspaceName, Role: string(r.Role)}
	}
	return out, nil
}
```
> Note: `pgxTimestamp`, `pgxTime`, `ptr`, `parseIP` are tiny adapters between Go types and the pgx-generated field types (`pgtype.Timestamptz`, `*string`, `netip`/`*netip.Addr`). Define them in `internal/app/identity/pgx_adapters.go`, matching the exact generated field types from Task 2. Adjust `RevokedAt`/`ExpiresAt` access to the generated nullable representation.

- [ ] **Step 2: Write unit tests with a fake store**

`internal/app/identity/service_test.go`: implement a `fakeStore` satisfying `storeIface` in-memory. Cover:
```go
// TestRegisterIssuesSession — RegisterTx returns ids -> session has RawRefresh, role owner.
// TestLoginWrongPassword — GetUserByEmail ok, CheckPassword false -> ErrInvalidCredentials.
// TestRefreshRotatesAndRevokesOld — first refresh returns new raw != old; old row marked revoked.
// TestRefreshReuseRevokesFamily — presenting an already-revoked hash calls RevokeFamily and errors.
// TestSwitchWorkspaceNonMember — GetMember error -> ErrNotMember.
```
Write full table-free tests, one function each, asserting the documented behavior (use a real argon2 hash from `auth.HashPassword` for the login test).

- [ ] **Step 3: Run tests**

Run: `export PATH="$PATH:/c/Program Files/Go/bin:$HOME/go/bin" && go test ./internal/app/identity/ -v`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/app/identity/service.go internal/app/identity/service_test.go internal/app/identity/pgx_adapters.go
git commit -m "feat(identity): auth service with rotation, reuse detection, workspace switch"
```

---

## Task 11: Identity HTTP handlers, cookies, routes

**Files:**
- Create: `internal/app/identity/cookies.go`
- Create: `internal/app/identity/handler.go`
- Create: `internal/app/identity/routes.go`
- Test: `internal/app/identity/handler_test.go`

**Interfaces:**
- Consumes: `Service` (Task 10), `auth.IssueToken`, `auth.NewCSRFToken`, `auth.RequireCSRF`, `auth.RequireAuth/UserFromContext`, config cookie/TTL settings.
- Produces: `NewHandler(svc *Service, jwtSecret []byte, accessTTL, refreshTTL time.Duration, cookieSecure bool, cookieDomain string) *Handler`; `PublicRoutes() http.Handler` (register/login/refresh/logout) and `ProtectedRoutes() http.Handler` (me/logout-all/switch-workspace).

- [ ] **Step 1: Cookie helpers `cookies.go`**

```go
package identity

import (
	"net/http"
	"time"

	"github.com/inroad/inroad/internal/app/auth"
)

const refreshCookieName = "inroad_refresh"

func (h *Handler) setRefreshCookie(w http.ResponseWriter, raw string) {
	http.SetCookie(w, &http.Cookie{
		Name: refreshCookieName, Value: raw, Path: "/api/v1/auth",
		Domain: h.cookieDomain, HttpOnly: true, Secure: h.cookieSecure,
		SameSite: http.SameSiteLaxMode, MaxAge: int(h.refreshTTL.Seconds()),
	})
}

func (h *Handler) setCSRFCookie(w http.ResponseWriter) (string, error) {
	tok, err := auth.NewCSRFToken()
	if err != nil {
		return "", err
	}
	http.SetCookie(w, &http.Cookie{
		Name: auth.CSRFCookieName, Value: tok, Path: "/",
		Domain: h.cookieDomain, HttpOnly: false, Secure: h.cookieSecure,
		SameSite: http.SameSiteLaxMode, MaxAge: int(h.refreshTTL.Seconds()),
	})
	return tok, nil
}

func (h *Handler) clearCookies(w http.ResponseWriter) {
	for _, c := range []struct{ name, path string }{{refreshCookieName, "/api/v1/auth"}, {auth.CSRFCookieName, "/"}} {
		http.SetCookie(w, &http.Cookie{Name: c.name, Value: "", Path: c.path, Domain: h.cookieDomain,
			HttpOnly: c.name == refreshCookieName, Secure: h.cookieSecure, SameSite: http.SameSiteLaxMode, MaxAge: -1, Expires: time.Unix(0, 0)})
	}
}
```

- [ ] **Step 2: Handlers `handler.go`**

Implement `register`, `login`, `refresh`, `logout`, `me`, `logoutAll`, `switchWorkspace`. Shared `issueSession(w, r, sess)` mints the access token, sets both cookies, and writes the JSON body. Full code for the shared parts and each handler:
```go
package identity

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/app/auth"
	"github.com/inroad/inroad/internal/platform/httpx"
)

type Handler struct {
	svc          *Service
	jwtSecret    []byte
	accessTTL    time.Duration
	refreshTTL   time.Duration
	cookieSecure bool
	cookieDomain string
}

func NewHandler(svc *Service, jwtSecret []byte, accessTTL, refreshTTL time.Duration, cookieSecure bool, cookieDomain string) *Handler {
	return &Handler{svc: svc, jwtSecret: jwtSecret, accessTTL: accessTTL, refreshTTL: refreshTTL, cookieSecure: cookieSecure, cookieDomain: cookieDomain}
}

type membershipDTO struct {
	WorkspaceID   string `json:"workspace_id"`
	WorkspaceName string `json:"workspace_name"`
	Role          string `json:"role"`
}
type sessionResponse struct {
	AccessToken       string          `json:"access_token"`
	ExpiresIn         int             `json:"expires_in"`
	UserID            string          `json:"user_id"`
	ActiveWorkspaceID string          `json:"active_workspace_id"`
	Role              string          `json:"role"`
	Memberships       []membershipDTO `json:"memberships"`
}

func clientMeta(r *http.Request) (ua, ip string) {
	return r.UserAgent(), strings.Split(r.RemoteAddr, ":")[0]
}

func (h *Handler) issueSession(w http.ResponseWriter, sess Session) {
	access, err := auth.IssueToken(h.jwtSecret, auth.Claims{
		UserID: sess.UserID.String(), WorkspaceID: sess.WorkspaceID.String(),
		Role: sess.Role, SessionID: sess.SessionID.String(),
	}, h.accessTTL)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "could not issue token")
		return
	}
	h.setRefreshCookie(w, sess.RawRefresh)
	if _, err := h.setCSRFCookie(w); err != nil {
		httpx.Error(w, http.StatusInternalServerError, "could not issue csrf token")
		return
	}
	dto := make([]membershipDTO, len(sess.Memberships))
	for i, m := range sess.Memberships {
		dto[i] = membershipDTO{m.WorkspaceID.String(), m.WorkspaceName, m.Role}
	}
	httpx.JSON(w, http.StatusOK, sessionResponse{
		AccessToken: access, ExpiresIn: int(h.accessTTL.Seconds()),
		UserID: sess.UserID.String(), ActiveWorkspaceID: sess.WorkspaceID.String(),
		Role: sess.Role, Memberships: dto,
	})
}

func (h *Handler) register(w http.ResponseWriter, r *http.Request) {
	var req struct{ WorkspaceName, Email, Password string }
	// use json tags
	var body struct {
		WorkspaceName string `json:"workspace_name"`
		Email         string `json:"email"`
		Password      string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid json")
		return
	}
	req.WorkspaceName, req.Email, req.Password = body.WorkspaceName, body.Email, body.Password
	if req.WorkspaceName == "" || req.Email == "" || len(req.Password) < 8 {
		httpx.Error(w, http.StatusBadRequest, "workspace_name, email, and 8+ char password required")
		return
	}
	ua, ip := clientMeta(r)
	sess, err := h.svc.Register(r.Context(), RegisterInput{WorkspaceName: req.WorkspaceName, Email: req.Email, Password: req.Password, UserAgent: ua, IP: ip})
	if err != nil {
		if isUniqueViolation(err) {
			httpx.Error(w, http.StatusConflict, "email already registered")
			return
		}
		httpx.Error(w, http.StatusInternalServerError, "could not register")
		return
	}
	h.issueSession(w, sess)
}

func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid json")
		return
	}
	ua, ip := clientMeta(r)
	sess, err := h.svc.Login(r.Context(), body.Email, body.Password, ua, ip)
	if err != nil {
		httpx.Error(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	h.issueSession(w, sess)
}

func (h *Handler) refresh(w http.ResponseWriter, r *http.Request) {
	c, err := r.Cookie(refreshCookieName)
	if err != nil {
		httpx.Error(w, http.StatusUnauthorized, "no refresh token")
		return
	}
	ua, ip := clientMeta(r)
	sess, err := h.svc.Refresh(r.Context(), c.Value, ua, ip)
	if err != nil {
		h.clearCookies(w)
		httpx.Error(w, http.StatusUnauthorized, "refresh failed")
		return
	}
	h.issueSession(w, sess)
}

func (h *Handler) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(refreshCookieName); err == nil {
		_ = h.svc.Logout(r.Context(), c.Value)
	}
	h.clearCookies(w)
	httpx.JSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *Handler) me(w http.ResponseWriter, r *http.Request) {
	claims, _ := auth.UserFromContext(r.Context())
	uid, _ := uuid.Parse(claims.UserID)
	mems, err := h.svc.Memberships(r.Context(), uid)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "could not load memberships")
		return
	}
	dto := make([]membershipDTO, len(mems))
	for i, m := range mems {
		dto[i] = membershipDTO{m.WorkspaceID.String(), m.WorkspaceName, m.Role}
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"user_id": claims.UserID, "active_workspace_id": claims.WorkspaceID,
		"role": claims.Role, "memberships": dto,
	})
}

func (h *Handler) logoutAll(w http.ResponseWriter, r *http.Request) {
	claims, _ := auth.UserFromContext(r.Context())
	uid, _ := uuid.Parse(claims.UserID)
	if err := h.svc.LogoutAll(r.Context(), uid); err != nil {
		httpx.Error(w, http.StatusInternalServerError, "could not revoke sessions")
		return
	}
	h.clearCookies(w)
	httpx.JSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *Handler) switchWorkspace(w http.ResponseWriter, r *http.Request) {
	claims, _ := auth.UserFromContext(r.Context())
	var body struct {
		WorkspaceID string `json:"workspace_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid json")
		return
	}
	uid, _ := uuid.Parse(claims.UserID)
	sid, _ := uuid.Parse(claims.SessionID)
	target, err := uuid.Parse(body.WorkspaceID)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid workspace id")
		return
	}
	activeWS, role, err := h.svc.SwitchWorkspace(r.Context(), sid, uid, target)
	if err != nil {
		httpx.Error(w, http.StatusForbidden, "not a member of that workspace")
		return
	}
	access, err := auth.IssueToken(h.jwtSecret, auth.Claims{
		UserID: claims.UserID, WorkspaceID: activeWS.String(), Role: role, SessionID: claims.SessionID,
	}, h.accessTTL)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "could not issue token")
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"access_token": access, "expires_in": int(h.accessTTL.Seconds()),
		"active_workspace_id": activeWS.String(), "role": role,
	})
}

func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "23505") // pg unique_violation
}

var _ = errors.Is
```

- [ ] **Step 3: Routes `routes.go`**

```go
package identity

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/inroad/inroad/internal/app/auth"
)

// PublicRoutes need no access token. refresh/logout self-authenticate via the
// refresh cookie + CSRF double-submit token.
func (h *Handler) PublicRoutes() http.Handler {
	r := chi.NewRouter()
	r.Post("/register", h.register)
	r.Post("/login", h.login)
	r.With(auth.RequireCSRF).Post("/refresh", h.refresh)
	r.With(auth.RequireCSRF).Post("/logout", h.logout)
	return r
}

// ProtectedRoutes require a valid access token (mounted under the protected group).
func (h *Handler) ProtectedRoutes() http.Handler {
	r := chi.NewRouter()
	r.Get("/me", h.me)
	r.Post("/logout-all", h.logoutAll)
	r.Post("/switch-workspace", h.switchWorkspace)
	return r
}
```

- [ ] **Step 4: Handler unit test (register validation + login 401)**

`internal/app/identity/handler_test.go`: construct a `Handler` with a service backed by a fake store; assert `register` with short password → 400, `login` with bad creds → 401, and a successful `register` sets an `inroad_refresh` cookie and returns `access_token`. (Full test bodies with `httptest`.)

- [ ] **Step 5: Run tests + build**

Run: `export PATH="$PATH:/c/Program Files/Go/bin:$HOME/go/bin" && go test ./internal/app/identity/ -v && go build ./internal/app/identity/`
Expected: PASS + compiles.

- [ ] **Step 6: Commit**

```bash
git add internal/app/identity/cookies.go internal/app/identity/handler.go internal/app/identity/routes.go internal/app/identity/handler_test.go
git commit -m "feat(identity): auth handlers, cookies, and routes"
```

---

## Task 12: OpenAPI contract + regenerate frontend types

**Files:**
- Modify: `api/openapi.yaml`
- Regenerate: `web/src/store/api.ts`

**Interfaces:**
- Produces: paths `/auth/register`, `/auth/login`, `/auth/refresh`, `/auth/logout`, `/auth/me`, `/auth/logout-all`, `/auth/switch-workspace`; schemas `RegisterRequest`, `LoginRequest`, `SessionResponse`, `Membership`, `MeResponse`, `SwitchWorkspaceRequest`, `SwitchWorkspaceResponse`. Generates hooks `useAuthRegisterMutation`, `useAuthLoginMutation`, `useAuthRefreshMutation`, `useAuthLogoutMutation`, `useAuthMeQuery`, `useAuthLogoutAllMutation`, `useAuthSwitchWorkspaceMutation` (exact names depend on the codegen `operationId`s — set them explicitly).

- [ ] **Step 1: Replace the auth section of `api/openapi.yaml`**

Remove the old `/workspaces/register` and `/workspaces/login` paths; add the `/auth/*` paths under server base `/api/v1`, each with an explicit `operationId` (e.g. `authRegister`, `authLogin`, `authRefresh`, `authLogout`, `authMe`, `authLogoutAll`, `authSwitchWorkspace`) and the request/response schemas above (mirror the DTO JSON shapes in Task 11). Keep `TokenResponse` removed/renamed to `SessionResponse`.

- [ ] **Step 2: Regenerate + typecheck**

Run: `cd web && npm run <openapi-codegen script> && npm run build`
(Use the repo's existing codegen script — check `web/package.json` / `openapi-codegen.ts`.)
Expected: `web/src/store/api.ts` updates with new hooks; `npm run build` will fail only where `login-form.tsx`/`register-form.tsx` use the old hook names/shapes — fixed in Task 15.

- [ ] **Step 3: Commit**

```bash
git add api/openapi.yaml web/src/store/api.ts
git commit -m "feat(api): auth endpoints in OpenAPI + regenerated client"
```

---

## Task 13: Deny-by-default router + wiring

**Files:**
- Modify: `internal/platform/httpx/router.go`
- Modify: `cmd/inroad/main.go`
- Modify: `internal/app/mailbox/routes.go`
- Delete: `internal/app/workspace/{handler,service,store,routes}.go` (auth responsibilities moved to `identity`; keep `workspace` only if other non-auth code needs it — otherwise remove the package)

**Interfaces:**
- Consumes: `identity.Handler.PublicRoutes()/ProtectedRoutes()`, `auth.RequireAuth`, `mailbox.Handler.Routes()`.
- Produces: mounted API where the protected group applies `auth.RequireAuth(secret)` at its root; public group is the small allowlist.

- [ ] **Step 1: Rewire `main.go`**

Replace the workspace handler wiring and mounts:
```go
identHandler := identity.NewHandler(
	identity.NewService(identity.NewStore(pool), cfg.RefreshTokenTTL),
	cfg.JWTSecret, cfg.AccessTokenTTL, cfg.RefreshTokenTTL, cfg.CookieSecure, cfg.CookieDomain,
)
mbHandler := mailbox.NewHandler(
	mailbox.NewService(mailbox.NewPgStore(queries), mail.NewNetTester(cfg.MailAllowPrivateHosts), sealer),
	cfg.JWTSecret,
)

router := httpx.NewRouter(logger)
// public group (no access-token middleware)
router.Mount("/api/v1/auth", identHandler.PublicRoutes())
// protected group: everything here requires a valid access token
router.Group(func(pr chi.Router) {
	pr.Use(auth.RequireAuth(cfg.JWTSecret))
	pr.Mount("/api/v1/auth", identHandler.ProtectedRoutes())
	pr.Mount("/api/v1/mailboxes", mbHandler.Routes())
})
```
> Note: mounting `/api/v1/auth` twice (public + protected group) is valid in chi because the paths differ (`/register` vs `/me`); if chi rejects the duplicate mount, split protected auth under `/api/v1/account` instead and update `openapi.yaml` + frontend paths to match. Decide at implementation time and keep consistent.

Add imports: `github.com/go-chi/chi/v5`, `github.com/inroad/inroad/internal/app/auth`, `github.com/inroad/inroad/internal/app/identity`; drop `workspace`.

- [ ] **Step 2: Simplify `mailbox/routes.go`**

Remove `r.Use(auth.RequireAuth(h.jwtSecret))` (now guarded at the group). Keep the route definitions. `mailbox.Handler` keeps `jwtSecret` only if still referenced elsewhere; otherwise drop it and the `auth` import.

- [ ] **Step 3: Remove the obsolete workspace auth package**

Delete `internal/app/workspace/*` (its register/login/service/store are superseded by `identity`). Grep for imports: `grep -rn "app/workspace" --include=*.go .` and remove/replace references.

- [ ] **Step 4: Build**

Run: `export PATH="$PATH:/c/Program Files/Go/bin:$HOME/go/bin" && go build ./... && go vet ./...`
Expected: whole module compiles.

- [ ] **Step 5: Commit**

```bash
git add -A
git commit -m "feat(api): deny-by-default routing; mount identity + mailbox under protected group"
```

---

## Task 14: Backend integration tests (deny-by-default + flows)

**Files:**
- Create: `internal/app/identity/service_integration_test.go` (build-tagged like existing integration tests)

**Interfaces:**
- Consumes: real Postgres via the test DB, the identity `Store`/`Service`, and an `httptest.Server` wired like `main.go`.

- [ ] **Step 1: Write integration tests**

Mirror the existing integration-test setup (see `internal/app/workspace/service_integration_test.go` before deletion for the DB harness pattern) and cover:
```
// register -> 200, sets inroad_refresh cookie, creates user+ws+owner member
// register duplicate email -> 409
// login -> 200 with access_token + memberships
// refresh (with cookie + csrf) -> 200 new access; reuse of the pre-rotation cookie -> 401 and family revoked
// GET /mailboxes without token -> 401  (deny-by-default)
// GET /auth/me with token from workspace A cannot see workspace B data -> scoped correctly
```

- [ ] **Step 2: Run integration tests**

Run: `export PATH="$PATH:/c/Program Files/Go/bin:$HOME/go/bin" && make db-up && make migrate-up && make test-integration`
Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add internal/app/identity/service_integration_test.go
git commit -m "test(identity): integration tests for auth flows and deny-by-default"
```

---

## Task 15: Frontend — session slice, bootstrap, reauth

**Files:**
- Modify: `web/src/store/slices/auth.ts`
- Modify: `web/src/store/index.ts`
- Modify: `web/src/store/empty-api.ts`
- Create: `web/src/features/auth/use-auth-bootstrap.ts`
- Modify: `web/src/routes/app.tsx`
- Modify: `web/src/features/auth/{login-form,register-form}.tsx`
- Test: `web/src/store/empty-api.test.ts`

**Interfaces:**
- Produces: `auth` slice `{ accessToken, userId, activeWorkspaceId, role, memberships, status }` (memory only); `setSession`, `clearSession`, `setActiveWorkspace`; `baseQueryWithReauth`; `useAuthBootstrap()`.

- [ ] **Step 1: Reshape the auth slice**

`web/src/store/slices/auth.ts`: state holds `accessToken: string | null`, `userId`, `activeWorkspaceId`, `role`, `memberships: Membership[]`, `status: 'idle'|'loading'|'authed'|'anon'`. `setSession(payload)` fills from the `SessionResponse`; `clearSession()` resets; `setActiveWorkspace({workspaceId, role, accessToken})`.

- [ ] **Step 2: Remove `auth` from persist whitelist**

`web/src/store/index.ts`: change `whitelist: ['ui', 'auth']` → `whitelist: ['ui']`. (Session now comes from the refresh cookie, not localStorage.)

- [ ] **Step 3: `baseQueryWithReauth` + CSRF**

`web/src/store/empty-api.ts`: wrap `fetchBaseQuery` (keep `prepareHeaders` attaching the in-memory access token as Bearer, and reading `csrf_token` cookie → `X-CSRF-Token` header) with a reauth wrapper:
```ts
const rawBaseQuery = fetchBaseQuery({
  baseUrl: '/api/v1',
  credentials: 'include', // send refresh + csrf cookies to /auth endpoints
  prepareHeaders: (headers, { getState }) => {
    const token = (getState() as { auth?: { accessToken?: string | null } }).auth?.accessToken
    if (token) headers.set('authorization', `Bearer ${token}`)
    const csrf = document.cookie.split('; ').find((c) => c.startsWith('csrf_token='))?.split('=')[1]
    if (csrf) headers.set('x-csrf-token', decodeURIComponent(csrf))
    return headers
  },
})

let refreshing: Promise<unknown> | null = null
const baseQueryWithReauth: typeof rawBaseQuery = async (args, api, extra) => {
  let result = await rawBaseQuery(args, api, extra)
  if (result.error?.status === 401) {
    refreshing ??= rawBaseQuery({ url: '/auth/refresh', method: 'POST' }, api, extra).finally(() => { refreshing = null })
    const refreshed = await refreshing
    if ('data' in refreshed && refreshed.data) {
      api.dispatch(setSession(refreshed.data as SessionResponse))
      result = await rawBaseQuery(args, api, extra)
    } else {
      api.dispatch(clearSession())
    }
  }
  return result
}
```
Use `baseQueryWithReauth` as `emptyApi`'s `baseQuery`. (Import `setSession`/`clearSession` lazily to avoid a store↔api cycle — or dispatch by action type string.)

- [ ] **Step 4: Bootstrap hook + guard**

`use-auth-bootstrap.ts`: on mount, if `status==='idle'`, POST `/auth/refresh` once; dispatch `setSession` or `clearSession`; set `status`. `routes/app.tsx` `beforeLoad`: if `auth.status==='idle'`, await a bootstrap promise, then redirect to `/` when no token. (Simplest: run bootstrap in `__root` and gate `/app` on `status!=='anon' && token`.)

- [ ] **Step 5: Update login/register forms**

Point to the regenerated hook names (Task 12), read the new `SessionResponse` (`access_token`, `active_workspace_id`, `role`, `memberships`) into `setSession`, then `navigate({ to: '/app/mailboxes' })`. Update the login test mock/assertions if hook names changed.

- [ ] **Step 6: Frontend tests**

`web/src/store/empty-api.test.ts`: with a mocked fetch, assert a 401 triggers one `/auth/refresh` then a retry; and that a failed refresh dispatches `clearSession`. Keep the existing login-form test green.

- [ ] **Step 7: Run FE build + tests**

Run: `cd web && npm run build && npx vitest run`
Expected: build clean; tests pass.

- [ ] **Step 8: Commit**

```bash
git add web/src
git commit -m "feat(web): in-memory session, silent refresh, reauth-on-401, CSRF header"
```

---

## Task 16: Frontend — workspace switcher + logout

**Files:**
- Create: `web/src/features/auth/workspace-switcher.tsx`
- Modify: `web/src/components/layout/app-header.tsx`

**Interfaces:**
- Consumes: `useAuthMeQuery`/memberships from the slice, `useAuthSwitchWorkspaceMutation`, `useAuthLogoutMutation`, `useAuthLogoutAllMutation`.

- [ ] **Step 1: Build the switcher**

`workspace-switcher.tsx`: a `DropdownMenu` listing memberships (active checked); selecting one calls `switch-workspace`, dispatches `setActiveWorkspace`, and invalidates workspace-scoped queries (RTK Query `api.util.resetApiState()` or targeted tag invalidation).

- [ ] **Step 2: Wire header**

`app-header.tsx`: replace the static "Acme Owner" block — put the `WorkspaceSwitcher` near the brand and wire the user-menu **Log out** to `useAuthLogoutMutation` (then `clearSession()` + `navigate({to:'/'})`), plus a **Log out everywhere** item calling `useAuthLogoutAllMutation`.

- [ ] **Step 3: Build + lint**

Run: `cd web && npm run build && npm run lint`
Expected: clean (benign fast-refresh warnings only).

- [ ] **Step 4: Commit**

```bash
git add web/src
git commit -m "feat(web): workspace switcher and logout wired to auth endpoints"
```

---

## Task 17: End-to-end verification

**Files:** none (verification only).

- [ ] **Step 1: Full backend suite**

Run: `export PATH="$PATH:/c/Program Files/Go/bin:$HOME/go/bin" && go build ./... && go vet ./... && make test && make test-integration`
Expected: all green.

- [ ] **Step 2: Drive the app**

Start API + web; register a new workspace → land in `/app`; reload → still authed (silent refresh); switch workspace; log out → redirected to `/` and `/app` now redirects to login; confirm an API call after logout 401s and does not loop. Use the `run`/`verify` skill or browser MCP to screenshot the authed shell.

- [ ] **Step 3: Final commit (docs)**

Update `docs/architecture.md` / `docs/self-hosting.md` with the new env vars and auth flow; commit `docs: document production auth (phase 1)`.

---

## Self-Review

**Spec coverage:** schema (T1–T2), argon2id (T3), access token role/sid (T4), opaque refresh + rotation + reuse detection (T5, T10), CSRF (T7, T11), config (T6), middleware + RequireRole (T8), identity store/service/handlers/routes (T9–T11), OpenAPI (T12), deny-by-default routing (T13), integration incl. 401/403 (T14), frontend memory session + silent refresh + reauth + switcher + logout (T15–T16), verification (T17). All spec sections mapped.

**Placeholder scan:** logic-bearing steps contain real code. Two flagged adaptation points are explicit, not vague: (a) sqlc-generated type/field names must be matched after `sqlc generate` (T2/T9/T10), and (b) the chi double-mount of `/api/v1/auth` may need the `/account` fallback (T13) — both name the exact decision and the fallback.

**Type consistency:** `Claims{UserID,WorkspaceID,Role,SessionID}` used consistently (T4→T8→T11). `Session`/`Membership` service types flow into handler DTOs (T10→T11) and the `SessionResponse` JSON shape into the frontend slice (T11→T12→T15). `NewRefreshToken`/`HashRefreshToken` (T5) used in the service (T10). Store method names match between `storeIface` (T10) and `Store` (T9).
