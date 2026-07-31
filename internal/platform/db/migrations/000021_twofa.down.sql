-- Reverse 000021. No FK dependencies between the three tables (each only
-- references users), so drop order is free; each DROP removes its own indexes.
-- up/down/up is clean.
DROP TABLE two_factor_challenges;
DROP TABLE user_recovery_codes;
DROP TABLE user_totp;
