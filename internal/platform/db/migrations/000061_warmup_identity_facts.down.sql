-- Reverse of 000061.
--
-- Unlike 000060, this rollback is a formality by construction, and that is worth
-- stating rather than assuming. 000060 WIDENED an existing CHECK, so the rows it
-- made legal had to be rewritten before the narrow one could return. 000061 adds
-- only new columns and a CHECK that did not exist before it, so there is no prior
-- definition to restore and no pre-existing row that the reverted schema rejects.
--
-- Nor is any reputation evidence lost: the placement, its attribution, its
-- capability and its daily projection all live in columns this migration never
-- touched. What a rollback discards is the identity metadata ON those
-- observations, which by design §7 no threshold, lane or promotion decision reads
-- — so a rolled-back deployment decides exactly what it decided before.
--
-- Dropped explicitly and BEFORE the columns even though DROP COLUMN would take it
-- with them: the drop is then re-runnable against a database where a partial
-- rollback already removed the columns, and it names the constraint the re-applied
-- up migration expects to be able to create.
ALTER TABLE warmup_observations
    DROP CONSTRAINT IF EXISTS warmup_observations_auth_results_check;

ALTER TABLE warmup_observations
    DROP COLUMN IF EXISTS dkim_domain,
    DROP COLUMN IF EXISTS return_path_domain,
    DROP COLUMN IF EXISTS spf_result,
    DROP COLUMN IF EXISTS dkim_result,
    DROP COLUMN IF EXISTS dmarc_result;
