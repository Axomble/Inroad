-- Warmup had no notion of WHERE a mailbox is, so warmup.NextDue computed its
-- waking-hours window in UTC for every participant: a US-based sender warmed up
-- between 03:00 and 15:00 local, which is the opposite of the human-looking
-- ramp warmup exists to produce.
--
-- Campaigns solved the same problem in 000031_send_windows with campaigns.timezone,
-- and its note explains the shape of the bug: the send path "would happily deliver
-- at 03:00 local on a Sunday". This is that fix for the warmup side.
--
-- Per PARTICIPANT rather than per workspace: mailboxes belong to individual reps,
-- and a workspace with people in two regions must warm each mailbox on its own
-- clock. TEXT rather than an enum for the same reason campaigns chose TEXT — the
-- IANA database changes, and validation belongs at the boundary
-- (time.LoadLocation), not in a constraint we would have to migrate.
--
-- 'UTC' default preserves exactly today's behaviour for every existing row, so
-- this migration changes no schedule until an operator sets a zone.
ALTER TABLE warmup_participants
    ADD COLUMN timezone TEXT NOT NULL DEFAULT 'UTC';
