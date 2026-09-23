-- Inbox full-text search over BOTH legs of a thread.
--
-- A thread's messages live in two places, and a search that covered only one
-- would silently miss half the conversation:
--   * inbox_messages holds every inbound reply and every manual outbound reply.
--   * A campaign's own sends are NOT copied there. The outbound leg is
--     synthesized at read time from sends -> sequence_steps (or, for an A/B
--     send, sequence_step_variants), so the text an operator sent in a campaign
--     exists only on those two tables.
-- So three tables get a search index, all built from ONE document definition.
--
-- EXPRESSION INDEXES, NOT A GENERATED COLUMN. contacts.search_text (000034) is
-- a generated column, and that shape was considered and rejected here for two
-- reasons specific to tsvector:
--   1. Every `SELECT *` / `RETURNING *` over these tables would start carrying
--      the tsvector to the client — ListInboxMessagesByThread reads every row of
--      a thread on each open — and sqlc has no Go type for tsvector, so the
--      generated models would grow an interface{} field.
--   2. ADD COLUMN ... GENERATED STORED rewrites the table under ACCESS
--      EXCLUSIVE, blocking reads. CREATE INDEX takes SHARE, which blocks writes
--      only, and needs no backfill: the build IS the backfill.
-- The cost of an expression index is that the query must repeat the exact
-- expression for the planner to use it. That is why the expression lives in a
-- function (inbox_search_document) both sides call, rather than being written
-- out twice; the planner inlines a plain SQL function into both the index
-- definition and the query before matching them. The integration suite asserts
-- the index is actually chosen, so a query that drifts from the function fails
-- a test rather than silently seq-scanning.
--
-- NOT CONCURRENTLY: golang-migrate's pgx5 driver runs each file in ONE
-- transaction (internal/platform/db/migrate.go sets no x-multi-statement) and
-- CREATE INDEX CONCURRENTLY cannot run inside a transaction block — the same
-- constraint 20260827185855_inbox_messages_mailbox_id documents. Each build
-- holds SHARE on its table, blocking writes (the reply poller's inserts, manual
-- replies, step edits) for its duration; reads keep serving. inbox_messages
-- holds replies, not campaign sends, so it is thousands of rows on an ordinary
-- installation and the build is sub-second; sequence_steps and
-- sequence_step_variants are campaign CONTENT (a handful of rows per campaign).
-- The table that is genuinely large — sends — is not indexed or touched here.
-- An installation with tens of millions of inbox_messages can pre-build the
-- index by hand with CONCURRENTLY under the same name before migrating; the
-- IF NOT EXISTS below then makes this file a no-op for it.

-- The search document for one message: its subject and plain-text body.
--
-- 'english' rather than 'simple': stemming is what makes "meeting" find
-- "meetings" and "pricing" find "price", which is the difference between a
-- search that works and one operators learn not to trust. The cost is that
-- non-English mail is stemmed by English rules — harmless in practice (a
-- foreign word rarely collides with an English stem), and the stop-word list
-- only removes English function words.
--
-- left(..., 100000) is a CORRECTNESS guard, not a tuning knob. body_text has no
-- length limit anywhere upstream, and to_tsvector raises "string is too long
-- for tsvector" once a document's lexemes pass 1MB. Because an expression index
-- is evaluated on INSERT, an unbounded document would turn one enormous inbound
-- email into a failed insert — and the poller would retry that same message
-- forever, wedging its mailbox's reply ingestion. 100,000 characters bounds the
-- tsvector well under the limit for any text, while covering every body an
-- operator plausibly writes or receives in full. Text past that point is not
-- searchable; the integration suite pins that a multi-megabyte body still
-- inserts.
--
-- IMMUTABLE is truthful — to_tsvector with an explicit regconfig and left()
-- are both immutable — and required, since an index expression may call only
-- immutable functions. No SET clause and no plpgsql: either would stop the
-- planner inlining it, and inlining is what lets a query's call match the
-- index's.
CREATE FUNCTION inbox_search_document(p_subject TEXT, p_body TEXT) RETURNS tsvector
LANGUAGE sql
IMMUTABLE
PARALLEL SAFE
AS $$
    SELECT to_tsvector('english'::regconfig, left(p_subject, 1000) || ' ' || left(p_body, 100000))
$$;

-- The operator's query, parsed with websearch_to_tsquery: quoted phrases, OR
-- and -exclusion work as a web search box leads people to expect, and — unlike
-- to_tsquery — no input is a syntax error, so arbitrary user text can never
-- turn into a 500. Same regconfig as the document, which is why it is a
-- function too: a query parsed under a different configuration than the index
-- was built with matches nothing, silently.
CREATE FUNCTION inbox_search_query(p_query TEXT) RETURNS tsquery
LANGUAGE sql
IMMUTABLE
PARALLEL SAFE
AS $$
    SELECT websearch_to_tsquery('english'::regconfig, p_query)
$$;

-- workspace_id leads each index (btree_gin, installed by 000034), so a search
-- never visits another tenant's postings to discard them — the same reasoning
-- as idx_contacts_search.
CREATE INDEX IF NOT EXISTS idx_inbox_messages_search
    ON inbox_messages USING gin (workspace_id, inbox_search_document(subject, body_text));

CREATE INDEX IF NOT EXISTS idx_sequence_steps_search
    ON sequence_steps USING gin (workspace_id, inbox_search_document(subject, body_text));

CREATE INDEX IF NOT EXISTS idx_sequence_step_variants_search
    ON sequence_step_variants USING gin (workspace_id, inbox_search_document(subject, body_text));
