CREATE TABLE inbox_threads (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id     UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    mailbox_id       UUID NOT NULL REFERENCES mailboxes(id) ON DELETE CASCADE,
    campaign_id      UUID REFERENCES campaigns(id) ON DELETE SET NULL,
    contact_id       UUID REFERENCES contacts(id) ON DELETE SET NULL,
    root_message_id  TEXT NOT NULL DEFAULT '',
    subject          TEXT NOT NULL DEFAULT '',
    last_reply_class TEXT NOT NULL DEFAULT '',
    unread           BOOLEAN NOT NULL DEFAULT true,
    last_message_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_inbox_threads_workspace_mailbox_last
    ON inbox_threads (workspace_id, mailbox_id, last_message_at DESC);
CREATE INDEX idx_inbox_threads_workspace_last
    ON inbox_threads (workspace_id, last_message_at DESC);
CREATE INDEX idx_inbox_threads_workspace_unread
    ON inbox_threads (workspace_id) WHERE unread;
CREATE INDEX idx_inbox_threads_workspace_unread_positive
    ON inbox_threads (workspace_id) WHERE unread AND last_reply_class = 'positive';
-- root_message_id is '' for a legacy direct-send match (no enrollment to anchor
-- on) — those must not collide with each other, so the uniqueness constraint
-- is partial. A legacy match therefore never groups into one thread across
-- multiple replies; each becomes its own thread. Accepted: legacy sends are a
-- closed historical population, not a going-forward concern.
CREATE UNIQUE INDEX uq_inbox_threads_workspace_mailbox_root
    ON inbox_threads (workspace_id, mailbox_id, root_message_id) WHERE root_message_id <> '';

CREATE TABLE inbox_messages (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    thread_id     UUID NOT NULL REFERENCES inbox_threads(id) ON DELETE CASCADE,
    workspace_id  UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    direction     TEXT NOT NULL CHECK (direction IN ('inbound','outbound')),
    message_id    TEXT NOT NULL DEFAULT '',
    from_email    TEXT NOT NULL DEFAULT '',
    from_name     TEXT NOT NULL DEFAULT '',
    to_email      TEXT NOT NULL DEFAULT '',
    subject       TEXT NOT NULL DEFAULT '',
    body_text     TEXT NOT NULL DEFAULT '',
    body_html     TEXT NOT NULL DEFAULT '',
    reply_class   TEXT NOT NULL DEFAULT '',
    occurred_at   TIMESTAMPTZ NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_inbox_messages_thread ON inbox_messages (thread_id, occurred_at);
-- Idempotent re-poll of the same message never duplicates it. Partial: an
-- empty message_id (no Message-ID header on the source) must not collide.
CREATE UNIQUE INDEX uq_inbox_messages_workspace_message
    ON inbox_messages (workspace_id, message_id) WHERE message_id <> '';
