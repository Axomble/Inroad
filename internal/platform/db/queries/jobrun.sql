-- Scheduled-job run ledger (migration 20260908123022_scheduled_job_runs).
-- scheduled_job_runs carries no workspace_id at all — see the migration's own
-- comment for why these rows are instance-scoped, not tenant-scoped — so
-- neither statement below touches a tenant-scoped table and neither needs (or
-- may correctly bear) a workspace_id predicate. The tenancy guard in
-- db/tenancyqueries_test.go confirms that empirically; it needs no allowlist
-- entry here because it never sees this file's queries as touching tenant
-- data in the first place.

-- name: InsertScheduledJobRun :exec
-- Written once per periodic reconcile invocation by
-- internal/platform/jobrun's Record decorator, through the coreapi
-- in-process client (internal/coreapi/inprocess/jobrun.go). See jobrun.
-- Record's own doc for why this write is best-effort: a failure here is
-- logged and swallowed by the CALLER, never allowed to fail the sweep it is
-- only trying to observe.
INSERT INTO scheduled_job_runs (job_name, started_at, finished_at, duration_ms, outcome, error_message)
VALUES (@job_name, @started_at, @finished_at, @duration_ms, @outcome, @error_message);

-- name: PurgeScheduledJobRuns :one
-- Retention sweep, same shape as PurgeTaskDeadLetters / PurgeWebhookDeliveries
-- (queries/deadletter.sql, queries/webhook.sql): batched at 5000 rows,
-- deleted oldest-first by the column idx_scheduled_job_runs_started leads
-- with (so repeated sweeps make monotonic progress rather than re-reading the
-- same head of the table), global (no workspace pin — deployment
-- maintenance, not a tenant read), and it returns only a count.
--
-- Six jobs recording a row per run, several every five minutes, make this
-- table unbounded without a sweep for exactly the reason task_dead_letters
-- and webhook_deliveries needed one (invariant 55's reasoning): nothing else
-- in the codebase ever deletes from it. 30 days comfortably outlives any
-- "did last night's domain-auth sweep run" investigation, and matches
-- webhook_deliveries' retention rather than task_dead_letters' longer 90
-- days — this is routine operational telemetry, not the record of a dropped
-- send that might still be worth triaging months later.
WITH deleted AS (
    DELETE FROM scheduled_job_runs
    WHERE id IN (
        SELECT id FROM scheduled_job_runs
        WHERE started_at < now() - interval '30 days'
        ORDER BY started_at LIMIT 5000
    )
    RETURNING 1
)
SELECT count(*)::bigint AS deleted_rows FROM deleted;
