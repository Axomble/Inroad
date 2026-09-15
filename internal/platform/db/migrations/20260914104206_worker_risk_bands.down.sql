DROP INDEX IF EXISTS mailbox_worker_assignments_worker_band;
ALTER TABLE mailbox_worker_assignments DROP CONSTRAINT IF EXISTS mailbox_worker_assignments_band_check;
ALTER TABLE mailbox_worker_assignments DROP COLUMN IF EXISTS band;
