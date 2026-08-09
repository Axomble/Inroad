-- A/B variants on sequence steps.
--
-- THE MODEL: the step's OWN subject/body is variant A. This table holds only the
-- alternatives (B, C, ...), and `sends.variant_id IS NULL` means "the step's base
-- content was used".
--
-- The obvious alternative -- move all content into variant rows and make the
-- step a pure container -- was rejected because of what it costs on data that
-- already exists. Every campaign in every deployment would need its content
-- copied into a variant row by this migration, every historic `sends` row would
-- need a variant_id it cannot truthfully be given (nobody recorded which variant
-- sent it, because there were none), and the send path would have to handle "a
-- step whose variant rows failed to materialise" forever. Treating the step as
-- variant A means zero backfill, zero rewritten history, and a step with no rows
-- here behaves exactly as it does today.
--
-- The cost is that the model is asymmetric, so the asymmetry is stated
-- everywhere it shows: in the weight column below, in the send path's selection,
-- and in the API's VariantStats.is_base.

-- Weight of the step's own content in the split. 1 alongside one variant of
-- weight 1 is an even A/B; 0 retires the base copy without deleting it, which is
-- how an operator promotes a winning variant B without losing what A said.
ALTER TABLE sequence_steps
    ADD COLUMN variant_weight INT NOT NULL DEFAULT 1 CHECK (variant_weight >= 0);

CREATE TABLE sequence_step_variants (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id  UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    step_id       UUID NOT NULL REFERENCES sequence_steps(id) ON DELETE CASCADE,

    -- Operator-facing name. Short because it labels a column in the results
    -- table; unique per step so a report can never show two rows called "B".
    label         TEXT NOT NULL CHECK (btrim(label) <> '' AND length(label) <= 40),

    -- Relative selection weight. 0 means "still here, no longer sending", which
    -- is what pausing a losing variant looks like -- deleting it would orphan
    -- the sends already attributed to it.
    weight        INT NOT NULL DEFAULT 1 CHECK (weight >= 0),

    -- Same shape as the step's own content. An empty subject on a follow-up step
    -- threads onto the previous message exactly as the base copy does.
    subject       TEXT NOT NULL DEFAULT '',
    body_text     TEXT NOT NULL DEFAULT '',
    body_html     TEXT NOT NULL DEFAULT '',

    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),

    UNIQUE (step_id, label),
    -- Parent key for the composite tenant FK on sends below: it lets PostgreSQL
    -- prove a send and the variant it names belong to the same workspace, rather
    -- than trusting every writer to have checked.
    UNIQUE (id, workspace_id)
);

CREATE INDEX idx_step_variants_step ON sequence_step_variants (step_id);

-- Which variant actually sent this message. NULL = the step's base content
-- (variant A), which is also every row written before this migration -- so the
-- column needs no backfill and no historic row is misattributed.
--
-- ON DELETE SET NULL rather than CASCADE or RESTRICT: deleting a variant must
-- not delete delivery history, and must not be blocked by it. The send survives
-- and falls back to reading as base content, which is the least wrong answer
-- once the row naming it is gone. (The UI steers operators to weight 0 instead,
-- precisely so attribution is kept.)
ALTER TABLE sends
    ADD COLUMN variant_id UUID,
    ADD CONSTRAINT sends_variant_workspace_fkey
        FOREIGN KEY (variant_id, workspace_id)
        REFERENCES sequence_step_variants (id, workspace_id) ON DELETE SET NULL;

-- Serves the per-variant rollup: "for this campaign, how did each variant do".
-- Partial because a NULL variant_id is the base copy, counted by the absence of
-- a row here rather than by scanning them.
CREATE INDEX idx_sends_variant ON sends (variant_id) WHERE variant_id IS NOT NULL;
