CREATE TABLE contacts (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id  UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    email         TEXT NOT NULL,
    first_name    TEXT NOT NULL DEFAULT '',
    last_name     TEXT NOT NULL DEFAULT '',
    company       TEXT NOT NULL DEFAULT '',
    custom_fields JSONB NOT NULL DEFAULT '{}',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- Case-insensitive dedup per workspace (RFC-pragmatic: emails compared lower-case).
CREATE UNIQUE INDEX idx_contacts_ws_email ON contacts (workspace_id, lower(email));

CREATE TABLE lists (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id  UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    name          TEXT NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_lists_workspace ON lists (workspace_id);

CREATE TABLE list_members (
    list_id    UUID NOT NULL REFERENCES lists(id) ON DELETE CASCADE,
    contact_id UUID NOT NULL REFERENCES contacts(id) ON DELETE CASCADE,
    added_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (list_id, contact_id)
);
CREATE INDEX idx_list_members_contact ON list_members (contact_id);

CREATE TABLE campaigns (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id  UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    name          TEXT NOT NULL,
    mailbox_id    UUID NOT NULL REFERENCES mailboxes(id) ON DELETE RESTRICT,
    list_id       UUID NOT NULL REFERENCES lists(id) ON DELETE RESTRICT,
    subject       TEXT NOT NULL,
    body_text     TEXT NOT NULL DEFAULT '',
    body_html     TEXT NOT NULL DEFAULT '',
    status        TEXT NOT NULL DEFAULT 'draft'
                    CHECK (status IN ('draft','running','paused','done')),
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    launched_at   TIMESTAMPTZ
);
CREATE INDEX idx_campaigns_workspace ON campaigns (workspace_id);

CREATE TABLE sends (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id  UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    campaign_id   UUID NOT NULL REFERENCES campaigns(id) ON DELETE CASCADE,
    contact_id    UUID NOT NULL REFERENCES contacts(id) ON DELETE CASCADE,
    mailbox_id    UUID NOT NULL REFERENCES mailboxes(id) ON DELETE CASCADE,
    to_email      TEXT NOT NULL,
    status        TEXT NOT NULL DEFAULT 'queued'
                    CHECK (status IN ('queued','sent','failed','skipped')),
    error         TEXT NOT NULL DEFAULT '',
    message_id    TEXT NOT NULL DEFAULT '',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    sent_at       TIMESTAMPTZ,
    -- Idempotency: one send per contact per campaign. A re-launch can't double-send.
    UNIQUE (campaign_id, contact_id)
);
-- Hot path 1: daily cap counting (sends today for a mailbox). Partial index keeps it tiny.
CREATE INDEX idx_sends_mailbox_sent ON sends (mailbox_id, sent_at) WHERE status = 'sent';
-- Hot path 2: campaign stats (counts by status).
CREATE INDEX idx_sends_campaign_status ON sends (campaign_id, status);

CREATE TABLE suppression (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id  UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    email         TEXT NOT NULL,
    reason        TEXT NOT NULL CHECK (reason IN ('unsubscribe','bounce','manual')),
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX idx_suppression_ws_email ON suppression (workspace_id, lower(email));
