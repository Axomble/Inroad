-- Reverses the up migration exactly, restoring migration 000069's index set.
--
-- The recreated definition is 000069's VERBATIM — (workspace_id, status,
-- created_at DESC), three columns, no id tiebreak — because a down migration that
-- leaves the schema subtly different from the state it claims to restore is worse
-- than no down migration: the next `up` would then be a no-op on a database that
-- is not actually in the pre-up state.
--
-- Dropping idx_task_dead_letters_workspace_created is safe on the way down for
-- the same reason it was needed on the way up: the only statement that wanted it
-- is the keyset ListTaskDeadLetters, which reverts to LIMIT/OFFSET alongside this
-- file. The offset form's unfiltered case was equally unserved by the composite
-- index before this migration, so reversing restores the status quo rather than
-- introducing a regression.
--
-- Not CONCURRENTLY, for the same transaction-per-file reason stated in the up.
DROP INDEX IF EXISTS idx_task_dead_letters_workspace_created;

DROP INDEX IF EXISTS idx_task_dead_letters_workspace_status_created;
CREATE INDEX idx_task_dead_letters_workspace_status_created
    ON task_dead_letters (workspace_id, status, created_at DESC);
