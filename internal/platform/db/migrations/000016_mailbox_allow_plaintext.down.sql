-- Reverse 000016. Re-add use_tls with its EXACT original definition from 000002
-- (BOOLEAN NOT NULL DEFAULT TRUE), then drop allow_plaintext. up/down/up is
-- clean; the per-row use_tls values are not preserved across the cycle (the
-- column carried no live behavior).
ALTER TABLE mailboxes ADD COLUMN use_tls BOOLEAN NOT NULL DEFAULT TRUE;

ALTER TABLE mailboxes DROP COLUMN allow_plaintext;
