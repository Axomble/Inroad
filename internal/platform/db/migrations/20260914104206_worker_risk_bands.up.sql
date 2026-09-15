-- Fleet F5: risk-band segregation. A mailbox whose deliverability is
-- degrading must never share a worker's egress IP with a healthy one, or its
-- reputation damage is inherited by every mailbox on that IP.
--
-- band lives on the ASSIGNMENT row, not on `workers`. `workers` is a GLOBAL
-- infrastructure registry with no tenant-derived column (migration 000017's
-- split); a mailbox's risk band is derived from ITS OWN warmup lane — tenant
-- data — so it belongs beside the tenant-scoped assignment it decided, not on
-- the shared worker row. A worker's own "current band" is therefore implicit:
-- whichever band its live assignments carry (enforced by the picker in
-- AssignMailboxWorker, not by a constraint here) — because a single-live-worker
-- deployment (self-host) is deliberately allowed to mix bands: with no choice
-- of worker, segregation would only mean refusing to send.
--
-- Defaulted to 'healthy' for existing rows and then backfilled below from the
-- mailbox's current warmup lane, so a degraded mailbox is not misclassified
-- as healthy until its next assignment happens to run. Anything the backfill
-- cannot see (no warmup_participants row: not a warmup participant at all)
-- keeps the default 'healthy' — the same "opting out of warmup costs nothing"
-- rule RiskBandForLane applies live.
ALTER TABLE mailbox_worker_assignments
    ADD COLUMN band TEXT NOT NULL DEFAULT 'healthy';
ALTER TABLE mailbox_worker_assignments
    ADD CONSTRAINT mailbox_worker_assignments_band_check
    CHECK (band IN ('healthy', 'degraded'));

UPDATE mailbox_worker_assignments a
SET band = 'degraded'
FROM warmup_participants p
WHERE p.mailbox_id = a.mailbox_id
  AND p.workspace_id = a.workspace_id
  AND p.lane <> 'healthy';

-- Serves the per-worker band lookups added in fix-round-1: PickPureWorkerForBand's
-- EXISTS(band = X)/NOT EXISTS(band <> X) pair and PickMixedWorker's
-- COUNT(DISTINCT band). The pre-existing plain worker_id index still serves the
-- load COUNT(*) subquery shared by every pick and PickIdleLiveWorker's NOT EXISTS.
CREATE INDEX mailbox_worker_assignments_worker_band
    ON mailbox_worker_assignments (worker_id, band);
