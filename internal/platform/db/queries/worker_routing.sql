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
-- It returns the worker and NOTHING ELSE, which is the whole of fleet F4's
-- incumbency rule: a live incumbent is kept, unconditionally. IP trust accrues
-- per (mailbox, IP) pair at the PROVIDER — it is what decides whether a sign-in
-- from that address is challenged — and moving a mailbox discards it, so no
-- packing preference is worth paying that. Two columns this query used to
-- return for F5, the stored band and a COUNT(DISTINCT band) "is the worker
-- mixed" flag, are gone with the tiers that read them: both existed to make a
-- LIVE incumbent's placement worth re-deciding, and under F4 it never is.
-- Whether a mailbox should MOVE at all is rotation's question, answered on its
-- own tick by ListRotationCandidates and RotateMailboxWorkerAssignment below,
-- not something the send path decides in passing.
SELECT a.worker_id
FROM mailbox_worker_assignments a
JOIN workers w ON w.worker_id = a.worker_id
WHERE a.mailbox_id = $1
  AND a.workspace_id = $2
  AND w.last_seen_at >= @live_since::timestamptz;

-- name: GetMailboxPlacementFacts :one
-- The two mailbox facts placement scores on, in one workspace-pinned round
-- trip: which transport leg it runs (mailboxes.provider — an API-backed mailbox
-- occupies a worker far less than an SMTP+IMAP one) and its warmup lane, from
-- which the caller derives the risk band through warmup.RiskBandForLane. The
-- LANE is returned rather than a band computed here, so that mapping stays in
-- one place instead of being re-implemented in SQL.
--
-- The LEFT JOIN is load-bearing: a mailbox with no warmup_participants row is
-- not enrolled in warmup at all, and gets an empty lane, which RiskBandForLane
-- reads as healthy. Opting out of warmup must not cost a mailbox placement.
--
-- Zero rows means the mailbox does not belong to this workspace. The caller
-- fails closed on that (coreapi.ErrCrossTenant) BEFORE any placement work,
-- rather than discovering it at the insert.
SELECT m.provider, coalesce(p.lane, '')::text AS lane
FROM mailboxes m
LEFT JOIN warmup_participants p
       ON p.mailbox_id = m.id AND p.workspace_id = m.workspace_id
WHERE m.id = $1 AND m.workspace_id = $2;

-- name: PickLeastLoadedWorker :one
-- The least-loaded LIVE worker (heartbeat at or after live_since), with NO
-- scoring of any kind. Used only when the fleet has at most one live worker
-- (self-host): with no second worker there is no placement CHOICE to make, so
-- every later refinement — bands under F5, the score under F4, the health gate
-- — would only ever amount to refusing to send from the one worker there is.
-- This path is deliberately byte-for-byte what it was before either feature
-- existed. Load is the current assignment count across ALL workspaces — workers
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
-- skips scoring entirely and falls back to PickLeastLoadedWorker.
SELECT count(*) FROM workers WHERE last_seen_at >= @live_since::timestamptz;

-- name: ListPlacementCandidates :many
-- Every live worker, with the facts fleet F4 scores it on. One aggregate, one
-- round trip; the scoring itself is a pure function in
-- internal/platform/fleetscore, so the weights are unit-testable without a
-- database and this query holds no policy at all.
--
-- FLEET-WIDE BY DESIGN, and it returns no tenant row. `workers` is global
-- infrastructure (migration 000017's trust-domain split) and a worker's
-- occupancy is a fact about a host, not about a tenant. The two workspace_id
-- predicates here are NOT a tenancy scope and must not be read as one: one is
-- the composite join to mailboxes (integrity — the same (id, workspace_id) pair
-- the FK uses), the other is the FILTER that measures how much of the CALLER's
-- workspace already sits on each worker. Everything returned is a count or a
-- worker id.
--
-- The provider split is three columns rather than a weighted sum computed here
-- for the reason the whole file follows: a weight is policy, and policy that
-- lives in SQL cannot be unit-tested or tuned without a migration.
--
-- Signals are read PER PROVIDER (worker_provider_signals, migration
-- 20260914150140). A worker Google has blocked is still a perfectly good home
-- for an SMTP mailbox, so asking "how is this worker doing" fleet-wide would
-- take workers out of service for providers that are perfectly happy with them.
-- 'blocked' and 'unreachable' are the two verdicts that say the provider will
-- not talk to this address (providersignal's own doc: 'rejected' is about the
-- recipient and must never be read as per-IP risk); the softer trio is returned
-- alongside for the score, not the gate.
SELECT
    w.worker_id,
    count(mb.id) FILTER (WHERE mb.provider = 'smtp')::bigint  AS smtp_mailboxes,
    count(mb.id) FILTER (WHERE mb.provider = 'gmail')::bigint AS gmail_mailboxes,
    count(mb.id) FILTER (WHERE mb.provider = 'm365')::bigint  AS m365_mailboxes,
    count(mb.id) FILTER (WHERE a.workspace_id = @workspace_id::uuid)::bigint AS same_workspace_mailboxes,
    count(mb.id) FILTER (WHERE a.band <> @band::text)::bigint AS other_band_mailboxes,
    sig.ok_events,
    sig.block_events,
    sig.throttle_events
FROM workers w
LEFT JOIN mailbox_worker_assignments a ON a.worker_id = w.worker_id
LEFT JOIN mailboxes mb ON mb.id = a.mailbox_id AND mb.workspace_id = a.workspace_id
LEFT JOIN LATERAL (
    SELECT
        coalesce(sum(s.events) FILTER (WHERE s.reason = 'ok'), 0)::bigint AS ok_events,
        coalesce(sum(s.events) FILTER (WHERE s.reason IN ('blocked', 'unreachable')), 0)::bigint AS block_events,
        coalesce(sum(s.events) FILTER (WHERE s.reason IN ('rate_limited', 'throttled', 'auth_failed')), 0)::bigint AS throttle_events
    FROM worker_provider_signals s
    WHERE s.worker_id = w.worker_id
      AND s.provider = @provider::text
      AND s.window_end >= @signals_since::timestamptz
) sig ON TRUE
WHERE w.last_seen_at >= @live_since::timestamptz
GROUP BY w.worker_id, sig.ok_events, sig.block_events, sig.throttle_events
ORDER BY w.worker_id ASC;

-- name: InsertMailboxWorkerAssignment :one
-- Persist an assignment (with the risk band it was placed under, fleet F5).
-- Self-enforcing tenancy (defense in depth): the row is written ONLY when the
-- mailbox truly belongs to the workspace, so a mismatched (mailbox, workspace)
-- pair inserts zero rows and RETURNING yields pgx.ErrNoRows — the caller maps
-- that to a cross-tenant rejection.
--
-- On a mailbox_id conflict the incumbent row is kept UNTOUCHED if its worker is
-- LIVE, and replaced otherwise. Two callers depend on exactly that rule:
--
--   * concurrent first-send race — whichever racer inserted first wins, and
--     every later racer sees a live incumbent and adopts that answer, so all of
--     them resolve to ONE queue. Two IPs sending as one mailbox is the
--     deliverability failure the pin exists to prevent, and it is why this stays
--     a single atomic upsert rather than a DELETE + INSERT in the caller.
--   * reassignment after a worker died — the incumbent is not live, so the row
--     moves to the caller's freshly-picked worker instead of staying pinned to a
--     queue nobody consumes.
--
-- The band and mixedness conditions this CASE used to carry are gone with fleet
-- F4. Both existed to let a LATER call MOVE a mailbox off a live worker — when
-- its warmup lane changed band, or when a purer worker became available — and
-- F4 does not move mailboxes: a live incumbent is kept (see
-- GetLiveMailboxWorkerAssignment), and whether a mailbox should move at all is
-- rotation's separate decision. Keeping the conditions would have left this
-- statement able to repoint a row that the caller had already decided to leave
-- alone.
--
-- band therefore records the band the mailbox was PLACED under, and is not
-- refreshed while the mailbox stays put. That staleness is deliberate and
-- bounded: the band is the weakest term in the score precisely because a
-- warmup lane is derived from recipient-side signals the worker's egress IP is
-- invisible to, so paying a write on every resolve to keep it exact would cost
-- more than the term is worth.
INSERT INTO mailbox_worker_assignments (mailbox_id, workspace_id, worker_id, band)
SELECT $1, $2, $3, $4 FROM mailboxes WHERE id = $1 AND workspace_id = $2
ON CONFLICT (mailbox_id)
DO UPDATE SET worker_id = CASE
    WHEN EXISTS (
        SELECT 1 FROM workers w
        WHERE w.worker_id = mailbox_worker_assignments.worker_id
          AND w.last_seen_at >= @live_since::timestamptz
    )
    THEN mailbox_worker_assignments.worker_id
    ELSE EXCLUDED.worker_id
END,
band = CASE
    WHEN EXISTS (
        SELECT 1 FROM workers w
        WHERE w.worker_id = mailbox_worker_assignments.worker_id
          AND w.last_seen_at >= @live_since::timestamptz
    )
    THEN mailbox_worker_assignments.band
    ELSE EXCLUDED.band
END,
assigned_at = CASE
    WHEN EXISTS (
        SELECT 1 FROM workers w
        WHERE w.worker_id = mailbox_worker_assignments.worker_id
          AND w.last_seen_at >= @live_since::timestamptz
    )
    THEN mailbox_worker_assignments.assigned_at
    ELSE now()
END
RETURNING worker_id;

-- name: ListRotationCandidates :many
-- Every assignment the rotation sweep should LOOK at this tick, with the facts
-- internal/platform/fleetrotate decides on. It decides nothing itself.
--
-- FLEET-WIDE BY DESIGN, like ListPlacementCandidates above and for the same
-- reason: whether a mailbox can still send from the egress IP it is pinned to is
-- a question about the fleet, and the workers a mailbox could move between are
-- global infrastructure (migration 000017's trust-domain split). The tenant pin
-- is not absent, it lives one step later: every row RETURNS its workspace_id,
-- and the move itself (RotateMailboxWorkerAssignment) is pinned to the
-- (mailbox, workspace) pair this row reported. The composite join to mailboxes
-- is the same (id, workspace_id) pair the FK uses.
--
-- THE WHERE CLAUSE IS A PREFILTER, NOT THE GATE. It is deliberately a SUPERSET
-- of what the policy will accept, so that policy can live in Go where it is
-- unit-testable:
--
--   * the incumbent is not live — its affinity queue has no consumer;
--   * the incumbent has ANY 'blocked'/'unreachable' event for this mailbox's
--     provider in the window. Wider than fleetscore.Eligible, which also
--     requires that nothing succeeded since; restating Eligible here would be a
--     second definition of health, and two disagreeing definitions are worse
--     than either alone;
--   * the assignment has settled past the residency floor, which is the only
--     condition under which a non-urgent move is considered at all. The caller
--     derives settled_before from the SAME policy value it then applies, so the
--     two cannot drift.
--
-- ORDER BY is a SCAN HEURISTIC and nothing more. The caller re-derives the real
-- tier in Go and re-sorts (fleetrotate.Prioritise), so if this ordering were
-- wrong the only cost would be a tick that moved less, never a wrong move. Its
-- four keys are two pairs, and the pairs answer different questions.
--
-- KEYS 1-2 DECIDE WHO PREEMPTS, and they are absolute. An incumbent that is not
-- live sorts ahead of everything (its affinity queue has no consumer, so the
-- mailbox's tasks neither run nor fail nor alert), then anything the provider has
-- refused, then everything else. Every urgent row therefore precedes every
-- merely-settled one whatever the keys below do — which is what lets the pair
-- below be a fairness device rather than a delay on a broken mailbox.
--
-- KEYS 3-4 DECIDE WHICH SETTLED ROWS THIS TICK GETS TO SEE. Together they are a
-- RING over mailbox_id starting at scan_cursor: rows at or after the cursor
-- first, then the rest, each arc in id order. The caller advances the cursor with
-- the wall clock (coreapi/inprocess.rotationScanCursor), so successive ticks read
-- successive arcs of the fleet and every qualifying row comes round.
--
-- They replaced `a.assigned_at ASC`, which starved. Ordered by residency alone
-- the LIMIT always returns the same longest-resident rows, and a settled
-- assignment is NOT changed by being scanned — so a window full of rows the
-- policy declines (the normal state of a healthy fleet, and the state this
-- prefilter is a deliberate superset for) stays full forever and every qualifying
-- assignment behind it is never examined again. On any fleet with more than
-- row_limit settled assignments that is the steady state, not an edge case.
-- Residency has not been lost: fleetrotate.Prioritise still spends the tick's
-- move budget longest-resident first, WITHIN the rows this returned.
--
-- mailboxes.id is gen_random_uuid() with no client-supplied path, and v4's fixed
-- version/variant nibbles sit below the 48 random leading bits that decide this
-- ordering, so the ring is an unbiased sample: a row's expected wait does not
-- depend on where it sits. The cursor costs nothing — the ORDER BY was already a
-- top-N sort over a fleet-wide scan of this disjunction, and swapping a timestamp
-- key for a boolean does not change that.
--
-- Paused and errored mailboxes are excluded: they are not sending, so moving one
-- buys nothing and would spend a unit of the tick's move budget and one
-- decision-log row on a mailbox nobody is waiting for.
SELECT
    a.mailbox_id,
    a.workspace_id,
    a.worker_id,
    a.assigned_at,
    mb.provider,
    -- The LANE, not a band: warmup.RiskBandForLane is the one source of truth
    -- for that mapping (see GetMailboxPlacementFacts, which returns it for the
    -- same reason). The LEFT JOIN is load-bearing in the same way too — a
    -- mailbox with no warmup_participants row is not a warmup participant at
    -- all, and an empty lane reads as healthy.
    coalesce(p.lane, '')::text AS lane,
    -- Cast explicitly: without it sqlc cannot infer the type of a bare IS NOT
    -- NULL and generates `interface{}`, which the caller would then have to
    -- type-assert at runtime.
    (w.worker_id IS NOT NULL)::boolean AS incumbent_live,
    coalesce(sig.ok_events, 0)::bigint AS incumbent_ok_events,
    coalesce(sig.block_events, 0)::bigint AS incumbent_block_events
FROM mailbox_worker_assignments a
JOIN mailboxes mb ON mb.id = a.mailbox_id AND mb.workspace_id = a.workspace_id
LEFT JOIN warmup_participants p
       ON p.mailbox_id = a.mailbox_id AND p.workspace_id = a.workspace_id
LEFT JOIN workers w
       ON w.worker_id = a.worker_id AND w.last_seen_at >= @live_since::timestamptz
LEFT JOIN LATERAL (
    SELECT
        coalesce(sum(s.events) FILTER (WHERE s.reason = 'ok'), 0)::bigint AS ok_events,
        coalesce(sum(s.events) FILTER (WHERE s.reason IN ('blocked', 'unreachable')), 0)::bigint AS block_events
    FROM worker_provider_signals s
    WHERE s.worker_id = a.worker_id
      AND s.provider = mb.provider
      AND s.window_end >= @signals_since::timestamptz
) sig ON TRUE
WHERE mb.status = 'active'
  AND (
        w.worker_id IS NULL
     OR coalesce(sig.block_events, 0) > 0
     OR a.assigned_at <= @settled_before::timestamptz
  )
ORDER BY (w.worker_id IS NOT NULL) ASC,
         coalesce(sig.block_events, 0) DESC,
         (a.mailbox_id < @scan_cursor::uuid) ASC,
         a.mailbox_id ASC
LIMIT @row_limit::int;

-- name: RotateMailboxWorkerAssignment :one
-- Move ONE mailbox from the worker it is on to another, and re-stamp the
-- residency clock the rotation floor is measured against.
--
-- Pinned to (mailbox_id, workspace_id) AND to the worker the caller decided
-- FROM. That third predicate is the concurrency guard: between the scan and this
-- write, the send path may have re-placed the mailbox itself (a dead incumbent
-- is re-placed on the next resolve — see InsertMailboxWorkerAssignment), and a
-- blind UPDATE would then undo a placement made on fresher facts than the ones
-- this decision used. A mismatch matches zero rows, so the caller skips the
-- mailbox and records nothing, which is the honest outcome: the move it decided
-- did not happen.
--
-- band is refreshed here and NOT refreshed by InsertMailboxWorkerAssignment, and
-- the difference is deliberate. That statement's band is the band a mailbox was
-- PLACED under and is left stale while the mailbox stays put, because paying a
-- write on every resolve to keep it exact would cost more than the weakest term
-- in the score is worth. A rotation is not a resolve: the row is being rewritten
-- anyway, the decision that produced it was scored under the mailbox's CURRENT
-- band, and storing the band the decision did not use would leave the row
-- disagreeing with the decision log entry beside it.
UPDATE mailbox_worker_assignments
SET worker_id = @to_worker_id::text,
    band = @band::text,
    assigned_at = now()
WHERE mailbox_id = @mailbox_id::uuid
  AND workspace_id = @workspace_id::uuid
  AND worker_id = @from_worker_id::text
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
