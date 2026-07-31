-- name: CreateWebAuthnCredential :one
INSERT INTO webauthn_credentials (
    user_id, credential_id, public_key, sign_count, aaguid, transports,
    attestation_type, backup_eligible, backup_state, label
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
RETURNING *;

-- name: GetWebAuthnCredentialByCredentialID :one
SELECT * FROM webauthn_credentials WHERE credential_id = $1;

-- name: ListWebAuthnCredentialsByUser :many
SELECT * FROM webauthn_credentials WHERE user_id = $1 ORDER BY created_at;

-- name: TouchWebAuthnCredentialSignCount :execrows
-- Persist the post-login signature counter and stamp last_used_at. The $3 >=
-- sign_count guard forbids a REGRESSION (a clone/replay presenting a stale, lower
-- counter affects 0 rows) while still allowing a zero-counter authenticator (many
-- passkeys always report 0) to update last_used_at. Belt-and-braces: the service
-- has already rejected a regressed counter via the library's clone-warning check.
-- User-pinned so a caller can only advance their own credential.
UPDATE webauthn_credentials
SET sign_count = $3, last_used_at = now()
WHERE id = $1 AND user_id = $2 AND $3 >= sign_count;

-- name: DeleteWebAuthnCredential :execrows
-- Own-only: a foreign id affects 0 rows, which the handler maps to 404 so a
-- caller cannot probe or delete another user's credential.
DELETE FROM webauthn_credentials WHERE id = $1 AND user_id = $2;

-- name: CreateWebAuthnChallenge :one
INSERT INTO webauthn_challenges (session_key, user_id, session_data, kind, expires_at)
VALUES ($1, $2, $3, $4, $5)
RETURNING id;

-- name: ConsumeWebAuthnChallenge :one
-- Atomic single-use claim: mark the challenge consumed and return its stored
-- ceremony state in ONE statement, only if it is still live (unconsumed AND
-- unexpired). 0 rows (pgx.ErrNoRows) means the session is unknown, already
-- consumed, or expired — a dead ceremony. Doing the liveness check and the
-- consume together closes the window where two concurrent finishes both proceed.
UPDATE webauthn_challenges SET consumed_at = now()
WHERE session_key = $1 AND consumed_at IS NULL AND expires_at > now()
RETURNING *;
