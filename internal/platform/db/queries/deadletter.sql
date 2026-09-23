-- Dead-letter capture and replay (migration 000069). Every tenant-facing
-- statement is workspace-pinned: a dead letter carries a task payload naming a
-- mailbox, an enrollment or a recipient, so a missing pin here would leak one
-- tenant's pending work to another.
--
-- A payload names WHAT failed; it never carries the content of a message. That
-- is enforced upstream, in internal/platform/queue, and it has teeth here
-- because GET /dead-letters returns the payload verbatim under campaigns:read —
-- an OAuth-grantable scope. (It was not always true: inbox:reply_send carried
-- the operator's reply text, which migration
-- 20260828133405_task_dead_letters_redact_reply_bodies strips from the rows that
-- already exist.)
--
-- The one unpinned statement is the retention purge at the bottom, which is
-- deployment maintenance rather than a tenant read; its own comment says why.

-- name: InsertTaskDeadLetter :one
-- Records one retry-exhausted task. Written by the capture path
-- (queue.DeadLetterErrorHandler via coreapi), never by an HTTP caller.
--
-- @status is passed rather than left to the column default because capture is
-- not always "this is replayable". A terminal inbox:reply_send arriving from a
-- worker that predates the payload fix is stored REDACTED and already
-- 'discarded' (deadletter.Service.Capture), for the same reason migration
-- 20260828133405 flips the historical ones: a body-stripped reply left pending
-- would be replayable, and replaying it delivers a blank message to a real
-- contact. The service is the only caller and it never takes the value from a
-- request.
--
-- @queue is the asynq queue the task was RUNNING on when it died (nullable:
-- see migration 20260911101701). It is read back by replay, which re-enqueues
-- onto it rather than onto whatever the task type routes to today — the only
-- way a replayed warmup:tick returns to the same worker, and therefore the same
-- IP, it was warming from.
INSERT INTO task_dead_letters (workspace_id, task_type, payload, last_error, attempt_count, status, queue)
VALUES (@workspace_id, @task_type, @payload, @last_error, @attempt_count, @status, sqlc.narg(queue))
RETURNING *;

-- name: ListTaskDeadLetters :many
-- The operator's triage list, newest first. @status is the filter and the empty
-- string means "any": passing a sentinel rather than splitting this into two
-- queries keeps one statement for both cases, and '' can never collide with a
-- real status because the CHECK constraint on the column admits only the three
-- named values.
--
-- KEYSET, not LIMIT/OFFSET, and the reason is correctness rather than depth-scan
-- cost. This is a QUEUE THE READER MUTATES: replaying or discarding a row does
-- not delete it, but it DOES move it out of the status-filtered set the operator
-- is paging through. Triage three rows on page one under status=pending and every
-- later row shifts three positions up; OFFSET 50 then starts three rows past
-- where page two begins, and those three are never shown. Silently. A keyset
-- cursor names a ROW rather than a count, so page two resumes exactly where page
-- one stopped no matter what left the set in between.
--
-- The seek is the standard row-compare trick: @seek false yields the first page,
-- true resumes strictly after (@cursor_time, @cursor_id). Comparing the pair as a
-- tuple — not `created_at < x OR (created_at = x AND id < y)` — is what lets the
-- planner range-seek the index directly, and it needs the tiebreak because a
-- burst of retry exhaustions shares one created_at (see the migration).
--
-- The workspace pin is FIRST and unconditional, outside the seek guard: a cursor
-- is caller-supplied, and a tenant filter that any caller-supplied value can
-- switch off is not a tenant filter.
SELECT * FROM task_dead_letters
WHERE workspace_id = @workspace_id
  AND (@status::text = '' OR status = @status::text)
  AND (@seek::bool = false
       OR (created_at, id) < (@cursor_time::timestamptz, @cursor_id::uuid))
ORDER BY created_at DESC, id DESC
LIMIT @page_limit;

-- name: GetTaskDeadLetter :one
SELECT * FROM task_dead_letters
WHERE workspace_id = @workspace_id AND id = @id;

-- name: ClaimTaskDeadLetterReplay :one
-- THE exactly-once guard for replay, and the single most important statement in
-- this domain. The status='pending' predicate makes the flip to 'replayed' a
-- CLAIM: Postgres serialises two concurrent UPDATEs on the same row, so the
-- second one re-evaluates the predicate against the already-committed
-- 'replayed' value, matches nothing, and returns no row (pgx.ErrNoRows).
--
-- The service therefore re-enqueues ONLY when this returns a row, which is why
-- two concurrent replay calls — or a double-clicked button, or a client retry —
-- can enqueue the task at most once. Claim-before-enqueue, not
-- enqueue-then-mark: the opposite order would send first and could then fail to
-- record it, leaving the row replayable again and the mail sent twice. A claim
-- that succeeds but whose enqueue then fails loses the replay instead, which is
-- the safe direction to fail (the row is still visible, and an operator can see
-- it was replayed and act) — see Service.Replay for how that case is reported.
--
-- Returning the whole row (rather than :execrows) hands the caller the payload
-- it must enqueue in the same statement that won the claim, so no second read
-- can observe a row another request mutated in between.
UPDATE task_dead_letters
SET status = 'replayed', replayed_at = now()
WHERE workspace_id = @workspace_id AND id = @id AND status = 'pending'
RETURNING *;

-- name: ReleaseTaskDeadLetterReplay :execrows
-- Compensates a claim whose re-enqueue then failed, returning the row to
-- 'pending' so the operator can try again. Guarded on 'replayed' so it can only
-- ever undo a claim this same request won moments ago, never reopen a replay
-- that genuinely happened and completed.
--
-- This is safe precisely BECAUSE the enqueue failed: nothing was handed to the
-- queue, so returning the row to 'pending' cannot make a delivered task
-- deliverable a second time.
UPDATE task_dead_letters
SET status = 'pending', replayed_at = NULL
WHERE workspace_id = @workspace_id AND id = @id AND status = 'replayed'
  AND replayed_at = @replayed_at;

-- name: DiscardTaskDeadLetter :execrows
-- Files a dead letter as triaged without re-running it. Guarded on 'pending' for
-- the same reason as the replay claim: discarding an already-replayed row would
-- rewrite history to say the task was never re-run.
UPDATE task_dead_letters
SET status = 'discarded'
WHERE workspace_id = @workspace_id AND id = @id AND status = 'pending';

-- name: PurgeTaskDeadLetters :one
-- Bound the table. task_dead_letters is append-only from the application's point
-- of view (triage flips a status, it never deletes) and it was in NO maintenance
-- sweep at all, so it grew forever — one row per permanently failed background
-- task, on a system whose failure modes are provider outages that fail hundreds
-- of queued sends at once.
--
-- Invariant 55's reasoning applies unchanged: warmup_observations was bounded at
-- 90 days because it is append-only and written by events nobody schedules, and
-- this is the same shape. 90 days — INROAD_RETENTION_DEAD_LETTERS_DAYS' default —
-- is comfortably beyond any triage window: a dropped send nobody has looked at in
-- three months is not going to be replayed, and it keeps the audit answer
-- migration 000069 exists for ("what did we re-run last week") intact by a wide
-- margin.
--
-- Every status is eligible, 'pending' included, and that is deliberate: the
-- table's indexes are not partial on 'pending' because terminal rows ARE the
-- audit trail, and the window is what bounds how long that trail is kept. A
-- 'pending' row past the window is work nobody triaged in the whole window.
--
-- One bounded batch of the retention sweep (queries/retention.sql has the shared
-- shape: database clock, SKIP LOCKED for overlapping replicas, a keyset cursor).
-- Oldest-first on idx_task_dead_letters_created.
--
-- Global (no workspace pin), for the same reason PurgeExpiredSecurityArtifacts
-- is: retention is deployment maintenance, not a tenant read. It removes rows by
-- age alone and returns only a count, so it can neither surface nor cross tenant
-- data.
WITH doomed AS (
    SELECT id, created_at
    FROM task_dead_letters
    WHERE created_at < now() - make_interval(secs => sqlc.arg(older_than_seconds)::bigint)
      AND (created_at, id) > (sqlc.arg(after_at)::timestamptz, sqlc.arg(after_id)::uuid)
    ORDER BY created_at, id
    LIMIT sqlc.arg(batch_limit)::int
    FOR UPDATE SKIP LOCKED
),
deleted AS (
    DELETE FROM task_dead_letters t
    USING doomed d
    WHERE t.id = d.id
    RETURNING t.id, t.created_at
),
last_row AS (
    SELECT created_at, id FROM deleted ORDER BY created_at DESC, id DESC LIMIT 1
)
SELECT (SELECT count(*) FROM deleted)::bigint AS deleted_rows,
       COALESCE((SELECT created_at FROM last_row), '0001-01-01T00:00:00Z'::timestamptz)::timestamptz AS last_at,
       COALESCE((SELECT id FROM last_row), '00000000-0000-0000-0000-000000000000'::uuid)::uuid AS last_id;
