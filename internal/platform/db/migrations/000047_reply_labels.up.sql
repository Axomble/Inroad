-- User-definable reply-label taxonomy. Replaces the seven hardcoded reply
-- classes as the thing reply automation dispatches on: the classifier still
-- produces a key, but WHAT HAPPENS to the enrollment/contact/deal is now read
-- off the label row's role flags rather than compiled into a switch.
--
-- Shape is copied from pipeline_stages (000042:90-116) — the repo's proven
-- user-editable-taxonomy pattern: a stable machine `key` the code matches on,
-- an editable `label`/`color` the human sees, a non-unique `position`, and a
-- small set of semantic role booleans (the analogue of is_won/is_lost).

CREATE TABLE reply_labels (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    key          TEXT NOT NULL CHECK (key ~ '^[a-z][a-z0-9_]{0,63}$'),
    label        TEXT NOT NULL CHECK (char_length(btrim(label)) BETWEEN 1 AND 80),
    color        TEXT NOT NULL CHECK (color ~ '^#[0-9A-Fa-f]{6}$'),
    -- Deliberately NOT unique on (workspace_id, position), for the same reason
    -- pipeline_stages.position is not (000042:106-112): a reorder rewrites
    -- several rows and a non-deferrable unique constraint turns every such
    -- write into a hard 409 on the first intermediate collision. Order is
    -- therefore (position, id) everywhere — total, stable, reorderable one row
    -- at a time.
    position     INTEGER NOT NULL CHECK (position >= 0),

    -- Role flags. is_builtin marks a seeded label: its key is immutable and it
    -- cannot be deleted, because historical rows and the classifier both name
    -- it. The rest are the automation contract read by the inbox poller.
    is_builtin         BOOLEAN NOT NULL DEFAULT false,
    stops_enrollment   BOOLEAN NOT NULL DEFAULT true,
    -- is_automated marks the OOO/auto-reply family: machine-generated mail that
    -- must never be treated as a human reply.
    is_automated       BOOLEAN NOT NULL DEFAULT false,
    suppresses_contact BOOLEAN NOT NULL DEFAULT false,
    captures_deal      BOOLEAN NOT NULL DEFAULT false,
    -- defers_enrollment reschedules the next step past a stated return date
    -- instead of stopping the sequence (out-of-office).
    defers_enrollment  BOOLEAN NOT NULL DEFAULT false,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (workspace_id, key),
    UNIQUE (id, workspace_id)
);
CREATE INDEX idx_reply_labels_order ON reply_labels (workspace_id, position, id);

CREATE TRIGGER reply_labels_touch_updated_at
BEFORE UPDATE ON reply_labels FOR EACH ROW EXECUTE FUNCTION crm_touch_updated_at();

-- The builtin label set has exactly ONE definition, called from both the
-- backfill below and the new-workspace trigger, so the two cannot drift — the
-- same construction as seed_pipeline_stages (000042:243-287).
--
-- The flags reproduce the pre-000047 hardcoded switch in
-- internal/worker/inbox/poll.go EXACTLY:
--   positive              -> stop + capture a deal
--   negative/neutral/unknown -> stop only
--   unsubscribe           -> suppress the address, then stop (compliance)
--   auto_reply            -> automated: tag only, enrollment stays active
--   out_of_office         -> automated: tag only, enrollment stays active
-- defers_enrollment is OFF on out_of_office by default: turning it on is an
-- opt-in behaviour change, not a silent migration of existing workspaces.
CREATE FUNCTION seed_reply_labels(target_workspace UUID) RETURNS void
LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO reply_labels (workspace_id, key, label, color, position,
                              is_builtin, stops_enrollment, is_automated,
                              suppresses_contact, captures_deal, defers_enrollment)
    VALUES
      (target_workspace, 'positive',      'Interested',    '#22C55E', 0, true, true,  false, false, true,  false),
      (target_workspace, 'negative',      'Not interested','#EF4444', 1, true, true,  false, false, false, false),
      (target_workspace, 'neutral',       'Neutral',       '#64748B', 2, true, true,  false, false, false, false),
      (target_workspace, 'unsubscribe',   'Unsubscribe',   '#B91C1C', 3, true, true,  false, true,  false, false),
      (target_workspace, 'out_of_office', 'Out of office', '#F59E0B', 4, true, false, true,  false, false, false),
      (target_workspace, 'auto_reply',    'Auto-reply',    '#A855F7', 5, true, false, true,  false, false, false),
      (target_workspace, 'unknown',       'Unclassified',  '#94A3B8', 6, true, true,  false, false, false, false)
    ON CONFLICT (workspace_id, key) DO NOTHING;
END;
$$;

SELECT seed_reply_labels(id) FROM workspaces;

CREATE FUNCTION seed_new_workspace_reply_labels() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    PERFORM seed_reply_labels(NEW.id);
    RETURN NEW;
END;
$$;
CREATE TRIGGER workspaces_seed_reply_labels
AFTER INSERT ON workspaces FOR EACH ROW EXECUTE FUNCTION seed_new_workspace_reply_labels();

-- Two places hardcoded the old closed class set; both have to go, or a custom
-- label is unwritable / unindexed.

-- 1. The inbox unread index pinned last_reply_class = 'positive'
--    (000046_inbox.up.sql:20), so it served exactly one label. Replaced with an
--    unconditional (workspace_id, unread) index, which serves the unread filter
--    for ANY label rather than one privileged value.
DROP INDEX idx_inbox_threads_workspace_unread_positive;
CREATE INDEX idx_inbox_threads_workspace_unread_any ON inbox_threads (workspace_id, unread);

-- 2. sequence_enrollments.reply_class CHECK pinned the seven classes
--    (000014_reply_class.up.sql:14-17). Dropped, and deliberately NOT replaced
--    with an FK to reply_labels: an enrollment row must SURVIVE its label being
--    deleted. reply_class is free text holding the label key; readers resolve
--    it for display and degrade to the raw key when unresolvable.
ALTER TABLE sequence_enrollments DROP CONSTRAINT sequence_enrollments_reply_class_chk;
