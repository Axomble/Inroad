-- Remove operators' reply bodies from captured dead letters, and stop the
-- redacted rows from being replayable.
--
-- WHAT WENT WRONG. queue.InboxReplySendPayload.BodyText carried the operator's
-- free-text reply inside an inbox:reply_send task payload. On terminal failure
-- queue.DeadLetterErrorHandler stored that payload byte-for-byte here, and
-- GET /dead-letters serves the payload VERBATIM under campaigns:read — a scope
-- that IS OAuth-grantable, while inbox:read deliberately is NOT, precisely
-- because reply bodies are correspondence (internal/app/auth/scopes.go). So
-- correspondence reached a scope structurally denied it. The code side is fixed
-- (the body now lives in an inbox_pending_replies row and the task carries only
-- its id; capture of this task type is refused outright); this file deals with
-- the rows already on disk.
--
-- REDACT, DO NOT DELETE. The row is the operator's only record that a send was
-- permanently lost — which enrollment, which thread, which error, how many
-- attempts. Dropping it to remove one key would trade a disclosure for silent
-- data loss on the exact surface that exists to make dropped sends visible.
-- `payload - 'body_text'` removes the one key and leaves thread_id,
-- workspace_id and task_id, so the row still names what it always named. The
-- workspace_id in particular has to survive: Service.Replay validates the
-- payload against the row's own workspace before enqueuing anything.
--
-- THE jsonb_typeof GUARD IS NOT DECORATION. `payload - 'body_text'` is defined
-- only on a jsonb OBJECT; on a scalar Postgres raises "cannot delete from
-- scalar", which aborts this statement, aborts the migration, and leaves
-- schema_migrations DIRTY — blocking every later migration and every fresh
-- deploy over one malformed row. Capture normalises an absent payload to JSON
-- `null`, which is exactly such a scalar, so the shape is reachable rather than
-- theoretical. A non-object payload is emptied instead of key-stripped: it
-- cannot be shown to be free of correspondence, and it holds no ids to keep.
-- deadletter.redactLegacyReplyBody applies the identical rule at the API
-- boundary, so a row this statement has swept and one it has not look the same
-- to a client.
--
-- THE STATUS FLIP IS PART OF THE SAME STATEMENT, not a follow-up. A row left
-- 'pending' is REPLAYABLE, and replaying a body-stripped inbox:reply_send would
-- hand the queue a reply with an empty body and deliver a BLANK message to a
-- real contact. Doing it in one UPDATE means there is no window — not even a
-- crashed-migration window — in which a redacted row is also replayable.
-- 'replayed' and 'discarded' are left alone: both are terminal, neither can be
-- re-enqueued, and rewriting them would falsify the audit trail this table
-- keeps ("what did we re-run last week", migration 000069).
UPDATE task_dead_letters
SET payload = CASE WHEN jsonb_typeof(payload) = 'object'
                   THEN payload - 'body_text'
                   ELSE 'null'::jsonb END,
    status = CASE WHEN status = 'pending' THEN 'discarded' ELSE status END
WHERE task_type = 'inbox:reply_send';
