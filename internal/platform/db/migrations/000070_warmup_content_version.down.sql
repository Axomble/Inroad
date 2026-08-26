-- Reverse of 000069.
--
-- Like 000061 and 000062, and unlike 000060, this rollback restores nothing: the
-- migration adds two new columns and two CHECKs that had no predecessor, so there
-- is no prior definition to re-create and no pre-existing row the reverted schema
-- rejects. (000060 WIDENED an existing CHECK, which is why its rows had to be
-- rewritten before the narrow one could come back.)
--
-- No reputation evidence is lost. Every placement, its attribution, its capability,
-- its identity facts, its destination route and its daily projection live in columns
-- this migration never touched. What a rollback discards is WHICH CONTENT those
-- observations were measured on — which by design no threshold, lane, health state
-- or promotion decision reads, so a rolled-back deployment decides exactly what it
-- decided before. What it also discards is unrecoverable by replay: the version is
-- computed at send time from the library as it stood then, so re-applying 000069
-- starts the history fresh rather than backfilling it. That is a reporting gap, not
-- a correctness one.
--
-- Both constraints are dropped explicitly and BEFORE their columns, even though DROP
-- COLUMN would take them along. That makes this file re-runnable against a database
-- where a partial rollback already removed a column, and it names the constraints the
-- re-applied up migration expects to be able to create.
ALTER TABLE warmup_observations
    DROP CONSTRAINT IF EXISTS warmup_observations_content_version_check;

ALTER TABLE warmup_sends
    DROP CONSTRAINT IF EXISTS warmup_sends_content_version_check;

ALTER TABLE warmup_observations
    DROP COLUMN IF EXISTS content_version;

ALTER TABLE warmup_sends
    DROP COLUMN IF EXISTS content_version;
