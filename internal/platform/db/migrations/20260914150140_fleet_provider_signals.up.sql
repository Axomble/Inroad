-- Fleet: the two substrates placement scoring, rotation and quarantine are
-- missing. Neither is read by the placement path yet; both exist so that it can
-- stop guessing.
--
-- WHY THIS IS PER-WORKER AND NOT PER-MAILBOX
--
-- Risk-band segregation (migration 20260914104206) partitions workers by a
-- mailbox's warmup lane, and that lane is derived entirely from RECIPIENT-side
-- signals: hard bounces, complaints, spam placement. But Inroad never delivers
-- to a recipient's MX. Every send authenticates to the CUSTOMER's own provider
-- (their SMTP relay, the Gmail API, or Graph) and that provider delivers from
-- its own outbound pool -- NetSender.Send dials mailboxes.smtp_host, and there
-- is no MX lookup anywhere in internal/platform/mail.
--
-- So the recipient never sees the worker's egress IP, and recipient-side
-- reputation cannot transfer between mailboxes that share a worker. The
-- PROVIDER, however, sees that IP on every authentication and every send, and
-- throttles it, challenges sign-ins from it, and rate-limits it. That is the
-- real per-IP risk, and nothing measured it until this table.

-- worker_provider_signals: what the provider told ONE worker, per window.
--
-- GLOBAL INFRASTRUCTURE, NOT TENANT DATA -- the same trust domain as `workers`
-- (migration 000017's split), and for the same reason: a row here is a fact
-- about an egress IP's standing with a provider, which is a property of the
-- fleet host and not of any tenant. Several workspaces' mailboxes share one
-- worker, so there is no workspace this row could honestly be pinned to, and
-- inventing one would make a fleet-wide fact look tenant-scoped. Never returned
-- on a tenant-facing API (security invariant 24's rule for `workers`).
--
-- COUNTERS ARE WINDOW DELTAS, NOT RUNNING TOTALS. Each row is the number of
-- events a worker accumulated in memory between two flushes. Aggregation is
-- therefore a plain SUM over a time range with no per-worker baseline to track,
-- a worker restart costs at most one partial window instead of corrupting a
-- cumulative series, and a lost flush loses a window of counts and nothing else.
--
-- REASON IS CLASSIFIED AT THE CAPTURE POINT, never here. The worker parses the
-- provider's reply (SMTP code + RFC 3463 enhanced status; HTTP status + machine
-- reason token) while it still holds it, and stores the verdict. Storing raw
-- response strings instead would push that parsing onto every reader and every
-- future query -- and onto whoever writes the operator dashboard.
CREATE TABLE worker_provider_signals (
    id            BIGSERIAL PRIMARY KEY,
    worker_id     TEXT NOT NULL,
    -- Which transport leg ran. Mirrors mailboxes.provider's own CHECK
    -- (migration 000028), and the worker normalises onto it using the SAME rule
    -- mail.MultiSender dispatches on, so the label always names the leg that
    -- actually dialed rather than whatever string was stored.
    provider      TEXT NOT NULL CHECK (provider IN ('smtp', 'gmail', 'm365')),
    -- What the leg was doing. Without this dimension an auth failure on a poll
    -- and one on a send pool into the same counter, and "send attempts vs
    -- successes" becomes uncomputable.
    operation     TEXT NOT NULL CHECK (operation IN ('send', 'poll')),
    -- The classified verdict. Mirrors providersignal.Reason exactly; that
    -- package's Classify is asserted to return only these values, and its
    -- Collector normalises anything else to 'other' before it can reach here --
    -- one stray value would fail the whole batch insert, not just its own row.
    --
    -- 'rejected' is deliberately part of the vocabulary and deliberately NOT a
    -- per-IP signal: a permanent non-security 5xx is about the recipient (dead
    -- address, oversized message). It is counted so attempts and successes
    -- reconcile. 'blocked' and 'rate_limited' are the per-IP signals.
    reason        TEXT NOT NULL CHECK (reason IN (
        'ok', 'auth_failed', 'rate_limited', 'throttled',
        'blocked', 'rejected', 'unreachable', 'other'
    )),
    events        BIGINT NOT NULL CHECK (events > 0),
    window_start  TIMESTAMPTZ NOT NULL,
    window_end    TIMESTAMPTZ NOT NULL,
    CONSTRAINT worker_provider_signals_window_check CHECK (window_end >= window_start)
);

-- The one read shape that exists: "what has this worker been told lately",
-- grouped by reason. window_end DESC leads because every query is a recency
-- window; worker_id first because a per-worker rollup is the only access path a
-- placement scorer or an operator dashboard has.
CREATE INDEX worker_provider_signals_worker_window
    ON worker_provider_signals (worker_id, window_end DESC);

-- Retention. window_end alone, so the purge is index-backed independently of
-- worker_id (the composite above leads with worker_id and cannot serve a
-- fleet-wide age scan). Invariant 55's rule: a table nothing in the application
-- ever deletes from needs a sweep from day one, not after it has grown.
CREATE INDEX worker_provider_signals_retention
    ON worker_provider_signals (window_end);

-- fleet_decisions: every automated fleet decision, append-only.
--
-- This is what makes "why is this mailbox on this worker?" answerable. Placement
-- today leaves no trace: an assignment row records the ANSWER and never the
-- reasoning, so a mailbox that moved, or one that was refused a placement
-- entirely, is indistinguishable from one nothing ever considered.
--
-- WHY workspace_id IS HERE, against the brief that specified only
-- {kind, worker_id, mailbox_id, reason, triggered_by, created_at}:
--
-- a row naming a mailbox_id IS tenant data by this repo's own rule.
-- mailbox_worker_assignments carries workspace_id for exactly this reason
-- (security invariant 24 calls it tenant data specifically because it names a
-- mailbox), while `workers` carries none because it names no tenant row. A
-- decision log that names mailboxes but cannot be filtered by tenant cannot
-- answer "show me decisions about MY mailboxes" without joining mailboxes to
-- find out -- which is the join a forgotten predicate turns into a leak.
--
-- It is NULLABLE, and the CHECK below ties it to mailbox_id: a mailbox-scoped
-- decision always carries its tenant, a fleet-scoped one (quarantine this
-- worker) carries neither. So there is no workspace_id here that cannot be
-- legitimately filled.
--
-- The composite FK makes a cross-tenant row UNREPRESENTABLE rather than merely
-- rejected in Go -- migration 000028's pattern, which mailboxes' UNIQUE (id,
-- workspace_id) exists to serve.
--
-- ON DELETE CASCADE, so deleting a mailbox takes its decision history with it.
-- Deliberate: this log exists to explain a mailbox's CURRENT placement, and that
-- question stops existing with the mailbox. It is operability, not an audit
-- trail with an independent retention obligation.
CREATE TABLE fleet_decisions (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    -- What was decided. 'refused' is not in the brief's list and is the one the
    -- only live call site needs: strict band segregation declining to place a
    -- mailbox is a decision, and it was previously invisible (the warmup sweep
    -- counted it as an anonymous failure and continued).
    kind          TEXT NOT NULL CHECK (kind IN ('assign', 'rotate', 'quarantine', 'refused')),
    -- NULL when the decision names no worker: a refusal has no destination, and
    -- that is the honest representation of it.
    worker_id     TEXT,
    mailbox_id    UUID,
    workspace_id  UUID,
    -- Human-readable prose an operator can act on, built only through
    -- internal/platform/fleetdecision's Reason constructors. An entry must never
    -- print a score comparison it did not actually make: a forced decision
    -- (worker unhealthy, one candidate, none) has no runner-up, and rendering it
    -- against a 0.00 that was never computed makes the log lie.
    reason        TEXT NOT NULL CHECK (length(reason) > 0),
    -- Who decided: 'auto:assign' | 'auto:rotate' | 'auto:quarantine' |
    -- 'operator:<user id>'. Free text rather than a CHECK, because the operator
    -- form carries an id and a CHECK over a prefix pattern would have to be
    -- rewritten for every new automated actor.
    triggered_by  TEXT NOT NULL CHECK (length(triggered_by) > 0),
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- A mailbox-scoped decision carries its tenant; a fleet-scoped one carries
    -- neither. Nothing in between is representable, so a row can never name a
    -- mailbox the tenant filter would miss.
    CONSTRAINT fleet_decisions_tenant_pairing_check
        CHECK ((mailbox_id IS NULL) = (workspace_id IS NULL)),
    CONSTRAINT fleet_decisions_mailbox_fkey
        FOREIGN KEY (mailbox_id, workspace_id) REFERENCES mailboxes(id, workspace_id) ON DELETE CASCADE
);

-- "Why is THIS mailbox here" -- the question the table exists to answer.
CREATE INDEX fleet_decisions_mailbox
    ON fleet_decisions (workspace_id, mailbox_id, created_at DESC)
    WHERE mailbox_id IS NOT NULL;

-- "What has this worker been doing" -- the operator dashboard's other axis, and
-- the only access path for a fleet-scoped decision that names no mailbox.
CREATE INDEX fleet_decisions_worker
    ON fleet_decisions (worker_id, created_at DESC)
    WHERE worker_id IS NOT NULL;

-- Retention, same reasoning as worker_provider_signals above.
CREATE INDEX fleet_decisions_retention ON fleet_decisions (created_at);
