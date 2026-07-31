-- Production schema hardening: database-enforced tenant ownership, current
-- referential integrity, domain checks, and indexes matched to live queries.

-- ---------------------------------------------------------------------------
-- Campaign content: sequence_steps is authoritative. campaigns keeps a small
-- compatibility projection for existing API responses and the legacy direct
-- sender, but it is maintained by PostgreSQL rather than application convention.
-- Repair existing drift before installing the trigger.
-- ---------------------------------------------------------------------------
UPDATE campaigns c
SET subject = s.subject, body_text = s.body_text, body_html = s.body_html
FROM sequence_steps s
WHERE s.campaign_id = c.id AND s.step_order = 1
  AND (c.subject, c.body_text, c.body_html)
      IS DISTINCT FROM (s.subject, s.body_text, s.body_html);

CREATE FUNCTION sync_campaign_content_from_steps() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    target_campaign UUID;
BEGIN
    IF TG_OP = 'DELETE' THEN
        target_campaign := OLD.campaign_id;
    ELSE
        target_campaign := NEW.campaign_id;
    END IF;
    UPDATE campaigns c
    SET subject = COALESCE(s.subject, ''),
        body_text = COALESCE(s.body_text, ''),
        body_html = COALESCE(s.body_html, '')
    FROM (SELECT 1) anchor
    LEFT JOIN LATERAL (
        SELECT st.subject, st.body_text, st.body_html
        FROM sequence_steps st
        WHERE st.campaign_id = target_campaign
        ORDER BY st.step_order
        LIMIT 1
    ) s ON true
    WHERE c.id = target_campaign;
    IF TG_OP = 'DELETE' THEN
        RETURN OLD;
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER sequence_steps_sync_campaign_content
AFTER INSERT OR UPDATE OR DELETE ON sequence_steps
FOR EACH ROW EXECUTE FUNCTION sync_campaign_content_from_steps();

-- ---------------------------------------------------------------------------
-- sends.id is the identity used by every worker and tracking token. Restore it
-- as the actual PK; the old composite key was preparation for a partitioning
-- project that never landed and prevented a tracking FK.
-- ---------------------------------------------------------------------------
ALTER TABLE sends DROP CONSTRAINT sends_pkey;
ALTER TABLE sends ADD CONSTRAINT sends_pkey PRIMARY KEY (id);
ALTER TABLE sends DROP CONSTRAINT sends_campaign_id_contact_id_created_at_key;

-- Parent keys used by composite tenant FKs. id remains the primary identity;
-- these redundant-looking unique keys let PostgreSQL prove parent ownership.
ALTER TABLE mailboxes ADD CONSTRAINT mailboxes_id_workspace_key UNIQUE (id, workspace_id);
ALTER TABLE lists ADD CONSTRAINT lists_id_workspace_key UNIQUE (id, workspace_id);
ALTER TABLE contacts ADD CONSTRAINT contacts_id_workspace_key UNIQUE (id, workspace_id);
ALTER TABLE campaigns ADD CONSTRAINT campaigns_id_workspace_key UNIQUE (id, workspace_id);
ALTER TABLE sends ADD CONSTRAINT sends_id_workspace_key UNIQUE (id, workspace_id);
ALTER TABLE warmup_threads ADD CONSTRAINT warmup_threads_id_workspace_key UNIQUE (id, workspace_id);
ALTER TABLE warmup_sends ADD CONSTRAINT warmup_sends_id_workspace_key UNIQUE (id, workspace_id);

-- OAuth clients created by the API are workspace-owned. Pre-1.0 rows with no
-- owner cannot safely authorize tenant data, so fail the migration if any exist.
ALTER TABLE oauth_clients ALTER COLUMN workspace_id SET NOT NULL;
ALTER TABLE oauth_clients ADD CONSTRAINT oauth_clients_client_workspace_key
    UNIQUE (client_id, workspace_id);

-- ---------------------------------------------------------------------------
-- Cross-workspace relationships. Keep direct workspace FKs for workspace-wide
-- cascade, and replace parent-only FKs with composite ownership FKs.
-- ---------------------------------------------------------------------------
ALTER TABLE campaigns DROP CONSTRAINT campaigns_mailbox_id_fkey;
ALTER TABLE campaigns DROP CONSTRAINT campaigns_list_id_fkey;
ALTER TABLE campaigns ADD CONSTRAINT campaigns_mailbox_tenant_fkey
    FOREIGN KEY (mailbox_id, workspace_id) REFERENCES mailboxes(id, workspace_id) ON DELETE RESTRICT;
ALTER TABLE campaigns ADD CONSTRAINT campaigns_list_tenant_fkey
    FOREIGN KEY (list_id, workspace_id) REFERENCES lists(id, workspace_id) ON DELETE RESTRICT;

-- list_members predates explicit tenant columns. A constraint trigger enforces
-- that both already-FK-locked parents belong to the same workspace without
-- duplicating workspace_id into the pure many-to-many key.
CREATE FUNCTION enforce_list_member_tenant() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM lists l JOIN contacts c ON c.id = NEW.contact_id
        WHERE l.id = NEW.list_id AND l.workspace_id = c.workspace_id
    ) THEN
        RAISE EXCEPTION 'list and contact must belong to the same workspace'
            USING ERRCODE = '23503';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER list_members_tenant_guard
BEFORE INSERT OR UPDATE ON list_members
FOR EACH ROW EXECUTE FUNCTION enforce_list_member_tenant();

ALTER TABLE sends DROP CONSTRAINT sends_campaign_id_fkey;
ALTER TABLE sends DROP CONSTRAINT sends_contact_id_fkey;
ALTER TABLE sends DROP CONSTRAINT sends_mailbox_id_fkey;
ALTER TABLE sends ADD CONSTRAINT sends_campaign_tenant_fkey
    FOREIGN KEY (campaign_id, workspace_id) REFERENCES campaigns(id, workspace_id) ON DELETE CASCADE;
ALTER TABLE sends ADD CONSTRAINT sends_contact_tenant_fkey
    FOREIGN KEY (contact_id, workspace_id) REFERENCES contacts(id, workspace_id) ON DELETE CASCADE;
ALTER TABLE sends ADD CONSTRAINT sends_mailbox_tenant_fkey
    FOREIGN KEY (mailbox_id, workspace_id) REFERENCES mailboxes(id, workspace_id) ON DELETE CASCADE;

ALTER TABLE sequence_steps DROP CONSTRAINT sequence_steps_campaign_id_fkey;
ALTER TABLE sequence_steps ADD CONSTRAINT sequence_steps_campaign_tenant_fkey
    FOREIGN KEY (campaign_id, workspace_id) REFERENCES campaigns(id, workspace_id) ON DELETE CASCADE;

ALTER TABLE sequence_enrollments DROP CONSTRAINT sequence_enrollments_campaign_id_fkey;
ALTER TABLE sequence_enrollments DROP CONSTRAINT sequence_enrollments_contact_id_fkey;
ALTER TABLE sequence_enrollments ADD CONSTRAINT sequence_enrollments_campaign_tenant_fkey
    FOREIGN KEY (campaign_id, workspace_id) REFERENCES campaigns(id, workspace_id) ON DELETE CASCADE;
ALTER TABLE sequence_enrollments ADD CONSTRAINT sequence_enrollments_contact_tenant_fkey
    FOREIGN KEY (contact_id, workspace_id) REFERENCES contacts(id, workspace_id) ON DELETE CASCADE;

ALTER TABLE tracking_events DROP CONSTRAINT tracking_events_campaign_id_fkey;
ALTER TABLE tracking_events ADD CONSTRAINT tracking_events_campaign_tenant_fkey
    FOREIGN KEY (campaign_id, workspace_id) REFERENCES campaigns(id, workspace_id) ON DELETE CASCADE;
ALTER TABLE tracking_events ADD CONSTRAINT tracking_events_send_tenant_fkey
    FOREIGN KEY (send_id, workspace_id) REFERENCES sends(id, workspace_id) ON DELETE CASCADE;

ALTER TABLE mailbox_worker_assignments DROP CONSTRAINT mailbox_worker_assignments_mailbox_id_fkey;
ALTER TABLE mailbox_worker_assignments ADD CONSTRAINT mailbox_worker_assignments_mailbox_tenant_fkey
    FOREIGN KEY (mailbox_id, workspace_id) REFERENCES mailboxes(id, workspace_id) ON DELETE CASCADE;

ALTER TABLE warmup_participants DROP CONSTRAINT warmup_participants_mailbox_id_fkey;
ALTER TABLE warmup_participants ADD CONSTRAINT warmup_participants_mailbox_tenant_fkey
    FOREIGN KEY (mailbox_id, workspace_id) REFERENCES mailboxes(id, workspace_id) ON DELETE CASCADE;

ALTER TABLE warmup_threads DROP CONSTRAINT warmup_threads_sender_mailbox_fkey;
ALTER TABLE warmup_threads DROP CONSTRAINT warmup_threads_partner_mailbox_fkey;
ALTER TABLE warmup_threads ADD CONSTRAINT warmup_threads_sender_tenant_fkey
    FOREIGN KEY (sender_mailbox, workspace_id) REFERENCES mailboxes(id, workspace_id) ON DELETE CASCADE;
ALTER TABLE warmup_threads ADD CONSTRAINT warmup_threads_partner_tenant_fkey
    FOREIGN KEY (partner_mailbox, workspace_id) REFERENCES mailboxes(id, workspace_id) ON DELETE CASCADE;

ALTER TABLE warmup_sends DROP CONSTRAINT warmup_sends_thread_id_fkey;
ALTER TABLE warmup_sends DROP CONSTRAINT warmup_sends_from_mailbox_fkey;
ALTER TABLE warmup_sends DROP CONSTRAINT warmup_sends_to_mailbox_fkey;
ALTER TABLE warmup_sends ADD CONSTRAINT warmup_sends_thread_tenant_fkey
    FOREIGN KEY (thread_id, workspace_id) REFERENCES warmup_threads(id, workspace_id) ON DELETE CASCADE;
ALTER TABLE warmup_sends ADD CONSTRAINT warmup_sends_from_tenant_fkey
    FOREIGN KEY (from_mailbox, workspace_id) REFERENCES mailboxes(id, workspace_id) ON DELETE CASCADE;
ALTER TABLE warmup_sends ADD CONSTRAINT warmup_sends_to_tenant_fkey
    FOREIGN KEY (to_mailbox, workspace_id) REFERENCES mailboxes(id, workspace_id) ON DELETE CASCADE;

ALTER TABLE warmup_receipts DROP CONSTRAINT warmup_receipts_recipient_mailbox_fkey;
ALTER TABLE warmup_receipts DROP CONSTRAINT warmup_receipts_warmup_send_id_fkey;
ALTER TABLE warmup_receipts ADD CONSTRAINT warmup_receipts_recipient_tenant_fkey
    FOREIGN KEY (recipient_mailbox, workspace_id) REFERENCES mailboxes(id, workspace_id) ON DELETE CASCADE;
ALTER TABLE warmup_receipts ADD CONSTRAINT warmup_receipts_send_tenant_fkey
    FOREIGN KEY (warmup_send_id, workspace_id) REFERENCES warmup_sends(id, workspace_id) ON DELETE SET NULL (warmup_send_id);

ALTER TABLE warmup_daily_stats DROP CONSTRAINT warmup_daily_stats_mailbox_id_fkey;
ALTER TABLE warmup_daily_stats ADD CONSTRAINT warmup_daily_stats_mailbox_tenant_fkey
    FOREIGN KEY (mailbox_id, workspace_id) REFERENCES mailboxes(id, workspace_id) ON DELETE CASCADE;

-- A session or OAuth grant can only name a workspace the user belongs to.
ALTER TABLE sessions ADD CONSTRAINT sessions_membership_fkey
    FOREIGN KEY (workspace_id, user_id) REFERENCES workspace_members(workspace_id, user_id) ON DELETE CASCADE;

-- Revoke stale pre-constraint artifacts that name a workspace the resource
-- owner does not belong to. OAuth clients are workspace-owned for administration
-- but may be authorized against any workspace the resource owner belongs to.
DELETE FROM oauth_authorization_requests r
WHERE NOT EXISTS (
    SELECT 1 FROM workspace_members wm
    WHERE wm.workspace_id = r.workspace_id AND wm.user_id = r.user_id
);
DELETE FROM oauth_consents r
WHERE NOT EXISTS (
    SELECT 1 FROM workspace_members wm
    WHERE wm.workspace_id = r.workspace_id AND wm.user_id = r.user_id
);
DELETE FROM oauth_authorization_codes r
WHERE NOT EXISTS (
    SELECT 1 FROM workspace_members wm
    WHERE wm.workspace_id = r.workspace_id AND wm.user_id = r.user_id
);
DELETE FROM oauth_access_tokens r
WHERE NOT EXISTS (
    SELECT 1 FROM workspace_members wm
    WHERE wm.workspace_id = r.workspace_id AND wm.user_id = r.user_id
);
DELETE FROM oauth_refresh_tokens r
WHERE NOT EXISTS (
    SELECT 1 FROM workspace_members wm
    WHERE wm.workspace_id = r.workspace_id AND wm.user_id = r.user_id
);

ALTER TABLE oauth_authorization_requests ADD CONSTRAINT oauth_auth_requests_membership_fkey
    FOREIGN KEY (workspace_id, user_id) REFERENCES workspace_members(workspace_id, user_id) ON DELETE CASCADE;
ALTER TABLE oauth_consents ADD CONSTRAINT oauth_consents_membership_fkey
    FOREIGN KEY (workspace_id, user_id) REFERENCES workspace_members(workspace_id, user_id) ON DELETE CASCADE;
ALTER TABLE oauth_authorization_codes ADD CONSTRAINT oauth_codes_membership_fkey
    FOREIGN KEY (workspace_id, user_id) REFERENCES workspace_members(workspace_id, user_id) ON DELETE CASCADE;
ALTER TABLE oauth_access_tokens ADD CONSTRAINT oauth_access_membership_fkey
    FOREIGN KEY (workspace_id, user_id) REFERENCES workspace_members(workspace_id, user_id) ON DELETE CASCADE;
ALTER TABLE oauth_refresh_tokens ADD CONSTRAINT oauth_refresh_membership_fkey
    FOREIGN KEY (workspace_id, user_id) REFERENCES workspace_members(workspace_id, user_id) ON DELETE CASCADE;

-- ---------------------------------------------------------------------------
-- Domain constraints: reject invalid state even when a caller bypasses Go.
-- ---------------------------------------------------------------------------
ALTER TABLE mailboxes ADD CONSTRAINT mailboxes_provider_check
    CHECK (provider IN ('smtp','gmail','m365'));
ALTER TABLE mailboxes ADD CONSTRAINT mailboxes_status_check
    CHECK (status IN ('active','paused','error'));
ALTER TABLE mailboxes ADD CONSTRAINT mailboxes_smtp_port_check CHECK (smtp_port BETWEEN 1 AND 65535);
ALTER TABLE mailboxes ADD CONSTRAINT mailboxes_imap_port_check CHECK (imap_port BETWEEN 1 AND 65535);
ALTER TABLE mailboxes ADD CONSTRAINT mailboxes_daily_cap_check CHECK (daily_cap >= 0);
ALTER TABLE mailboxes ADD CONSTRAINT mailboxes_min_interval_check CHECK (min_interval_seconds >= 0);
ALTER TABLE mailboxes ADD CONSTRAINT mailboxes_ramp_check
    CHECK (ramp_start_cap >= 0 AND ramp_days > 0);

ALTER TABLE warmup_participants ADD CONSTRAINT warmup_participants_volume_check
    CHECK (start_volume >= 0 AND max_volume >= start_volume AND ramp_increment >= 0);
ALTER TABLE warmup_participants ADD CONSTRAINT warmup_participants_reply_rate_check
    CHECK (reply_rate BETWEEN 0 AND 1);
ALTER TABLE warmup_threads ADD CONSTRAINT warmup_threads_distinct_mailboxes_check
    CHECK (sender_mailbox <> partner_mailbox);
ALTER TABLE warmup_threads ADD CONSTRAINT warmup_threads_turn_check CHECK (turn >= 0);
ALTER TABLE warmup_daily_stats ADD CONSTRAINT warmup_daily_stats_nonnegative_check
    CHECK (sent >= 0 AND received >= 0 AND inbox >= 0 AND spam >= 0 AND replies >= 0);
ALTER TABLE sequence_enrollments ADD CONSTRAINT sequence_enrollments_reply_confidence_check
    CHECK (reply_confidence IS NULL OR reply_confidence BETWEEN 0 AND 1);
ALTER TABLE sequence_enrollments ADD CONSTRAINT sequence_enrollments_cap_deferrals_check
    CHECK (cap_deferrals >= 0);
ALTER TABLE sends ADD CONSTRAINT sends_attempts_check CHECK (attempts >= 0);
ALTER TABLE api_keys ADD CONSTRAINT api_keys_rate_limit_check
    CHECK (rate_limit_per_min IS NULL OR rate_limit_per_min > 0);
ALTER TABLE email_otp_codes ADD CONSTRAINT email_otp_attempts_check
    CHECK (attempts >= 0 AND max_attempts > 0 AND attempts <= max_attempts);
ALTER TABLE two_factor_challenges ADD CONSTRAINT two_factor_attempts_check CHECK (attempts >= 0);

-- Email identity is case-insensitive throughout the product.
ALTER TABLE mailboxes DROP CONSTRAINT mailboxes_workspace_id_email_key;
CREATE UNIQUE INDEX mailboxes_workspace_email_key
    ON mailboxes (workspace_id, lower(email));

-- ---------------------------------------------------------------------------
-- Indexes: support FK cascades/restrict checks and actual sort/filter patterns.
-- ---------------------------------------------------------------------------
DROP INDEX idx_sequence_steps_campaign; -- duplicates UNIQUE(campaign_id, step_order)
DROP INDEX idx_suppression_email;        -- every lookup is tenant-scoped
DROP INDEX idx_sends_workspace_created; -- no send-history query uses it

DROP INDEX idx_campaigns_workspace;
CREATE INDEX idx_campaigns_workspace_created ON campaigns(workspace_id, created_at DESC);
DROP INDEX idx_lists_workspace;
CREATE INDEX idx_lists_workspace_created ON lists(workspace_id, created_at DESC);
DROP INDEX idx_mailboxes_workspace;
CREATE INDEX idx_mailboxes_workspace_created ON mailboxes(workspace_id, created_at DESC);
DROP INDEX idx_api_keys_workspace;
CREATE INDEX idx_api_keys_workspace_created ON api_keys(workspace_id, created_at DESC);
DROP INDEX idx_oauth_clients_workspace;
CREATE INDEX idx_oauth_clients_workspace_created ON oauth_clients(workspace_id, created_at DESC);

CREATE INDEX idx_campaigns_mailbox ON campaigns(mailbox_id);
CREATE INDEX idx_campaigns_list ON campaigns(list_id);
CREATE INDEX idx_sends_contact ON sends(contact_id);
CREATE INDEX idx_sends_mailbox ON sends(mailbox_id);
CREATE INDEX idx_enrollments_contact ON sequence_enrollments(contact_id);
CREATE INDEX idx_sessions_workspace ON sessions(workspace_id);
CREATE INDEX idx_email_otp_user ON email_otp_codes(user_id);
CREATE INDEX idx_recovery_codes_user ON user_recovery_codes(user_id);
CREATE INDEX idx_two_factor_challenges_user ON two_factor_challenges(user_id);
CREATE INDEX idx_webauthn_challenges_user ON webauthn_challenges(user_id) WHERE user_id IS NOT NULL;
CREATE INDEX idx_workspace_invites_workspace_created ON workspace_invites(workspace_id, created_at DESC);
CREATE INDEX idx_workspace_invites_invited_by ON workspace_invites(invited_by);
CREATE INDEX idx_api_keys_created_by ON api_keys(created_by_user_id) WHERE created_by_user_id IS NOT NULL;

CREATE INDEX idx_oauth_auth_requests_workspace_user ON oauth_authorization_requests(workspace_id, user_id);
CREATE INDEX idx_oauth_auth_requests_user ON oauth_authorization_requests(user_id);
CREATE INDEX idx_oauth_codes_workspace_user ON oauth_authorization_codes(workspace_id, user_id);
CREATE INDEX idx_oauth_codes_user ON oauth_authorization_codes(user_id);
CREATE INDEX idx_oauth_consents_workspace ON oauth_consents(workspace_id);
CREATE INDEX idx_oauth_access_workspace_user ON oauth_access_tokens(workspace_id, user_id);
CREATE INDEX idx_oauth_access_user ON oauth_access_tokens(user_id);
CREATE INDEX idx_oauth_refresh_workspace_user ON oauth_refresh_tokens(workspace_id, user_id);
CREATE INDEX idx_oauth_refresh_user ON oauth_refresh_tokens(user_id);

DROP INDEX warmup_participants_ws;
CREATE INDEX idx_warmup_participants_workspace_created ON warmup_participants(workspace_id, created_at DESC);
DROP INDEX warmup_threads_partner;
CREATE INDEX idx_warmup_threads_sender_pair_activity
    ON warmup_threads(workspace_id, sender_mailbox, partner_mailbox, last_activity_at DESC);
CREATE INDEX idx_warmup_threads_partner_pair_activity
    ON warmup_threads(workspace_id, partner_mailbox, sender_mailbox, last_activity_at DESC);
CREATE INDEX idx_warmup_threads_sender ON warmup_threads(sender_mailbox);
CREATE INDEX idx_warmup_threads_partner ON warmup_threads(partner_mailbox);
CREATE INDEX idx_warmup_sends_workspace ON warmup_sends(workspace_id);
CREATE INDEX idx_warmup_sends_thread ON warmup_sends(thread_id);
CREATE INDEX idx_warmup_sends_from ON warmup_sends(from_mailbox);
CREATE INDEX idx_warmup_sends_to ON warmup_sends(to_mailbox);
CREATE INDEX idx_warmup_receipts_workspace ON warmup_receipts(workspace_id);
CREATE INDEX idx_warmup_daily_stats_workspace_day ON warmup_daily_stats(workspace_id, day DESC);

-- Worker assignment rows are meaningful only for known workers, but a worker
-- may go offline without being deleted; RESTRICT preserves stable assignments.
ALTER TABLE mailbox_worker_assignments ADD CONSTRAINT mailbox_worker_assignments_worker_fkey
    FOREIGN KEY (worker_id) REFERENCES workers(worker_id) ON DELETE RESTRICT;
