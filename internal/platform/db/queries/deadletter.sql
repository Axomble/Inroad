-- Dead-letter capture and replay (migration 000069). Every statement is
-- workspace-pinned: a dead letter carries a task payload naming a mailbox, an
-- enrollment or a reply body, so a missing pin here would leak one tenant's
-- pending work to another.

-- name: InsertTaskDeadLetter :one
-- Records one retry-exhausted task. Written by the capture path
-- (queue.DeadLetterErrorHandler via coreapi), never by an HTTP caller.
INSERT INTO task_dead_letters (workspace_id, task_type, payload, last_error, attempt_count)
VALUES (@workspace_id, @task_type, @payload, @last_error, @attempt_count)
RETURNING *;

-- name: ListTaskDeadLetters :many
-- The operator's triage list, newest first. @status is the filter and the empty
-- string means "any": passing a sentinel rather than splitting this into two
-- queries keeps one statement (and one index access path) for both cases, and
-- '' can never collide with a real status because the CHECK constraint on the
-- column admits only the three named values.
--
-- Paged by LIMIT/OFFSET rather than a keyset cursor: this list is bounded by how
-- many tasks a workspace has permanently lost, which is a number that should be
-- small enough to read, and an operator paging deep into it has a bigger problem
-- than pagination cost.
SELECT * FROM task_dead_letters
WHERE workspace_id = @workspace_id
  AND (@status::text = '' OR status = @status::text)
ORDER BY created_at DESC, id DESC
LIMIT @row_limit OFFSET @row_offset;

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
