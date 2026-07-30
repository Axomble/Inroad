# Auth Hardening — Design Spec (v1)

Status: approved for build · Owner: platform · Date: 2026-07-31

Bring Inroad's auth from "solid single-factor password + JWT" up to a production
multi-factor identity system — revocable sessions, TOTP 2FA, passkeys, scoped API
keys, login rate-limiting, and a **decoupled** OAuth 2.1 provider — **without**
losing the two things Inroad already does better than the reference: refresh-token
family reuse-detection and httpOnly+CSRF cookie hygiene. Clean, Inroad-idiomatic
code; reference implementations are inspiration, not a source to copy.

## 0. North-star architecture — the unified principal seam

Every authenticated request resolves to one `Principal`, produced by trying a set
of pluggable `Verifier`s. This is the single seam that lets first-party sessions,
API keys, and third-party OAuth tokens coexist **without** any of them coupling to
the core, and it is what keeps the OAuth provider extractable into its own service
later.

```go
// internal/app/auth (core — defines the seam, imports no auth-mechanism domain)
type PrincipalKind int
const ( KindSession PrincipalKind = iota; KindAPIKey; KindOAuth )

type Principal struct {
    UserID, WorkspaceID, Role string
    SessionID string        // "" for api-key / oauth
    Kind      PrincipalKind
    Scopes    []string      // full first-party scope for sessions; granted subset for api-key/oauth
}

// A Verifier turns an inbound credential into a Principal, or (false,nil) to defer
// to the next verifier, or (false,err) for a hard auth failure.
type Verifier interface {
    Verify(ctx context.Context, r *http.Request) (Principal, bool, error)
}
```

- `RequireAuth(verifiers ...Verifier)` iterates them; first to return `ok` wins;
  all-defer → 401. `UserFromContext` returns `Principal`; `WorkspaceID`,
  `RequireRole`, `RequireVerified` keep working (Principal carries the same fields,
  so existing handlers don't change).
- **Scope enforcement:** a new `RequireScope("mailboxes:write")` middleware. A
  first-party session Principal implicitly holds all scopes; api-key/oauth
  Principals hold only their granted subset, so the SAME routes work for all three
  callers with per-route scope gating. Scopes are one owned list
  (`internal/app/auth/scopes.go`), the single source of truth.
- Each auth-mechanism domain (`identity`/session, `apikey`, `oauthprovider`)
  provides its `Verifier` at the composition root (`cmd/inroad`). The core never
  imports them (dependency inversion — same pattern as `coreapi.Client`).

## 1. Phases (each contract-first, gated: reviewer + security + qa, live-DB integration)

### P1 — Revocable sessions + the Verifier seam (FOUNDATION)
Turn `RequireAuth` from a stateless parse into the session verifier above.
- `sessionVerifier`: parse the HS256 access JWT (alg-pinned, unchanged) → load the
  session by `sid` → reject if `revoked_at IS NOT NULL`, if the session is expired,
  or if a per-session `token_version` doesn't match the token's `tv` claim (bump on
  security events to invalidate all live access tokens for that session).
- **Access-token TTL drops to ~5 min**; a short-TTL Redis cache of session
  validity avoids a DB hit per request (miss → Postgres → cache); revocation and
  `token_version` bumps bust the cache so a killed session dies on the next request
  (≤ cache TTL).
- Migration: add `token_version INT NOT NULL DEFAULT 0` to `sessions` (the table
  already has `family_id`/`revoked_at`/`last_seen_at`/`user_agent`).
- Session-management API: `GET /auth/sessions` (list, current-flagged),
  `DELETE /auth/sessions/{id}` (revoke one), `POST /auth/sessions/revoke-others`.
- **Preserve:** refresh family reuse-detection, httpOnly+CSRF cookies. Add `tv` to
  claims. Keep the enumeration-safe login/reset flows intact.

### P2 — TOTP 2FA + recovery codes
New domain `internal/app/twofa`. `user_totp` (sealed secret via `crypto.Sealer`,
`confirmed_at`), `user_recovery_codes` (argon2-hashed, single-use).
- Enroll (`POST /auth/2fa/totp` → secret + otpauth URI/QR), confirm
  (`POST /auth/2fa/totp/confirm` with a code → activates + returns recovery codes
  once), disable (requires a fresh code — proof of possession).
- **Login gate:** when a user has confirmed 2FA, `/auth/login` returns a short-lived,
  single-use **2FA challenge** (not a session) instead of tokens; `POST /auth/2fa/verify`
  (TOTP or recovery code) completes it → session. Challenge: 5-min TTL, ≤5 tries,
  per-IP throttle. RFC 6238, constant-time compare, ±1 step skew.

### P3 — Passkeys / WebAuthn
New domain `internal/app/passkey` over `github.com/go-webauthn/webauthn`.
`webauthn_credentials` (credential id, public key, sign count, transports, aaguid,
label). Registration (attestation) + authentication (assertion) ceremonies with
server-stored, single-use, TTL'd challenges. Discoverable/usernameless login
(resident key). A passkey login satisfies MFA (skips the TOTP gate). Manage/delete
credentials.

### P4 — Scoped API keys
New domain `internal/app/apikey` + an `apiKeyVerifier`. `api_keys` (prefix,
sha-256 hash of the 256-bit secret, workspace_id, scopes, ip_allowlist CIDRs,
per-minute rate limit, last_used_at, revoked_at, expires_at).
- Create returns the raw key **once** (`inrk_<prefix>_<secret>`); only the hash is
  stored. Verifier: constant-time hash lookup → IP-allowlist check (fail-closed) →
  Redis atomic per-key rate limit → Principal with the key's scopes.
- List / revoke; usage timestamp. Scopes are the P0 owned list.

### P5 — Login rate-limiting + optional email-OTP + config-gated captcha
- **Rate-limit** (`internal/platform/ratelimit`, Redis atomic): login, 2fa/verify,
  password/forgot, register, per-IP + per-account, fail-open on Redis down.
- **Email-OTP** (optional, `INROAD_LOGIN_OTP=on`): a 6-digit code emailed on
  password login before a session issues — Warmbly's primary brute-force defense;
  composes with the P2 gate (2FA users skip OTP; OTP applies to password-only users).
- **Captcha** (config-gated `INROAD_TURNSTILE_*`, `internal/platform/captcha`):
  Turnstile verify on register/login/forgot; unset keys → skipped (self-host stays
  open), same optional pattern as the mailbox OAuth providers.

### P6a — OAuth 2.1 provider: clients, authorize, consent (DECOUPLED)
New, self-contained domain `internal/app/oauthprovider` — its own tables, routes,
service; imported by NOTHING in the product domains.
- `oauth_clients` (RFC 7591 Dynamic Client Registration `POST /oauth/register`:
  public PKCE clients by default; redirect-URI allowlist rejecting
  `javascript:`/`data:`/opaque/non-loopback-http; per-IP registration cap; scope cap
  that structurally EXCLUDES dangerous scopes — no api-key/admin/send-all grants).
- `GET /oauth/authorize` (PKCE `code_challenge` required, `S256`): authenticates the
  **resource owner** through a narrow `ResourceOwner` interface backed by the P1
  session (never re-implements login) → renders a **consent screen** listing the
  requested scopes → issues a single-use auth code (short TTL, bound to
  client+redirect+PKCE+scopes+user+workspace).

### P6b — OAuth 2.1 provider: token, introspection, revocation
- `POST /oauth/token`: `authorization_code` (verify PKCE `code_verifier`, one-time
  code) + `refresh_token` grants → issues a **scoped OAuth access token** (its own
  short-TTL bearer, distinct from first-party session tokens) + rotating refresh.
- Registers the `oauthVerifier` into P1's resolver: an OAuth bearer → Principal
  with the granted scopes only (so `RequireScope` gates it on the SAME product
  routes, no per-caller branching).
- `POST /oauth/introspect`, `POST /oauth/revoke`. Consent/grant records per
  (user, client) with a "connected apps" management surface.
- **Extractability:** the verifier can later become a token-introspection HTTP call
  with zero change to product code — the seam is the service boundary.

### Frontend (rides each phase)
Security settings hub: session list + revoke; 2FA setup (QR + recovery-code
display/regenerate); passkey register/list/delete; API-key create (one-time reveal)
/list/revoke; the login-with-2FA/passkey/OTP flows; the OAuth consent screen +
"connected apps". Typed from `api/openapi.yaml` (regenerated), errors via
`@/lib/rtk-error`, code-split, tested — the project's frontend bar.

## 2. Security invariants (append to docs/security.md as they land)
1. **Access tokens are revocable ≤ the session-cache TTL.** Every request resolves
   a server-side session and rejects `revoked_at`/stale `token_version`; logout,
   revoke-session, password-reset, 2FA-disable, and passkey-removal all bump/revoke
   and bust the cache.
2. **Second-factor secrets are sealed at rest** (`crypto.Sealer`, per-workspace
   DEK, AAD-bound), never returned after enrollment, never logged. Recovery codes
   and API-key secrets are argon2/sha-256 hashed — the raw value is shown exactly
   once and never re-derivable.
3. **The 2FA/OTP login gate is fail-closed and rate-limited** — a confirmed second
   factor cannot be skipped; challenges are single-use, TTL'd, try-capped, per-IP
   limited; constant-time code compare.
4. **API-key and OAuth callers are scope-limited and tenant-pinned.** They receive
   only granted scopes; `RequireScope` gates every non-trivial route; IP-allowlist
   fail-closed on keys; the OAuth scope cap structurally excludes admin/api-key/
   send-all grants; every query stays `workspace_id`-pinned (invariant 4).
5. **The OAuth provider authenticates the resource owner only through the P1 session
   seam** and never re-implements login; auth codes are single-use, short-TTL, and
   bound to client+redirect+PKCE+scope+user; PKCE `S256` is mandatory; redirect URIs
   are strictly validated (no open redirect / `javascript:`/`data:`).
6. **The provider domain is decoupled:** no product domain imports it; it reaches
   the core only via the `Verifier` + `ResourceOwner` seams.
7. **Preserved:** refresh-family reuse-detection revokes the whole family; the
   long-lived refresh token stays httpOnly+`SameSite=Strict`+CSRF-double-submit and
   never enters JS-reachable storage; login/reset stay enumeration- and
   timing-safe.

## 3. Build order & gates
P1 → P2 → P3 → P4 → P5 → P6a → P6b, plus the frontend per phase (parallel where
disjoint). backend-developer + frontend-developer implement contract-first; every
phase gated by reviewer + security + qa with live-DB integration tests (Docker up).
The `Verifier`/`Principal`/scopes seam (P1) is the contract every later phase plugs
into and must land first. Never reference any third-party product in code, comments,
or commits.
