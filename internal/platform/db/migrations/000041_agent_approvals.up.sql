-- Human approval gates for consequential agent tools (agent-platform spec A4).
-- Relationships are workspace-composite so a pending action can never point
-- at another tenant's run, thread, message, or part.
ALTER TABLE agent_message_parts
    ADD CONSTRAINT uq_agent_message_parts_id_workspace UNIQUE (id, workspace_id);

CREATE TABLE pending_actions (
    id                    UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id          UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    thread_id             UUID NOT NULL,
    run_id                UUID NOT NULL,
    message_id            UUID NOT NULL,
    message_part_id       UUID NOT NULL,
    turn_id               UUID NOT NULL,
    created_by_user_id    UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    actor_role            TEXT NOT NULL,
    tool_name             TEXT NOT NULL,
    tool_call_id          TEXT NOT NULL,
    arguments             JSONB NOT NULL CHECK (jsonb_typeof(arguments) = 'object'),
    edited_arguments      JSONB CHECK (edited_arguments IS NULL OR jsonb_typeof(edited_arguments) = 'object'),
    risk_tier             TEXT NOT NULL CHECK (risk_tier IN ('consequential', 'irreversible')),
    status                TEXT NOT NULL DEFAULT 'pending' CHECK (status IN (
                              'pending', 'approved', 'rejected', 'expired', 'executed', 'failed')),
    decision_reason       TEXT NOT NULL DEFAULT '',
    decided_by_user_id    UUID REFERENCES users(id) ON DELETE SET NULL,
    decided_at            TIMESTAMPTZ,
    expires_at            TIMESTAMPTZ NOT NULL,
    result                JSONB,
    error_message         TEXT NOT NULL DEFAULT '',
    executed_at           TIMESTAMPTZ,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    FOREIGN KEY (thread_id, workspace_id)
        REFERENCES agent_threads (id, workspace_id) ON DELETE CASCADE,
    FOREIGN KEY (run_id, workspace_id)
        REFERENCES agent_runs (id, workspace_id) ON DELETE CASCADE,
    FOREIGN KEY (message_id, workspace_id)
        REFERENCES agent_messages (id, workspace_id) ON DELETE CASCADE,
    FOREIGN KEY (message_part_id, workspace_id)
        REFERENCES agent_message_parts (id, workspace_id) ON DELETE CASCADE,
    UNIQUE (id, workspace_id),
    UNIQUE (run_id, tool_call_id)
);

CREATE INDEX idx_pending_actions_owner
    ON pending_actions (workspace_id, created_by_user_id, status, expires_at, created_at DESC);
CREATE INDEX idx_pending_actions_run
    ON pending_actions (workspace_id, run_id, status);
CREATE INDEX idx_pending_actions_expiry
    ON pending_actions (expires_at) WHERE status = 'pending';

-- Append-only evidence for every proposal, human decision, expiry, and tool
-- execution. pending_actions holds current state; this table explains how it
-- got there without mutable JSON history.
CREATE TABLE pending_action_audit (
    id                 UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id       UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    pending_action_id  UUID NOT NULL,
    actor_user_id      UUID REFERENCES users(id) ON DELETE SET NULL,
    event              TEXT NOT NULL CHECK (event IN (
                           'created', 'approved', 'rejected', 'expired', 'executed', 'failed')),
    details            JSONB NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(details) = 'object'),
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    FOREIGN KEY (pending_action_id, workspace_id)
        REFERENCES pending_actions (id, workspace_id) ON DELETE CASCADE
);

CREATE INDEX idx_pending_action_audit_action
    ON pending_action_audit (workspace_id, pending_action_id, created_at, id);
