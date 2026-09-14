-- Reverse 20260914104450. Dropping the column drops its CHECK constraint
-- with it — nothing else to undo.
ALTER TABLE workers DROP COLUMN id_family;
