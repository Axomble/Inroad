-- Bot/prefetch classification for tracking events.
--
-- Apple Mail Privacy Protection, the Gmail image proxy, Outlook SafeLinks and
-- corporate link scanners PRE-FETCH tracking pixels and links, and every one of
-- those hits was previously counted as a genuine open or click.
--
-- The classification is STORED, never used to drop the row. A machine open is
-- real data about what happened to a message; discarding it would leave
-- reporting unable to say "N opens, M of them machine" and would silently
-- present a truncated number as if it were the whole truth. Reporting excludes
-- machine events from the headline rate by FILTERING on this column instead.
--
-- Both columns are NOT NULL with defaults, so the backfill of existing rows is
-- the default itself and no table rewrite of judgement is implied: rows recorded
-- before this migration are marked human, which is exactly what every report
-- already assumed about them. Re-classifying history is a separate, deliberate
-- backfill job, not a side effect of a schema change.
-- One ADD COLUMN per statement, matching every other migration here (sqlc's
-- parser rejects a multi-column list).
ALTER TABLE tracking_events
    ADD COLUMN IF NOT EXISTS is_machine BOOLEAN NOT NULL DEFAULT false;

-- Free TEXT rather than an enum: the classifier's signal set is expected to grow
-- (a new scanner vendor, a new heuristic), and adding a value to an enum takes a
-- lock on what is the highest-volume table in the schema. The allowed values are
-- the botfilter.Reason constants; '' accompanies a human verdict. The CHECK
-- below ties the two columns together.
ALTER TABLE tracking_events
    ADD COLUMN IF NOT EXISTS machine_reason TEXT NOT NULL DEFAULT '';

-- The source address of the hit, INET rather than TEXT so the burst rule can ask
-- "same /24?" with a subnet containment operator instead of a string prefix match
-- (which would group 203.0.113.1 with 203.0.1131). NULLABLE on purpose: behind an
-- untrusted proxy the resolver legitimately cannot determine a client IP, and
-- NULL says "unknown" where 0.0.0.0 would be a lie the burst count would group on.
ALTER TABLE tracking_events
    ADD COLUMN IF NOT EXISTS client_ip INET;

-- The verdict and its reason can never disagree: a machine row must say WHY, and
-- a human row must claim no reason. Written as its own statement rather than
-- inside the ADD COLUMN list above because sqlc's parser rejects a table
-- constraint there.
ALTER TABLE tracking_events
    ADD CONSTRAINT tracking_events_machine_reason_matches_verdict
        CHECK ((is_machine AND machine_reason <> '') OR (NOT is_machine AND machine_reason = ''));

-- The headline aggregations (CountEngagedSendsByKind, CountHumanOpens,
-- ListCampaignResults, ContactEngagement) all filter machine events out, so
-- is_machine has to sit inside the index they already use or every candidate row
-- gets heap-fetched just to test it -- which would undo the index-only scan that
-- migration 000011's comment was explicitly built for. Placed after kind and
-- before send_id: kind is still an equality predicate, is_machine becomes one,
-- and send_id stays trailing so COUNT(DISTINCT send_id) keeps its sorted run.
DROP INDEX idx_tracking_campaign_kind;
CREATE INDEX idx_tracking_campaign_kind ON tracking_events (campaign_id, workspace_id, kind, is_machine, send_id);

-- The classifier's ordering and burst rules read this send's own recent history
-- on the tracking hot path (a public, unauthenticated endpoint), so that lookup
-- must never become a scan. Covers both reads: the human-open probe filters
-- (send_id, kind, is_machine) and wants the latest created_at, and the burst
-- count filters (send_id, kind) over a created_at window.
CREATE INDEX idx_tracking_send_recent ON tracking_events (send_id, kind, is_machine, created_at DESC);

-- PRIVACY NOTE. client_ip is personal data about a message RECIPIENT, not a
-- user of this system, and it is retained only because the burst rule cannot
-- work without it. Two consequences a future change must preserve: it is an
-- internal classification input and must never appear in an API response DTO
-- (the tracking domain returns no event rows at all today), and it inherits
-- tracking_events' existing ON DELETE CASCADE from workspaces, so erasing a
-- workspace erases these addresses with it.
