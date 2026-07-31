-- Reverse 000025. The table only references users, so drop order is free and the
-- DROP removes its own partial index. up/down/up is clean.
DROP TABLE email_otp_codes;
