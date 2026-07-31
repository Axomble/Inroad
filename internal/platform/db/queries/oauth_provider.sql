-- name: CreateOauthClient :one
-- Register a client (RFC 7591). client_secret_hash is NULL for a public PKCE client.
-- created_by_user_id / workspace_id carry the registering admin's user + workspace:
-- registration is admin-authed, so both are populated for every API-minted client
-- (the columns stay nullable only to avoid a breaking NOT NULL migration).
INSERT INTO oauth_clients (
    client_id, client_secret_hash, client_name, redirect_uris, grant_types,
    response_types, scopes, client_type, token_endpoint_auth_method,
    created_by_user_id, workspace_id
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
RETURNING *;

-- name: GetOauthClient :one
-- Resolve a client by its public client_id (the /authorize + token lookup).
SELECT * FROM oauth_clients WHERE client_id = $1;

-- name: ListOauthClientsByWorkspace :many
-- Admin management list. Deliberately omits client_secret_hash so no digest can
-- leak through a response DTO (secrets absent by construction).
SELECT id, client_id, client_name, redirect_uris, grant_types, response_types,
       scopes, client_type, token_endpoint_auth_method, created_by_user_id,
       workspace_id, created_at, revoked_at
FROM oauth_clients
WHERE workspace_id = $1
ORDER BY created_at DESC;

-- name: RevokeOauthClient :execrows
-- Idempotent, tenant-pinned revoke: COALESCE keeps an already-set revoked_at, while
-- a foreign or unknown (workspace_id, client_id) pair affects 0 rows -> 404.
UPDATE oauth_clients SET revoked_at = COALESCE(revoked_at, now())
WHERE client_id = $1 AND workspace_id = $2;

-- name: CreateOauthAuthRequest :exec
-- Persist a fully-validated authorization request for the consent handoff.
INSERT INTO oauth_authorization_requests (
    consent_id, client_id, redirect_uri, scopes, state, code_challenge,
    code_challenge_method, user_id, workspace_id, expires_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10);

-- name: GetOauthAuthRequest :one
-- Load a pending authorization request by its opaque consent_id.
SELECT * FROM oauth_authorization_requests WHERE consent_id = $1;

-- name: ConsumeOauthAuthRequest :execrows
-- Single-use consume of a pending request, pinned to its owner and TTL. 0 rows means
-- unknown / already consumed / expired / not this user's — the caller maps that to a
-- 404 (no oracle distinguishing the cases).
UPDATE oauth_authorization_requests SET consumed_at = now()
WHERE consent_id = $1 AND user_id = $2 AND consumed_at IS NULL AND expires_at > now();

-- name: GetOauthConsent :one
-- The remembered consent for (user, client) IN A SPECIFIC WORKSPACE, used to SKIP the
-- consent screen only when it already covers the requested scopes FOR THE CURRENT
-- workspace. Pinning on workspace_id is the anti-cross-tenant guard: a consent granted
-- while active in workspace A must never satisfy an authorize running in workspace B.
SELECT * FROM oauth_consents WHERE user_id = $1 AND client_id = $2 AND workspace_id = $3;

-- name: UpsertOauthConsent :exec
-- Record/refresh a granted consent. One row per (user, client, workspace): a re-grant
-- in the SAME workspace replaces the stored scope set and stamps updated_at; a grant in
-- a different workspace is a separate row (consent is workspace-scoped).
INSERT INTO oauth_consents (user_id, client_id, scopes, workspace_id)
VALUES ($1, $2, $3, $4)
ON CONFLICT (user_id, client_id, workspace_id)
DO UPDATE SET scopes = EXCLUDED.scopes, updated_at = now();

-- name: CreateOauthAuthCode :exec
-- Persist a single-use authorization code bound to every parameter the token
-- endpoint (P6b) must verify. Only the SHA-256 of the raw code is stored.
INSERT INTO oauth_authorization_codes (
    code_hash, client_id, redirect_uri, code_challenge, code_challenge_method,
    scopes, user_id, workspace_id, expires_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9);

-- name: GetOauthAuthCode :one
-- Read an authorization code by its hash (P6a tests assert the persisted binding;
-- the P6b token endpoint adds its own atomic single-use consume below).
SELECT * FROM oauth_authorization_codes WHERE code_hash = $1;

-- name: ConsumeOauthAuthCode :one
-- Atomic single-use consume of an authorization code at the token endpoint (P6b):
-- claim the row ONLY if it is still unconsumed and unexpired, stamping consumed_at,
-- and RETURN its bindings so the exchange can verify client_id/redirect_uri/PKCE. Zero
-- rows (unknown / already consumed / expired) surfaces as pgx.ErrNoRows -> the caller
-- maps it to invalid_grant. A concurrent double-redeem: only the first UPDATE matches
-- (consumed_at IS NULL), the loser gets zero rows — so a code is redeemable exactly once.
UPDATE oauth_authorization_codes
SET consumed_at = now()
WHERE code_hash = $1 AND consumed_at IS NULL AND expires_at > now()
RETURNING *;

-- name: CreateOauthAccessToken :exec
-- Persist an issued opaque access token (only its SHA-256 hash), pinned to the client,
-- resource owner, workspace, and granted scope subset.
INSERT INTO oauth_access_tokens (
    token_hash, client_id, user_id, workspace_id, scopes, expires_at
) VALUES ($1, $2, $3, $4, $5, $6);

-- name: GetOauthAccessTokenByHash :one
-- The verifier + introspection lookup: resolve an access token by its hash. The
-- verifier re-checks revoked_at + expiry each request, so revocation is immediate.
SELECT * FROM oauth_access_tokens WHERE token_hash = $1;

-- name: RevokeOauthAccessToken :execrows
-- Client-scoped revoke (RFC 7009): a client may revoke only its OWN access token. A
-- foreign or unknown (token_hash, client_id) pair flips 0 rows, so the handler still
-- returns 200 with no token-existence oracle. Idempotent via COALESCE.
UPDATE oauth_access_tokens SET revoked_at = COALESCE(revoked_at, now())
WHERE token_hash = $1 AND client_id = $2;

-- name: CreateOauthRefreshToken :exec
-- Persist an issued rotating refresh token (only its SHA-256 hash), tagged with its
-- rotation family so a reuse revoke can kill the whole chain.
INSERT INTO oauth_refresh_tokens (
    token_hash, family_id, client_id, user_id, workspace_id, scopes, expires_at
) VALUES ($1, $2, $3, $4, $5, $6, $7);

-- name: GetOauthRefreshTokenByHash :one
-- Resolve a presented refresh token by its hash for the rotation grant + introspection
-- + revoke. The caller inspects consumed_at/revoked_at/expires_at to detect reuse.
SELECT * FROM oauth_refresh_tokens WHERE token_hash = $1;

-- name: ConsumeOauthRefreshToken :execrows
-- Guarded single-use rotation: mark the presented refresh token consumed ONLY if it is
-- still live (unconsumed, unrevoked, unexpired). 0 rows means it was consumed/revoked/
-- expired between the caller's read and here (a lost race = concurrent reuse) -> the
-- caller revokes the whole family and rejects.
UPDATE oauth_refresh_tokens SET consumed_at = now()
WHERE token_hash = $1 AND consumed_at IS NULL AND revoked_at IS NULL AND expires_at > now();

-- name: RevokeOauthRefreshFamily :execrows
-- Revoke every still-live refresh token sharing a rotation family — the reuse-detection
-- kill switch and the RFC 7009 refresh revoke (revoking one refresh token revokes its
-- whole family). Idempotent.
UPDATE oauth_refresh_tokens SET revoked_at = now()
WHERE family_id = $1 AND revoked_at IS NULL;
