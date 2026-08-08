-- name: CreateOauthLoginState :exec
-- Persist the server-side half of a federated sign-in state: only the nonce's
-- SHA-256 (the raw nonce rides in a URL, so it is a bearer credential), the PKCE
-- verifier the exchange must replay, and optionally the HASH of a pending invite
-- token so the invite credential itself never travels to the provider.
INSERT INTO oauth_login_states (nonce_hash, purpose, code_verifier, invite_token_hash, return_to, expires_at)
VALUES ($1, $2, $3, $4, $5, $6);

-- name: ConsumeOauthLoginState :one
-- Atomic single-use consume: claim the row ONLY if it is still unconsumed and
-- unexpired, and return what the callback needs. Zero rows (unknown nonce, wrong
-- purpose, already consumed, expired) surfaces as pgx.ErrNoRows and the callback
-- rejects the state -- so a leaked state URL is usable exactly once. A concurrent
-- double-use: only the first UPDATE matches consumed_at IS NULL, the loser gets
-- zero rows.
UPDATE oauth_login_states SET consumed_at = now()
WHERE nonce_hash = $1 AND purpose = $2 AND consumed_at IS NULL AND expires_at > now()
RETURNING code_verifier, invite_token_hash, return_to;
