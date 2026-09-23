-- Deliberately a NO-OP. The up migration only fills a column the previous
-- migration added; resetting it to NULL here could not tell a backfilled value
-- from one a claim stamped since, and would erase the latter. Rolling back past
-- 20260923105515 drops the column, which is the real reversal.
--
-- golang-migrate needs valid SQL in the file, so it holds a statement with no
-- effect rather than nothing (see 20260828133405's down for the same posture).
SELECT 1 WHERE false;
