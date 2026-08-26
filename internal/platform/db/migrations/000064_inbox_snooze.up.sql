-- Snooze: hide a thread from the default inbox until a chosen moment, then let
-- it come back. The operator's "not now, but don't let me forget" control.
--
-- One row per snoozed thread, deleted on un-snooze rather than tombstoned:
-- "is this thread snoozed" is a question about the present, and a history of
-- past snoozes has no reader. thread_id is the PRIMARY KEY (not a surrogate
-- id), which makes "a thread has at most one active snooze" a schema
-- guarantee rather than something application code has to maintain.
--
-- Deliberately NOT a column on inbox_threads: a snooze is sparse (a handful of
-- threads out of many thousands), and a partial index over a separate table
-- keeps the hot path — the unsnoozed list — reading an index that only
-- contains snoozed rows at all.
--
-- No sweeper job. Whether a snooze is still in force is `snooze_until > now()`,
-- evaluated at read time; a worker flipping rows to "expired" would add a
-- moving part, a lag window, and a second source of truth for no gain.
CREATE TABLE inbox_thread_snoozes (
    thread_id    UUID PRIMARY KEY REFERENCES inbox_threads(id) ON DELETE CASCADE,
    workspace_id UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    -- The moment the thread returns to the inbox. Always in the future when
    -- written (the service rejects a past value); may be in the past by the
    -- time it is read, which is exactly how a snooze expires.
    snooze_until TIMESTAMPTZ NOT NULL,
    -- Who snoozed it, for display ("snoozed by Ada"). ON DELETE SET NULL: a
    -- departed member must not drag their teammates' snoozes out of the inbox
    -- with them.
    snoozed_by   UUID REFERENCES users(id) ON DELETE SET NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Serves both directions of the snooze question:
--   * the `snoozed` scope — "which of this workspace's threads are still
--     snoozed", an index-only range scan on (workspace_id, snooze_until);
--   * every other scope's exclusion of them, via the same index.
CREATE INDEX idx_inbox_thread_snoozes_workspace_until
    ON inbox_thread_snoozes (workspace_id, snooze_until);
