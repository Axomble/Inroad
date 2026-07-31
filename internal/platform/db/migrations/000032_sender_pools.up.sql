-- Sender pools: a campaign sends from a SET of mailboxes rather than the single
-- campaigns.mailbox_id FK. campaigns.mailbox_id STAYS — it is the fallback for a
-- campaign that has no pool rows (an invariant that depends on every writer
-- remembering to seed a table is not an invariant) and the column the direct-send
-- path still reads. Removing it is a separate cleanup.

-- One row per mailbox in a campaign's pool, plus the rotation state selection
-- reads. Composite tenant FKs (the 000028 pattern) make a cross-tenant campaign
-- or mailbox unrepresentable rather than merely rejected in Go.
CREATE TABLE campaign_senders (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id     UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    campaign_id      UUID NOT NULL,
    mailbox_id       UUID NOT NULL,
    weight           INT NOT NULL DEFAULT 1 CHECK (weight BETWEEN 1 AND 100),
    enabled          BOOLEAN NOT NULL DEFAULT TRUE,
    -- Rotation state. assigned_count drives round-robin, last_assigned_at drives
    -- LRU. Both are preserved across a pool edit for mailboxes that stay in it,
    -- so changing a weight does not reset the spread.
    assigned_count   BIGINT NOT NULL DEFAULT 0,
    last_assigned_at TIMESTAMPTZ,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (campaign_id, mailbox_id),
    CONSTRAINT campaign_senders_campaign_tenant_fkey
        FOREIGN KEY (campaign_id, workspace_id) REFERENCES campaigns(id, workspace_id) ON DELETE CASCADE,
    CONSTRAINT campaign_senders_mailbox_tenant_fkey
        FOREIGN KEY (mailbox_id, workspace_id) REFERENCES mailboxes(id, workspace_id) ON DELETE CASCADE
);

-- Selection reads a campaign's whole pool at once; workspace_id leads so the
-- index also serves the tenant-scoped listing the API does.
CREATE INDEX idx_campaign_senders_campaign ON campaign_senders (workspace_id, campaign_id);

-- How a contact is assigned a mailbox from the pool. 'weighted' is the default:
-- it is the only mode that accounts for remaining capacity, so it is the right
-- behaviour for an operator who never opens the setting.
ALTER TABLE campaigns
    ADD COLUMN rotation_mode TEXT NOT NULL DEFAULT 'weighted'
      CHECK (rotation_mode IN ('round_robin','least_recently_used','weighted'));

-- The mailbox this enrollment's whole thread sends from, pinned at its first
-- send and reused by every follow-up step: a follow-up is a reply carrying
-- In-Reply-To/References from the previous message, so switching mailbox
-- mid-thread would reference a Message-ID that mailbox never sent. NULL until
-- the first step sends. A plain (non-composite) FK because ON DELETE SET NULL on
-- a composite would try to null the NOT NULL workspace_id too; cross-tenant
-- values cannot arise here since the value only ever comes from campaign_senders
-- (tenant-FK'd above) or campaigns.mailbox_id.
ALTER TABLE sequence_enrollments
    ADD COLUMN mailbox_id UUID REFERENCES mailboxes(id) ON DELETE SET NULL;

-- Extend the stop-reason CHECK (last set in 000014) with 'mailbox_removed'. The
-- SET NULL above is a live hazard for a thread mid-flight: deleting a pool-only
-- mailbox clears the pin, and a naive re-resolve would send "Re: <subject>"
-- from a NEW address carrying In-Reply-To/References for a Message-ID that
-- mailbox never sent — the exact broken-threading spam signal the pin exists to
-- prevent. The thread's identity is gone, so the sequence stops with a reason of
-- its own instead of continuing incorrectly.
ALTER TABLE sequence_enrollments DROP CONSTRAINT sequence_enrollments_stop_reason_check;
ALTER TABLE sequence_enrollments ADD CONSTRAINT sequence_enrollments_stop_reason_check
    CHECK (stop_reason IS NULL OR stop_reason IN
        ('replied','bounced','suppressed','manual','failed','unsubscribed','mailbox_removed'));

-- Backfill: every existing campaign becomes a one-mailbox pool, so the API shows
-- the mailbox it actually sends from rather than an empty pool.
INSERT INTO campaign_senders (workspace_id, campaign_id, mailbox_id)
SELECT workspace_id, id, mailbox_id FROM campaigns;

-- And every enrollment that has already sent keeps the mailbox it actually sent
-- from, so its in-flight thread is not re-routed by the first rotation.
UPDATE sequence_enrollments e SET mailbox_id = c.mailbox_id
FROM campaigns c WHERE c.id = e.campaign_id AND e.current_step > 0;
