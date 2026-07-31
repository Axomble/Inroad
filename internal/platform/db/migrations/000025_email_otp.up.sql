-- Email-OTP passwordless login codes.
--
-- A single-use, short-lived numeric code emailed to a user who requests a
-- passwordless login. Like user_totp / user_recovery_codes, this is a
-- USER-level artifact (keyed on user_id, no workspace_id): a login code proves
-- inbox possession for the user, independent of which workspace they later
-- activate. Only the argon2id hash of the code is stored; the raw 6-digit code
-- lives only in the delivered email and is never persisted or logged.
CREATE TABLE email_otp_codes (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id      UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    -- argon2id hash of the numeric code (reuses the password hasher params).
    code_hash    TEXT NOT NULL,
    attempts     INT NOT NULL DEFAULT 0,
    -- Per-row cap so a code dies after a bounded number of wrong guesses.
    max_attempts INT NOT NULL DEFAULT 5,
    consumed_at  TIMESTAMPTZ,
    expires_at   TIMESTAMPTZ NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- Verify looks up a user's single active (unconsumed) code; the partial index
-- keeps that lookup cheap and drops consumed codes out of it. Start invalidates
-- any prior unconsumed code for the same user (one active code at a time).
CREATE INDEX idx_email_otp_codes_user_active ON email_otp_codes (user_id) WHERE consumed_at IS NULL;
