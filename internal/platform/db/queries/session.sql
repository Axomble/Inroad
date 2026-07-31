-- name: CreateSession :one
INSERT INTO sessions (user_id, workspace_id, token_hash, family_id, expires_at, user_agent, ip)
VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING *;

-- name: GetSessionByHash :one
SELECT * FROM sessions WHERE token_hash = $1;

-- name: RevokeSession :execrows
UPDATE sessions SET revoked_at = now() WHERE id = $1 AND revoked_at IS NULL;

-- name: RevokeFamily :many
-- Revoke every still-live session in a refresh-token family, returning the ids
-- actually flipped so an in-process caller (logout) can bust the verifier's
-- cached auth-state for each one (a revoke otherwise waits out the cache TTL).
UPDATE sessions SET revoked_at = now() WHERE family_id = $1 AND revoked_at IS NULL
RETURNING id;

-- name: RevokeAllForUser :many
-- Revoke every still-live session for a user (logout-everywhere / password
-- reset), returning the ids actually flipped so the caller can bust each
-- session's cached auth-state in-process (mirrors RevokeOtherSessionsForUser).
UPDATE sessions SET revoked_at = now() WHERE user_id = $1 AND revoked_at IS NULL
RETURNING id;

-- name: RepointSessionWorkspace :one
-- user_id is included in the WHERE clause so a caller can only ever
-- repoint their OWN session, never someone else's — even if a session id
-- somehow leaked into a caller's context. RETURNING token_version surfaces the
-- session's LIVE token_version so a same-session re-issue (switch-workspace)
-- mints an access token whose `tv` matches the session row — never a hardcoded
-- 0 that a later tv-bump would instantly invalidate. A non-matching (session,
-- user) pair updates 0 rows, so this returns pgx.ErrNoRows, which the store
-- maps to ErrNotMember (403).
UPDATE sessions SET workspace_id = $2 WHERE id = $1 AND user_id = $3
RETURNING token_version;

-- name: GetSessionAuthState :one
-- The minimal per-request validation state the store-backed verifier needs:
-- primary-key lookup returning only revocation/expiry/token_version and the
-- owning user (to cross-check against the token's `sub`). Deliberately does
-- NOT return the token hash — the verifier never needs it.
SELECT user_id, revoked_at, expires_at, token_version FROM sessions WHERE id = $1;

-- name: BumpSessionTokenVersion :exec
-- Invalidate every live access token for a single session (a security event on
-- that session) by advancing its token_version past the `tv` those tokens carry.
UPDATE sessions SET token_version = token_version + 1 WHERE id = $1;

-- name: BumpTokenVersionForUser :exec
-- Invalidate every live access token across ALL of a user's sessions (e.g. a
-- password reset). Belt-and-braces alongside RevokeAllForUser: even a session
-- that somehow escapes revocation has its outstanding access tokens rejected.
UPDATE sessions SET token_version = token_version + 1 WHERE user_id = $1;

-- name: ListActiveSessionsForUser :many
-- A user's live (non-revoked, unexpired) sessions for the session-management
-- UI. Token hashes are never selected. Ordered newest-first.
SELECT id, workspace_id, user_agent, ip, created_at, expires_at
FROM sessions
WHERE user_id = $1 AND revoked_at IS NULL AND expires_at > now()
ORDER BY created_at DESC;

-- name: RevokeSessionOwned :execrows
-- Revoke a single session, pinned to its owning user so a caller can only ever
-- revoke their OWN session (a foreign or unknown id flips 0 rows).
UPDATE sessions SET revoked_at = now() WHERE id = $1 AND user_id = $2 AND revoked_at IS NULL;

-- name: RevokeOtherSessionsForUser :many
-- Revoke every one of a user's active sessions EXCEPT the one given (revoke
-- "other" devices while keeping the current session alive), returning the ids
-- actually revoked so the caller can invalidate any cached auth-state for them.
UPDATE sessions SET revoked_at = now()
WHERE user_id = $1 AND id <> $2 AND revoked_at IS NULL
RETURNING id;
