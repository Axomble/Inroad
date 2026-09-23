-- Configurable retention for the five recipient-identifying tables — sends,
-- tracking_events, deliverability_events, inbox_threads and inbox_messages —
-- under four windows (a thread and its messages share one). The sweep is
-- maintenance:retention
-- (internal/worker/maintenance/retention.go); its windows are
-- INROAD_RETENTION_*_DAYS, and the recipient-data ones default to DISABLED —
-- how long to keep data about the people a workspace emails is a Privacy/Legal
-- decision, not a code default (docs: deploy/environment-variables).
--
-- This file holds ONLY what is instant: two new, empty tables and a view. The
-- age indexes the sweep seeks on are each in a file of their own
-- (20260923144743..20260923144749), built CONCURRENTLY, so no write to sends or
-- tracking_events waits on an index build; 20260923144750 tunes autovacuum.
--
-- LOCKING, stated exactly because it was stated wrongly before. golang-migrate
-- sends this whole file as one simple-protocol Exec, which Postgres runs as ONE
-- implicit transaction. Creating tracking_event_rollups' foreign keys takes SHARE
-- ROW EXCLUSIVE on sends, campaigns and workspaces, and that lock is held until
-- this file's transaction commits. Nothing else in the file scans or builds
-- anything — the new table is empty, the view is catalog-only — so the window is
-- milliseconds. That is also why no index on a populated table may ever be added
-- to this file: it would build while those three locks were held, blocking every
-- send claim and every workspace write for the length of the build.

-- ---------------------------------------------------------------------------
-- tracking_event_rollups: the per-send summary a raw tracking event folds into
-- when it ages out.
--
-- The aggregate readers ask one of three questions, and all three are
-- answerable from (send, kind, verdict) plus a count and a time range:
--   * "how many distinct sends had >= 1 human open/click" — every campaign,
--     contact and per-variant rate (COUNT(DISTINCT send_id));
--   * "how many human opens has THIS send had, and when was the last one" — the
--     bot classifier's ordering rule (GetSendTrackingContext);
--   * "when did anything last happen to this contact's mail" (ContactTrackingStats).
-- So the grain is exactly one row per (send_id, kind, is_machine): nothing those
-- reports read is lost, and what none of them reads — the user agent, the client
-- IP (personal data about the recipient, see 20260827190215) and the click URL —
-- is not carried over. Historical numbers survive; per-hit detail about a person
-- does not.
--
-- ANY query that reads tracking events must read tracking_engagement (below),
-- not tracking_events, or it silently loses every event older than the tracking
-- window. The exceptions are pinned by an allowlist test
-- (internal/platform/db/trackingreaders_test.go): the classifier's 10-minute
-- burst rule (needs client_ip, far inside any window), the CRM deal activity feed
-- (an event log, which a rollup cannot preserve — rolled-up events leave it), and
-- the sandbox seeder (writes).
--
-- is_machine is frozen at rollup time. A future backfill that RECLASSIFIES raw
-- events (a new botfilter rule applied to history) cannot reach a rolled-up row:
-- the evidence it would re-judge — the user agent and IP — is exactly what the
-- rollup dropped. Such a backfill covers raw rows only, and must say so.
--
-- fillfactor 90: the ON CONFLICT DO UPDATE in RollupTrackingEvents rewrites a
-- send's row each time more of its events age out; free space on the page lets
-- that be a HOT update rather than a new tuple plus index entries.
CREATE TABLE IF NOT EXISTS tracking_event_rollups (
    workspace_id UUID                NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    campaign_id  UUID                NOT NULL,
    send_id      UUID                NOT NULL,
    kind         tracking_event_kind NOT NULL,
    is_machine   BOOLEAN             NOT NULL,
    events       BIGINT              NOT NULL CHECK (events > 0),
    first_at     TIMESTAMPTZ         NOT NULL,
    last_at      TIMESTAMPTZ         NOT NULL,
    CHECK (first_at <= last_at),
    PRIMARY KEY (send_id, kind, is_machine),
    -- The same two tenant FKs tracking_events carries (000028), with the same
    -- cascade: a rollup is the send's engagement, so when the send goes — a
    -- sends retention delete, a contact or campaign deletion — its engagement
    -- goes with it and a rate's numerator and denominator shrink together.
    FOREIGN KEY (send_id, workspace_id) REFERENCES sends(id, workspace_id) ON DELETE CASCADE,
    FOREIGN KEY (campaign_id, workspace_id) REFERENCES campaigns(id, workspace_id) ON DELETE CASCADE
) WITH (fillfactor = 90);
-- The campaign aggregations' access path, mirroring idx_tracking_campaign_kind on
-- the raw table so the rollup arm of every campaign rate is index-only too.
-- Plain CREATE INDEX is right here and only here: the table was created empty a
-- statement ago.
CREATE INDEX IF NOT EXISTS idx_tracking_rollups_campaign_kind
    ON tracking_event_rollups (campaign_id, workspace_id, kind, is_machine, send_id);
-- ListCampaignPerformance aggregates a whole workspace by (kind, verdict) and
-- GROUPs BY campaign_id; carrying campaign_id ahead of send_id lets that be
-- index-only. The PK leads with send_id, which that query does not have.
CREATE INDEX IF NOT EXISTS idx_tracking_rollups_workspace_kind
    ON tracking_event_rollups (workspace_id, kind, is_machine, campaign_id, send_id);

-- tracking_engagement is the ONE definition of "every tracking event, rolled up
-- or not". A plain UNION ALL view: Postgres pushes each reader's WHERE into both
-- arms, so each keeps its own index path. It holds no data and needs no refresh.
-- A raw row is one event whose first and last sighting are the same instant.
CREATE OR REPLACE VIEW tracking_engagement AS
SELECT workspace_id, campaign_id, send_id, kind, is_machine,
       1::bigint  AS events,
       created_at AS first_at,
       created_at AS last_at
FROM tracking_events
UNION ALL
SELECT workspace_id, campaign_id, send_id, kind, is_machine,
       events, first_at, last_at
FROM tracking_event_rollups;

-- ---------------------------------------------------------------------------
-- retention_cursors: where each table's sweep got to, across runs.
--
-- Without it every run restarts from the oldest row, and on a table whose oldest
-- rows are KEPT by a guard (sends of long-lived active enrollments, a running
-- campaign's history) a run can spend its whole batch budget re-reading kept rows
-- and never reach the deletable ones behind them. The sweep resumes from here and
-- starts over from the oldest row when a table drains, or once a day
-- (cycle_started_at), so a row a guard stopped protecting is reached again.
--
-- Deployment infrastructure, not tenant data: one row per swept table, holding a
-- position in (age, id) order and nothing about any row's content.
CREATE TABLE IF NOT EXISTS retention_cursors (
    table_name       TEXT        PRIMARY KEY CHECK (table_name <> ''),
    after_at         TIMESTAMPTZ NOT NULL,
    after_id         UUID        NOT NULL,
    cycle_started_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);
