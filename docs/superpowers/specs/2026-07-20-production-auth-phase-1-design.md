# Production Auth — Phase 1: Identity Core & Secure-by-Default Routing

**Status:** Approved design · **Date:** 2026-07-20 · **Owner:** frontend/backend

Phase 1 of a three-phase effort to replace Inroad's v1 stateless-JWT auth with a production-grade
identity system. This phase delivers the backbone: multi-workspace identity, an access/refresh token
model with server-side revocation, CSRF protection, and deny-by-default route security. Phases 2 (account
lifecycle via email: verification, password reset, invites) and 3 (rate limiting, lockout, audit logging)
build on this and are specified separately.

## Goals

- Users are accounts that can belong to **many workspaces**; every request is scoped to one active
  workspace and authorized by the member's role in it.
- **Short-lived access tokens** (in memory on the client) + **rotating, revocable refresh tokens** (httpOnly
  cookie), so sessions can be ended server-side ("log out everywhere").
- **Every API route is authenticated by default**; public routes are a small explicit allowlist.
- CSRF-safe: normal API calls use Bearer access tokens (CSRF-immune); the only cookie-authenticated
  endpoints (`/auth/refresh`, `/auth/logout`) are protected with a double-submit CSRF token.
- `argon2id` password hashing.

## Non-goals (deferred)

- Email verification, password reset, workspace invites → **Phase 2**.
- Rate limiting, account lockout, audit logging → **Phase 3**.
- SSO / OAuth / SAML, 2FA/TOTP, passkeys → not scheduled.
- Workspace subdomains/slugs.

## Decisions (resolved during brainstorming)

- Token transport: in-memory access token + httpOnly refresh cookie. **Approved.**
- Feature scope split into 3 phases; build Phase 1 first. **Approved.**
- Password hashing: `argon2id`. **Approved.**
- Multi-workspace membership (Twenty-style). **Approved.**
- Transactional email: pluggable sender (console in dev, SMTP in prod) — **Phase 2**, noted here for schema
  forward-compatibility only.
- **Login with multiple workspaces:** default the session to the user's most-recently-used membership
  (fallback: earliest-joined); the user changes workspace via the in-app switcher. No separate
  "choose a workspace" screen. **Approved.**

---

## 1. Data model

New migration pair `internal/platform/db/migrations/000002_auth_multiworkspace.{up,down}.sql`. This
restructures the v1 `users` table (which embedded `workspace_id` + `role`). The app is pre-production with
no real data to preserve; the migration recreates the identity tables.

```sql
-- users: one account per person, globally unique email
CREATE TABLE users (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    email             CITEXT NOT NULL UNIQUE,           -- case-insensitive unique
    password_hash     TEXT NOT NULL,
    email_verified_at TIMESTAMPTZ,                       -- nullable; enforced in Phase 2
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE workspaces (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name       TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- membership + role live here (the join)
CREATE TYPE member_role AS ENUM ('owner', 'admin', 'member');
CREATE TABLE workspace_members (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    user_id      UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role         member_role NOT NULL DEFAULT 'member',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at TIMESTAMPTZ,                            -- drives "most-recent workspace" at login
    UNIQUE (workspace_id, user_id)
);
CREATE INDEX idx_members_user ON workspace_members (user_id);

-- refresh-token sessions (opaque tokens, stored hashed)
CREATE TABLE sessions (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id      UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    workspace_id UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    token_hash   BYTEA NOT NULL UNIQUE,                  -- SHA-256 of the opaque refresh token
    family_id    UUID NOT NULL,                          -- rotation lineage; reuse revokes the family
    expires_at   TIMESTAMPTZ NOT NULL,
    revoked_at   TIMESTAMPTZ,
    user_agent   TEXT,
    ip           INET,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_sessions_user ON sessions (user_id);
CREATE INDEX idx_sessions_family ON sessions (family_id);
```

- `CITEXT` (Postgres extension) makes email uniqueness case-insensitive; enable via
  `CREATE EXTENSION IF NOT EXISTS citext;` in the migration.
- sqlc queries added to `internal/platform/db/queries/{user,workspace,member,session}.sql` and regenerated
  into `internal/platform/db/gen`.

## 2. Token strategy

### Access token
- JWT, HS256, signed with `INROAD_JWT_SECRET`. TTL **15m** (`INROAD_ACCESS_TOKEN_TTL`).
- Claims: `sub`=userID, `wid`=active workspaceID, `role`=member role in that workspace, `sid`=session id,
  `iat`, `exp`. Parser keeps the existing HMAC-method guard against `alg` confusion.
- Client stores it **in memory only** (Redux, not persisted).

### Refresh token
- **Opaque**, 32 bytes from `crypto/rand`, base64url-encoded. Not a JWT — no secret needed; it's a lookup
  key. TTL **30d** (`INROAD_REFRESH_TOKEN_TTL`, default `720h`).
- Stored as `SHA-256(token)` in `sessions.token_hash`; the raw value exists only in the cookie.
- Delivered as cookie: `HttpOnly; Secure (INROAD_COOKIE_SECURE); SameSite=Lax; Path=/api/v1/auth;
  Domain=INROAD_COOKIE_DOMAIN; Max-Age=<ttl>`.
- **Rotation:** every `/auth/refresh` issues a new refresh token in the same `family_id`, marks the
  presented one `revoked_at=now()`, and returns a fresh access token.
- **Reuse detection:** if a token that is already revoked (or unknown) is presented, revoke **all sessions
  sharing its `family_id`** and reject (401). This neutralizes stolen-token replay.

### CSRF
- API calls carry the access token as `Authorization: Bearer …` → not cookie-driven → CSRF-immune.
- The cookie-authenticated endpoints (`/auth/refresh`, `/auth/logout`) require a **double-submit token**:
  on login/refresh the server also sets a readable `csrf_token` cookie (not httpOnly); the client echoes it
  in an `X-CSRF-Token` header; the server rejects (403) if header ≠ cookie.

## 3. Endpoints

Base path `/api/v1/auth`. Request/response bodies documented in `api/openapi.yaml`.

| Method | Path | Auth | Purpose |
|---|---|---|---|
| POST | `/register` | public | Create user + first workspace + `owner` membership; start a session |
| POST | `/login` | public | Verify credentials; start a session on the default workspace |
| POST | `/refresh` | refresh cookie + CSRF | Rotate refresh token; return new access token |
| POST | `/logout` | refresh cookie + CSRF | Revoke current session; clear cookies |
| POST | `/logout-all` | access token | Revoke all of the user's sessions |
| GET | `/me` | access token | Current user, memberships, active workspace + role |
| POST | `/switch-workspace` | access token | Verify membership; re-point session + issue access token for target workspace |

- **Register/login success** → body `{ access_token, expires_in, user, memberships, active_workspace_id }`;
  `Set-Cookie` refresh + csrf.
- **Register** is transactional: user + workspace + membership commit together, or nothing does (fixes the
  v1 orphan-workspace bug). Duplicate email → **409**.
- **Login** default workspace = membership with newest `last_seen_at`, else earliest `created_at`; updates
  `last_seen_at`.
- **switch-workspace**: 403 if the user isn't a member of the target; on success updates `sessions.workspace_id`
  and `last_seen_at`, returns a new access token.

## 4. Secure-by-default routing

- `internal/app/auth/middleware.go` gains:
  - `RequireAuth` (exists; extended to load `{userID, workspaceID, role, sessionID}` into context).
  - `RequireRole(min member_role)` for admin/owner-gated routes (e.g. workspace/member management).
- Router structure (`cmd/inroad/main.go` + `internal/platform/httpx`): a **public group** (no `RequireAuth`,
  tiny and explicit) and a **protected group** that has `RequireAuth` applied at the group root. New feature
  routers mount under the protected group, so they are secure unless a maintainer consciously places them in
  the public group.
  - **Public group** (no access-token middleware): `GET /healthz`, `POST /auth/register`, `POST /auth/login`,
    `POST /auth/refresh`, `POST /auth/logout`. `refresh` and `logout` are not "unauthenticated" — they
    self-authenticate via the refresh cookie + CSRF token rather than an access token, which is why they sit
    outside the access-token middleware.
  - **Protected group** (access-token `RequireAuth`): everything else — `GET /auth/me`,
    `POST /auth/logout-all`, `POST /auth/switch-workspace`, and all feature routers (mailboxes, …).
- Existing mailbox routes move under the protected group; the ad-hoc `r.Use(auth.RequireAuth(...))` in
  `internal/app/mailbox/routes.go` is removed in favor of the group-level guard.
- The `wid` in the access token remains the tenant-scoping key handlers use.

## 5. Frontend

- **Auth slice** (`web/src/store/slices/auth.ts`): holds `accessToken` (memory), `user`, `memberships`,
  `activeWorkspaceId`, `role`. **Removed from the redux-persist whitelist** — only `ui` persists. Session
  continuity comes from the refresh cookie, not localStorage.
- **Bootstrap silent refresh:** on app start, call `/auth/refresh`; on success populate the slice, on
  failure treat as logged out. The `/app` route guard (`beforeLoad`) awaits this bootstrap so guards don't
  race the refresh.
- **RTK Query base query** (`web/src/store/empty-api.ts`): `baseQueryWithReauth` — on a 401, attempt one
  `/auth/refresh` (single-flight/mutex so concurrent 401s don't stampede), retry the original request once;
  if refresh fails, dispatch `clearSession` and redirect to `/`. Attach `X-CSRF-Token` on the auth cookie
  endpoints.
- **UI:** header **workspace switcher** (from `/me` memberships) calling `/auth/switch-workspace`; a real
  **logout** and **logout-everywhere** in the user menu; role available for conditional UI.
- New generated types/hooks flow from the updated `api/openapi.yaml` into `store/api.ts` (never hand-edited).

## 6. Password hashing

`internal/app/auth/password.go` switches to `argon2id` (`golang.org/x/crypto/argon2`):
- Params (initial): time=1, memory=64 MiB, threads=4, keyLen=32, 16-byte random salt.
- Encoded as the standard `$argon2id$v=19$m=65536,t=1,p=4$<salt>$<hash>` string stored in
  `users.password_hash`.
- `HashPassword` / `CheckPassword` keep their signatures; `CheckPassword` parses params from the encoded
  hash (forward-compatible if params change). Constant-time compare.

## 7. Config

`internal/platform/config/config.go` adds:
- `INROAD_ACCESS_TOKEN_TTL` (default `15m`)
- `INROAD_REFRESH_TOKEN_TTL` (default `720h`)
- `INROAD_COOKIE_SECURE` (default `true`; may be `false` for local http dev)
- `INROAD_COOKIE_DOMAIN` (default empty → host-only cookie)

`INROAD_JWT_SECRET` continues to sign access tokens (still required, ≥16 bytes). No new secret needed —
refresh tokens are opaque.

## 8. Testing

**Go unit**
- argon2id hash → verify round-trip; wrong password fails; params parsed from encoded hash.
- access token issue → parse (claims round-trip); expired token rejected; non-HMAC `alg` rejected.
- refresh rotation: new token issued, old revoked; **reuse of revoked token revokes the whole family**.
- CSRF double-submit: mismatch/missing header → 403.

**Go integration** (needs `make db-up`)
- register → sets cookies, creates user+workspace+owner membership atomically; duplicate email → 409.
- login → default workspace selection; `/me` shape.
- refresh → rotates cookie + returns access; logout → session revoked, subsequent refresh 401.
- switch-workspace → member OK / non-member 403.
- **deny-by-default:** representative protected route unauthenticated → 401; cross-workspace access → 403.

**Frontend**
- `baseQueryWithReauth` refreshes then retries on 401; gives up + clears session when refresh fails.
- `/app` guard redirects to `/` when bootstrap yields no session.

## 9. Rollout / sequencing within Phase 1

1. Migration + sqlc queries + regenerate.
2. `argon2id` password module.
3. Token module (access issue/parse; opaque refresh mint/hash/rotate/reuse-detect).
4. Session store + auth service (register/login/refresh/logout/switch).
5. Handlers + `openapi.yaml` + regenerate FE types.
6. Deny-by-default router + role middleware; move mailbox under protected group.
7. Frontend: slice, bootstrap refresh, baseQueryWithReauth, switcher, logout.
8. Tests (unit + integration + FE) and verification.

## Open questions

None outstanding. (Multi-workspace login default resolved above.)
