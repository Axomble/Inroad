-- Per-user last-consumed TOTP time-step (RFC 6238 §5.2 replay defense).
--
-- Without a persisted high-water mark, a single valid TOTP code can complete
-- MULTIPLE login challenges within its ±1-step (~90s) validity window. last_step
-- records the highest time-step counter already accepted for the user; a code
-- whose matched step is <= last_step is rejected as an already-consumed step, and
-- an accepted step advances the mark in the SAME transaction that consumes the
-- challenge (login) / confirms the factor (enroll). Defaults to 0 so every first
-- accepted step (always > 0 for any real wall-clock time) passes.
ALTER TABLE user_totp ADD COLUMN last_step BIGINT NOT NULL DEFAULT 0;
