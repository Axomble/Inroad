-- Agentic CRM foundation (Phase B / B1). Every relationship that crosses a
-- workspace-owned table carries workspace_id in its foreign key so tenant
-- ownership is enforced by PostgreSQL, not application convention.

CREATE TABLE companies (
    id                    UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id          UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    name                  TEXT NOT NULL CHECK (char_length(btrim(name)) BETWEEN 1 AND 200),
    domain                CITEXT,
    owner_user_id         UUID,
    annual_revenue_micros BIGINT CHECK (annual_revenue_micros IS NULL OR annual_revenue_micros >= 0),
    currency              CHAR(3) NOT NULL DEFAULT 'USD' CHECK (currency = upper(currency)),
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (id, workspace_id),
    FOREIGN KEY (workspace_id, owner_user_id)
        REFERENCES workspace_members(workspace_id, user_id) ON DELETE SET NULL (owner_user_id)
);
CREATE UNIQUE INDEX uq_companies_workspace_domain
    ON companies (workspace_id, lower(domain)) WHERE domain IS NOT NULL AND btrim(domain::text) <> '';
CREATE INDEX idx_companies_workspace_name ON companies (workspace_id, lower(name), id);

ALTER TABLE contacts
    ADD COLUMN company_id UUID,
    ADD COLUMN job_title TEXT NOT NULL DEFAULT '',
    ADD COLUMN linkedin_url TEXT NOT NULL DEFAULT '',
    ADD COLUMN updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    ADD CONSTRAINT contacts_company_tenant_fkey
        FOREIGN KEY (company_id, workspace_id) REFERENCES companies(id, workspace_id) ON DELETE RESTRICT;

CREATE TABLE contact_emails (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    contact_id   UUID NOT NULL,
    workspace_id UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    email        CITEXT NOT NULL CHECK (position('@' in email::text) > 1),
    is_primary   BOOLEAN NOT NULL DEFAULT false,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (id, workspace_id),
    UNIQUE (workspace_id, email),
    FOREIGN KEY (contact_id, workspace_id)
        REFERENCES contacts(id, workspace_id) ON DELETE CASCADE
);
CREATE UNIQUE INDEX uq_contact_emails_primary
    ON contact_emails (workspace_id, contact_id) WHERE is_primary;
CREATE INDEX idx_contact_emails_contact ON contact_emails (workspace_id, contact_id, is_primary DESC, created_at);

INSERT INTO contact_emails (contact_id, workspace_id, email, is_primary)
SELECT id, workspace_id, email, true FROM contacts;

-- Keeps contacts.email and its contact_emails alias row in lockstep. The
-- function is self-contained on purpose: any writer of contacts.email (bulk
-- import, capture worker, a future admin path) gets a correct alias set
-- without also having to clear primaries first, and an address already owned
-- by ANOTHER contact fails loud as 23505 rather than silently leaving the
-- contact with zero alias rows.
CREATE FUNCTION sync_contact_primary_email() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE claimed_by UUID;
BEGIN
    SELECT contact_id INTO claimed_by FROM contact_emails
     WHERE workspace_id = NEW.workspace_id AND email = NEW.email;
    IF claimed_by IS NOT NULL AND claimed_by <> NEW.id THEN
        RAISE EXCEPTION 'contact_emails: address already belongs to another contact in this workspace'
            USING ERRCODE = 'unique_violation';
    END IF;
    UPDATE contact_emails SET is_primary = false
     WHERE workspace_id = NEW.workspace_id AND contact_id = NEW.id AND is_primary;
    INSERT INTO contact_emails (contact_id, workspace_id, email, is_primary)
    VALUES (NEW.id, NEW.workspace_id, NEW.email, true)
    ON CONFLICT (workspace_id, email) DO UPDATE SET is_primary = true;
    RETURN NEW;
END;
$$;
CREATE TRIGGER contacts_sync_primary_email
AFTER INSERT OR UPDATE OF email ON contacts
FOR EACH ROW EXECUTE FUNCTION sync_contact_primary_email();

CREATE TABLE pipelines (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    name         TEXT NOT NULL CHECK (char_length(btrim(name)) BETWEEN 1 AND 120),
    is_default   BOOLEAN NOT NULL DEFAULT false,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (id, workspace_id)
);
CREATE UNIQUE INDEX uq_pipelines_default ON pipelines (workspace_id) WHERE is_default;
CREATE UNIQUE INDEX uq_pipelines_workspace_name ON pipelines (workspace_id, lower(name));

CREATE TABLE pipeline_stages (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    pipeline_id  UUID NOT NULL,
    workspace_id UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    key          TEXT NOT NULL CHECK (key ~ '^[a-z][a-z0-9_]{0,63}$'),
    label        TEXT NOT NULL CHECK (char_length(btrim(label)) BETWEEN 1 AND 80),
    color        TEXT NOT NULL CHECK (color ~ '^#[0-9A-Fa-f]{6}$'),
    position     INTEGER NOT NULL CHECK (position >= 0),
    is_won       BOOLEAN NOT NULL DEFAULT false,
    is_lost      BOOLEAN NOT NULL DEFAULT false,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (NOT (is_won AND is_lost)),
    UNIQUE (id, workspace_id),
    UNIQUE (id, pipeline_id, workspace_id),
    UNIQUE (pipeline_id, key),
    -- Deliberately NOT unique on (pipeline_id, position): a reorder rewrites
    -- several rows and a non-deferrable unique constraint turns every such
    -- write into a hard 409 on the first intermediate collision. Order is
    -- therefore (position, id) everywhere — total, stable, and reorderable one
    -- row at a time. A deferrable constraint was the alternative, but it would
    -- still reject a single-stage position edit (the shape the HTTP contract
    -- already exposes) and would disable ON CONFLICT arbitration on the seed path.
    FOREIGN KEY (pipeline_id, workspace_id)
        REFERENCES pipelines(id, workspace_id) ON DELETE CASCADE
);
CREATE INDEX idx_pipeline_stages_order ON pipeline_stages (workspace_id, pipeline_id, position, id);

CREATE TABLE deals (
    id                    UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id          UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    pipeline_id           UUID NOT NULL,
    stage_id              UUID NOT NULL,
    company_id            UUID,
    primary_contact_id    UUID,
    owner_user_id         UUID,
    name                  TEXT NOT NULL CHECK (char_length(btrim(name)) BETWEEN 1 AND 200),
    amount_micros         BIGINT CHECK (amount_micros IS NULL OR amount_micros >= 0),
    currency              CHAR(3) NOT NULL DEFAULT 'USD' CHECK (currency = upper(currency)),
    close_date            DATE,
    position              NUMERIC(30, 12) NOT NULL DEFAULT 1000,
    source                TEXT NOT NULL DEFAULT 'manual' CHECK (source IN ('manual','reply','import','agent')),
    source_campaign_id    UUID,
    source_thread_ref     TEXT NOT NULL DEFAULT '',
    created_by_actor      JSONB NOT NULL DEFAULT '{"type":"system","subsystem":"migration"}'::jsonb
                              CHECK (jsonb_typeof(created_by_actor) = 'object'),
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (id, workspace_id),
    FOREIGN KEY (pipeline_id, workspace_id)
        REFERENCES pipelines(id, workspace_id) ON DELETE RESTRICT,
    FOREIGN KEY (stage_id, pipeline_id, workspace_id)
        REFERENCES pipeline_stages(id, pipeline_id, workspace_id) ON DELETE RESTRICT,
    FOREIGN KEY (company_id, workspace_id)
        REFERENCES companies(id, workspace_id) ON DELETE RESTRICT,
    FOREIGN KEY (primary_contact_id, workspace_id)
        REFERENCES contacts(id, workspace_id) ON DELETE RESTRICT,
    FOREIGN KEY (workspace_id, owner_user_id)
        REFERENCES workspace_members(workspace_id, user_id) ON DELETE SET NULL (owner_user_id),
    FOREIGN KEY (source_campaign_id, workspace_id)
        REFERENCES campaigns(id, workspace_id) ON DELETE RESTRICT
);
CREATE INDEX idx_deals_board ON deals (workspace_id, pipeline_id, stage_id, position, id);
CREATE INDEX idx_deals_company ON deals (workspace_id, company_id) WHERE company_id IS NOT NULL;
CREATE INDEX idx_deals_contact ON deals (workspace_id, primary_contact_id) WHERE primary_contact_id IS NOT NULL;

CREATE TABLE notes (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id     UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    title            TEXT NOT NULL DEFAULT '' CHECK (char_length(title) <= 200),
    body             TEXT NOT NULL CHECK (char_length(btrim(body)) BETWEEN 1 AND 20000),
    created_by_actor JSONB NOT NULL CHECK (jsonb_typeof(created_by_actor) = 'object'),
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (id, workspace_id)
);

CREATE TABLE tasks (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id     UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    title            TEXT NOT NULL CHECK (char_length(btrim(title)) BETWEEN 1 AND 200),
    body             TEXT NOT NULL DEFAULT '' CHECK (char_length(body) <= 20000),
    due_at           TIMESTAMPTZ,
    status           TEXT NOT NULL DEFAULT 'open' CHECK (status IN ('open','in_progress','done','cancelled')),
    assignee_user_id UUID,
    created_by_actor JSONB NOT NULL CHECK (jsonb_typeof(created_by_actor) = 'object'),
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (id, workspace_id),
    FOREIGN KEY (workspace_id, assignee_user_id)
        REFERENCES workspace_members(workspace_id, user_id) ON DELETE SET NULL (assignee_user_id)
);
CREATE INDEX idx_tasks_due ON tasks (workspace_id, status, due_at) WHERE status IN ('open','in_progress');

CREATE TABLE note_targets (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    note_id      UUID NOT NULL,
    workspace_id UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    contact_id   UUID,
    company_id   UUID,
    deal_id      UUID,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (num_nonnulls(contact_id, company_id, deal_id) = 1),
    FOREIGN KEY (note_id, workspace_id) REFERENCES notes(id, workspace_id) ON DELETE CASCADE,
    FOREIGN KEY (contact_id, workspace_id) REFERENCES contacts(id, workspace_id) ON DELETE CASCADE,
    FOREIGN KEY (company_id, workspace_id) REFERENCES companies(id, workspace_id) ON DELETE CASCADE,
    FOREIGN KEY (deal_id, workspace_id) REFERENCES deals(id, workspace_id) ON DELETE CASCADE
);
CREATE UNIQUE INDEX uq_note_target_contact ON note_targets (note_id, contact_id) WHERE contact_id IS NOT NULL;
CREATE UNIQUE INDEX uq_note_target_company ON note_targets (note_id, company_id) WHERE company_id IS NOT NULL;
CREATE UNIQUE INDEX uq_note_target_deal ON note_targets (note_id, deal_id) WHERE deal_id IS NOT NULL;
-- The uq_* indexes above lead with note_id and so cannot serve the read path,
-- which always seeks by target. These are the indexes the list queries use.
CREATE INDEX idx_note_targets_contact ON note_targets (workspace_id, contact_id) WHERE contact_id IS NOT NULL;
CREATE INDEX idx_note_targets_company ON note_targets (workspace_id, company_id) WHERE company_id IS NOT NULL;
CREATE INDEX idx_note_targets_deal ON note_targets (workspace_id, deal_id) WHERE deal_id IS NOT NULL;

CREATE TABLE task_targets (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    task_id      UUID NOT NULL,
    workspace_id UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    contact_id   UUID,
    company_id   UUID,
    deal_id      UUID,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (num_nonnulls(contact_id, company_id, deal_id) = 1),
    FOREIGN KEY (task_id, workspace_id) REFERENCES tasks(id, workspace_id) ON DELETE CASCADE,
    FOREIGN KEY (contact_id, workspace_id) REFERENCES contacts(id, workspace_id) ON DELETE CASCADE,
    FOREIGN KEY (company_id, workspace_id) REFERENCES companies(id, workspace_id) ON DELETE CASCADE,
    FOREIGN KEY (deal_id, workspace_id) REFERENCES deals(id, workspace_id) ON DELETE CASCADE
);
CREATE UNIQUE INDEX uq_task_target_contact ON task_targets (task_id, contact_id) WHERE contact_id IS NOT NULL;
CREATE UNIQUE INDEX uq_task_target_company ON task_targets (task_id, company_id) WHERE company_id IS NOT NULL;
CREATE UNIQUE INDEX uq_task_target_deal ON task_targets (task_id, deal_id) WHERE deal_id IS NOT NULL;
CREATE INDEX idx_task_targets_contact ON task_targets (workspace_id, contact_id) WHERE contact_id IS NOT NULL;
CREATE INDEX idx_task_targets_company ON task_targets (workspace_id, company_id) WHERE company_id IS NOT NULL;
CREATE INDEX idx_task_targets_deal ON task_targets (workspace_id, deal_id) WHERE deal_id IS NOT NULL;

CREATE FUNCTION crm_touch_updated_at() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    NEW.updated_at := now();
    RETURN NEW;
END;
$$;
CREATE TRIGGER companies_touch_updated_at BEFORE UPDATE ON companies FOR EACH ROW EXECUTE FUNCTION crm_touch_updated_at();
CREATE TRIGGER contacts_touch_updated_at BEFORE UPDATE ON contacts FOR EACH ROW EXECUTE FUNCTION crm_touch_updated_at();
CREATE TRIGGER pipelines_touch_updated_at BEFORE UPDATE ON pipelines FOR EACH ROW EXECUTE FUNCTION crm_touch_updated_at();
CREATE TRIGGER pipeline_stages_touch_updated_at BEFORE UPDATE ON pipeline_stages FOR EACH ROW EXECUTE FUNCTION crm_touch_updated_at();
CREATE TRIGGER deals_touch_updated_at BEFORE UPDATE ON deals FOR EACH ROW EXECUTE FUNCTION crm_touch_updated_at();
CREATE TRIGGER notes_touch_updated_at BEFORE UPDATE ON notes FOR EACH ROW EXECUTE FUNCTION crm_touch_updated_at();
CREATE TRIGGER tasks_touch_updated_at BEFORE UPDATE ON tasks FOR EACH ROW EXECUTE FUNCTION crm_touch_updated_at();

-- The default stage set has exactly one definition. Both the workspace seed
-- below and the application's "create a pipeline" path call this function, so
-- the two cannot drift.
CREATE FUNCTION seed_pipeline_stages(target_pipeline UUID, target_workspace UUID) RETURNS void
LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO pipeline_stages (pipeline_id, workspace_id, key, label, color, position, is_won, is_lost)
    VALUES
      (target_pipeline, target_workspace, 'lead', 'Lead', '#64748B', 0, false, false),
      (target_pipeline, target_workspace, 'qualified', 'Qualified', '#3B82F6', 1, false, false),
      (target_pipeline, target_workspace, 'proposal', 'Proposal', '#8B5CF6', 2, false, false),
      (target_pipeline, target_workspace, 'won', 'Won', '#22C55E', 3, true, false),
      (target_pipeline, target_workspace, 'lost', 'Lost', '#EF4444', 4, false, true)
    ON CONFLICT DO NOTHING;
END;
$$;

CREATE FUNCTION seed_workspace_default_pipeline(target_workspace UUID) RETURNS UUID
LANGUAGE plpgsql AS $$
DECLARE pipeline UUID;
BEGIN
    INSERT INTO pipelines (workspace_id, name, is_default)
    VALUES (target_workspace, 'Sales pipeline', true)
    ON CONFLICT DO NOTHING
    RETURNING id INTO pipeline;
    IF pipeline IS NULL THEN
        SELECT id INTO pipeline FROM pipelines WHERE workspace_id = target_workspace AND is_default;
    END IF;
    PERFORM seed_pipeline_stages(pipeline, target_workspace);
    RETURN pipeline;
END;
$$;

SELECT seed_workspace_default_pipeline(id) FROM workspaces;

CREATE FUNCTION seed_new_workspace_pipeline() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    PERFORM seed_workspace_default_pipeline(NEW.id);
    RETURN NEW;
END;
$$;
CREATE TRIGGER workspaces_seed_default_pipeline
AFTER INSERT ON workspaces FOR EACH ROW EXECUTE FUNCTION seed_new_workspace_pipeline();
