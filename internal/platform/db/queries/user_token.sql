-- name: CreateUserToken :one
INSERT INTO user_tokens (user_id, kind, token_hash, expires_at)
VALUES ($1,$2,$3,$4) RETURNING *;

-- name: GetUserTokenByHash :one
SELECT * FROM user_tokens WHERE token_hash = $1 AND kind = $2;

-- name: ConsumeUserToken :one
-- Single-use: only succeeds if not already consumed and not expired.
UPDATE user_tokens SET consumed_at = now()
WHERE token_hash = $1 AND kind = $2 AND consumed_at IS NULL AND expires_at > now()
RETURNING user_id;

-- name: CountRecentUserTokens :one
-- Rate-limit support: how many of this kind issued to this user since $3.
SELECT count(*) FROM user_tokens
WHERE user_id = $1 AND kind = $2 AND created_at > $3;
