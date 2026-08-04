-- Agent chat: threads, messages, normalized message parts, and runs
-- (agent-platform spec §2, PR A2).
--
-- Every table is workspace-pinned and child rows carry composite (id,
-- workspace_id) foreign keys — the migration-000028 pattern — so a
-- cross-tenant parent reference is UNREPRESENTABLE rather than merely
-- rejected in Go.
--
-- A thread is additionally OWNER-scoped: it is visible only to the user who
-- created it, within their workspace (spec §7.7). The workspace_id column is
-- what every query pins (security invariant 4); created_by_user_id is the
-- second filter the service applies.
CREATE TABLE agent_threads (
    id                    UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id          UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    created_by_user_id    UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    title                 TEXT NOT NULL DEFAULT '',
    -- Running totals across every run of the thread. Anthropic reports output
    -- tokens EXCLUDING reasoning and the OpenAI adapters normalize to match
    -- (ai.Usage contract), so these sum without knowing the provider.
    total_input_tokens    BIGINT NOT NULL DEFAULT 0 CHECK (total_input_tokens >= 0),
    total_output_tokens   BIGINT NOT NULL DEFAULT 0 CHECK (total_output_tokens >= 0),
    -- The context window of the model the thread last ran on, so the UI can
    -- show how full the conversation is without resolving the model itself.
    context_window_tokens INT NOT NULL DEFAULT 0 CHECK (context_window_tokens >= 0),
    -- Denormalized pointer to the thread's live run, maintained by the
    -- service. Deliberately NOT a foreign key: agent_runs already references
    -- agent_threads, and a second FK back would make the pair cyclic for both
    -- insert ordering and cascade deletion. agent_runs (status filtered) stays
    -- the AUTHORITATIVE answer to "is a run active"; this column is a read
    -- convenience for the thread DTO.
    active_run_id         UUID,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at            TIMESTAMPTZ,
    UNIQUE (id, workspace_id)
);

-- The thread-list read path: one user's own live threads, newest activity
-- first. Partial on deleted_at so soft-deleted rows are not even scanned.
CREATE INDEX idx_agent_threads_owner ON agent_threads (workspace_id, created_by_user_id, updated_at DESC)
    WHERE deleted_at IS NULL;

-- One conversation turn as the model sees it. Tool results ride as parts of a
-- USER message (the shape every provider family accepts), never as a role of
-- their own — matching internal/platform/ai's ChatMessage contract.
--
-- status is the queueing state (spec §1.7): a message sent while a run is
-- already active is stored 'queued' and promoted to 'processing' when the run
-- that will answer it starts; 'sent' is the terminal state.
--
-- turn_id groups a user message with every assistant/tool message produced in
-- response to it, so the UI can collapse a multi-step answer into one turn.
--
-- browsing_context is the client's page context for THIS message
-- ({type:'record_page'…} | {type:'list_view'…}). It is persisted per-message
-- rather than per-run because a resumed run (A4) must rebuild the exact
-- prompt, and because it is appended to the LAST USER MESSAGE — never the
-- system prompt — to keep the provider's prompt cache stable (spec §5).
CREATE TABLE agent_messages (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id     UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    thread_id        UUID NOT NULL,
    turn_id          UUID NOT NULL,
    role             TEXT NOT NULL CHECK (role IN ('user', 'assistant')),
    status           TEXT NOT NULL DEFAULT 'sent' CHECK (status IN ('sent', 'queued', 'processing')),
    browsing_context JSONB,
    processed_at     TIMESTAMPTZ,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    FOREIGN KEY (thread_id, workspace_id)
        REFERENCES agent_threads (id, workspace_id) ON DELETE CASCADE,
    UNIQUE (id, workspace_id)
);

-- Conversation replay order, and the queue read ("oldest queued message in
-- this thread") the run-end promotion runs.
CREATE INDEX idx_agent_messages_thread ON agent_messages (workspace_id, thread_id, created_at, id);

-- NORMALIZED column-per-kind, deliberately not a JSON blob: a part is
-- Go-struct friendly, and SQL can answer "every tool call in this thread"
-- without blob wrangling. Only the columns relevant to `type` are populated.
--
-- state tracks a tool call's lifecycle ('running' | 'done' | 'error' |
-- 'awaiting_approval'); it is empty for text/reasoning parts.
CREATE TABLE agent_message_parts (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id      UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    message_id        UUID NOT NULL,
    order_index       INT  NOT NULL CHECK (order_index >= 0),
    type              TEXT NOT NULL CHECK (type IN (
                        'text', 'reasoning', 'tool_call', 'tool_result', 'compaction_notice')),
    text_content      TEXT NOT NULL DEFAULT '',
    reasoning_content TEXT NOT NULL DEFAULT '',
    tool_name         TEXT NOT NULL DEFAULT '',
    tool_call_id      TEXT NOT NULL DEFAULT '',
    tool_input        JSONB,
    tool_output       JSONB,
    state             TEXT NOT NULL DEFAULT '' CHECK (state IN (
                        '', 'running', 'done', 'error', 'awaiting_approval')),
    error_message     TEXT NOT NULL DEFAULT '',
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    FOREIGN KEY (message_id, workspace_id)
        REFERENCES agent_messages (id, workspace_id) ON DELETE CASCADE,
    -- Part order within a message is unique by construction, so a retried
    -- persist can never interleave a duplicate part into the transcript.
    UNIQUE (message_id, order_index)
);

CREATE INDEX idx_agent_message_parts_message ON agent_message_parts (workspace_id, message_id, order_index);

-- One execution of the agent loop. Runs execute in the API binary as managed
-- goroutines (never asynq — workers must never hold LLM credentials), so a run
-- left 'running' after a crash is unrecoverable and is swept to 'failed' on
-- startup.
CREATE TABLE agent_runs (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    thread_id    UUID NOT NULL,
    status       TEXT NOT NULL DEFAULT 'running' CHECK (status IN (
                    'running', 'paused_approval', 'done', 'failed', 'cancelled')),
    model_id     TEXT NOT NULL DEFAULT '',
    error        TEXT NOT NULL DEFAULT '',
    started_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at  TIMESTAMPTZ,
    FOREIGN KEY (thread_id, workspace_id)
        REFERENCES agent_threads (id, workspace_id) ON DELETE CASCADE,
    UNIQUE (id, workspace_id)
);

-- AT MOST ONE live run per thread, enforced by the database rather than by a
-- read-then-write in Go. This is what makes queueing correct under a race: two
-- concurrent sends both attempt the run insert, exactly one wins, and the
-- loser's message stays 'queued' instead of starting a second interleaved run
-- against the same transcript. 'paused_approval' counts as live — the run is
-- suspended awaiting a human decision, not finished (A4 resumes it).
CREATE UNIQUE INDEX uq_agent_runs_one_active_per_thread ON agent_runs (thread_id)
    WHERE status IN ('running', 'paused_approval');

-- Crash recovery scans only the live rows.
CREATE INDEX idx_agent_runs_running ON agent_runs (started_at) WHERE status = 'running';

CREATE INDEX idx_agent_runs_thread ON agent_runs (workspace_id, thread_id, started_at DESC);
