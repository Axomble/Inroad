-- Agent chat persistence (agent-platform spec §2, PR A2). Every statement is
-- workspace-pinned as its first bound argument (security invariant 4); thread
-- reads additionally pin created_by_user_id, because a thread is visible only
-- to the user who created it within the workspace (spec §7.7).

-- name: InsertAgentThread :one
INSERT INTO agent_threads (workspace_id, created_by_user_id, title)
VALUES ($1, $2, $3)
RETURNING *;

-- name: GetAgentThread :one
-- Owner-scoped: another user's thread in the same workspace reads zero rows,
-- so the service 404s rather than leaking its existence.
SELECT * FROM agent_threads
WHERE workspace_id = $1 AND created_by_user_id = $2 AND id = $3 AND deleted_at IS NULL;

-- name: ListAgentThreads :many
-- Newest activity first, matching idx_agent_threads_owner's ordering so the
-- list is an index-only range scan.
SELECT * FROM agent_threads
WHERE workspace_id = $1 AND created_by_user_id = $2 AND deleted_at IS NULL
ORDER BY updated_at DESC, id DESC
LIMIT $4 OFFSET $3;

-- name: RenameAgentThread :one
UPDATE agent_threads
SET title = $4, updated_at = now()
WHERE workspace_id = $1 AND created_by_user_id = $2 AND id = $3 AND deleted_at IS NULL
RETURNING *;

-- name: SoftDeleteAgentThread :execrows
-- Soft delete: the transcript stays for audit while disappearing from every
-- read path (all of which filter deleted_at IS NULL).
UPDATE agent_threads
SET deleted_at = now(), updated_at = now(), active_run_id = NULL
WHERE workspace_id = $1 AND created_by_user_id = $2 AND id = $3 AND deleted_at IS NULL;

-- name: SetAgentThreadTitle :execrows
-- Generated titles never overwrite a user's rename: the WHERE clause requires
-- the title to still be empty, so a rename that landed while the fast model
-- was thinking wins.
UPDATE agent_threads
SET title = $3, updated_at = now()
WHERE workspace_id = $1 AND id = $2 AND title = '' AND deleted_at IS NULL;

-- name: AddAgentThreadUsage :exec
-- Accumulates one run's token usage and records the context window of the
-- model it ran on. OutputTokens excludes reasoning for every provider
-- (ai.Usage normalization contract), so these totals sum provider-agnostically.
UPDATE agent_threads
SET total_input_tokens = total_input_tokens + $3,
    total_output_tokens = total_output_tokens + $4,
    context_window_tokens = $5,
    updated_at = now()
WHERE workspace_id = $1 AND id = $2;

-- name: SetAgentThreadActiveRun :exec
UPDATE agent_threads
SET active_run_id = $3, updated_at = now()
WHERE workspace_id = $1 AND id = $2;

-- name: InsertAgentMessage :one
-- The thread reference is self-enforcing: the INSERT ... SELECT emits zero
-- rows (pgx.ErrNoRows) when the thread is not this user's within this
-- workspace, so a foreign thread id can never grow a message.
INSERT INTO agent_messages (workspace_id, thread_id, turn_id, role, status, browsing_context)
SELECT $1, t.id, $3, $4, $5, $6
FROM agent_threads t
WHERE t.id = $2 AND t.workspace_id = $1 AND t.deleted_at IS NULL
RETURNING *;

-- name: ListAgentMessages :many
SELECT * FROM agent_messages
WHERE workspace_id = $1 AND thread_id = $2
ORDER BY created_at, id;

-- name: ListAgentTranscriptMessages :many
-- The transcript the runtime replays to the provider: everything already
-- answered or being answered now. A 'queued' message is NOT part of the
-- prompt — it starts its own run once this one ends.
SELECT * FROM agent_messages
WHERE workspace_id = $1 AND thread_id = $2 AND status <> 'queued'
ORDER BY created_at, id;

-- name: ListAgentMessagePartsByThread :many
-- All parts of a thread in one round trip, keyed by message so the caller can
-- fan them back out without an N+1.
SELECT p.* FROM agent_message_parts p
JOIN agent_messages m ON m.id = p.message_id AND m.workspace_id = p.workspace_id
WHERE p.workspace_id = $1 AND m.thread_id = $2
ORDER BY m.created_at, m.id, p.order_index;

-- name: ListQueuedAgentMessages :many
SELECT * FROM agent_messages
WHERE workspace_id = $1 AND thread_id = $2 AND status = 'queued'
ORDER BY created_at, id;

-- name: PromoteOldestQueuedAgentMessage :one
-- Promotes exactly one message into the run that is about to answer it. The
-- scalar subquery pins the same workspace/thread, so the promotion can never
-- reach across tenants; zero rows (pgx.ErrNoRows) means the queue was empty.
UPDATE agent_messages AS m
SET status = 'processing'
WHERE m.workspace_id = $1 AND m.thread_id = $2 AND m.id = (
    SELECT oldest.id FROM agent_messages AS oldest
    WHERE oldest.workspace_id = $1 AND oldest.thread_id = $2 AND oldest.status = 'queued'
    ORDER BY oldest.created_at, oldest.id
    LIMIT 1
)
RETURNING *;

-- name: FinishProcessingAgentMessages :execrows
-- Terminal transition for the messages a finished run answered.
UPDATE agent_messages
SET status = 'sent', processed_at = now()
WHERE workspace_id = $1 AND thread_id = $2 AND status = 'processing';

-- name: DeleteQueuedAgentMessage :execrows
-- Only a message still waiting its turn can be withdrawn; one already being
-- answered is part of the transcript.
DELETE FROM agent_messages
WHERE workspace_id = $1 AND thread_id = $2 AND id = $3 AND status = 'queued';

-- name: InsertAgentMessagePart :one
-- Self-enforcing tenancy again: an unknown or foreign message id emits zero
-- rows rather than relying on the caller having checked.
INSERT INTO agent_message_parts (
    workspace_id, message_id, order_index, type,
    text_content, reasoning_content, tool_name, tool_call_id,
    tool_input, tool_output, state, error_message)
SELECT $1, m.id, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12
FROM agent_messages m
WHERE m.id = $2 AND m.workspace_id = $1
RETURNING *;

-- name: InsertAgentRun :one
-- Starts a run. The partial unique index uq_agent_runs_one_active_per_thread
-- makes "at most one live run per thread" a database guarantee: a concurrent
-- second send raises 23505 and its message stays queued, instead of two runs
-- interleaving against one transcript.
INSERT INTO agent_runs (workspace_id, thread_id, model_id)
SELECT $1, t.id, $3
FROM agent_threads t
WHERE t.id = $2 AND t.workspace_id = $1 AND t.deleted_at IS NULL
RETURNING *;

-- name: GetAgentRun :one
SELECT * FROM agent_runs WHERE workspace_id = $1 AND id = $2;

-- name: GetActiveAgentRun :one
-- The authoritative "is this thread busy" read (agent_threads.active_run_id is
-- only a denormalized convenience).
SELECT * FROM agent_runs
WHERE workspace_id = $1 AND thread_id = $2 AND status IN ('running', 'paused_approval')
ORDER BY started_at DESC
LIMIT 1;

-- name: SetAgentRunModel :exec
UPDATE agent_runs SET model_id = $3 WHERE workspace_id = $1 AND id = $2;

-- name: FinishAgentRun :execrows
-- Terminal transition. Guarded on the run still being live so a cancellation
-- racing a natural completion cannot overwrite the outcome that already
-- landed — first writer wins, and the loser sees zero rows.
UPDATE agent_runs
SET status = $3, error = $4, finished_at = now()
WHERE workspace_id = $1 AND id = $2 AND status IN ('running', 'paused_approval');

-- name: PauseAgentRunForApproval :execrows
-- The A4 seam's persistence half: the run stays LIVE (the partial unique index
-- still counts it) so no new run can start on the thread while a human decides.
UPDATE agent_runs
SET status = 'paused_approval'
WHERE workspace_id = $1 AND id = $2 AND status = 'running';

-- name: FailStuckAgentRuns :execrows
-- Crash recovery, run once at API startup. Runs execute as goroutines inside
-- this binary, so a row still 'running' at boot belongs to a process that is
-- gone and can never finish. Deployment-scoped infrastructure repair, not a
-- tenant read — hence no workspace filter.
UPDATE agent_runs
SET status = 'failed', error = $1, finished_at = now()
WHERE status = 'running';

-- name: ResetStuckAgentMessages :execrows
-- Companion to FailStuckAgentRuns: a message left 'processing' by a crashed
-- run is marked terminal rather than re-queued. Auto-restarting a model call
-- (with its token cost and its side effects) at boot, without anyone asking,
-- is the wrong default — the user resends.
UPDATE agent_messages
SET status = 'sent', processed_at = now()
WHERE status = 'processing';
