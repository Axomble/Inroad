DROP INDEX IF EXISTS idx_warmup_participants_sentinel;
ALTER TABLE warmup_participants DROP COLUMN IF EXISTS is_sentinel;
