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
