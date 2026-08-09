-- Workspace-defined custom fields on contacts.
--
-- The VALUES half of this feature already existed: contacts.custom_fields has
-- been a JSONB column since migration 000003, and personalize.substitute has
-- always resolved {{custom.<key>}} against it. What was missing is the
-- DEFINITION half -- nothing said which keys a workspace has, what type they
-- hold, or what to render in a form -- so nothing above the worker could write
-- or display them and CSV import dropped unknown columns on the floor.
--
-- Definitions live in their own table rather than as a JSONB blob on workspaces
-- because they are queried per-key (preflight validates one token at a time) and
-- because a type/options shape deserves real constraints, not application-only
-- validation of an opaque document.
CREATE TYPE contact_field_type AS ENUM ('text', 'number', 'date', 'select');

CREATE TABLE contact_field_defs (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,

    -- key is what appears inside a template token ({{custom.<key>}}) and what
    -- keys the contacts.custom_fields JSONB object, so it is deliberately
    -- narrower than personalize's customRE ([a-zA-Z0-9_]+): lower-case only,
    -- leading letter. That regex is case-SENSITIVE and resolves a miss to the
    -- empty string, so allowing both `Industry` and `industry` to be defined
    -- would let a one-character casing slip send a blank where a value was
    -- intended. One canonical spelling per key removes the failure mode instead
    -- of documenting it.
    key          TEXT NOT NULL CHECK (key ~ '^[a-z][a-z0-9_]{0,39}$'),
    label        TEXT NOT NULL CHECK (btrim(label) <> '' AND length(label) <= 80),
    field_type   contact_field_type NOT NULL,

    -- Populated for 'select' and NULL for every other type -- the biconditional
    -- makes "a select with no choices" and "a text field carrying choices"
    -- equally unrepresentable, rather than trusting each writer to remember.
    options      TEXT[],

    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- Deleting a definition does NOT delete stored values (issue #62 asks this
    -- be explicit): archiving hides the field from forms, import mapping and
    -- token completion, while the JSONB values stay exactly where they are. A
    -- destructive delete would silently rewrite every contact row in the
    -- workspace, and there is no undo for that; an operator who truly wants the
    -- data gone can clear values first, then archive.
    archived_at  TIMESTAMPTZ,

    CONSTRAINT contact_field_defs_options_match_type
        CHECK ((field_type = 'select') = (options IS NOT NULL)),
    CONSTRAINT contact_field_defs_options_bounded
        CHECK (options IS NULL OR (array_length(options, 1) BETWEEN 1 AND 100))
);

-- Unconditionally unique, NOT partial on archived_at IS NULL. A partial index
-- would let an archived `industry` be replaced by a new `industry` of a
-- different type -- and since values are keyed by `key` in a schemaless JSONB
-- object, the new definition would inherit the old one's values and try to read
-- them as the wrong type. Archiving retires a key permanently.
CREATE UNIQUE INDEX idx_contact_field_defs_ws_key ON contact_field_defs (workspace_id, key);

-- Serves the "list this workspace's live fields" read that the contact form,
-- import mapper and preflight all issue.
CREATE INDEX idx_contact_field_defs_ws_live
    ON contact_field_defs (workspace_id, key) WHERE archived_at IS NULL;
