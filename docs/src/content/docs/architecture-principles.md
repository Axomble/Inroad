---
title: Architecture Principles
description: The rules that govern how Inroad is built — what they cost, where they are proven in the codebase, and when to break them.
---

`architecture.md` describes the *shape* of the system. This describes the
*rules*, and — more usefully — the reasoning behind each one, so a change that
must break a rule can weigh what it is giving up.

Every principle below cites code where it is already proven. None is aspirational.

---

## Universal

### U1. Fail loud, never silent

A silent failure is worse than a crash, because a crash gets fixed.

The reference case is PR #128: an unresolvable bounce returned `false, nil` with
no log. Symptomatically identical to "this workspace has no bounces". A DSN
parser that stopped resolving anything would have surfaced only as sender
reputation decaying, weeks later.

**Rule.** Every error is handled or propagated with context (`%w` in Go,
`@/lib/rtk-error` on the web). No `catch {}`, no discarded returns. When a
condition is *expected* and benign — a bounce for mail this workspace never sent
— it is still logged at a level an operator can alert on, with a stable reason
token so it can be grouped and its *rate* watched.

**Corollary.** If a guard cannot establish that it is safe, it refuses. The
sandbox harness (`internal/sandbox/guard.go`) refuses an empty `INROAD_ENV`
rather than assuming non-production: a binary that cannot determine where it is
has not established that it is outside production.

### U2. Validate at boundaries, trust within

Trust nothing crossing HTTP, the database, env, or user input. Once validated,
pass typed values inward rather than re-checking at every layer.

**Corollary — unauthenticated input stays untrusted even when it looks
authoritative.** A DSN bounce report names a `Final-Recipient`, and suppressing
on it would be the obvious fix. It is also a vulnerability: the report is
unauthenticated, so anyone could suppress an address they do not own. Inroad
resolves bounces only by a `Message-ID` it actually sent.

### U3. Make illegal states unrepresentable

Prefer a type that cannot express the wrong thing over a check that catches it.
No `any`, no `interface{}` escape hatches. One source of truth for every shape —
derive from the generated or owning definition, never hand-copy.

**Where this is hard-won:** sqlc silently generates `interface{}` for `FILTER` /
`COALESCE` aggregates without explicit casts, and it still compiles. Grep `gen/`
after `sqlc generate`.

### U4. Isolate side effects; prefer pure cores

Push I/O to the edges. A pure function is testable without a fixture, a mock, or
a clock.

Proven repeatedly: `platform/rotation` ("no database, no clock, no math/rand —
selection is a function of the candidate state handed in"), `platform/cadence`,
`platform/sendcap` (pure arithmetic; enforcement lives in Postgres),
`platform/abtest`, and `internal/sandbox/timeline.go`.

No package-level mutable state. Inject dependencies.

### U5. Tests assert behaviour, and a test that cannot fail is a liability

Cover error, empty, and edge branches — not the happy path alone.

**The specific trap this codebase has hit twice:** a fake that discards the
tenant dimension makes a cross-tenant test a tautology. `FindSendByMessageID`'s
stub ignored the workspace id, so a test "proving" tenant isolation asserted
nothing (fixed in PR #128).

**Therefore:** when a test guards a critical invariant, verify it can fail.
Mutation-test it — the dead-letter work removed the status guard and confirmed
the suite reported `enqueued 8 times, want exactly 1`. A guard test that has
never been seen to fail is decoration.

### U6. Comments explain why, not what

The best comments in this codebase document a *decision and its alternative*.
`stream.go:37-41` explains why one Redis subscription serves a hundred panels.
`db.go:12-16` explains why the pool floor is 25. Both let a later reader
challenge the reasoning rather than guess at it.

---

## Backend

### B1. The plane boundary is absolute

The worker reaches relational data and decrypted credentials **only** through
`internal/coreapi` — never Postgres directly. This is what lets the execution
plane move to a separate host, or a remote transport, without touching worker
code.

**No transaction spans the seam.** The send path claims in a transaction, commits,
*then* does SMTP (`stepsendjob.go:493-536`). The residual window — SMTP succeeds,
the delivery mark fails to commit, lease reclaim re-sends — is the irreducible
cost of non-transactional SMTP, and it is documented where it lives.

### B2. Optional capabilities are separate interfaces

When only some callers need a method, declare a small standalone interface beside
the main one and feature-detect with a type assertion. `CRMCaptureClient`,
`InboxCaptureClient`, `WarmupEvidenceClient`, `ReplyLabelClient`,
`DeadLetterClient` — five instances of one idiom.

**Why it matters:** adding a method to `coreapi.Client` forces every worker fake
and every future remote client to implement it. A capability interface does not.
Absence degrades to prior behaviour, loudly logged, rather than failing.

### B3. Domains own their data access; services depend on interfaces

Each `internal/app/<domain>/` owns its `store.go` defining its own `Store`
interface. Services depend on the interface, not the sqlc-backed struct. `app/*`
may import `platform/*`, never the reverse, and `app/*` packages do not import
each other.

sqlc models *are* the persistence type — no parallel DTO hierarchy. The interface
boundary is where decoupling lives.

### B4. Every query is workspace-pinned

The workspace comes from the authenticated principal, never from a body or path
parameter. A foreign id and a malformed id return the same 404, so an endpoint
cannot be used to probe for another tenant's ids.

**Known weakness, stated plainly:** this is enforced by convention across ~300
hand-written queries, not by the database. One omission is a cross-tenant leak
that would pass any test exercising a single workspace. See
`scalable-architecture.md` §5b.

### B5. Concurrency correctness lives in the database

Under N replicas, the only reliable arbiter is Postgres.

`ClaimStepSend` is the reference: a single `INSERT … ON CONFLICT … DO UPDATE`
where the unique index *is* the lock and stale-lease reclaim is a predicate. Zero
rows returned means the claim was lost. No advisory locks, no read-then-write, no
`SKIP LOCKED` required. The dead-letter replay claim follows the same shape.

**Ordering matters and is not interchangeable.** Claim *then* act. Acting first
and recording after can hand work to a queue and then fail to record it — for a
mail system, that is a double send.

### B6. Every scan is bounded; loops do no per-row I/O

A periodic scan has a `LIMIT` and an index. A loop over rows does not make a
database call per row.

The enrollment sweeper is the template (`LIMIT 500` over a partial index, no I/O
in the loop). The inbox and warmup sweeps currently violate this and are tracked.

### B7. Idempotency is a property of the operation, not a middleware

The HTTP `Idempotency-Key` cache is a client convenience with a 24h TTL. It is
**not** a correctness mechanism for internal operations, because a window that
expires silently re-opens the failure it was preventing.

Operations that must not run twice enforce it structurally — a status-guarded
`UPDATE` claim, a unique index — with no expiry.

### B8. Config is explicit, defaulted to today's behaviour

Every operational constant is an env var with a default equal to the current
value, so an existing deployment changes nothing on upgrade. Validate at startup
and fail loud — a bad value should not surface as a stall at first use.

Precedence is documented where it is implemented.

---

## Frontend

### F1. Route composition, feature ownership

`routes/*` compose from `features/*`. A feature never imports another feature's
UI. The one deliberate exception: read-only RTK Query *hooks* may cross features,
marked with a comment — hooks only, never components or state.

### F2. Shared UI has two homes, and neither is another feature

- **No domain knowledge, no fetching → `components/shared/`.** The reference is
  `record-page.tsx`, which takes a finished `message` string rather than an RTK
  error precisely so it needs no feature's error copy.
  `components/shared/no-feature-imports.test.ts` enforces this mechanically.
- **Fetches, but belongs to no one domain → a neutral feature.**
  `features/records/` serves contacts, companies and deals polymorphically.
- **Genuinely one domain's concept → leave it there** and restructure the caller.
  A little duplication beats a shared module that knows about everything.

### F3. The generated API client is the source of truth

`store/api.ts` is generated from `api/openapi.yaml` and never hand-edited.
Features extend via `enhanceEndpoints` with their own tag types. A tag lives
centrally only when something *outside* the owning feature invalidates it.

**Trap:** an injected endpoint silently no-ops if the generated client already
claims the name.

### F4. Patch the cache; invalidate only when you must

Invalidation triggers a refetch. For a change you already know the shape of —
an optimistic update or a pushed event — patch with `updateQueryData`.

**Use `selectCachedArgsForQuery`.** `crm/api.ts:151` patches *every* cached arg
rather than a hard-coded `undefined`, because the caller does not know which args
are live. This is not a refinement; it is the only correct form.

### F5. Never persist server state

`redux-persist` whitelists UI slices only — never the RTK Query cache, never
session, never a connection slice. `store/__tests__/persist-whitelist.test.ts`
asserts this against what redux-persist actually writes.

The access token is in memory only, restored via the httpOnly refresh cookie.
This is deliberate and constrains what is possible elsewhere — notably WebSocket
handshake auth (see `realtime-websocket.md` §3).

### F6. Transport, state, and React binding are three modules

The agent stream splits `stream-client.ts` (transport), `stream-state.ts` (pure
reducer), `use-agent-stream.ts` (React binding) — which is what makes it testable
with no socket. Any new streaming or realtime surface mirrors this.

### F7. Error copy is per-feature; error *inspection* is shared

Every feature owns its mapper (`crmErrorMessage`, `recordErrorMessage`,
`agentErrorMessage`) because the scope a 403 names differs by domain. A
domain-specific mapper handles only the statuses where naming its domain helps,
and delegates the rest.

Inspection goes through one seam — `@/lib/rtk-error` — never ad-hoc casts.

**Known dead code:** RTK hook errors carry no `meta`, so `retryAfterSeconds()` is
unreachable from a hook error. Three features have dead "wait N seconds"
branches.

### F8. Code-split at the route, and mount providers where auth is guaranteed

Heavy dependencies — a canvas, a rich-text editor — are lazy chunks, never in the
initial bundle.

Providers needing a session mount in `app.tsx`'s `AppLayout`, **not** `main.tsx`
(which mounts before auth bootstrap resolves) and **not** `__root.tsx` (which
covers login and other unauthenticated routes).

**Testing trap:** `React.lazy` makes RTL `findBy*` race a dynamic import. Resolve
the module in `beforeEach`, then run the file several times to confirm stability.

---

## When to break a rule

These encode tradeoffs, not laws. Breaking one is legitimate when the cost is
understood and stated. The requirement is that the *reasoning* is written down
where the exception lives — as `db.go:12-16` does for the pool floor, and as
migration 000008's comment does for the partition-key conflict it knowingly
introduced.

An undocumented exception is indistinguishable from a mistake. That is the only
rule here without an exception.
