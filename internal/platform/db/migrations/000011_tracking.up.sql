-- Open/click tracking. tracking_events is a high-volume, append-mostly
-- table (one row per pixel hit / link click, potentially many per send) —
-- built partition-ready from day one the same way `sends` was in 000006:
-- PK is (id, created_at) so a future range-partition by created_at doesn't
-- require touching call sites. send_id is a plain indexed column, NOT a
-- FK, because sends' PK is the composite (id, created_at) — a FK would
-- need created_at threaded through every insert here for no benefit; the
-- worker already has the send id in hand when it fires a tracking pixel.
CREATE TYPE tracking_event_kind AS ENUM ('open', 'click');

CREATE TABLE tracking_events (
    id           UUID        NOT NULL DEFAULT gen_random_uuid(),
    workspace_id UUID        NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    campaign_id  UUID        NOT NULL REFERENCES campaigns(id)  ON DELETE CASCADE,
    send_id      UUID        NOT NULL,
    kind         tracking_event_kind NOT NULL,
    url          TEXT        NOT NULL DEFAULT '',
    user_agent   TEXT        NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (id, created_at)
);
-- Aggregation-tuned: CountEngagedSendsByKind filters on (campaign_id,
-- workspace_id) and GROUP BYs kind — workspace_id has to sit in the index
-- ahead of kind/send_id, or the workspace filter can't be applied index-only
-- and every matching row gets heap-fetched just to check tenancy. With
-- workspace_id in place, the scan stays index-only and send_id trailing
-- still gives COUNT(DISTINCT send_id) a sorted run to dedupe over.
CREATE INDEX idx_tracking_campaign_kind ON tracking_events (campaign_id, workspace_id, kind, send_id);

ALTER TABLE campaigns ADD COLUMN tracking_enabled BOOLEAN NOT NULL DEFAULT true;
