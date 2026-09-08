-- Per-run ledger for the six periodic reconciles cmd/worker/scheduler.go
-- registers (see sweepRegistrars(): enrollments, inbox sweep, warmup sweep,
-- maintenance cleanup, domain auth sweep, recipient esp sweep).
--
-- WHY THIS EXISTS: before this table, "is the domain-auth sweep actually
-- running" was answerable only by grepping worker logs.
-- internal/platform/metrics.SweepCompleted exists, but only three of the six
-- sweeps call it (the enrollment, inbox and warmup scans), and even where it
-- IS called a Prometheus counter does not survive a scrape gap and carries no
-- error message — exactly what an operator needs when a sweep silently stops
-- running, or starts failing every tick. This table is written by
-- internal/platform/jobrun's decorator (jobrun.Record), wrapped around all
-- six handlers in internal/worker/handlers.go, so it is a durable, queryable
-- fact rather than a metric or a log line.
--
-- Deliberately INSTANCE-scoped, not workspace-scoped: a periodic reconcile
-- runs once per deployment, not once per tenant, so there is no workspace_id
-- to carry (this table is intentionally absent from every workspace-pinned
-- query the tenancy guard in db/tenancyqueries_test.go checks). The read
-- surface — who may query "is this sweep healthy" — belongs on the
-- instance-health dashboard (parity-plan P2.7), which is also where a
-- run-now trigger belongs; this migration adds neither, on purpose (see the
-- task brief's scope boundary).
CREATE TABLE scheduled_job_runs (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    -- The sweep's name from sweepRegistrars() (e.g. "domain auth sweep"),
    -- NOT the asynq task type ("domainauth:sweep") — so a row reads directly
    -- against that registrar list, which is the whole point of this table.
    -- Free text, like task_dead_letters.task_type: the vocabulary lives in
    -- Go constants (jobrun.NameXxx) and a CHECK here would turn "we shipped
    -- a seventh sweep" into "the ledger silently stops recording it".
    job_name      TEXT NOT NULL,
    started_at    TIMESTAMPTZ NOT NULL,
    finished_at   TIMESTAMPTZ NOT NULL,
    -- Stored explicitly rather than derived at read time from
    -- finished_at - started_at: the decorator already measured it with a
    -- single time.Since() against one monotonic clock reading, which is
    -- immune to a wall-clock adjustment mid-run in a way that subtracting
    -- two stored TIMESTAMPTZ columns afterwards would not be.
    duration_ms   BIGINT NOT NULL CHECK (duration_ms >= 0),
    -- 'error' covers both a returned error AND a recovered panic — see
    -- jobrun.Record's doc for why a panic must still land a row here rather
    -- than being swallowed.
    outcome       TEXT NOT NULL CHECK (outcome IN ('ok', 'error')),
    -- '' for an 'ok' run; the error (or "panic: ...") message otherwise.
    -- Plain text like task_dead_letters.last_error: operator-facing
    -- diagnostics about a background job.
    --
    -- It is NOT guaranteed to be free of tenant content, and an earlier
    -- version of this comment claiming so was wrong: the writer
    -- (internal/platform/jobrun.Record) stores err.Error() from six handlers
    -- it does not own, and any one of them wrapping a mailbox address or a
    -- recipient would make that claim false. What IS guaranteed is a bound --
    -- jobrun.capErrorMessage caps the value at 2 KiB on a rune boundary --
    -- because an unbounded column reachable by arbitrary error text, in a
    -- table with no workspace_id and no per-tenant read path, is a problem
    -- whichever way the content question is answered. The 30-day retention
    -- purge (PurgeScheduledJobRuns) is the other half.
    error_message TEXT NOT NULL DEFAULT ''
);

-- The retention purge (queries/jobrun.sql:PurgeScheduledJobRuns, wired into
-- the existing maintenance cleanup path) filters on started_at alone, so it
-- needs an index that LEADS with started_at to seek rather than seq-scan —
-- exactly the reasoning migration 20260828152300_task_dead_letters_retention_
-- index gives for task_dead_letters, which this table's growth shape matches
-- closely: six jobs, several on a 5-minute cadence, so it accumulates rows
-- far faster than a table someone remembers to prune by hand.
--
-- Created directly (no IF NOT EXISTS): the table above is brand new in this
-- same migration, so there is no pre-existing index this could collide with,
-- unlike the retrofit case the precedent comment describes. Not CONCURRENTLY
-- for the same reason as that precedent: golang-migrate runs one file in one
-- transaction, and CREATE INDEX CONCURRENTLY cannot run inside one.
CREATE INDEX idx_scheduled_job_runs_started
    ON scheduled_job_runs (started_at);
