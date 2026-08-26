-- Composing a NEW email from the inbox, and autosaving it while it is written.
--
-- Until now the inbox could only reply within an existing thread: a manual
-- reply is anchored to an inbound message, and its recipient, subject and
-- threading headers are all derived from that message. A fresh email has none
-- of those, so it needs its own columns rather than reusing the reply path.

-- Drafts are per-USER, not per-workspace. A colleague must never resume my
-- half-written mail — it may say something I have not decided to say yet, and
-- attributing it to them on send would be worse still.
CREATE TABLE inbox_compose_drafts (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    -- NOT NULL and ON DELETE CASCADE, unlike snoozed_by/created_by elsewhere: a
    -- draft with no author is nobody's to resume, so a departing member's
    -- drafts go with them rather than becoming orphans no one can claim.
    user_id      UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    -- The mailbox it will be sent from. Nullable because a draft is saved from
    -- the first keystroke, long before the operator has necessarily chosen one.
    mailbox_id   UUID REFERENCES mailboxes(id) ON DELETE SET NULL,
    -- Recipients as arrays rather than a comma-joined TEXT: the composer works
    -- in discrete chips, and splitting a string back into addresses is exactly
    -- the kind of parsing that mangles a display name containing a comma.
    to_emails    TEXT[] NOT NULL DEFAULT '{}',
    cc_emails    TEXT[] NOT NULL DEFAULT '{}',
    bcc_emails   TEXT[] NOT NULL DEFAULT '{}',
    subject      TEXT NOT NULL DEFAULT '',
    body_text    TEXT NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- The drafts list: this user's own, most recently edited first.
CREATE INDEX idx_inbox_compose_drafts_user
    ON inbox_compose_drafts (workspace_id, user_id, updated_at DESC);

-- A composed (non-reply) email waiting to go out.
--
-- Deliberately NOT folded into inbox_pending_replies. That table's every row is
-- anchored to a thread_id NOT NULL and derives its recipient and threading
-- headers from the thread's latest inbound message; a fresh email has no thread
-- and carries its own recipients. Sharing one table would mean a nullable
-- thread_id plus recipient columns that are meaningless for half the rows — and
-- a CHECK constraint to express which half. Two tables with two honest shapes
-- beat one table with a disjunction.
CREATE TABLE inbox_pending_composes (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    mailbox_id   UUID NOT NULL REFERENCES mailboxes(id) ON DELETE CASCADE,
    to_emails    TEXT[] NOT NULL,
    cc_emails    TEXT[] NOT NULL DEFAULT '{}',
    bcc_emails   TEXT[] NOT NULL DEFAULT '{}',
    subject      TEXT NOT NULL DEFAULT '',
    body_text    TEXT NOT NULL,
    -- The same lifecycle as inbox_pending_replies, for the same reasons: see
    -- migration 000066's comments on why cancellation is a status flip the
    -- handler re-reads rather than a queue operation.
    status       TEXT NOT NULL DEFAULT 'scheduled'
                  CHECK (status IN ('scheduled','sending','sent','cancelled','failed')),
    send_after   TIMESTAMPTZ NOT NULL,
    claimed_at   TIMESTAMPTZ,
    sent_at      TIMESTAMPTZ,
    message_id   TEXT NOT NULL DEFAULT '',
    last_error   TEXT NOT NULL DEFAULT '',
    created_by   UUID REFERENCES users(id) ON DELETE SET NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_inbox_pending_composes_workspace_pending
    ON inbox_pending_composes (workspace_id, send_after)
    WHERE status IN ('scheduled', 'sending');
