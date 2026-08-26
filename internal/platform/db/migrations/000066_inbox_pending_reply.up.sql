-- Deferred and undoable manual replies.
--
-- Today a manual reply is enqueued straight to asynq and there is no row for it:
-- the only durable trace is an idempotency claim keyed on the task id, which can
-- express "already delivered" but not "cancelled". This table is the missing
-- state — it is both the cancel handle and the delivery claim.
--
-- WHY A ROW AND NOT A QUEUE OPERATION: asynq's Inspector is not used anywhere in
-- this codebase, and reaching into the queue to delete a scheduled task would
-- make cancellation depend on the broker's internal state. Instead the delayed
-- task ALWAYS fires and the handler reads this row: `cancelled` means no-op.
-- Cancelling is one UPDATE, costs no queue interaction, and cannot race the
-- worker into a half-cancelled send (see the status guards on every transition
-- in queries/inbox.sql).
CREATE TABLE inbox_pending_replies (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    thread_id    UUID NOT NULL REFERENCES inbox_threads(id) ON DELETE CASCADE,
    body_text    TEXT NOT NULL,
    -- The lifecycle, following the house pattern (`sends`, `warmup_sends`):
    -- 'sending' is the transient claim state, and claimed_at is the lease a
    -- crashed worker's row is reclaimed by. 'cancelled' is what those two
    -- tables lack and this one exists for.
    status       TEXT NOT NULL DEFAULT 'scheduled'
                  CHECK (status IN ('scheduled','sending','sent','cancelled','failed')),
    -- When the send should go out. now() + the workspace's undo window for an
    -- ordinary reply, or an explicit future instant for a scheduled one. The
    -- worker's delayed task is enqueued for this moment; the column is the
    -- authority the handler re-reads, not the queue's own schedule.
    send_after   TIMESTAMPTZ NOT NULL,
    claimed_at   TIMESTAMPTZ,
    sent_at      TIMESTAMPTZ,
    message_id   TEXT NOT NULL DEFAULT '',
    last_error   TEXT NOT NULL DEFAULT '',
    -- Who wrote it, for the outbox display. ON DELETE SET NULL: a departing
    -- member must not cancel their own in-flight sends by leaving.
    created_by   UUID REFERENCES users(id) ON DELETE SET NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- The outbox: everything still waiting, soonest first. Partial, because the
-- overwhelming majority of rows are terminal ('sent') within seconds and the
-- outbox never wants them.
CREATE INDEX idx_inbox_pending_replies_workspace_pending
    ON inbox_pending_replies (workspace_id, send_after)
    WHERE status IN ('scheduled', 'sending');

-- "Does this thread have a reply in flight" — read by the reader to show its
-- pending state, so it is worth its own partial index rather than a scan of the
-- workspace's outbox.
CREATE INDEX idx_inbox_pending_replies_thread_pending
    ON inbox_pending_replies (thread_id)
    WHERE status IN ('scheduled', 'sending');

-- The workspace's undo window, and the cap on how far ahead a send may be
-- scheduled. A per-domain singleton keyed on workspace_id, following
-- workspace_ai_settings rather than inventing a generic settings table.
CREATE TABLE workspace_inbox_settings (
    workspace_id UUID PRIMARY KEY REFERENCES workspaces(id) ON DELETE CASCADE,
    -- Seconds between pressing Send and the mail actually going out, during
    -- which the operator can undo it. 0 disables the window entirely (send
    -- immediately), which is why the CHECK admits zero.
    --
    -- Default 10s rather than Gmail's 30: this is a reply typed in response to
    -- something already read, not a cold email, and a 30-second wait on every
    -- reply is felt as latency rather than safety.
    undo_send_seconds INT NOT NULL DEFAULT 10
                       CHECK (undo_send_seconds >= 0 AND undo_send_seconds <= 120),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);
