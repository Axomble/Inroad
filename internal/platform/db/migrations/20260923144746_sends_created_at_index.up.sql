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
-- Why: PurgeSends walks sends oldest-first by (created_at, id) with no
-- workspace predicate; the only created_at index is partial on status='queued'.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_sends_created_at
    ON sends (created_at, id);
