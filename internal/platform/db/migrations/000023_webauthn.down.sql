-- Reverse 000023. Both tables only reference users, so drop order is free; each
-- DROP removes its own indexes and the kind CHECK constraint. up/down/up is clean.
DROP TABLE webauthn_challenges;
DROP TABLE webauthn_credentials;
