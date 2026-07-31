-- Campaign sending schedule: a weekly set of open intervals evaluated in the
-- campaign's own IANA timezone. Before this, the send path had no notion of WHEN
-- a campaign may send and would happily deliver at 03:00 local on a Sunday.

-- btree_gist supplies the equality operator classes the exclusion constraint
-- below needs for its UUID and smallint columns. Enabled explicitly here so a
-- Postgres without contrib fails loudly at migrate time rather than at the first
-- window write.
CREATE EXTENSION IF NOT EXISTS btree_gist;

-- The zone every window on the campaign is interpreted in. TEXT rather than an
-- enum: the IANA database changes, and validation belongs at the boundary
-- (time.LoadLocation), not in a constraint we would have to migrate.
ALTER TABLE campaigns
    ADD COLUMN timezone TEXT NOT NULL DEFAULT 'UTC';

-- One row per open interval per weekday. Minutes from local midnight, half-open
-- [start_minute, end_minute), so 09:00-12:00 and 12:00-17:00 are adjacent rather
-- than overlapping. end_minute may reach 1440 (exclusive midnight).
CREATE TABLE campaign_send_windows (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    campaign_id  UUID NOT NULL REFERENCES campaigns(id) ON DELETE CASCADE,
    weekday      SMALLINT NOT NULL CHECK (weekday BETWEEN 0 AND 6), -- 0 = Sunday, matching time.Weekday
    start_minute INT NOT NULL CHECK (start_minute BETWEEN 0 AND 1439),
    end_minute   INT NOT NULL CHECK (end_minute BETWEEN 1 AND 1440),
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT send_window_not_inverted CHECK (start_minute < end_minute),
    -- Overlapping intervals on one weekday are unrepresentable, so the cadence
    -- engine never has to reconcile them and a buggy caller cannot persist a
    -- schedule it would then misread. int4range is half-open, matching the
    -- column semantics exactly.
    CONSTRAINT send_window_no_overlap EXCLUDE USING gist (
        campaign_id WITH =,
        weekday WITH =,
        int4range(start_minute, end_minute) WITH &&
    )
);

-- The scheduler reads a campaign's whole week at once; workspace_id leads so the
-- index also serves tenant-scoped listing.
CREATE INDEX idx_send_windows_campaign ON campaign_send_windows (workspace_id, campaign_id, weekday);

-- Backfill every existing campaign with the same default a new campaign now gets
-- (Mon-Fri 09:00-17:00). No campaign may be left window-less: an empty week means
-- "no valid send instant exists", which the engine treats as a corrupted row.
INSERT INTO campaign_send_windows (workspace_id, campaign_id, weekday, start_minute, end_minute)
SELECT c.workspace_id, c.id, d.weekday, 9 * 60, 17 * 60
FROM campaigns c
CROSS JOIN (VALUES (1), (2), (3), (4), (5)) AS d(weekday);
