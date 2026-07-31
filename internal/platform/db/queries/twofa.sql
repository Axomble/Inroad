-- name: GetUserTOTP :one
SELECT * FROM user_totp WHERE user_id = $1;

-- name: UpsertPendingTOTP :execrows
-- Store a fresh, UNCONFIRMED secret for the user. Overwrites an existing pending
-- secret (re-enrollment before confirm) but the WHERE guard refuses to touch a
-- row that is already confirmed, so 0 rows affected means "already enrolled".
-- A re-enrollment resets last_step to 0: the new secret starts its own
-- replay-defense high-water mark.
INSERT INTO user_totp (user_id, secret_ciphertext)
VALUES ($1, $2)
ON CONFLICT (user_id) DO UPDATE
    SET secret_ciphertext = EXCLUDED.secret_ciphertext,
        confirmed_at = NULL,
        last_step = 0,
        created_at = now()
    WHERE user_totp.confirmed_at IS NULL;

-- name: ConfirmTOTP :execrows
-- Activate a pending secret and seed the replay high-water mark with the step the
-- confirming code matched. Guarded so a replay (already-confirmed) or a missing
-- row flips 0 rows.
UPDATE user_totp SET confirmed_at = now(), last_step = $2
WHERE user_id = $1 AND confirmed_at IS NULL;

-- name: AdvanceTOTPStep :execrows
-- Advance the per-user last-consumed TOTP step. The last_step < $2 guard makes the
-- advance itself the atomic replay gate: two concurrent logins presenting the SAME
-- code both read the old mark, but only the first advance past that step affects a
-- row — the loser flips 0 rows and its verify is rejected, so a code is accepted at
-- most once even across a race.
UPDATE user_totp SET last_step = $2
WHERE user_id = $1 AND last_step < $2;

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

-- name: ClaimChallengeAttempt :one
-- Atomically claim one wrong-guess slot: increment attempts iff the challenge is
-- still live (unconsumed) and under the cap, returning the new count. 0 rows
-- (pgx.ErrNoRows) means the challenge is consumed or exhausted — a dead challenge.
-- Doing the cap check and the increment in ONE statement closes the TOCTOU window
-- where N concurrent verifies each read a stale sub-cap count and all proceed.
UPDATE two_factor_challenges SET attempts = attempts + 1
WHERE id = $1 AND consumed_at IS NULL AND attempts < $2
RETURNING attempts;

-- name: ConsumeChallenge :execrows
-- Single-use: only succeeds if not already consumed.
UPDATE two_factor_challenges SET consumed_at = now()
WHERE id = $1 AND consumed_at IS NULL;

-- name: CountRecentChallengesForIP :one
-- Per-IP throttle on challenge issuance. IS NOT DISTINCT FROM behaves like = for a
-- real address but ALSO matches NULL = NULL, so unknown-IP callers (a null/
-- unparseable client IP stored as NULL) share ONE bucket and are throttled
-- together — the throttle fails CLOSED instead of skipping on a null IP.
SELECT count(*) FROM two_factor_challenges
WHERE ip IS NOT DISTINCT FROM $1 AND created_at > $2;

-- name: DeleteChallengesForUser :exec
DELETE FROM two_factor_challenges WHERE user_id = $1;
