-- Configurable retention for the recipient-identifying tables (migration
-- 20260923110315). Each query is ONE bounded batch; the loop, the per-run budget,
-- the persisted cursor and the enable/disable decision live in
-- internal/worker/maintenance/retention.go.
--
-- Shared shape, and why:
--
--   * The window arrives as seconds and the cutoff is computed from the
--     DATABASE's now(), never from a worker clock — replicas with skewed clocks
--     must agree on what "older than 400 days" means.
--   * A batch is bounded by the rows it SCANS, not the rows it deletes. The
--     guarded tables first take the next `batch_limit` rows past the cursor in
--     (age, id) order — an index range scan of fixed length — and only then apply
--     the guards to those candidates. The cursor returned is the last row
--     SCANNED, kept or not, so a run never re-reads a guard-kept row, and a batch
--     whose candidates are all kept still costs one bounded scan instead of
--     walking every kept row in the table looking for a deletable one. Bounding
--     deletes alone was the earlier shape, and on a table whose oldest million
--     rows are all kept it made a single statement read all of them.
--   * Each guard is a NOT EXISTS ... OFFSET 0. The OFFSET 0 is load-bearing: it
--     stops the planner pulling the sublink up into an anti-join, which it would
--     otherwise happily do as a HASH anti-join over the entire guard table (every
--     inbox thread, every enrollment) once per batch. As a SubPlan each guard is
--     one index probe per candidate, which is bounded by the batch size.
--   * Candidates are locked FOR UPDATE SKIP LOCKED, so a row the live path holds
--     (a send mid-claim) is skipped rather than waited on. The sweep itself is
--     single-instance across replicas (TryRetentionSweepLock).
--   * The zero cursor (year 1, the nil UUID) is before every row.
--   * Global (no workspace pin), for the same reason as every purge in
--     maintenance.sql: retention is deployment maintenance, not a tenant read.
--     Each query deletes by age and guard alone and returns only counts and a
--     cursor, so it can neither surface nor cross tenant data. Every join a
--     guard makes is pinned on workspace_id on both sides.
--
-- The caller runs each of these inside a transaction with SET LOCAL
-- statement_timeout (inprocess/retention.go), so no batch can outlive its
-- budget however the plan turns out.

-- name: RollupTrackingEvents :one
-- Fold raw tracking events past the window into tracking_event_rollups, then
-- they are gone. See the migration for what the rollup keeps and what it drops.
--
-- The rollup is fed from the DELETE's own RETURNING, in the same statement,
-- which is what makes it exactly-once: a row is counted if and only if THIS
-- statement deleted it. A failed statement rolls back its delete and its
-- increment together. Reading the rows first and deleting them second, in two
-- statements, would double-count on any retry between the two.
--
-- Scanned and deleted are the same rows here — there is no guard — so the
-- LIMIT bounds both.
--
-- Grouped by (send_id, kind, is_machine) — the conflict key — rather than by
-- every carried column, because an INSERT whose SELECT produces two rows with
-- one conflict key fails outright ("ON CONFLICT DO UPDATE command cannot affect
-- row a second time") and would wedge the sweep on that batch forever.
-- workspace_id is determined by send_id (the tenant FK to sends); campaign_id
-- should be too, and taking one arbitrary value costs nothing if it ever is not.
WITH doomed AS (
    SELECT te.id, te.created_at
    FROM tracking_events te
    WHERE te.created_at < now() - make_interval(secs => sqlc.arg(older_than_seconds)::bigint)
      AND (te.created_at, te.id) > (sqlc.arg(after_at)::timestamptz, sqlc.arg(after_id)::uuid)
    ORDER BY te.created_at, te.id
    LIMIT sqlc.arg(batch_limit)::int
    FOR UPDATE SKIP LOCKED
),
deleted AS (
    DELETE FROM tracking_events te
    USING doomed d
    WHERE te.id = d.id AND te.created_at = d.created_at
    RETURNING te.id, te.workspace_id, te.campaign_id, te.send_id, te.kind, te.is_machine, te.created_at
),
rolled AS (
    INSERT INTO tracking_event_rollups AS r
        (workspace_id, campaign_id, send_id, kind, is_machine, events, first_at, last_at)
    SELECT (array_agg(workspace_id))[1], (array_agg(campaign_id))[1], send_id, kind, is_machine,
           count(*), min(created_at), max(created_at)
    FROM deleted
    GROUP BY send_id, kind, is_machine
    ON CONFLICT (send_id, kind, is_machine) DO UPDATE
    SET events   = r.events + EXCLUDED.events,
        first_at = LEAST(r.first_at, EXCLUDED.first_at),
        last_at  = GREATEST(r.last_at, EXCLUDED.last_at)
    RETURNING 1
),
last_row AS (
    SELECT created_at, id FROM deleted ORDER BY created_at DESC, id DESC LIMIT 1
)
SELECT (SELECT count(*) FROM deleted)::bigint AS deleted_rows,
       (SELECT count(*) FROM rolled)::bigint AS rollup_rows,
       COALESCE((SELECT created_at FROM last_row), '0001-01-01T00:00:00Z'::timestamptz)::timestamptz AS last_at,
       COALESCE((SELECT id FROM last_row), '00000000-0000-0000-0000-000000000000'::uuid)::uuid AS last_id;

-- name: PurgeDeliverabilityEvents :one
-- Delete provider bounce/complaint events past the window, EXCEPT the two kinds
-- of row a rate still reads.
--
-- (1) An event inside a running OR PAUSED campaign's breaker window. The
--     breaker's rolling window is 7 days, but when a campaign has too few recent
--     sends it falls back to "everything since supervision began" —
--     campaigns.guardrails_enabled_at (internal/app/deliverability/service.go
--     assessCampaign). That fallback is unbounded in age, so no fixed floor can
--     protect it; this guard does. Paused counts as running because a paused
--     campaign is one resume away from being assessed on exactly this history:
--     its stopped-as-bounced enrollments survive (they are not in any sweep), so
--     deleting the events and sends beside them while it was paused would
--     leave a numerator with a shrunken denominator and trip the breaker the
--     moment it resumed. The guard keeps a superset of what the breaker reads —
--     its floor is the LATER of enabled_at and the last operator override.
--     Warmup health (30 days) and the workspace rollup (7 days) are fixed
--     windows, protected by the 90-day floor RetentionPolicy enforces.
--
-- (2) The workspace's newest complaint. GetCampaignDeliverabilityCounts and
--     GetWorkspaceDeliverabilityCounts report complaint_feed = "has a complaint
--     feed EVER reported here", which separates measured-and-clean from NOT
--     MEASURED. Deleting a workspace's last complaint would flip a live feed to
--     "not measured". Keeping one row preserves that answer; (received_at, id)
--     makes "newest" total.
--
-- The dedup key (workspace_id, provider_event_id) goes with the row: a provider
-- replaying an event after it was deleted would be ingested again, at its NEW
-- received_at. Provider retry windows are hours to days; the 90-day floor makes
-- that unreachable in practice.
WITH scanned AS MATERIALIZED (
    SELECT d.id, d.received_at
    FROM deliverability_events d
    WHERE d.received_at < now() - make_interval(secs => sqlc.arg(older_than_seconds)::bigint)
      AND (d.received_at, d.id) > (sqlc.arg(after_at)::timestamptz, sqlc.arg(after_id)::uuid)
    ORDER BY d.received_at, d.id
    LIMIT sqlc.arg(batch_limit)::int
),
doomed AS (
    SELECT d.id
    FROM deliverability_events d
    JOIN scanned c ON c.id = d.id
    WHERE NOT EXISTS (
          SELECT 1
          FROM sends s
          JOIN campaigns cam ON cam.id = s.campaign_id AND cam.workspace_id = s.workspace_id
          WHERE s.id = d.send_id AND s.workspace_id = d.workspace_id
            AND cam.status IN ('running', 'paused')
            AND d.received_at >= cam.guardrails_enabled_at
          OFFSET 0
      )
      AND NOT (
          d.kind = 'complaint'
          AND NOT EXISTS (
              SELECT 1 FROM deliverability_events n
              WHERE n.workspace_id = d.workspace_id AND n.kind = 'complaint'
                AND (n.received_at, n.id) > (d.received_at, d.id)
              OFFSET 0
          )
      )
    FOR UPDATE OF d SKIP LOCKED
),
deleted AS (
    DELETE FROM deliverability_events d
    USING doomed x
    WHERE d.id = x.id
    RETURNING 1
),
last_scanned AS (
    SELECT received_at, id FROM scanned ORDER BY received_at DESC, id DESC LIMIT 1
)
SELECT (SELECT count(*) FROM scanned)::bigint AS scanned_rows,
       (SELECT count(*) FROM deleted)::bigint AS deleted_rows,
       COALESCE((SELECT received_at FROM last_scanned), '0001-01-01T00:00:00Z'::timestamptz)::timestamptz AS last_at,
       COALESCE((SELECT id FROM last_scanned), '00000000-0000-0000-0000-000000000000'::uuid)::uuid AS last_id;

-- name: PurgeInboxThreads :one
-- Delete whole inbox conversations with no activity inside the window, messages
-- and all.
--
-- The unit is the THREAD, not the message. Deleting old messages one by one
-- would leave a conversation with its opening missing and its later replies
-- intact, which an operator would read as corruption, and the thread's
-- synthesized outbound leg (from sends) would no longer line up with the
-- replies to it. A thread is eligible when its last_message_at — bumped by every
-- inbound reply and manual send — is past the window, i.e. the whole
-- conversation is.
--
-- Kept regardless of age:
--   * a thread with a manual reply still scheduled or sending — the operator's
--     own queued words; deleting the thread would cascade them away unsent;
--   * a thread snoozed into the future — the operator asked to be shown it again;
--   * a thread whose campaign enrollment is still active — the sequence is still
--     writing to this conversation (its steps are its outbound leg);
--   * a thread with any message inside the window, belt and braces for a writer
--     that did not bump last_message_at.
-- Labels, snoozes and finished pending replies cascade with the thread (the
-- pending-reply cascade seeks idx_inbox_pending_replies_thread).
--
-- The messages are deleted explicitly, in their own CTE, rather than left to the
-- thread FK's ON DELETE CASCADE, so the count is real. The cascade then finds
-- nothing left to do. A thread's messages come with it whatever their number,
-- which is what keeps a conversation whole.
WITH scanned AS MATERIALIZED (
    SELECT t.id, t.last_message_at
    FROM inbox_threads t
    WHERE t.last_message_at < now() - make_interval(secs => sqlc.arg(older_than_seconds)::bigint)
      AND (t.last_message_at, t.id) > (sqlc.arg(after_at)::timestamptz, sqlc.arg(after_id)::uuid)
    ORDER BY t.last_message_at, t.id
    LIMIT sqlc.arg(batch_limit)::int
),
doomed AS (
    SELECT t.id
    FROM inbox_threads t
    JOIN scanned c ON c.id = t.id
    WHERE NOT EXISTS (
          SELECT 1 FROM inbox_pending_replies p
          WHERE p.thread_id = t.id AND p.workspace_id = t.workspace_id
            AND p.status IN ('scheduled', 'sending')
          OFFSET 0
      )
      AND NOT EXISTS (
          SELECT 1 FROM inbox_thread_snoozes z
          WHERE z.thread_id = t.id AND z.workspace_id = t.workspace_id
            AND z.snooze_until > now()
          OFFSET 0
      )
      AND NOT EXISTS (
          SELECT 1 FROM sequence_enrollments e
          WHERE e.campaign_id = t.campaign_id AND e.contact_id = t.contact_id
            AND e.workspace_id = t.workspace_id AND e.status = 'active'
          OFFSET 0
      )
      AND NOT EXISTS (
          SELECT 1 FROM inbox_messages m
          WHERE m.thread_id = t.id AND m.workspace_id = t.workspace_id
            AND m.occurred_at >= now() - make_interval(secs => sqlc.arg(older_than_seconds)::bigint)
          OFFSET 0
      )
    FOR UPDATE OF t SKIP LOCKED
),
deleted_messages AS (
    DELETE FROM inbox_messages m
    USING doomed d
    WHERE m.thread_id = d.id
    RETURNING 1
),
deleted_threads AS (
    DELETE FROM inbox_threads t
    USING doomed d
    WHERE t.id = d.id
    RETURNING 1
),
last_scanned AS (
    SELECT last_message_at, id FROM scanned ORDER BY last_message_at DESC, id DESC LIMIT 1
)
SELECT (SELECT count(*) FROM scanned)::bigint AS scanned_rows,
       (SELECT count(*) FROM deleted_threads)::bigint AS deleted_rows,
       (SELECT count(*) FROM deleted_messages)::bigint AS deleted_messages,
       COALESCE((SELECT last_message_at FROM last_scanned), '0001-01-01T00:00:00Z'::timestamptz)::timestamptz AS last_at,
       COALESCE((SELECT id FROM last_scanned), '00000000-0000-0000-0000-000000000000'::uuid)::uuid AS last_id;

-- name: PurgeSends :one
-- Delete campaign sends past the window that nothing live still depends on.
--
-- Deleting a send removes it from EVERY report — sent counts, per-step and
-- per-variant results, outcome attribution, the deliverability series — and
-- with it its tracking events and rollups, so a rate's numerator and
-- denominator shrink together rather than the rate drifting. That is what
-- "retention" means for this table, and it is why it is off unless an operator
-- turns it on.
--
-- Kept regardless of age, each guard naming the reader it protects:
--   * status queued/sending — work in flight; the row IS the send claim
--     (stepsend.sql ClaimStepSend);
--   * an ACTIVE enrollment for (campaign, contact) — the sequence engine threads
--     its next step off earlier sends (LatestSentForContact: In-Reply-To and
--     References), branches on their engagement, and stops on a reply matched
--     through sends.message_id (GetSendByMessageID);
--   * a RUNNING OR PAUSED campaign's send on or after guardrails_enabled_at —
--     the breaker's fallback denominator (see PurgeDeliverabilityEvents for why
--     paused counts): a campaign that can still run keeps its history;
--   * an inbox thread for (campaign, contact) — the thread's outbound leg is
--     synthesized from these rows (ListSentOutboundStepsForThread) and its
--     "who spoke last" rule reads them (inbox_thread_awaiting_reply). The thread
--     has its own retention; once it is gone the send is free;
--   * a deliverability event naming it — the FK is ON DELETE SET NULL, which
--     would silently strip the event of its campaign and mailbox attribution
--     while the event itself is still retained;
--   * a CRM deal sourced from (campaign, contact) — the deal's activity feed
--     lists the campaign's sent messages (crm integration_store ListEvents).
--
-- NOT a guard, deliberately: suppression. Nothing in suppression references a
-- send, and an unsubscribe token carries (workspace, email), not a send id, so
-- old unsubscribe links keep working. What does stop working is an old OPEN
-- PIXEL or CLICK LINK — the tracking endpoint returns 404 for a send that is
-- gone — and matching a late reply or DSN back to the campaign, which goes
-- through sends.message_id.
--
-- The tracking events and rollups are deleted explicitly, before the FK cascade
-- would, so the count reported is real. Those deletes take no SKIP LOCKED: a
-- concurrent RollupTrackingEvents holding those rows is excluded by the sweep's
-- single-instance lock, not by this statement.
WITH scanned AS MATERIALIZED (
    SELECT s.id, s.created_at
    FROM sends s
    WHERE s.created_at < now() - make_interval(secs => sqlc.arg(older_than_seconds)::bigint)
      AND (s.created_at, s.id) > (sqlc.arg(after_at)::timestamptz, sqlc.arg(after_id)::uuid)
    ORDER BY s.created_at, s.id
    LIMIT sqlc.arg(batch_limit)::int
),
doomed AS (
    SELECT s.id
    FROM sends s
    JOIN scanned c ON c.id = s.id
    WHERE (s.sent_at IS NULL OR s.sent_at < now() - make_interval(secs => sqlc.arg(older_than_seconds)::bigint))
      AND s.status IN ('sent', 'failed', 'skipped')
      AND NOT EXISTS (
          SELECT 1 FROM sequence_enrollments e
          WHERE e.campaign_id = s.campaign_id AND e.contact_id = s.contact_id
            AND e.workspace_id = s.workspace_id AND e.status = 'active'
          OFFSET 0
      )
      AND NOT EXISTS (
          SELECT 1 FROM campaigns cam
          WHERE cam.id = s.campaign_id AND cam.workspace_id = s.workspace_id
            AND cam.status IN ('running', 'paused')
            AND s.sent_at >= cam.guardrails_enabled_at
          OFFSET 0
      )
      AND NOT EXISTS (
          SELECT 1 FROM inbox_threads t
          WHERE t.workspace_id = s.workspace_id AND t.campaign_id = s.campaign_id
            AND t.contact_id = s.contact_id
          OFFSET 0
      )
      AND NOT EXISTS (
          SELECT 1 FROM deliverability_events d
          WHERE d.send_id = s.id AND d.workspace_id = s.workspace_id
          OFFSET 0
      )
      AND NOT EXISTS (
          SELECT 1 FROM deals dl
          WHERE dl.workspace_id = s.workspace_id AND dl.primary_contact_id = s.contact_id
            AND dl.source_campaign_id = s.campaign_id
          OFFSET 0
      )
    FOR UPDATE OF s SKIP LOCKED
),
deleted_tracking AS (
    DELETE FROM tracking_events te
    USING doomed d
    WHERE te.send_id = d.id
    RETURNING 1
),
deleted_rollups AS (
    DELETE FROM tracking_event_rollups r
    USING doomed d
    WHERE r.send_id = d.id
    RETURNING 1
),
deleted_sends AS (
    DELETE FROM sends s
    USING doomed d
    WHERE s.id = d.id
    RETURNING 1
),
last_scanned AS (
    SELECT created_at, id FROM scanned ORDER BY created_at DESC, id DESC LIMIT 1
)
SELECT (SELECT count(*) FROM scanned)::bigint AS scanned_rows,
       (SELECT count(*) FROM deleted_sends)::bigint AS deleted_rows,
       ((SELECT count(*) FROM deleted_tracking) + (SELECT count(*) FROM deleted_rollups))::bigint AS deleted_tracking,
       COALESCE((SELECT created_at FROM last_scanned), '0001-01-01T00:00:00Z'::timestamptz)::timestamptz AS last_at,
       COALESCE((SELECT id FROM last_scanned), '00000000-0000-0000-0000-000000000000'::uuid)::uuid AS last_id;

-- name: GetRetentionCursor :one
-- Where a table's sweep got to. cycle_expired is computed on the database clock
-- so the "once a day, start over" rule does not depend on which worker asks.
SELECT after_at, after_id, (cycle_started_at < now() - interval '24 hours')::boolean AS cycle_expired
FROM retention_cursors
WHERE table_name = @table_name;

-- name: SaveRetentionCursor :exec
-- Record a table's position. new_cycle restarts the day's clock: the sweep sets
-- it when it starts over from the oldest row (the table drained, or the cycle
-- expired), so "a row a guard stopped protecting is revisited within a day"
-- holds however large the backlog.
INSERT INTO retention_cursors (table_name, after_at, after_id)
VALUES (@table_name, @after_at, @after_id)
ON CONFLICT (table_name) DO UPDATE
SET after_at         = EXCLUDED.after_at,
    after_id         = EXCLUDED.after_id,
    cycle_started_at = CASE WHEN sqlc.arg(new_cycle)::boolean THEN now() ELSE retention_cursors.cycle_started_at END,
    updated_at       = now();

-- name: TryRetentionSweepLock :one
-- The single-sweeper lock. Session-level, taken on a connection the sweep holds
-- for its whole run, so exactly one retention run proceeds deployment-wide
-- however many replicas run the scheduler. Two concurrent runs were safe row by
-- row (SKIP LOCKED) but not table by table: PurgeSends deletes tracking rows
-- that a concurrent RollupTrackingEvents holds, while the rollup's insert needs
-- a KEY SHARE on the very send PurgeSends has locked — a deadlock Postgres would
-- resolve by aborting one of them every time they met. hashtext() turns the
-- name into the lock key so it is greppable rather than a magic number.
SELECT pg_try_advisory_lock(hashtext('inroad:maintenance:retention'))::boolean AS acquired;

-- name: ReleaseRetentionSweepLock :one
SELECT pg_advisory_unlock(hashtext('inroad:maintenance:retention'))::boolean AS released;
