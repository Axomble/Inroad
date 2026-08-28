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
