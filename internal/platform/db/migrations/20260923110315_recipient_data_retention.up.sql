-- Configurable retention for the recipient-identifying tables: sends,
-- tracking_events, deliverability_events and inbox threads/messages. The sweep
-- itself is maintenance:retention (internal/worker/maintenance/retention.go); its
-- windows are INROAD_RETENTION_*_DAYS and every one of them defaults to DISABLED,
-- because how long to keep data about the people a workspace emails is a
-- Privacy/Legal decision, not a code default (docs: deploy/environment-variables).
--
-- This file adds two things: somewhere for tracking events to ROLL UP to, so
-- deleting raw events does not rewrite historical reporting, and the indexes the
-- age-ordered batch deletes need to seek rather than scan.
--
-- LOCKING. golang-migrate runs this file in one transaction, so none of these
-- indexes can be CONCURRENTLY (see 20260827185855_inbox_messages_mailbox_id). Each
-- CREATE INDEX takes SHARE on its table, blocking WRITES to it — not reads — for
-- the length of one btree build over one timestamp column. Order of magnitude on
-- ordinary disk: ~1M rows is a second or two, ~10M rows 10-30s, ~100M rows a few
-- minutes. sends and tracking_events are the two tables where that matters (a
-- blocked sends write is a stalled send claim, a blocked tracking write is a
-- tracking pixel that waits). An installation at the high end should create the
-- four idx_* indexes below by hand with CREATE INDEX CONCURRENTLY first; every
-- statement here is IF NOT EXISTS, so the migration then finds them and skips the
-- build.

-- ---------------------------------------------------------------------------
-- tracking_event_rollups: the per-send summary a raw tracking event folds into
-- when it ages out.
--
-- Every reader of tracking_events asks one of three questions, and all three are
-- answerable from (send, kind, verdict) plus a count and a time range:
--   * "how many distinct sends had >= 1 human open/click" — every campaign,
--     contact and per-variant rate (COUNT(DISTINCT send_id));
--   * "how many human opens has THIS send had, and when was the last one" — the
--     bot classifier's ordering rule (GetSendTrackingContext);
--   * "when did anything last happen to this contact's mail" (ContactEngagement).
-- So the grain here is exactly one row per (send_id, kind, is_machine): nothing a
-- report reads is lost, and everything no report reads — the user agent, the
-- client IP (personal data about the recipient, see 20260827190215) and the click
-- URL — is not carried over. That is the point of the rollup: historical numbers
-- survive, the per-hit detail about a person does not.
--
-- What does NOT survive, deliberately: the CRM deal activity feed lists
-- individual open/click events (internal/app/crm/integration_store.go ListEvents)
-- and reads raw tracking_events only, so an event older than the window leaves
-- that feed. An event log cannot be rolled up without keeping the events.
--
-- The recent-hit burst rule (CountRecentSendOpensFromSubnet) reads raw rows only,
-- inside botfilter.BurstWindow (10 minutes). The retention floor for this table is
-- far wider than that, so no rolled-up row can ever have been one it would read.
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
);
-- The campaign aggregations' access path, mirroring idx_tracking_campaign_kind on
-- the raw table so the rollup arm of every campaign rate is index-only too.
CREATE INDEX IF NOT EXISTS idx_tracking_rollups_campaign_kind
    ON tracking_event_rollups (campaign_id, workspace_id, kind, is_machine, send_id);
-- ListCampaignRollup aggregates a whole workspace by kind. The PK leads with
-- send_id, which that query does not have.
CREATE INDEX IF NOT EXISTS idx_tracking_rollups_workspace_kind
    ON tracking_event_rollups (workspace_id, kind, is_machine, send_id);

-- tracking_engagement is the ONE definition of "every tracking event, rolled up
-- or not". Every aggregate reader selects from this rather than from
-- tracking_events, so a report cannot forget the rollup arm — which would make a
-- campaign's open rate fall the day retention first ran, with nothing in the
-- query to say why. A raw row is one event whose first and last sighting are the
-- same instant.
--
-- A plain UNION ALL view: Postgres pushes each reader's WHERE into both arms, so
-- each arm keeps its own index path. It holds no data and needs no refresh.
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
-- Age indexes for the batched sweeps. Each sweep selects its oldest rows past the
-- window (ORDER BY <age> LIMIT n FOR UPDATE SKIP LOCKED) with no workspace
-- predicate, and every existing index on these tables either leads with
-- workspace_id / campaign_id or is partial on a status — the same gap
-- 20260828152300 closed for task_dead_letters. Without them each batch is a Seq
-- Scan plus a Sort of the whole table, which on the largest tables in the schema
-- is a retention policy in name only.
--
-- Each carries id as a tiebreaker because the sweep walks it as a KEYSET — a run
-- resumes each batch strictly after the last row the previous batch deleted. Two
-- reasons that matters here and did not for the older purges: rows a guard KEEPS
-- (a send whose enrollment is still active, a complaint that is the workspace's
-- only evidence of a feed) stay older than the cutoff indefinitely, and a sweep
-- that restarted from the oldest row every batch would re-walk all of them each
-- time; and the index entries of rows the previous batch just deleted are dead
-- until vacuum, which a from-the-start scan would also re-walk.
CREATE INDEX IF NOT EXISTS idx_tracking_events_created_at
    ON tracking_events (created_at, id);
CREATE INDEX IF NOT EXISTS idx_deliverability_events_received_at
    ON deliverability_events (received_at, id);
CREATE INDEX IF NOT EXISTS idx_inbox_threads_last_message_at
    ON inbox_threads (last_message_at, id);
CREATE INDEX IF NOT EXISTS idx_sends_created_at
    ON sends (created_at, id);

-- The sends sweep keeps any send a live inbox thread still renders: the thread's
-- outbound leg is SYNTHESIZED from sends by (workspace_id, campaign_id,
-- contact_id) at read time (ListSentOutboundStepsForThread), so deleting such a
-- send would silently remove the campaign's side of a conversation the operator
-- can still open. The anti-join needs to probe threads by exactly that key;
-- nothing on inbox_threads carries campaign_id or contact_id in an index today.
CREATE INDEX IF NOT EXISTS idx_inbox_threads_campaign_contact
    ON inbox_threads (workspace_id, campaign_id, contact_id)
    WHERE campaign_id IS NOT NULL;
