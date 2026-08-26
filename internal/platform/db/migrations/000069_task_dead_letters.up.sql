-- Dead-letter capture for tasks that exhausted their asynq retries.
--
-- WHY THIS EXISTS: asynq moves a retry-exhausted task to its own archive in
-- Redis, which is invisible to an operator of THIS product (no Asynqmon is
-- shipped), untenanted, and lost whenever Redis is flushed or replaced. For a
-- system whose entire job is "send this email at this time", a permanently
-- dropped send currently produces no durable record and no way to retry it.
-- This table is that record, in Postgres, scoped to the workspace that owns
-- the work, so it can be listed and replayed through the ordinary API.
--
-- It is deliberately NOT a general task log: only the TERMINAL failure lands
-- here (queue.DeadLetterErrorHandler fires solely when asynq reports the last
-- attempt), so a task that eventually succeeds on retry 3 writes nothing.
CREATE TABLE task_dead_letters (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    -- Tenant-scoped and NOT NULL: every capturable task payload in this
    -- codebase carries a workspace_id (see internal/platform/queue), and a row
    -- no tenant owns could never be listed or replayed through the
    -- workspace-pinned API. A task whose payload has no resolvable workspace is
    -- logged and dropped by the capture path rather than stored unowned.
    workspace_id UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    -- The asynq task type ("sequence:advance", "inbox:pending_reply_send", …).
    -- Stored as the raw string rather than an enum: the vocabulary lives in Go
    -- constants and grows every time a handler is added, and a CHECK here would
    -- turn "we shipped a new task type" into "capture silently fails at exactly
    -- the moment we most need the record".
    task_type    TEXT NOT NULL,
    -- The original task payload, byte-for-byte what replay re-enqueues. JSONB
    -- rather than BYTEA because every payload in this codebase is JSON and an
    -- operator triaging a dropped send needs to READ it (which enrollment? which
    -- mailbox?) — and because the workspace pin below is derived from it, so it
    -- must be queryable, not opaque.
    payload      JSONB NOT NULL,
    -- The final error asynq reported. Operator-facing diagnostics, so it is
    -- plain text and may be empty (a handler can fail with an empty message).
    last_error   TEXT NOT NULL DEFAULT '',
    -- How many attempts were made before giving up. Recorded from asynq's own
    -- retry counter at the terminal failure, not inferred.
    attempt_count INT NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    -- The triage lifecycle. 'pending' is untouched; the two terminal states are
    -- reached only through status-guarded UPDATEs (queries/deadletter.sql), which
    -- is what makes replay exactly-once — see ReplayTaskDeadLetter.
    status       TEXT NOT NULL DEFAULT 'pending'
                  CHECK (status IN ('pending', 'replayed', 'discarded')),
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- Set together with status='replayed'; NULL for every other state. Not a
    -- generic updated_at: 'discarded' is a filing action with no re-enqueue
    -- behind it, and conflating the two would make "when did we re-run this
    -- task" unanswerable.
    replayed_at  TIMESTAMPTZ
);

-- The one read this table has: an operator listing a workspace's dead letters,
-- optionally filtered by status, newest first. Column order matches that access
-- path exactly (equality on workspace_id, equality-or-any on status, range/sort
-- on created_at), so the filtered and unfiltered lists both use it.
--
-- Not partial on status='pending': unlike the inbox outbox indexes, terminal
-- rows here stay interesting — "what did we replay last week" is the audit
-- question this table exists to answer, and it is asked as often as the pending
-- list.
CREATE INDEX idx_task_dead_letters_workspace_status_created
    ON task_dead_letters (workspace_id, status, created_at DESC);
