-- name: GetUserTOTP :one
SELECT * FROM user_totp WHERE user_id = $1;

-- name: UpsertPendingTOTP :execrows
-- Store a fresh, UNCONFIRMED secret for the user. Overwrites an existing pending
-- secret (re-enrollment before confirm) but the WHERE guard refuses to touch a
-- row that is already confirmed, so 0 rows affected means "already enrolled".
INSERT INTO user_totp (user_id, secret_ciphertext)
VALUES ($1, $2)
ON CONFLICT (user_id) DO UPDATE
    SET secret_ciphertext = EXCLUDED.secret_ciphertext,
        confirmed_at = NULL,
        created_at = now()
    WHERE user_totp.confirmed_at IS NULL;

-- name: ConfirmTOTP :execrows
-- Activate a pending secret. Guarded so a replay (already-confirmed) or a missing
-- row flips 0 rows.
UPDATE user_totp SET confirmed_at = now()
WHERE user_id = $1 AND confirmed_at IS NULL;

-- name: DeleteTOTP :exec
DELETE FROM user_totp WHERE user_id = $1;

-- name: CreateRecoveryCode :exec
INSERT INTO user_recovery_codes (user_id, code_hash) VALUES ($1, $2);

-- name: ListUnusedRecoveryCodes :many
SELECT id, code_hash FROM user_recovery_codes
WHERE user_id = $1 AND used_at IS NULL;

-- name: CountUnusedRecoveryCodes :one
SELECT count(*) FROM user_recovery_codes
WHERE user_id = $1 AND used_at IS NULL;

-- name: UseRecoveryCode :execrows
-- Single-use: only succeeds if the code has not already been used.
UPDATE user_recovery_codes SET used_at = now()
WHERE id = $1 AND used_at IS NULL;

-- name: DeleteRecoveryCodes :exec
DELETE FROM user_recovery_codes WHERE user_id = $1;

-- name: CreateChallenge :one
INSERT INTO two_factor_challenges (user_id, challenge_hash, ip, expires_at)
VALUES ($1, $2, $3, $4)
RETURNING id;

-- name: GetChallengeByHash :one
SELECT * FROM two_factor_challenges WHERE challenge_hash = $1;

-- name: IncrementChallengeAttempts :one
UPDATE two_factor_challenges SET attempts = attempts + 1
WHERE id = $1
RETURNING attempts;

-- name: ConsumeChallenge :execrows
-- Single-use: only succeeds if not already consumed.
UPDATE two_factor_challenges SET consumed_at = now()
WHERE id = $1 AND consumed_at IS NULL;

-- name: CountRecentChallengesForIP :one
-- Per-IP throttle on challenge issuance. A NULL ip never matches (unknown-IP
-- callers are not throttled against each other).
SELECT count(*) FROM two_factor_challenges
WHERE ip = $1 AND created_at > $2;

-- name: DeleteChallengesForUser :exec
DELETE FROM two_factor_challenges WHERE user_id = $1;
