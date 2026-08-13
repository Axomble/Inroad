-- Warmup evidence and minimum-sample health. This is Phase 0 of the reputation
-- network design: keep the existing local pool, but stop equating silence with
-- health and retain the evidence behind every automatic state change.

ALTER TABLE warmup_participants
    DROP CONSTRAINT warmup_participants_health_state_check;
ALTER TABLE warmup_participants
    ALTER COLUMN health_state SET DEFAULT 'unknown';
ALTER TABLE warmup_participants
    ADD CONSTRAINT warmup_participants_health_state_check
    CHECK (health_state IN ('unknown','healthy','watch','throttled','paused'));

-- Immutable normalized observations. Placement is attributed to the sender;
-- observer_mailbox_id records who reported it. Invalid tokens are deliberately
-- allowed to have no attributed mailbox: an unauthenticated token may claim any
-- sender, so using that claim for health would create a trivial DoS primitive.
CREATE TABLE warmup_observations (
    id                    UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id          UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    mailbox_id            UUID,
    observer_mailbox_id   UUID,
    warmup_send_id        UUID,
    kind                  TEXT NOT NULL
                          CHECK (kind IN ('placement','hard_bounce','complaint','invalid_token')),
    placement             TEXT CHECK (placement IN ('inbox','spam','other')),
    source                TEXT NOT NULL CHECK (btrim(source) <> ''),
    reason_code           TEXT NOT NULL DEFAULT '',
    attribution_trusted   BOOLEAN NOT NULL DEFAULT false,
    idempotency_key       TEXT NOT NULL CHECK (btrim(idempotency_key) <> ''),
    observed_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (workspace_id, idempotency_key),
    FOREIGN KEY (mailbox_id, workspace_id)
        REFERENCES mailboxes(id, workspace_id) ON DELETE CASCADE,
    FOREIGN KEY (observer_mailbox_id, workspace_id)
        REFERENCES mailboxes(id, workspace_id) ON DELETE SET NULL (observer_mailbox_id),
    FOREIGN KEY (warmup_send_id, workspace_id)
        REFERENCES warmup_sends(id, workspace_id) ON DELETE SET NULL (warmup_send_id),
    CHECK ((kind = 'placement') = (placement IS NOT NULL)),
    -- A placement must name the sender it is attributed to. It deliberately does
    -- NOT require warmup_send_id: the send FK above nulls that column when the
    -- send is deleted, and a CHECK demanding it would abort the referential
    -- action instead — making any mailbox with warmup history undeletable. The
    -- send anchor is enforced where it can be, at INSERT: every writer selects
    -- FROM warmup_sends, so a placement never enters without one. Losing the
    -- anchor later must not destroy a DIFFERENT mailbox's reputation evidence.
    CHECK (kind <> 'placement' OR mailbox_id IS NOT NULL),
    CHECK (kind <> 'invalid_token' OR reason_code <> '')
);

CREATE INDEX idx_warmup_observations_subject_time
    ON warmup_observations (workspace_id, mailbox_id, kind, observed_at DESC)
    WHERE mailbox_id IS NOT NULL;
CREATE INDEX idx_warmup_observations_observer_time
    ON warmup_observations (workspace_id, observer_mailbox_id, observed_at DESC)
    WHERE observer_mailbox_id IS NOT NULL;

-- Supports the send FK's ON DELETE SET NULL. Without it every cascaded send
-- delete sequentially scans this table once per parent row, so removing one
-- mailbox — which cascades to every send it sent or received — would not finish.
CREATE INDEX idx_warmup_observations_send
    ON warmup_observations (warmup_send_id, workspace_id)
    WHERE warmup_send_id IS NOT NULL;

-- Backfill the receipt history so deployment does not erase the evidence behind
-- an existing participant's state. The receipt is the idempotency boundary.
INSERT INTO warmup_observations (
    workspace_id, mailbox_id, observer_mailbox_id, warmup_send_id,
    kind, placement, source, attribution_trusted, idempotency_key, observed_at
)
SELECT r.workspace_id, s.from_mailbox, r.recipient_mailbox, r.warmup_send_id,
       'placement', r.placement, 'warmup_receipt', true,
       'receipt:' || r.id::text, r.received_at
FROM warmup_receipts r
JOIN warmup_sends s
  ON s.id = r.warmup_send_id AND s.workspace_id = r.workspace_id;

-- Append-only explanation of every automatic health transition. Rates are
-- fractions (0.10 = ten percent), matching platform/warmup policy inputs.
CREATE TABLE warmup_state_transitions (
    id                    UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id          UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    mailbox_id            UUID NOT NULL,
    from_state            TEXT NOT NULL
                          CHECK (from_state IN ('unknown','healthy','watch','throttled','paused')),
    to_state              TEXT NOT NULL
                          CHECK (to_state IN ('unknown','healthy','watch','throttled','paused')),
    reason_code           TEXT NOT NULL CHECK (btrim(reason_code) <> ''),
    reason                TEXT NOT NULL,
    placement_samples     INT NOT NULL CHECK (placement_samples >= 0),
    spam_rate             REAL NOT NULL CHECK (spam_rate BETWEEN 0 AND 1),
    bounce_samples        INT NOT NULL CHECK (bounce_samples >= 0),
    bounce_rate           REAL NOT NULL CHECK (bounce_rate BETWEEN 0 AND 1),
    complaint_samples     INT NOT NULL CHECK (complaint_samples >= 0),
    complaint_rate        REAL NOT NULL CHECK (complaint_rate BETWEEN 0 AND 1),
    invalid_tokens        INT NOT NULL CHECK (invalid_tokens >= 0),
    policy_version        TEXT NOT NULL CHECK (btrim(policy_version) <> ''),
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    FOREIGN KEY (mailbox_id, workspace_id)
        REFERENCES mailboxes(id, workspace_id) ON DELETE CASCADE
);

CREATE INDEX idx_warmup_state_transitions_mailbox
    ON warmup_state_transitions (workspace_id, mailbox_id, created_at DESC);

-- Existing healthy rows with too little recent evidence become unknown. Never
-- downgrade an already-degraded row during migration: recovery still has to be
-- earned through the evaluator after deployment.
UPDATE warmup_participants p
SET health_state = 'unknown',
    health_reason = 'not enough recent placement evidence',
    updated_at = now()
WHERE p.health_state = 'healthy'
  AND (
      SELECT COUNT(*)
      FROM warmup_observations o
      WHERE o.workspace_id = p.workspace_id
        AND o.mailbox_id = p.mailbox_id
        AND o.kind = 'placement'
        AND o.placement IN ('inbox','spam')
        AND o.observed_at >= now() - interval '7 days'
  ) < 20;
