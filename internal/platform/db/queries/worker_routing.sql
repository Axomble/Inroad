-- name: UpsertWorker :exec
-- Heartbeat: register or refresh this worker's row. egress_ip is recorded for
-- observability; id_family records which identity source produced worker_id
-- (ipv4 | ipv6 | hostname | override — see internal/platform/workerid), so a
-- NAT'd/hostname-derived worker is diagnosable from an IP-derived one without
-- cross-referencing logs; last_seen_at drives the live-worker window in the
-- assigner.
INSERT INTO workers (worker_id, egress_ip, id_family, last_seen_at)
VALUES ($1, $2, $3, now())
ON CONFLICT (worker_id)
DO UPDATE SET egress_ip = EXCLUDED.egress_ip, id_family = EXCLUDED.id_family, last_seen_at = now();

-- name: GetLiveMailboxWorkerAssignment :one
-- Existing assignment for a mailbox, but ONLY if the assigned worker is still
-- live (heartbeat at or after live_since). Workspace-pinned: tenant data, so a
-- foreign workspace_id matches zero rows.
--
-- The liveness join is the whole point. An assignment whose worker has stopped
-- heartbeating routes to "w:<dead-id>" — a queue no process consumes — so its
-- tasks neither run nor fail nor alert; the mailbox silently stops sending.
-- Treating a dead worker's assignment as absent lets the caller reassign it.
-- There is deliberately no FK from mailbox_worker_assignments to workers: the
-- assignment outlives a worker restart that reuses the same id (the common
-- case, and the one where keeping the pin preserves egress-IP stability).
--
-- band travels with the row (fleet F5: risk-band segregation) so the caller can
-- compare it against the mailbox's CURRENT computed band without a second
-- query: a mismatch (the mailbox's warmup lane moved since this row was
-- written) is treated exactly like a dead worker — fall through and reassign.
--
-- worker_mixed reports whether the ASSIGNED WORKER currently carries more than
-- one band (fix-round-1, Important 2's convergence design). It is what lets
-- the caller's idempotent fast path distinguish "stable, leave it" from "this
-- mailbox is parked on a legacy/last-resort mixed worker, and should keep
-- checking whether a pure or idle worker has since become available" — without
-- it, a mailbox landed on a mixed worker via the tier-3 fallback would never
-- re-evaluate and would sit there forever even after capacity opened up,
-- because its OWN band never changes.
SELECT a.worker_id, a.band,
       (SELECT count(DISTINCT a2.band) FROM mailbox_worker_assignments a2 WHERE a2.worker_id = a.worker_id) > 1 AS worker_mixed
FROM mailbox_worker_assignments a
JOIN workers w ON w.worker_id = a.worker_id
WHERE a.mailbox_id = $1
  AND a.workspace_id = $2
  AND w.last_seen_at >= @live_since::timestamptz;

-- name: PickLeastLoadedWorker :one
-- The least-loaded LIVE worker (heartbeat at or after live_since), with NO band
-- filter. Used only when the fleet has at most one live worker (self-host: there
-- is no second worker to segregate onto, so segregation must not apply — refusing
-- would stop self-host from sending) — the band-aware picks below are used
-- otherwise. Load is the current assignment count across ALL workspaces — workers
-- are global infra, so balancing is fleet-wide, not per-tenant. Deterministic
-- worker_id tie-break. No live worker => zero rows (the caller falls back to the
-- shared default queue).
SELECT w.worker_id
FROM workers w
WHERE w.last_seen_at >= @live_since::timestamptz
ORDER BY (
    SELECT count(*) FROM mailbox_worker_assignments a WHERE a.worker_id = w.worker_id
) ASC, w.worker_id ASC
LIMIT 1;

-- name: CountLiveWorkers :one
-- How many workers are currently live. Drives the self-host bypass: at most one
-- live worker means there is no placement CHOICE to make, so AssignMailboxWorker
-- skips band matching entirely and falls back to PickLeastLoadedWorker — the
-- exact pre-F5 behaviour, byte for byte, for the single-worker topology.
SELECT count(*) FROM workers WHERE last_seen_at >= @live_since::timestamptz;

-- name: PickPureWorkerForBand :one
-- Tier 1 (fix-round-1, Important 2). The least-loaded LIVE worker that is
-- PURELY this band — EVERY assignment it currently carries shares `band`,
-- never a worker that merely carries SOME of this band alongside others.
--
-- The original round-1 query (`... EXISTS (a.band = @band)`, no exclusion of
-- other bands) proved only "carries at least one row of this band", which a
-- MIXED worker satisfies for BOTH bands simultaneously. On a real fleet —
-- where placement was band-blind before this feature, and a brand-new
-- warmup_participants row defaults to lane='probation' (migration
-- 000055:12), which RiskBandForLane maps to degraded — essentially every
-- worker already carries a mix on migration day, so that query treated every
-- worker as a valid pick for every band: idle promotion (and therefore
-- ErrNoBandCapacity, the strictness guarantee) effectively never fired. Never
-- widening a pure worker's population with the WRONG band is what "never
-- newly mix a worker that is currently pure" means in practice — this tier
-- is the only one placement may pick from for FREE (see PickMixedWorker for
-- the last-resort tier that costs a warning log).
SELECT w.worker_id
FROM workers w
WHERE w.last_seen_at >= @live_since::timestamptz
  AND EXISTS (
      SELECT 1 FROM mailbox_worker_assignments a
      WHERE a.worker_id = w.worker_id AND a.band = @band::text
  )
  AND NOT EXISTS (
      SELECT 1 FROM mailbox_worker_assignments a
      WHERE a.worker_id = w.worker_id AND a.band <> @band::text
  )
ORDER BY (
    SELECT count(*) FROM mailbox_worker_assignments a WHERE a.worker_id = w.worker_id
) ASC, w.worker_id ASC
LIMIT 1;

-- name: PickIdleLiveWorker :one
-- Tier 2 (requirement 3's promotion path). A live worker carrying NO
-- assignments at all (any band) — only a genuinely idle worker may be
-- adopted into a band, never one already carrying another band's mailboxes.
-- Deterministic worker_id tie-break, matching the other picks.
--
-- MUST be called inside the SAME transaction as the LockWorkerPromotion
-- advisory lock immediately before it, and the InsertMailboxWorkerAssignment
-- that follows it (see coreapi/inprocess.client.claimIdleWorker). Without the
-- lock, two DIFFERENT mailboxes of DIFFERENT bands racing to place
-- concurrently can BOTH see the same worker as idle (neither has inserted
-- yet) and both succeed — mixing a worker the promotion path exists to keep
-- pure (fix-round-1, Important 1; TestAssignMailboxWorkerIdlePromotionRaceNeverMixesAWorker
-- proves it against the unlocked version first). ON CONFLICT on
-- mailbox_worker_assignments cannot catch this: the two racing INSERTs are
-- for DIFFERENT mailbox_ids, so they never conflict with each other.
SELECT w.worker_id
FROM workers w
WHERE w.last_seen_at >= @live_since::timestamptz
  AND NOT EXISTS (
      SELECT 1 FROM mailbox_worker_assignments a WHERE a.worker_id = w.worker_id
  )
ORDER BY w.worker_id ASC
LIMIT 1;

-- name: LockWorkerPromotion :exec
-- A single global advisory lock, held for the transaction (auto-released at
-- COMMIT or ROLLBACK — never the session-scoped form, which could strand the
-- lock on a pooled connection reused for something else after a crash)
-- serializing ONLY the tier-2 idle-promotion decision (fix-round-1,
-- Important 1).
--
-- Scoped narrowly on purpose. Tier 1 (PickPureWorkerForBand) and tier 3
-- (PickMixedWorker) need NO lock: concentrating another mailbox onto an
-- ALREADY-committed-band or already-mixed worker always inserts a DISTINCT
-- row (mailbox_id is the assignment's primary key), so two concurrent
-- inserts for different mailboxes never conflict — the race exists only
-- because ADOPTING an idle worker changes what "pure" means for that worker
-- for every future placement, and only one band may win that decision.
--
-- One fixed key rather than one per band or per worker: the lock is held only
-- across a single SELECT + INSERT (microseconds), and it is only ever
-- CONTENDED while a band has zero committed workers — i.e. during a fleet's
-- initial ramp-up, not steady-state operation, where tier 1 already satisfies
-- every placement without ever reaching this lock at all.
SELECT pg_advisory_xact_lock(hashtext('inroad:worker_idle_promotion'));

-- name: PickMixedWorker :one
-- Tier 3 (fix-round-1, Important 2's convergence design; last resort). The
-- least-loaded LIVE worker that is ALREADY mixed — carries more than one
-- band today, from before this feature existed or from the lane-derived
-- migration-day reality PickPureWorkerForBand's doc describes. Placing one
-- more mailbox here is never a NEW contamination — the worker was already
-- impure — and refusing when this is the only capacity left would stop a
-- real, already-mixed fleet from sending at all on its very first deploy,
-- which is a worse failure than an imperfectly segregated placement.
--
-- This is not a permanent home: every mailbox landed here still migrates off
-- lazily on its own NEXT AssignMailboxWorker call once a pure or idle worker
-- becomes available — GetLiveMailboxWorkerAssignment's worker_mixed flag is
-- what makes the caller keep re-evaluating a mailbox parked here instead of
-- treating it as stable forever, the same way a lane change does (requirement
-- 4) — so a fleet converges toward purity over time. The CALLER logs every
-- use of this tier so an operator can watch that convergence happen.
SELECT w.worker_id
FROM workers w
WHERE w.last_seen_at >= @live_since::timestamptz
  AND (
      SELECT count(DISTINCT a.band) FROM mailbox_worker_assignments a WHERE a.worker_id = w.worker_id
  ) > 1
ORDER BY (
    SELECT count(*) FROM mailbox_worker_assignments a WHERE a.worker_id = w.worker_id
) ASC, w.worker_id ASC
LIMIT 1;

-- name: InsertMailboxWorkerAssignment :one
-- Persist an assignment (with its risk band, fleet F5). Self-enforcing tenancy
-- (defense in depth): the row is written ONLY when the mailbox truly belongs to
-- the workspace, so a mismatched (mailbox, workspace) pair inserts zero rows and
-- RETURNING yields pgx.ErrNoRows — the caller maps that to a cross-tenant
-- rejection.
--
-- On a mailbox_id conflict the row is claimed for the incoming (worker, band)
-- ONLY if the incumbent is LIVE, already in the same band as the incoming
-- write, AND NOT CURRENTLY MIXED; otherwise the incumbent is replaced. That
-- single rule serves four callers:
--
--   * concurrent first-send race — both racers computed the SAME target band for
--     this mailbox (it hasn't changed mid-race), so whichever inserted first
--     wins and the loser's EXCLUDED.band matches the winner's stored band; the
--     existing row wins unchanged and both resolve to the same queue.
--   * reassignment after a worker died — the incumbent is not live, so the row
--     moves to the caller's freshly-picked worker instead of being pinned to a
--     queue nobody consumes.
--   * a mailbox's warmup lane changed band since it was last assigned — the
--     incumbent is live but its stored band now disagrees with EXCLUDED.band
--     (the caller already re-picked a worker matching the NEW band before
--     calling this), so the row moves even though the old worker is still up.
--     This is how a degrading mailbox's NEXT assignment lands in the degraded
--     band (requirement 4): AssignMailboxWorker recomputes and compares the
--     band on every call, so the very next warmup tick after a lane change
--     picks this branch.
--   * a mailbox is parked on an already-mixed worker, its OWN band hasn't
--     changed, but the caller found a pure or idle worker to move it to
--     (fix-round-1, Important 2's convergence design). Without the "not
--     mixed" clause, the incumbent's band matching EXCLUDED.band alone would
--     keep the row on the mixed worker forever — the caller's whole tiered
--     re-pick would be silently discarded here, because band-match was the
--     ONLY signal this statement used to check before that fix. The mixed
--     check is evaluated against mailbox_worker_assignments.worker_id (the
--     row's CURRENT, not-yet-updated worker) so it reads the incumbent's
--     population INCLUDING this row's own not-yet-moved band, matching
--     GetLiveMailboxWorkerAssignment's worker_mixed flag exactly.
--
-- Keeping this as one atomic upsert (rather than a DELETE + INSERT in the
-- caller) means two workers reassigning the same stranded mailbox converge: the
-- first takes it, the second sees a live, same-band, non-mixed incumbent and
-- adopts that answer.
INSERT INTO mailbox_worker_assignments (mailbox_id, workspace_id, worker_id, band)
SELECT $1, $2, $3, $4 FROM mailboxes WHERE id = $1 AND workspace_id = $2
ON CONFLICT (mailbox_id)
DO UPDATE SET worker_id = CASE
    WHEN EXISTS (
        SELECT 1 FROM workers w
        WHERE w.worker_id = mailbox_worker_assignments.worker_id
          AND w.last_seen_at >= @live_since::timestamptz
    ) AND mailbox_worker_assignments.band = EXCLUDED.band
    AND (
        SELECT count(DISTINCT a2.band) FROM mailbox_worker_assignments a2
        WHERE a2.worker_id = mailbox_worker_assignments.worker_id
    ) <= 1
    THEN mailbox_worker_assignments.worker_id
    ELSE EXCLUDED.worker_id
END,
band = CASE
    WHEN EXISTS (
        SELECT 1 FROM workers w
        WHERE w.worker_id = mailbox_worker_assignments.worker_id
          AND w.last_seen_at >= @live_since::timestamptz
    ) AND mailbox_worker_assignments.band = EXCLUDED.band
    AND (
        SELECT count(DISTINCT a2.band) FROM mailbox_worker_assignments a2
        WHERE a2.worker_id = mailbox_worker_assignments.worker_id
    ) <= 1
    THEN mailbox_worker_assignments.band
    ELSE EXCLUDED.band
END,
assigned_at = CASE
    WHEN EXISTS (
        SELECT 1 FROM workers w
        WHERE w.worker_id = mailbox_worker_assignments.worker_id
          AND w.last_seen_at >= @live_since::timestamptz
    ) AND mailbox_worker_assignments.band = EXCLUDED.band
    AND (
        SELECT count(DISTINCT a2.band) FROM mailbox_worker_assignments a2
        WHERE a2.worker_id = mailbox_worker_assignments.worker_id
    ) <= 1
    THEN mailbox_worker_assignments.assigned_at
    ELSE now()
END
RETURNING worker_id;

-- name: MailboxWorkerAssignmentExists :one
-- Observability only, and deliberately called from ONE branch:
-- AssignMailboxWorker's "no live assignment" path, after
-- GetLiveMailboxWorkerAssignment has already returned pgx.ErrNoRows and
-- before picking a replacement worker. At that point ErrNoRows is ambiguous
-- between "never assigned" (the common first-send case, nothing worth
-- reporting) and "assigned, but the incumbent fell out of the live window"
-- (liveness expiry — previously silent; see the F3 spec's observability
-- requirement). This query resolves that ambiguity with a second, cheap,
-- index-backed lookup that ignores liveness entirely — by the time it runs,
-- the caller already knows the row, if any, is not live.
SELECT EXISTS (
    SELECT 1 FROM mailbox_worker_assignments
    WHERE mailbox_id = $1 AND workspace_id = $2
) AS assignment_exists;
