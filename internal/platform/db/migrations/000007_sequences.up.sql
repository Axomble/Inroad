CREATE TABLE sequence_steps (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id  UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    campaign_id   UUID NOT NULL REFERENCES campaigns(id) ON DELETE CASCADE,
    step_order    INT  NOT NULL CHECK (step_order >= 1),
    delay_seconds INT  NOT NULL DEFAULT 0 CHECK (delay_seconds >= 0),
    subject       TEXT NOT NULL,
    body_text     TEXT NOT NULL DEFAULT '',
    body_html     TEXT NOT NULL DEFAULT '',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (campaign_id, step_order)
);
CREATE INDEX idx_sequence_steps_campaign ON sequence_steps (campaign_id, step_order);

CREATE TABLE sequence_enrollments (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id   UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    campaign_id    UUID NOT NULL REFERENCES campaigns(id) ON DELETE CASCADE,
    contact_id     UUID NOT NULL REFERENCES contacts(id) ON DELETE CASCADE,
    current_step   INT  NOT NULL DEFAULT 0,       -- 0 = enrolled/not started; N = last step sent
    status         TEXT NOT NULL DEFAULT 'active'
                     CHECK (status IN ('active','completed','stopped')),
    stop_reason    TEXT CHECK (stop_reason IS NULL OR stop_reason IN ('replied','bounced','suppressed','manual')),
    enrolled_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_sent_at   TIMESTAMPTZ,
    next_due_at    TIMESTAMPTZ,                    -- when the next step is scheduled
    thread_root_id TEXT NOT NULL DEFAULT '',       -- Message-ID of step 1, for References chain
    completed_at   TIMESTAMPTZ,
    stopped_at     TIMESTAMPTZ,
    UNIQUE (campaign_id, contact_id)
);
CREATE INDEX idx_enrollments_due
  ON sequence_enrollments (next_due_at)
  WHERE status = 'active' AND next_due_at IS NOT NULL;
CREATE INDEX idx_enrollments_workspace_status
  ON sequence_enrollments (workspace_id, status);

ALTER TABLE sends ADD COLUMN step_order        INT  NOT NULL DEFAULT 1;
ALTER TABLE sends ADD COLUMN references_header TEXT NOT NULL DEFAULT '';

-- Backfill: every existing campaign with a subject and no steps yet gets one
-- step_order=1 row copying its inline message. Idempotent (NOT EXISTS guard),
-- so a re-run after a partial apply is safe.
INSERT INTO sequence_steps (workspace_id, campaign_id, step_order, delay_seconds, subject, body_text, body_html)
SELECT c.workspace_id, c.id, 1, 0, c.subject, c.body_text, c.body_html
FROM campaigns c
WHERE c.subject != ''
  AND NOT EXISTS (SELECT 1 FROM sequence_steps s WHERE s.campaign_id = c.id);
