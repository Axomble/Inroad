-- name: CreateApiKey :one
-- Persist a new key. secret_hash is the SHA-256 of the raw secret (never the
-- secret itself). workspace_id pins tenancy; the caller derives it from the JWT.
INSERT INTO api_keys (
    workspace_id, created_by_user_id, name, prefix, secret_hash,
    scopes, ip_allowlist, rate_limit_per_min, expires_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
RETURNING *;

-- name: GetApiKeyByPrefix :one
-- Verify-path lookup by the globally-unique public prefix. Resolves the workspace
-- and returns the secret_hash for a constant-time compare in the service layer.
SELECT * FROM api_keys WHERE prefix = $1;

-- name: ListApiKeysByWorkspace :many
-- Management list. Deliberately omits secret_hash so no digest can leak through a
-- response DTO (secrets absent by construction, not by remembering to strip).
SELECT id, workspace_id, created_by_user_id, name, prefix,
       scopes, ip_allowlist, rate_limit_per_min, expires_at, revoked_at,
       last_used_at, created_at
FROM api_keys
WHERE workspace_id = $1
ORDER BY created_at DESC;

-- name: RevokeApiKey :execrows
-- Idempotent, tenant-pinned revoke: COALESCE keeps an already-set revoked_at (so a
-- repeat revoke is a no-op that still reports the row exists), while a foreign or
-- unknown (workspace_id, id) pair affects 0 rows -> the handler maps that to 404.
UPDATE api_keys SET revoked_at = COALESCE(revoked_at, now())
WHERE id = $1 AND workspace_id = $2;

-- name: TouchApiKeyLastUsed :exec
-- Best-effort last-use stamp, updated asynchronously off the request path.
UPDATE api_keys SET last_used_at = now() WHERE id = $1;
