CREATE TABLE workspace_crm_settings (
    workspace_id        UUID PRIMARY KEY REFERENCES workspaces(id) ON DELETE CASCADE,
    auto_capture_policy TEXT NOT NULL DEFAULT 'sent'
        CHECK (auto_capture_policy IN ('sent_and_received','sent','off')),
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE crm_threads (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id    UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    thread_ref      TEXT NOT NULL,
    subject         TEXT NOT NULL DEFAULT '',
    reply_class     TEXT NOT NULL DEFAULT '',
    campaign_id     UUID,
    contact_id      UUID,
    last_message_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (id, workspace_id),
    UNIQUE (workspace_id, thread_ref),
    FOREIGN KEY (campaign_id, workspace_id)
        REFERENCES campaigns(id, workspace_id) ON DELETE SET NULL (campaign_id),
    FOREIGN KEY (contact_id, workspace_id)
        REFERENCES contacts(id, workspace_id) ON DELETE SET NULL (contact_id)
);

CREATE TABLE crm_thread_participants (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    thread_id    UUID NOT NULL,
    workspace_id UUID NOT NULL,
    email        TEXT NOT NULL CHECK (email = lower(email)),
    display_name TEXT NOT NULL DEFAULT '',
    contact_id   UUID,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (workspace_id, thread_id, email),
    FOREIGN KEY (thread_id, workspace_id)
        REFERENCES crm_threads(id, workspace_id) ON DELETE CASCADE,
    FOREIGN KEY (contact_id, workspace_id)
        REFERENCES contacts(id, workspace_id) ON DELETE SET NULL (contact_id)
);
CREATE INDEX idx_crm_thread_participants_email
    ON crm_thread_participants (workspace_id, email);

CREATE TABLE crm_messages (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    thread_id       UUID NOT NULL,
    workspace_id    UUID NOT NULL,
    direction       TEXT NOT NULL CHECK (direction IN ('inbound','outbound')),
    kind            TEXT NOT NULL CHECK (kind IN ('sent','reply')),
    message_id      TEXT NOT NULL DEFAULT '',
    sender_email    TEXT NOT NULL DEFAULT '',
    recipient_email TEXT NOT NULL DEFAULT '',
    subject         TEXT NOT NULL DEFAULT '',
    reply_class     TEXT NOT NULL DEFAULT '',
    occurred_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (id, workspace_id),
    FOREIGN KEY (thread_id, workspace_id)
        REFERENCES crm_threads(id, workspace_id) ON DELETE CASCADE
);
CREATE UNIQUE INDEX uq_crm_messages_workspace_message
    ON crm_messages (workspace_id, message_id) WHERE message_id <> '';

CREATE TABLE deal_threads (
    deal_id     UUID NOT NULL,
    thread_id   UUID NOT NULL,
    workspace_id UUID NOT NULL,
    source      TEXT NOT NULL CHECK (source IN ('auto','manual')),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (deal_id, thread_id),
    FOREIGN KEY (deal_id, workspace_id)
        REFERENCES deals(id, workspace_id) ON DELETE CASCADE,
    FOREIGN KEY (thread_id, workspace_id)
        REFERENCES crm_threads(id, workspace_id) ON DELETE CASCADE
);
CREATE INDEX idx_deal_threads_thread ON deal_threads (workspace_id, thread_id);

ALTER TABLE deals ADD COLUMN source_message_id TEXT NOT NULL DEFAULT '';
CREATE UNIQUE INDEX uq_deals_reply_capture
    ON deals (workspace_id, source_thread_ref, primary_contact_id)
    WHERE source = 'reply' AND source_thread_ref <> '' AND primary_contact_id IS NOT NULL;

CREATE TABLE events (
    id                        UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id              UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    name                      TEXT NOT NULL CHECK (name ~ '^[a-z_]+\.[a-z_]+$'),
    kind                      TEXT NOT NULL,
    object_type               TEXT NOT NULL DEFAULT '',
    object_id                 UUID,
    contact_id                UUID,
    company_id                UUID,
    deal_id                   UUID,
    actor                     JSONB NOT NULL CHECK (jsonb_typeof(actor) = 'object'),
    data                      JSONB NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(data) = 'object'),
    linked_record_cached_name TEXT NOT NULL DEFAULT '',
    source_message_id         TEXT NOT NULL DEFAULT '',
    source_thread_ref         TEXT NOT NULL DEFAULT '',
    occurred_at               TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at                TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (id, workspace_id),
    FOREIGN KEY (contact_id, workspace_id)
        REFERENCES contacts(id, workspace_id) ON DELETE SET NULL (contact_id),
    FOREIGN KEY (company_id, workspace_id)
        REFERENCES companies(id, workspace_id) ON DELETE SET NULL (company_id),
    FOREIGN KEY (deal_id, workspace_id)
        REFERENCES deals(id, workspace_id) ON DELETE SET NULL (deal_id)
);
CREATE INDEX idx_events_workspace_time ON events (workspace_id, occurred_at DESC, id DESC);
CREATE INDEX idx_events_deal_time ON events (workspace_id, deal_id, occurred_at DESC) WHERE deal_id IS NOT NULL;
CREATE INDEX idx_events_company_time ON events (workspace_id, company_id, occurred_at DESC) WHERE company_id IS NOT NULL;
CREATE INDEX idx_events_contact_time ON events (workspace_id, contact_id, occurred_at DESC) WHERE contact_id IS NOT NULL;
CREATE UNIQUE INDEX uq_events_source_once
    ON events (workspace_id, name, source_message_id, deal_id)
    WHERE source_message_id <> '' AND deal_id IS NOT NULL;

CREATE OR REPLACE FUNCTION crm_match_participant_email() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    UPDATE crm_thread_participants
    SET contact_id = NEW.contact_id
    WHERE workspace_id = NEW.workspace_id AND email = lower(NEW.email)
      AND contact_id IS DISTINCT FROM NEW.contact_id;
    RETURN NEW;
END;
$$;
CREATE TRIGGER trg_contact_emails_match_threads
AFTER INSERT OR UPDATE OF email, contact_id ON contact_emails
FOR EACH ROW EXECUTE FUNCTION crm_match_participant_email();

CREATE TRIGGER trg_crm_settings_updated BEFORE UPDATE ON workspace_crm_settings
FOR EACH ROW EXECUTE FUNCTION crm_touch_updated_at();
CREATE TRIGGER trg_crm_threads_updated BEFORE UPDATE ON crm_threads
FOR EACH ROW EXECUTE FUNCTION crm_touch_updated_at();
