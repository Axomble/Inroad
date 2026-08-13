-- RENUMBERED from 000057. It collided with 000057_oauth_mailbox_ports, authored in
-- parallel against the same highest-version main; golang-migrate refuses to start at
-- all with two files on one version, so main was unable to migrate until this moved.
--
-- Every statement here is written to tolerate a re-run. An installation that applied
-- this file as version 57 before the collision has the columns already, and after the
-- renumber it will see 57 satisfied by oauth_mailbox_ports and attempt this as 58.
-- IF NOT EXISTS makes that a no-op instead of a hard failure on a duplicate column.
--
-- Warmup pair leases: a send may not fire under a lane or policy that no longer
-- holds, and a pair's daily budget must bound what the two mailboxes actually
-- exchange in BOTH directions.
-- See docs/superpowers/specs/2026-08-13-warmup-pair-leases-design.md.

-- The lease lives on the send row rather than in a warmup_pair_leases table
-- (design §3.1). warmup_sends is already the reservation — deterministic id,
-- claim-before-send, a status lifecycle, and the row the pair cap counts — so a
-- separate lease table would add a SECOND lifecycle that has to agree with this
-- one. Every repeated defect in this subsystem has been that exact shape.
--
-- All four columns are NULLABLE. Rows written before this migration genuinely
-- had no lease; back-filling a fabricated lane would put a claim in the record
-- that was never true, the same reasoning 000055 applied to warmup_state_transitions'
-- lane columns. A NULL issued_lane reads as "pre-lease" and revalidates as valid:
-- those sends have already completed.
ALTER TABLE warmup_sends
    ADD COLUMN IF NOT EXISTS issued_lane           TEXT,
    ADD COLUMN IF NOT EXISTS issued_policy_version TEXT,
    ADD COLUMN IF NOT EXISTS lease_expires_at      TIMESTAMPTZ,
    -- The constraint snapshot: which lane, which cooldown, what the pair budget
    -- was and how much of it remained at issue. Schemaless on purpose — it exists
    -- so a bad match is reproducible in an incident review rather than re-derived
    -- from state that has since moved, and a fixed schema would have to change
    -- every time the matcher gains an input.
    ADD COLUMN IF NOT EXISTS issued_constraints    JSONB;

-- Canonical, direction-free identity for the pair. GENERATED rather than written
-- by the application for the reason above: a denormalised key kept in sync by code
-- is two things that must agree.
--
-- It exists because the budget is symmetric. Expressed as
-- (from=A AND to=B) OR (from=B AND to=A), Postgres ORs two scans, and that counter
-- runs inside a LATERAL over every candidate partner on the hottest read in the
-- engine. One canonical key turns two scans into one index lookup.
ALTER TABLE warmup_sends
    ADD COLUMN IF NOT EXISTS pair_key TEXT GENERATED ALWAYS AS (
        least(from_mailbox::text, to_mailbox::text) || ':' ||
        greatest(from_mailbox::text, to_mailbox::text)
    ) STORED;

-- Serves the symmetric per-pair-per-UTC-day count in SelectWarmupPartner,
-- SelectWarmupReplyPartner and CountWarmupPairSendsToday.
--
-- The day is bounded on created_at, NOT sent_at (which the design sketched),
-- because the partial predicate below deliberately includes in-flight 'sending'
-- rows and those have sent_at IS NULL — bounding on sent_at would silently drop
-- every claimed-but-unsent row from the count and let concurrent workers overrun
-- the very cap this index exists to enforce. created_at is also immutable, so an
-- entry never moves when the row is finalized. Measured on a year of history for
-- one pair (2190 rows): trailing sent_at gives a bitmap heap scan touching 2228
-- buffers in 6.5ms; trailing created_at gives an index-only scan touching 9
-- buffers in 0.09ms, and unlike the former it does not degrade as history grows.
CREATE INDEX IF NOT EXISTS idx_warmup_sends_pair_day
    ON warmup_sends (workspace_id, pair_key, created_at)
    WHERE status IN ('sending','sent');
