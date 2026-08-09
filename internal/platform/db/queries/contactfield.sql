-- Workspace-defined custom field DEFINITIONS. The values they describe live in
-- contacts.custom_fields (JSONB, migration 000003) and are written by
-- queries/contact.sql -- see the note on UpsertContact.

-- name: ListContactFieldDefs :many
-- Every definition the workspace has, archived ones included. Callers that want
-- only live fields filter in Go rather than issuing a second statement: the set
-- is bounded (one row per field an operator has ever created) and every caller
-- that needs the live subset also needs to recognise an archived key, so
-- splitting this would mean two round trips to answer one question.
SELECT id, key, label, field_type, options, created_at, archived_at
FROM contact_field_defs
WHERE workspace_id = $1
ORDER BY archived_at NULLS FIRST, label, key;

-- name: GetContactFieldDef :one
SELECT id, key, label, field_type, options, created_at, archived_at
FROM contact_field_defs
WHERE workspace_id = $1 AND id = $2;

-- name: CreateContactFieldDef :one
-- A duplicate key raises 23505 on idx_contact_field_defs_ws_key, which the
-- service maps to a conflict. That includes colliding with an ARCHIVED key: the
-- index is deliberately not partial, because values are keyed by `key` in a
-- schemaless JSONB object and a reused key would inherit the retired field's
-- values (see the migration).
INSERT INTO contact_field_defs (workspace_id, key, label, field_type, options)
VALUES ($1, $2, $3, $4, $5)
RETURNING id, key, label, field_type, options, created_at, archived_at;

-- name: UpdateContactFieldDef :one
-- Label and select options are editable; key and field_type are NOT. Both are
-- load-bearing on data already written -- key addresses stored JSONB values and
-- appears in template tokens operators have already typed into live sequences,
-- and field_type is the promise under which every existing value was validated.
-- Changing either would silently reinterpret history, so the API offers archive
-- + create instead.
--
-- Archived rows are excluded: editing a retired field's label would move it
-- under an operator who is looking at why a value renders the way it does.
UPDATE contact_field_defs
SET label = $3, options = $4
WHERE workspace_id = $1 AND id = $2 AND archived_at IS NULL
RETURNING id, key, label, field_type, options, created_at, archived_at;

-- name: ArchiveContactFieldDef :one
-- Idempotent by design: archiving an already-archived field returns the row
-- unchanged rather than moving archived_at, so a double-click cannot rewrite
-- when the field was retired. Zero rows means "not in this workspace" (404).
UPDATE contact_field_defs
SET archived_at = COALESCE(archived_at, now())
WHERE workspace_id = $1 AND id = $2
RETURNING id, key, label, field_type, options, created_at, archived_at;
