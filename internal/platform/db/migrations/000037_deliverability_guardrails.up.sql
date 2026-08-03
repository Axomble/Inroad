-- Deliverability guardrails: the circuit breaker that stops a campaign sending
-- into a dead list, and the evidence trail that explains why it stopped.
--
-- Every signal this reads was already being collected (hard bounces, warmup
-- spam-vs-inbox placement, warmup health, SPF/DKIM/DMARC, suppression) and acted
-- on by nobody: a campaign sending into a dead list burned the domain at full
-- configured speed until a human noticed.

-- Why a campaign stopped. An auto-paused campaign must never be found stopped
-- with no explanation, so the reason, the metric, the observed value, the
-- threshold it crossed and the SAMPLE it was judged on are all recorded — that
-- last column is what lets an operator confirm the minimum-sample rule held.
--
-- Append-only history rather than one current row: a campaign can be paused,
-- restarted by hand, and paused again, and the earlier stop is still the record
-- of what happened.
CREATE TABLE campaign_pause_events (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    campaign_id  UUID NOT NULL,
    -- Vocabulary mirrored by the CampaignPauseEvent schema and the
    -- platform/deliverability Reason*/Metric* constants.
    reason       TEXT NOT NULL CHECK (reason IN ('bounce_spike','complaint_spike')),
    metric       TEXT NOT NULL CHECK (metric IN ('bounce_rate','complaint_rate')),
    -- The observed rate and the threshold it crossed, both as PERCENTAGES (8.0 is
    -- eight percent) — the single unit this whole feature uses, so a threshold can
    -- never be compared against a fraction.
    value        NUMERIC NOT NULL CHECK (value >= 0),
    threshold    NUMERIC NOT NULL CHECK (threshold > 0),
    -- The delivered count the judgement was made on. NOT NULL and > 0: a pause
    -- event with no sample would be a breaker that fired on nothing.
    delivered    INT NOT NULL CHECK (delivered > 0),
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- Composite tenant FK (the migration-000028 pattern): a pause event for
    -- another tenant's campaign is unrepresentable, not merely rejected in Go.
    FOREIGN KEY (campaign_id, workspace_id) REFERENCES campaigns(id, workspace_id) ON DELETE CASCADE
);

-- The only read: this campaign's pause history, newest first, for the campaign
-- detail card. Workspace leads because every query is tenant-pinned first.
CREATE INDEX idx_campaign_pause_events_campaign
    ON campaign_pause_events (workspace_id, campaign_id, created_at DESC);

-- Deliverability events reported by an EXTERNAL pipeline (an SES SNS subscriber,
-- a provider webhook) through POST /deliverability/events. Inroad detects hard
-- bounces itself from the inbox poller; this table is how a signal we cannot
-- observe — a complaint — gets in at all.
--
-- Idempotent on (workspace, provider_event_id): a webhook that retries must not
-- be able to inflate the rate a circuit breaker then acts on. That UNIQUE is the
-- guarantee; the handler's 200-vs-202 is only how it reports it.
CREATE TABLE deliverability_events (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id      UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    kind              TEXT NOT NULL CHECK (kind IN ('complaint','bounce')),
    -- CITEXT because email identity is case-insensitive throughout the product
    -- (the suppression and mailbox uniqueness rules both already use lower()).
    email             CITEXT NOT NULL,
    -- The send this event is about, when the provider told us. NULL is normal:
    -- most feeds report an address, not our internal id. Campaign attribution
    -- needs it, so an event without one counts toward the workspace rollup but
    -- not toward any campaign's breaker.
    --
    -- Column-specific SET NULL (the 000028 warmup_receipts pattern): deleting a
    -- send must not delete the complaint it caused, and must not null the
    -- workspace half of the composite key.
    send_id           UUID,
    provider_event_id TEXT NOT NULL CHECK (provider_event_id <> ''),
    received_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (workspace_id, provider_event_id),
    FOREIGN KEY (send_id, workspace_id) REFERENCES sends(id, workspace_id) ON DELETE SET NULL (send_id)
);

-- The rolling-window rollup: "this workspace's complaints/bounces since <cutoff>",
-- grouped by kind. Workspace-pinned first, kind next, received_at trailing so the
-- half-open window range-seeks rather than scanning the workspace's whole history.
CREATE INDEX idx_deliverability_events_workspace_kind_received
    ON deliverability_events (workspace_id, kind, received_at);
-- Campaign attribution joins through send_id, and the FK needs it anyway.
CREATE INDEX idx_deliverability_events_send ON deliverability_events (send_id)
    WHERE send_id IS NOT NULL;

-- A complaint is the strongest opt-out signal there is, so ingesting one
-- suppresses the address workspace-wide through the EXISTING suppression table
-- rather than a parallel one. It needs its own reason: recording a complaint as
-- 'unsubscribe' would lose the distinction between "they asked to stop" and
-- "they reported us as spam", which are very different things to see in a list.
ALTER TABLE suppression DROP CONSTRAINT suppression_reason_check;
ALTER TABLE suppression ADD CONSTRAINT suppression_reason_check
    CHECK (reason IN ('unsubscribe','bounce','manual','complaint'));

-- Per-campaign breaker settings.
--
-- ON by default. A safeguard nobody enables protects nobody, and the
-- minimum-delivered floor in platform/deliverability (50) is what makes
-- on-by-default safe: below it the breaker cannot fire whatever the ratio, so a
-- campaign that bounces its first three sends is not stopped on a 33% "rate".
--
-- The two percentages mirror platform/deliverability's DefaultBouncePausePct and
-- DefaultComplaintPausePct; the CHECK bounds mirror ThresholdMin/ThresholdMax and
-- the CampaignGuardrails schema. The floor is 0.1 rather than 0 because a
-- threshold of 0 means "pause at any rate at all".
ALTER TABLE campaigns
    ADD COLUMN auto_pause_enabled  BOOLEAN NOT NULL DEFAULT TRUE,
    ADD COLUMN bounce_pause_pct    NUMERIC NOT NULL DEFAULT 8.0
                 CHECK (bounce_pause_pct BETWEEN 0.1 AND 100),
    ADD COLUMN complaint_pause_pct NUMERIC NOT NULL DEFAULT 1.5
                 CHECK (complaint_pause_pct BETWEEN 0.1 AND 100);

-- The breaker counts a campaign's bounced enrollments over a rolling window on
-- every evaluation. idx_enrollments_workspace_status seeks by workspace and would
-- then filter every enrollment the workspace has; this partial index holds only
-- the stopped-bounced rows with stopped_at trailing, so the window range-seeks.
-- Mirrors the idx_sends_campaign_sent discipline from migration 000033.
CREATE INDEX idx_enrollments_campaign_bounced
    ON sequence_enrollments (campaign_id, stopped_at)
    WHERE stop_reason = 'bounced';
