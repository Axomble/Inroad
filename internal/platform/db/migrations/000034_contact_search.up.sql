-- Index-backed contact search + keyset pagination.

CREATE EXTENSION IF NOT EXISTS pg_trgm;
-- btree_gin supplies the GIN operator classes for scalar types (uuid), which is
-- what lets workspace_id share ONE index with the trigram predicate below.
CREATE EXTENSION IF NOT EXISTS btree_gin;

-- One searchable projection of the four text columns, maintained by Postgres.
-- Generated rather than trigger-maintained: it cannot drift from its sources and
-- the ALTER backfills every existing row. All four columns are NOT NULL, so ||
-- never yields NULL and the expression is immutable (required for STORED).
-- concat_ws is deliberately NOT used: it is only STABLE and Postgres rejects it
-- in a generated column.
ALTER TABLE contacts ADD COLUMN search_text TEXT
    GENERATED ALWAYS AS (
        lower(email || ' ' || first_name || ' ' || last_name || ' ' || company)
    ) STORED;

-- Substring search, not prefix: operators search partial emails and domains
-- ("acme.com" must find "jo@acme.com"), which a prefix index cannot serve.
-- workspace_id lives in the SAME index so a search never touches another
-- tenant's rows to discard them.
CREATE INDEX idx_contacts_search ON contacts
    USING gin (workspace_id, search_text gin_trgm_ops);

-- Keyset ordering indexes, one per sort. Both are scanned forwards for a next
-- page and backwards for a previous one, so two indexes cover four directions.
CREATE INDEX idx_contacts_ws_created ON contacts (workspace_id, created_at DESC, id DESC);
CREATE INDEX idx_contacts_ws_email_id ON contacts (workspace_id, lower(email), id);
