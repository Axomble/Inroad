-- name: CreateEmailOTP :one
INSERT INTO email_otp_codes (user_id, code_hash, max_attempts, expires_at)
VALUES ($1, $2, $3, $4)
RETURNING id;

-- name: DeleteActiveEmailOTP :exec
-- Invalidate any still-live (unconsumed) code for the user before seeding a fresh
-- one, so there is at most one active code per user at any time.
DELETE FROM email_otp_codes WHERE user_id = $1 AND consumed_at IS NULL;

-- name: GetActiveEmailOTP :one
-- The user's single active (unconsumed) code. ORDER BY created_at DESC LIMIT 1 is
-- belt-and-braces: DeleteActiveEmailOTP already keeps at most one live row.
SELECT id, code_hash, attempts, max_attempts, consumed_at, expires_at
FROM email_otp_codes
WHERE user_id = $1 AND consumed_at IS NULL
ORDER BY created_at DESC
LIMIT 1;

-- name: ClaimEmailOTPAttempt :one
-- Atomically claim one wrong-guess slot: increment attempts iff the code is still
-- live (unconsumed) and under its cap, returning the new count. 0 rows
-- (pgx.ErrNoRows) means the code is consumed or exhausted — dead. Doing the cap
-- check and the increment in ONE statement closes the TOCTOU window where N
-- concurrent verifies each read a stale sub-cap count and all proceed.
UPDATE email_otp_codes SET attempts = attempts + 1
WHERE id = $1 AND consumed_at IS NULL AND attempts < max_attempts
RETURNING attempts;

-- name: ConsumeEmailOTP :execrows
-- Single-use: only succeeds if not already consumed.
UPDATE email_otp_codes SET consumed_at = now()
WHERE id = $1 AND consumed_at IS NULL;
