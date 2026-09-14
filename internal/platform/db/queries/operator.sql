-- Queries for `inroadctl status` only. Kept in a file of its own rather than
-- added to worker_routing.sql: the per-IP routing system (workerrouting.go,
-- placement) is actively being worked on elsewhere at the time this was
-- written, and a second, unrelated hand in that file risks a merge collision
-- for no benefit — this query only ever reads the registry, it never joins
-- mailbox_worker_assignments.

-- name: CountWorkers :one
-- Total rows in the global worker registry and how many have heartbeated
-- since @live_since. workers is deployment infrastructure, not tenant data
-- (see migration 000017_worker_routing's table comment), so this is
-- deliberately un-pinned to any workspace.
SELECT
    count(*) AS total,
    count(*) FILTER (WHERE last_seen_at >= @live_since::timestamptz) AS live
FROM workers;
