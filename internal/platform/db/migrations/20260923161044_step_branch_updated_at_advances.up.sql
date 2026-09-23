-- sequence_step_branches.updated_at becomes the branch's concurrency token: a
-- PUT/DELETE may carry expected_updated_at and applies only if the stored value
-- still equals it (internal/app/sequencestep/branchstore.go). A token has to
-- change on EVERY write, and a plain `updated_at = now()` in the upsert does not
-- guarantee that:
--   * now() is the TRANSACTION start, taken before the graph lock is acquired,
--     so a transaction that began first but got the lock second writes an OLDER
--     value than the one it replaced;
--   * two writes can share a microsecond, or the clock can step backwards, and
--     the token would then repeat — a stale client's precondition would match;
--   * ON DELETE SET NULL (yes_step_id / no_step_id) rewrites a row when its
--     target step is deleted, and no query of ours runs to bump the column.
--
-- So the database owns the column, for every insert and every update including
-- the referential action: an insert takes clock_timestamp() (the statement's
-- real time, not the transaction's), and an update takes the later of
-- clock_timestamp() and one microsecond past the old value — strictly advancing
-- per row regardless of the clock. A timestamp rather than a version integer so
-- a branch deleted and re-created does not restart at a value a stale client
-- still holds, and so the already-served updated_at field is the token rather
-- than a second field saying the same thing.
CREATE FUNCTION sequence_step_branches_advance_updated_at() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'UPDATE' THEN
        NEW.updated_at := GREATEST(clock_timestamp(), OLD.updated_at + interval '1 microsecond');
    ELSE
        NEW.updated_at := clock_timestamp();
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER sequence_step_branches_advance_updated_at
BEFORE INSERT OR UPDATE ON sequence_step_branches
FOR EACH ROW EXECUTE FUNCTION sequence_step_branches_advance_updated_at();
