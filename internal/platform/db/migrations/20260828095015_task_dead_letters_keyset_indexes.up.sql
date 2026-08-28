-- Index the dead-letter triage list for KEYSET paging.
--
-- WHY THIS EXISTS: /dead-letters moved from LIMIT/OFFSET to a keyset cursor
-- (see queries/deadletter.sql and internal/app/deadletter/cursor.go). The seek
-- predicate is a ROW COMPARE — `(created_at, id) < (@cursor_time, @cursor_id)`
-- — and a row compare is only an index range-seek when BOTH members of the sort
-- key sit in the index, in the same order and the same direction as the ORDER
-- BY. The old index stopped at created_at, so the id tiebreak was a heap filter
-- and every page re-read (and re-sorted) the rows it had already returned. That
-- is the very cost keyset paging is adopted to remove, so the index has to move
-- with the query or the change is a no-op with extra steps.
--
-- The tiebreak is not cosmetic here. created_at defaults to now() and a burst of
-- retry exhaustions — one provider outage failing a hundred queued sends inside
-- the same statement_timestamp — lands many rows on the SAME created_at. Without
-- id in the ordering those rows have no stable relative position, so a cursor
-- pointing into the middle of the tie can skip or repeat them.

-- 1. The filtered tabs (status=pending / replayed / discarded).
--
-- Dropped and recreated rather than added alongside: the old three-column index
-- is a strict prefix of this one, so keeping it would be dead weight that every
-- INSERT still pays for. Equality (workspace_id, status) leads, then the sort key
-- (created_at DESC, id DESC), which is the access path exactly.
--
-- NOT CONCURRENTLY, deliberately: golang-migrate's pgx5 driver runs each file in
-- ONE transaction (internal/platform/db/migrate.go sets no x-multi-statement) and
-- CREATE/DROP INDEX CONCURRENTLY cannot run inside a transaction block. Same
-- constraint, and the same reasoning, as 20260827185855_inbox_messages_mailbox_id.
-- The cost is acceptable where it would not be on a hot table: task_dead_letters
-- holds one row per PERMANENTLY failed background task, so it is tens to
-- thousands of rows on a healthy installation, and the build takes ACCESS
-- EXCLUSIVE (drop) then SHARE (create) for the milliseconds that implies. The
-- only writer is the capture path, whose next attempt is a retry-exhausted task
-- that is already minutes old.
DROP INDEX IF EXISTS idx_task_dead_letters_workspace_status_created;
CREATE INDEX idx_task_dead_letters_workspace_status_created
    ON task_dead_letters (workspace_id, status, created_at DESC, id DESC);

-- 2. The unfiltered "All" tab, which the index above cannot serve.
--
-- With no status equality the planner cannot enter the composite index at
-- created_at: status is the second column, and PostgreSQL 16 has no index skip
-- scan (that arrives in 18), so the choices are a full index scan across every
-- status value or a heap scan — and either way a sort. On the default tab. Every
-- page. The two-column index gives the unfiltered list its own contiguous
-- ordered stripe per workspace, so page N seeks straight to the cursor instead of
-- scanning and sorting the workspace's whole history to discard the first N-1
-- pages of it.
--
-- Both indexes are kept because both tabs are real: the operator's default view
-- is unfiltered, and "what did we replay last week" (migration 000069's stated
-- audit question) is the filtered one.
CREATE INDEX idx_task_dead_letters_workspace_created
    ON task_dead_letters (workspace_id, created_at DESC, id DESC);
