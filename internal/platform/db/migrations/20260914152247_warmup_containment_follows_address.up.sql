-- Warmup containment must follow the ADDRESS, not the mailbox row.
--
-- UpsertWarmupParticipant already refuses to release a sealed lane across a
-- DISABLE (which deletes the participant row and leaves the mailbox alone). The
-- open path changed the KEY instead of removing the row: mailboxes.id defaults to
-- gen_random_uuid(), so deleting a mailbox and adding the same address again
-- minted a NEW id, and a carry-forward keyed on that id read none of the
-- address's history. A quarantined or blocked address came back to 'probation' —
-- a lane that may send and may take new campaign leads — off two ordinary UI
-- actions any member with mailboxes:write can perform.
--
-- Two things had to change, and the second is the one that is easy to miss.
--
--   1. The trail has to be READABLE under the new id. It now records the address
--      the transition was written about, so a lookup can key on (workspace_id,
--      canonical address) instead of on a row id that a delete/re-add rotates.
--
--   2. The trail has to SURVIVE. 000054 gave warmup_state_transitions a composite
--      FK to mailboxes(id, workspace_id) ON DELETE CASCADE, so deleting a mailbox
--      did not orphan its containment record — it ERASED it. Recording the address
--      on a row that is about to be deleted would fix nothing, so the cascade goes.
--
-- The trail is an append-only audit log, and an audit log that a subject can
-- delete by deleting themselves is not one. It stays bounded per tenant: the
-- workspaces FK still cascades, so removing a workspace still removes its trail.

ALTER TABLE warmup_state_transitions ADD COLUMN mailbox_email TEXT;

-- Backfill from the mailbox each row was written against.
--
-- Under the FK this migration is about to drop, EVERY existing row still has its
-- mailbox: the cascade guaranteed it, by destroying the rows that would have
-- become orphans. The COALESCE therefore fires nowhere today and exists so an
-- installation whose FK was already gone migrates instead of aborting mid-deploy.
--
-- '' means "the mailbox was gone before this column existed, so this row can no
-- longer name its address". That is genuinely unrecoverable — an address is only
-- derivable from a row that still exists — and it reads correctly rather than
-- dangerously: every lookup below compares against a live mailbox's canonical
-- address, which no reachable mailbox can have as ''. Containment we cannot name
-- is containment we cannot enforce, and it must not silently attach to someone
-- else's address.
--
-- Whatever containment was destroyed by mailbox deletions BEFORE this migration
-- is not recoverable by it. Those rows are gone, not orphaned.
UPDATE warmup_state_transitions t
SET mailbox_email = COALESCE((
        SELECT lower(btrim(m.email))
        FROM mailboxes m
        WHERE m.id = t.mailbox_id AND m.workspace_id = t.workspace_id
    ), '');

-- NOT NULL rather than nullable, unlike the lane columns 000055 added. Those were
-- nullable because rows written before lanes existed genuinely had no lane;
-- here every row CAN name its address (the backfill above is total), and a writer
-- that forgets the column must fail loudly at the INSERT rather than quietly
-- append a containment record no lookup can ever find.
ALTER TABLE warmup_state_transitions ALTER COLUMN mailbox_email SET NOT NULL;

-- The canonical form is STRUCTURAL, so "both sides lowercase it" is not a
-- convention four queries have to remember.
--
-- lower() matches mailboxes_workspace_email_key, which is UNIQUE (workspace_id,
-- lower(email)) — email identity is already case-insensitive product-wide, so two
-- spellings that differ only in case cannot be two mailboxes and must not be two
-- containment histories. btrim() is deliberately STRICTER than that index, which
-- does not trim: the service canonicalizes and refuses whitespace on every
-- mailbox write, but a row written before that guard (or by direct SQL) could
-- carry it, and there uniqueness is weaker than it looks. Containment being
-- stricter than uniqueness is safe; the reverse would move the laundering path
-- rather than close it.
ALTER TABLE warmup_state_transitions
    ADD CONSTRAINT warmup_state_transitions_mailbox_email_canonical
    CHECK (mailbox_email = lower(btrim(mailbox_email)));

-- The cascade that erased the record. mailbox_id stays, NOT NULL and no longer a
-- reference: it names the row the transition happened to, which remains true
-- after that row is gone. The sole writer re-proves the (mailbox, workspace) pair
-- against mailboxes in SQL — an INNER JOIN in ApplyWarmupParticipantTransition —
-- so the write-time guarantee this constraint gave is kept where the write is.
ALTER TABLE warmup_state_transitions
    DROP CONSTRAINT warmup_state_transitions_mailbox_id_workspace_id_fkey;

-- Every read of this table is now keyed by address, in exactly this order:
-- equality on workspace_id and mailbox_email, newest first. The carry-forward and
-- the two evaluator laterals take the newest matching row; ListWarmupTransitions
-- pages it.
CREATE INDEX idx_warmup_state_transitions_address
    ON warmup_state_transitions (workspace_id, mailbox_email, created_at DESC);

-- Which leaves the id-keyed index with no reader. An index earns its write cost
-- from a query that needs it, and after the re-key none does.
DROP INDEX idx_warmup_state_transitions_mailbox;
