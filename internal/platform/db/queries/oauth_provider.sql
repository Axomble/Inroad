-- name: CreateOauthClient :one
-- Register a client (RFC 7591). client_secret_hash is NULL for a public PKCE
-- client. created_by_user_id / workspace_id are NULL for an anonymous registration.
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
-- The remembered consent for (user, client), used to SKIP the consent screen when it
-- already covers the requested scopes.
SELECT * FROM oauth_consents WHERE user_id = $1 AND client_id = $2;

-- name: UpsertOauthConsent :exec
-- Record/refresh a granted consent. One row per (user, client): a re-grant replaces
-- the stored scope set and stamps updated_at.
INSERT INTO oauth_consents (user_id, client_id, scopes, workspace_id)
VALUES ($1, $2, $3, $4)
ON CONFLICT (user_id, client_id)
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
-- the P6b token endpoint will add its own atomic single-use consume).
SELECT * FROM oauth_authorization_codes WHERE code_hash = $1;
