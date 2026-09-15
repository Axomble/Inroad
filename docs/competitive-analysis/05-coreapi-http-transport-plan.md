# P3.8 — coreapi HTTP transport

Staged implementation plan for the last piece of "in-process now, HTTP
later" (`CLAUDE.md`, `internal/coreapi/coreapi.go:2`). Written from the
actual tree at `origin/main@ca85c71`, not from `docs/competitive-analysis/
04-parity-plan.md`'s description of P3.8/P3.9, which is now stale in one
important way — see below.

**Effort:** S ≈ days · M ≈ 1–2 weeks · L ≈ several weeks · XL ≈ months
(same scale as the parity plan). Overall program: **XL**, staged into six
independently-shippable pieces so no stage is a half-finished rewrite.

## Why this plan exists, and what it corrects

The parity plan's P3.8 row reads: *"give `coreapi` an HTTP transport, stand
up an internal-API listener, worker gets a client not a `pgxpool`, DEK
decryption moves behind it."* Three of those four clauses are **done**.
Fleet F2 (`9468cfc`, #207) shipped `internal/platform/credbroker`: a
dedicated internal listener, a shared-bearer-token `Opener` interface, and
an `HTTPOpener`/`Handler` pair that already lets a `send`-role worker run
with `INROAD_MASTER_KEY` unset entirely — DEK decryption already moves
behind it, exactly as the parity-plan row asked.

What's left is narrower than "give coreapi an HTTP transport" suggests:
**`inprocess.New` still takes a `*pgxpool.Pool` unconditionally**
(`internal/coreapi/inprocess/inprocess.go:169`), and `cmd/worker/main.go:226`
still passes one even to a fully-brokered `send`-role host, because the pool
backs `gen.New(pool)` for the ~26 business-data methods on `coreapi.Client`
that credbroker was never meant to cover (job data, claims, cursor
advances, worker routing — everything that isn't "open a secret"). That
pool needs a real `INROAD_DATABASE_URL`, so a `send`-role host today still
holds live database credentials even with brokering fully configured. THAT
is what "worker gets a client not a `pgxpool`" still means, and it's the
scope of this plan.

**Action item, not part of this plan's build:** update the parity-plan's
P3.8/P3.9 rows once this lands (Stage 5), so the next reconciliation pass
doesn't re-discover credbroker as outstanding the way this one nearly did.

## Grounding: why this is safer than it sounds

Two findings that de-risk the whole effort, worth stating up front because
they're not obvious from the interface's size alone:

**1. No distributed transaction to invent.** `coreapi.Client`'s own doc
comments describe the send path as already decomposed into idempotent,
individually-committed steps for exactly this reason — claim-before-send
plus recover-forward exists so a crash between "sent" and "cursor advanced"
self-heals on retry, not so a Go process holds a `*pgx.Tx` across two RPCs.
`MarkStepDelivered` and `AdvanceStepCursor` are already two separate
committed statements (`coreapi.go`'s own comment: *"Called immediately
after Send succeeds and BEFORE AdvanceStepCursor, so if the cursor advance
then fails, the asynq retry's claim sees the 'sent' row... and
recover-forwards instead of re-delivering"*). `FinalizeStepSend`'s
single-transaction record+advance is a **server-side** atomicity guarantee
— it happens inside one control-plane handler, same as it does today inside
one in-process call. Every method on the interface maps to exactly one HTTP
round trip with the same atomicity it has today. This is architecture the
send path already needed for crash-safety, and it happens to be
transport-shaped by construction.

**2. The god-interface finding and this plan solve each other.** An earlier
review flagged `coreapi.Client`'s ~35 methods as a single interface every
worker fake and future transport must implement in full
(`internal/coreapi/coreapi.go:227`), while six narrower capability
interfaces already exist in the same file for exactly this reason
(`CRMCaptureClient`, `InboxCaptureClient`, `WarmupEvidenceClient`,
`DeliverabilityComplaintClient`, `WarmupSendLookupClient`,
`ReplyLabelClient`). Splitting out the methods a `send`-role host actually
calls, as this plan's Stage 1 does, is the same fix — it isn't extra scope
bolted onto P3.8, it's the natural shape the transport split needs anyway.

## What a `send`-role host actually calls — and what it never does

`internal/worker/role.go` already draws this line for handler
registration; it's the same line the interface split follows. Six methods
are **sweep/fan-out only**, called exclusively by a `control`-role host
(*"stays on trusted infrastructure beside the API"* — `role.go:16`):
`ListDueEnrollments`, `ListActiveMailboxes`, `ListDueWarmupMailboxes`,
`EvaluateWarmupHealth`, `ListStaleSendingDomains`,
`RecordSendingDomainAuth`. These **never need HTTP transport** — a
control-role host is trusted infrastructure by definition, so it keeps a
direct pool forever, same as `cmd/inroad` does.

Everything else — `MailboxExists`, the 8 remaining step-send methods, 8
inbox methods, `UpsertWorkerHeartbeat` + `AssignMailboxWorker`, 6 warmup
send/receipt/engage methods — is per-message work a `send`-role host calls
today via its direct pool, and is the actual Stage 2 surface: ~26 methods,
not 35.

## Staging

### Stage 0 (S) — tighten credbroker's blast radius, independent of the rest — **done**

`credbroker`'s own doc comments used to flag this honestly: *"Scoping a
worker to the mailboxes actually routed to it needs per-worker identity AND
per-mailbox routing; neither exists yet"* (`client.go:73`). Both now exist
— F3 gave every worker a stable id (`internal/platform/workerid`), F4 gives
per-mailbox placement (`internal/coreapi/inprocess/workerrouting.go`).
Shipped: a request now carries the caller's claimed worker id
(`MailboxRef.WorkerID`, wire field `worker_id`, `omitempty` so an unset one
changes nothing on the wire), and `localOpener.checkWorkerAssignment`
(`internal/coreapi/inprocess/credentials.go`) refuses to open a mailbox
whose live fleet assignment names a different worker — mapped to HTTP 403,
which the client already folds into `ErrUnauthorized` so a caller learns it
was refused, never why.

**What this is not**, stated as precisely as the rest of this plan tries to
be: the bearer token (`INROAD_FLEET_BROKER_TOKEN`) is still one credential
shared by the whole fleet, so a worker id in a request is a claim, not
something the token cryptographically proves. This closes the accidental
case — a bug, a stale queue, a misrouted host asking for the wrong mailbox —
not a deliberate one; an actor who has extracted the shared token can still
claim any worker id and be believed. Making the claim unforgeable is exactly
what Stage 4's per-worker join credentials are for; this stage's wire field
and check are the seam that stage plugs into, not a substitute for it.

Tests: `internal/platform/credbroker/credbroker_test.go`
(`TestAConfiguredWorkerIDCrossesTheWire` and the unchanged
`TestTheRequestBodyCarriesIdsOnly`, which pins that an *empty* claim still
adds nothing to the wire) and `internal/coreapi/inprocess/
credentials_integration_test.go` (`TestABrokeredWorkerCannotOpenAMailboxAssignedToAnotherWorker`,
`TestABrokeredWorkerWithNoLiveAssignmentStillOpens` — the latter pins the
single-worker exemption so this stage cannot regress the smallest
legitimate brokered topology). The integration tests type-check under
`-tags integration` but were not run against a live Postgres in the session
that wrote them (no Docker daemon available there) — run
`make test-integration` to actually exercise them before relying on this
stage.

### Stage 1 (M) — split `coreapi.SendClient` out of `coreapi.Client`

Pure interface work, no transport, no behavior change. Define
`coreapi.SendClient` as the ~26-method subset above; `coreapi.Client`
(unchanged) continues to satisfy it structurally, the same pattern the six
existing narrow interfaces already use. Change `internal/worker`'s
per-message handler constructors to depend on `coreapi.SendClient` instead
of the full `Client` — the sweep methods become **uncallable from send-path
code at compile time**, not just unregistered at runtime by role. Every
existing worker fake shrinks to the subset it actually exercises. Low risk,
ships alone, valuable regardless of whether Stage 2 ever lands.

### Stage 2 (L) — the HTTP transport itself

New package, `internal/coreapi/httptransport` (or similar — name TBD at
implementation time), following `credbroker`'s own file layout as the
established precedent in this codebase:

- `wire.go` — request/response shapes for all ~26 methods, both sides in
  one file so client and server cannot drift on a field name (mirrors
  `credbroker/wire.go`'s stated reasoning exactly).
- `handler.go` — mounted on the **same dedicated internal listener**
  credbroker already established (never the public API router — same
  reasoning as `credbroker.NewHandler`'s doc comment). Each route wraps the
  existing `inprocess` method verbatim; no logic moves, only the transport
  around it.
- `client.go` — `HTTPClient` implementing `coreapi.SendClient`, same
  timeout/size-limit/redirect discipline as `credbroker.HTTPOpener`
  (`requestTimeout`, `maxResponseBytes`, no-redirect, https-required unless
  explicitly opted out).

Port order, dependency-first: worker routing (heartbeat + assignment —
nothing else can route without it) → step-send → inbox → warmup send →
warmup receipt/engage → `MailboxExists`. Each method ports with a
before/after parity test against the existing `inprocess` integration
tests, run against both transports.

Workspace pinning follows credbroker's already-established rule: the
control plane re-derives everything from ids it re-reads itself, and never
trusts a client-supplied secondary field (credbroker's `ref.Sealed`/
`ref.Provider` are documented as "a cache, not authority" for exactly this
reason — the HTTP transport's job data and claim fields need the same
framing).

### Stage 3 (M) — wire `cmd/worker` to choose a transport

Extend `resolveCredentialMode`/`buildCredentialWiring`
(`cmd/worker/credentials.go`) — or a sibling function following its
pattern — to also select `inprocess.New(pool, ...)` vs
`httptransport.NewClient(...)` for the `SendClient` half. `RoleSend` with a
configured remote coreapi backend makes `INROAD_DATABASE_URL` genuinely
optional for the first time. Refuse startup on the unsafe combination
(`RoleSend` set, no remote backend configured, no local pool either) the
same way `resolveCredentialMode` already refuses unsafe credential
combinations — this codebase already has the convention; extend it, don't
invent a new one.

### Stage 4 (M) — join-token enrollment

The genuinely unbuilt piece; today an operator hand-configures every fleet
host's env vars. `inroadctl` (F7) already has the right shape — one file
per subcommand (`cmd/inroadctl/*.go`) — so add `fleet join-token` there:
issues a node id + a token scoped per Stage 0's model. Backend serves
`GET /join.sh`, embedded so it always matches that backend's own version.
Inroad's join response renders `worker.env` from `INROAD_WORKER_ROLE=send`,
the coreapi backend URL/token, the credbroker URL/token, and
`INROAD_WORKER_EGRESS_IP` pinned to the host's own address; installs a
systemd unit; a timer pulls an updated image the way F7's install path
already knows how to.

### Stage 5 (S) — close the loop on documentation

- Update `docs/competitive-analysis/04-parity-plan.md`'s P3.8/P3.9 rows to
  reflect Stage 0–4 reality, so the next reconciliation pass starts from
  true state instead of rediscovering credbroker.
- Only once Stage 3 ships, update the public Astro docs
  (`docs/src/content/docs/deploy/environment-variables.md`'s "Splitting
  control and send roles" section) to say a `send`-role host no longer
  needs `INROAD_DATABASE_URL` — saying so before Stage 3 ships would be the
  exact doc/code mismatch this session spent its whole second half fixing.

## Security review checkpoint

Before Stage 2/3 ship, this needs the same scrutiny credbroker got:
token-scoping model (Stage 0's per-worker scoping applies here too — a
`SendClient` token should be bounded the same way a credential-broker token
is), TLS-required-unless-explicit-opt-out on the new listener (mirror
`credbroker.NewHTTPOpener`'s `ErrInsecureURL` rule exactly), request/response
size caps (mirror `maxRequestBytes`/`maxResponseBytes`), and confirmation
that no handler trusts a client-supplied workspace id without re-deriving it
from something the control plane itself looked up.

## Source anchors

- `internal/coreapi/coreapi.go` — the interface being split
- `internal/coreapi/inprocess/inprocess.go` — current pool-bound
  constructor and the `WithCredentialBroker` precedent this plan extends
- `internal/platform/credbroker/` — the transport pattern to mirror
  (`credbroker.go`, `client.go`, `handler.go`, `wire.go`)
- `cmd/worker/main.go`, `cmd/worker/credentials.go` — composition root to
  extend in Stage 3
- `internal/worker/role.go` — the control/send line this plan's interface
  split follows exactly
- `internal/coreapi/inprocess/workerrouting.go` — F3/F4, what Stage 0
  scopes credbroker tokens against
- `cmd/inroadctl/` — F7's CLI shape, extended in Stage 4
- `docs/competitive-analysis/04-parity-plan.md` — P3.8/P3.9 rows, updated
  in Stage 5
