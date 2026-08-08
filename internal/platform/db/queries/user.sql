-- name: CreateUser :one
INSERT INTO users (email, password_hash) VALUES ($1, $2) RETURNING *;

-- name: GetUserByEmail :one
SELECT * FROM users WHERE email = $1;

-- name: GetUserByID :one
SELECT * FROM users WHERE id = $1;

-- name: SetEmailVerified :exec
UPDATE users SET email_verified_at = now() WHERE id = $1;

-- name: UpdatePasswordHash :exec
UPDATE users SET password_hash = $2 WHERE id = $1;

-- name: GetUserIdentity :one
-- Resolve an external identity to its local user. Keyed on the provider's
-- immutable subject id, never on email (see migration 000051).
SELECT * FROM user_identities WHERE provider = $1 AND provider_subject = $2;

-- name: CreateUserIdentity :exec
-- Link an external identity to a local user. Deliberately NOT upsert-or-ignore:
-- every caller inserts only after confirming no row exists for this
-- (provider, subject), so a conflict means a concurrent request already claimed
-- this provider identity for SOME user -- possibly a different one. The
-- UNIQUE (provider, provider_subject) violation must surface as an error and fail
-- the sign-in, rather than being swallowed into a silent no-op that would hand out
-- a session with no link (or with someone else's link) behind it.
INSERT INTO user_identities (user_id, provider, provider_subject)
VALUES ($1, $2, $3);
