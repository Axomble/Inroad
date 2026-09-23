-- ONE STATEMENT, deliberately, and it must stay that way. golang-migrate's
-- pgx/v5 driver sends a migration file as a single simple-protocol Exec; with
-- one statement that is not a transaction block, so CREATE/DROP INDEX
-- CONCURRENTLY is allowed and the table keeps taking writes while the index
-- builds. A second statement in this file would turn it into an implicit
-- transaction and the CONCURRENTLY would be refused
-- (internal/platform/db/concurrentindex_test.go pins the one statement;
-- retention_integration_test.go asserts the build and pg_index.indisvalid).
--
-- IF A BUILD FAILS (a crash, a cancelled deploy, a deadlock) Postgres leaves the
-- index behind marked INVALID, golang-migrate marks the schema dirty, and IF NOT
-- EXISTS would then skip the broken index forever. Recover with:
--     DROP INDEX CONCURRENTLY IF EXISTS <name>;
--     UPDATE schema_migrations SET version = <previous version>, dirty = false;
-- and re-run the migrations. The deploy docs carry the same procedure.
--
-- Why: PurgeSends keeps any send an inbox thread still renders — the thread's
-- outbound leg is synthesized from sends by (workspace_id, campaign_id,
-- contact_id) — and probes inbox_threads by exactly that key once per candidate
-- send. Nothing on inbox_threads indexed campaign_id or contact_id.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_inbox_threads_campaign_contact
    ON inbox_threads (workspace_id, campaign_id, contact_id)
    WHERE campaign_id IS NOT NULL;
