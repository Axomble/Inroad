-- Reverse of 20260914152247. Order matters: restore the id-keyed index, then the
-- rows the FK is allowed to see, then the FK, then drop the column the address
-- key lives in (which would take the CHECK and the address index with it anyway —
-- both are dropped explicitly so this file states what it undoes).

CREATE INDEX idx_warmup_state_transitions_mailbox
    ON warmup_state_transitions (workspace_id, mailbox_id, created_at DESC);

DROP INDEX idx_warmup_state_transitions_address;

-- Restoring the FK means restoring what it enforced, and it CANNOT be added while
-- a row points at a mailbox that no longer exists. Those rows are exactly the ones
-- the cascade would already have deleted, so this deletes them — a lossy rollback,
-- stated plainly: rolling back re-adopts the behaviour that a mailbox deletion
-- erases the address's containment record, which is the defect the up migration
-- exists to fix.
DELETE FROM warmup_state_transitions t
WHERE NOT EXISTS (
    SELECT 1 FROM mailboxes m
    WHERE m.id = t.mailbox_id AND m.workspace_id = t.workspace_id
);

ALTER TABLE warmup_state_transitions
    ADD CONSTRAINT warmup_state_transitions_mailbox_id_workspace_id_fkey
    FOREIGN KEY (mailbox_id, workspace_id)
    REFERENCES mailboxes(id, workspace_id) ON DELETE CASCADE;

ALTER TABLE warmup_state_transitions
    DROP CONSTRAINT warmup_state_transitions_mailbox_email_canonical;

ALTER TABLE warmup_state_transitions DROP COLUMN mailbox_email;
