-- Warmup engine state (spec §3). Every table is workspace-pinned and CASCADEs
-- from both workspaces and mailboxes, so deleting either parent cleans up all
-- warmup state (no orphaned rows, no stale reputation history).

-- One row per opted-in mailbox: ramp config, current health, and enabled flag.
CREATE TABLE warmup_participants (
    mailbox_id        UUID PRIMARY KEY REFERENCES mailboxes(id) ON DELETE CASCADE,
    workspace_id      UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    enabled           BOOLEAN NOT NULL DEFAULT true,
    -- ramp
    start_volume      INT NOT NULL DEFAULT 4,      -- emails/day at day 0
    max_volume        INT NOT NULL DEFAULT 40,     -- ceiling
    ramp_increment    INT NOT NULL DEFAULT 2,      -- +N/day
    reply_rate        REAL NOT NULL DEFAULT 0.30,  -- P(a send is a reply)
    -- runtime
    started_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    health_state      TEXT NOT NULL DEFAULT 'healthy'
                       CHECK (health_state IN ('healthy','watch','throttled','paused')),
    health_reason     TEXT NOT NULL DEFAULT '',
    paused_until      TIMESTAMPTZ,                 -- set when throttled/paused
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- Pool lookup walks a workspace's enabled participants; a partial index keeps it
-- index-backed and skips disabled rows.
CREATE INDEX warmup_participants_ws ON warmup_participants(workspace_id) WHERE enabled;

-- A synthetic conversation between two participants.
CREATE TABLE warmup_threads (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id      UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    sender_mailbox    UUID NOT NULL REFERENCES mailboxes(id) ON DELETE CASCADE,
    partner_mailbox   UUID NOT NULL REFERENCES mailboxes(id) ON DELETE CASCADE,
    subject           TEXT NOT NULL,
    root_message_id   TEXT NOT NULL DEFAULT '',    -- Message-ID of first message
    turn              INT  NOT NULL DEFAULT 0,      -- messages exchanged so far
    content_key       TEXT NOT NULL,                -- which library thread drives it
    last_activity_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- Recent-partner-avoidance selects the partner's least-recently-active threads.
CREATE INDEX warmup_threads_partner ON warmup_threads(workspace_id, partner_mailbox, last_activity_at);

-- Claim-before-send lifecycle for one warmup email (mirrors sends).
CREATE TABLE warmup_sends (
    id                UUID PRIMARY KEY,             -- deterministic (spec §6)
    workspace_id      UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    thread_id         UUID NOT NULL REFERENCES warmup_threads(id) ON DELETE CASCADE,
    from_mailbox      UUID NOT NULL REFERENCES mailboxes(id) ON DELETE CASCADE,
    to_mailbox        UUID NOT NULL REFERENCES mailboxes(id) ON DELETE CASCADE,
    is_reply          BOOLEAN NOT NULL DEFAULT false,
    status            TEXT NOT NULL DEFAULT 'queued'
                       CHECK (status IN ('queued','sending','sent','failed','skipped')),
    message_id        TEXT NOT NULL DEFAULT '',
    token             TEXT NOT NULL,                -- HMAC receipt token (spec §7)
    claimed_at        TIMESTAMPTZ,                  -- lease
    sent_at           TIMESTAMPTZ,
    last_error        TEXT NOT NULL DEFAULT '',
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Receipt + placement of a warmup message that arrived in the partner's inbox.
CREATE TABLE warmup_receipts (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id      UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    warmup_send_id    UUID REFERENCES warmup_sends(id) ON DELETE SET NULL,
    recipient_mailbox UUID NOT NULL REFERENCES mailboxes(id) ON DELETE CASCADE,
    placement         TEXT NOT NULL CHECK (placement IN ('inbox','spam','other')),
    engaged           BOOLEAN NOT NULL DEFAULT false,
    received_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (warmup_send_id, recipient_mailbox)      -- idempotent receipt
);
-- Health evaluation sums a recipient's recent placements in received-at order.
CREATE INDEX warmup_receipts_health ON warmup_receipts(recipient_mailbox, received_at);

-- Rolling daily counters per participant (drives ramp + health, one row/day).
CREATE TABLE warmup_daily_stats (
    mailbox_id        UUID NOT NULL REFERENCES mailboxes(id) ON DELETE CASCADE,
    workspace_id      UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    day               DATE NOT NULL,              -- UTC day (DB session TZ = UTC); reads/writes anchor on CURRENT_DATE
    sent              INT NOT NULL DEFAULT 0,
    received          INT NOT NULL DEFAULT 0,
    inbox             INT NOT NULL DEFAULT 0,      -- of received, how many in inbox
    spam              INT NOT NULL DEFAULT 0,      -- of received, how many in spam
    replies           INT NOT NULL DEFAULT 0,
    PRIMARY KEY (mailbox_id, day)
);
