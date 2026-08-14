-- Reverse of 000062.
--
-- Like 000061 and unlike 000060, this rollback is a formality by construction:
-- the migration adds one new column and one CHECK that had no predecessor, so
-- there is no prior definition to restore and no pre-existing row the reverted
-- schema rejects. (000060 WIDENED an existing CHECK, which is why its rows had to
-- be rewritten before the narrow one could return.)
--
-- No reputation evidence is lost. The placement, its attribution, its capability,
-- its identity facts and its daily projection all live in columns this migration
-- never touched; what a rollback discards is WHICH DESTINATION those observations
-- were measured against, which by design §7 no threshold, lane or promotion
-- decision reads — so a rolled-back deployment decides exactly what it decided
-- before.
--
-- The constraint is dropped explicitly and BEFORE the column, even though DROP
-- COLUMN would take it along: the drop is then re-runnable against a database
-- where a partial rollback already removed the column, and it names the
-- constraint that the re-applied up migration expects to be able to create.
ALTER TABLE warmup_observations
    DROP CONSTRAINT IF EXISTS warmup_observations_destination_esp_check;

ALTER TABLE warmup_observations
    DROP COLUMN IF EXISTS destination_esp;
