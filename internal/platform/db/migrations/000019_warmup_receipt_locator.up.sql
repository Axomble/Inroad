-- Warmup receipt locator (spec §7): record WHERE a detected warmup message was
-- found so C5b's engager can relocate it — the RFC822 Message-ID of the received
-- message and the provider folder it landed in (e.g. 'INBOX', 'Junk', 'SPAM',
-- 'JunkEmail'). Both default to '' so the existing idempotent, self-enforcing
-- UpsertWarmupReceipt keeps working and pre-existing rows need no backfill.
ALTER TABLE warmup_receipts
    ADD COLUMN source_folder TEXT NOT NULL DEFAULT '',
    ADD COLUMN message_id    TEXT NOT NULL DEFAULT '';
