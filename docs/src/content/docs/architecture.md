---
title: System Architecture & Planes
description: Deep dive into Inroad control plane, execution plane, coreapi boundary, and system design.
---

Inroad separates operations into two distinct planes: the **Control Plane** and the **Execution Plane**.

:::note
This page describes the system's **shape**. For the **rules** that govern how it
is built — and the reasoning behind each, so a change that must break one can
weigh what it gives up — see [Architecture Principles](/architecture-principles/).
:::

## Planes & Isolation

- **Control Plane (`cmd/inroad`):** Hosts the HTTP REST API server, identity/authentication services, workspace configurations, webhooks, and database management. It directly manages PostgreSQL and Redis.
- **Execution Plane (`cmd/worker`):** Contains background engines responsible for sending campaign emails, inbox polling, deliverability evaluation, and mailbox warmup.
- **CoreAPI Boundary (`internal/coreapi`):** The worker *packages* reach relational data and unseal encrypted mailbox credentials only through `internal/coreapi` — one seam, so the execution plane can move to its own host without touching worker code. That much is enforced mechanically: a `depguard` rule in `.golangci.yml` fails the build if a non-test file under `internal/worker/` imports `internal/platform/db`. The *process* boundary now depends on the role, and the difference is the whole design.

  A **`role=send`** worker opens no database connection at all. `cmd/worker` does not call `db.ConnectSized` on that role, builds no keyring, and resolves `coreapi` to `internal/coreapi/remote` — an authenticated HTTP hop to the control plane's fleet listener, the same listener and the same token the credential broker uses. Its `coreapi.Client` is a `*remote.Client`, a type with no `*pgxpool.Pool` field, and a second `depguard` rule denies `internal/platform/db` and `pgxpool` inside that package. The worker refuses to start if it is given `INROAD_MASTER_KEY` *or* `INROAD_DATABASE_URL`. So a compromised fleet host cannot read the tenant database: it can name one subject at a time — this enrollment, this mailbox, this address — over a seam whose wire shapes cannot express a filter, a pattern, a limit or a cursor. What it can still do is obtain the credential of any mailbox it names, and read the content of any job it can name an id for; `docs/security.md` invariant 80 states the residual exposure in full rather than leaving it to be inferred.

  A **`role=control`** worker keeps its pool, and must: it runs the cross-tenant sweeps, which have no remote transport by design and answer `remote.ErrControlPlaneOnly` if one is ever attempted. **`role=all`** — the single-process self-host topology every compose file, Helm chart and Terraform config here runs — is entirely unchanged: same pool, same local keyring, same in-process client, and none of the fleet variables exist for it.

## The Sending Fleet

A `role=send` worker is a *sending identity*, not just a unit of throughput. This
section describes how mailboxes are placed onto workers, how that placement is
measured, and when it is revisited.

### The asymmetry the whole design rests on

**Inroad never delivers to a recipient's MX.** Every send authenticates to the
*customer's own* provider — their SMTP relay, the Gmail API, or Microsoft Graph —
and that provider delivers from its own outbound pool. `NetSender.Send` dials
`mailboxes.smtp_host`; the Gmail and Graph legs hit fixed provider hosts. The one
place a recipient domain's MX is resolved, `esp.LookupMX`, only *classifies* the
domain — [invariant 46](/security/) states the resolved host is never dialed.

Two consequences, and every other decision here follows from them:

- A worker's egress IP is **invisible to recipient-side spam filtering**.
  Recipient-side reputation — bounces, complaints, inbox-vs-spam placement — cannot
  transfer between mailboxes that share a worker, because the recipient never
  observed the worker.
- That same IP is **highly visible to the mailbox provider**, which sees it on
  every send and every poll. Providers challenge sign-ins, throttle and rate-limit
  *per source address* (`454 4.7.0`, `421 4.7.28`). That is the real per-IP risk.

So IP **stability** per mailbox beats IP **diversity**. Moving a mailbox to a
different worker discards trust that accrued per `(mailbox, IP)` pair at the
provider, which is why a migration is priced as a cost rather than a win.

### Provider signals (`platform/providersignal`, `worker/fleetsignal`)

Each worker classifies what the provider said — at the capture point, where the
reply is still in hand, never in SQL — into a closed vocabulary: `ok`,
`auth_failed`, `rate_limited`, `throttled`, `blocked`, `rejected`, `unreachable`,
`other`, keyed by transport leg (`smtp`/`gmail`/`m365`) and operation
(`send`/`poll`). Counts accumulate in memory as **window deltas**, never running
totals, and flush to `worker_provider_signals` every 5 minutes. A delta means
aggregation is a plain `SUM` over a time range, and a worker restart costs at most
one partial window rather than corrupting a cumulative series. The key space is
bounded by construction at 48 entries, so a hostile or novel provider response
cannot grow it.

Telemetry never fails a send: `Observe` does one map write under a mutex, and a
failed flush discards its window rather than restoring counts that may already
have landed.

### Placement by score (`platform/fleetscore`)

Placement resolves a mailbox's destination queue through
`coreapi/inprocess.AssignMailboxWorker`, in four steps:

1. **Incumbency.** An existing assignment to a *live* worker wins unchanged, and
   nothing else is read or computed. This is control flow, not a weight —
   expressed as a weight the rule would be an arbitrarily large number nothing
   could outvote. Liveness is still checked every resolve (15-minute window
   against a 5-minute heartbeat), because an assignment to a worker that stopped
   heartbeating routes to a queue nothing consumes.
2. **The mailbox's facts** — its provider leg and its warmup lane, mapped to a
   risk band through `warmup.RiskBandForLane`.
3. **Self-host bypass.** At most one live worker means there is no placement
   *choice*, so neither the band nor the score nor the health gate applies.
4. **Score** every live worker and take the best.

Scoring replaced an earlier tiered **filter** (pure-band match → idle promotion →
already-mixed → refuse), because filters compose multiplicatively: every added
constraint is another gate, and conjoined gates eventually admit nothing — so the
fleet refused placements exactly when it was fullest. A term added to a sum can
only shift a preference.

**Health is the only hard gate.** A worker is excluded only when every recent
verdict from *this mailbox's provider* was a refusal to talk to it and nothing has
completed since. A worker with no signals is **new, not unhealthy**. Exceeding the
load target produces a heavily degraded score, never ineligibility — a capacity
number is a guess, and refusing on a guess refuses when you are busiest.

The additive terms, each normalised to `[0,1]` so the weights are comparable:

| Term | Weight | What it prices |
| :--- | ---: | :--- |
| Headroom | +10 | Spare capacity — the packing term |
| Overload | −60 | Projected load past `TargetLoad` (40), on a ramp |
| Blast radius | −8 | Concentrating one workspace on one worker |
| Provider crowding | −6 | Piling one provider's mailboxes onto one egress IP |
| Band conflict | −2 | Co-locating risk bands |

The **ordering** is the design. Overload exceeds every other term combined, so no
mix of preferences can stack another mailbox onto an over-target worker while an
under-target one exists. Band conflict is deliberately weakest: it is the only
term whose causal story does not reach a worker's egress IP, since a band is
derived entirely from recipient-side signals.

Mailboxes are weighted by what they actually consume — an SMTP+IMAP mailbox costs
`1`, a Gmail/Graph mailbox `0.35`, because the latter has no persistent IMAP leg.
Counting both as "1 mailbox" overprices an all-Gmail worker by roughly 3× and
drives placement away from the workers with the most room. Ties resolve on worker
id ascending, so two callers racing the same first send converge on one worker
instead of splitting a mailbox's mail across two egress IPs.

An empty ranking is the one refusal placement can produce, and it means what it
says: nothing in the live fleet can serve this mailbox's provider right now.

### Rotation (`platform/fleetrotate`)

Placement treats a live incumbent as unconditional, which leaves exactly one hole:
a worker whose IP the provider has blocked keeps every mailbox already on it, and
each keeps failing forever because nothing re-asks the question. Rotation is the
separate urgency gate that closes it — a `fleet:rotate` task on the `control`
queue every 5 minutes.

It is deliberately an **exception**, not a tidying pass. A gate that fires easily
undoes the stability placement exists to provide, and a fleet that churns away
weeks of accrued reputation is worse off than one that never rotates. So every
number is a brake:

| Tier | Trigger | Bypasses brakes? |
| :--- | :--- | :--- |
| `unreachable` | Worker stopped heartbeating — its affinity queue has no consumer, so tasks neither run nor fail nor alert | Yes |
| `unhealthy` | The provider has been refusing this worker, nothing completed since | Yes |
| `balance` | Nothing is wrong; another worker merely scores better | No |
| `none` | Leave it alone | — |

- **Residency floor**, 12 hours, paid only by `balance`. The evidence an
  opportunistic move rests on is at most one hour old, so a shorter floor would let
  a mailbox move on evidence generated while it sat somewhere else.
- **Margin**, 10, which the destination must *beat*, never tie. That is exactly
  `HeadroomWeight`, and the identity is the point: the packing term spans its whole
  range in 10, so **imbalance alone cannot move a mailbox**. It also exceeds every
  soft penalty individually.
- **Caps**, 20 moves per tick and 5 per destination. The per-destination cap
  matters more than it looks: destination scores are measured, then acted on, and
  the send path is placing mailboxes concurrently that the tick cannot see, so the
  emptiest worker in the picture is one several callers may be converging on.

Health has **one** definition and rotation reuses `fleetscore`'s
`Eligible` unchanged — two disagreeing definitions would let a worker be too sick
to receive new mailboxes and well enough to keep the ones it has, which is the
original bug with the sign flipped. The incumbent is scored *vacated* (with the
mailbox taken off it), so staying and moving are comparable; without that the
incumbent is charged for the mailbox twice and every comparison leans toward
moving.

### The decision log (`platform/fleetdecision`)

An assignment row records the answer and never the reasoning, so a mailbox that
moved, one that was refused a placement, and one nothing ever considered were
indistinguishable afterwards. The append-only `fleet_decisions` log records every
`assign` / `rotate` / `quarantine` / `refused`, with an actor (`auto:<kind>` or
`operator:<user id>`).

Its one hard rule is enforced by construction: **an entry must not print a score
comparison it did not make.** `Reason` has no exported field and can only be built
through `Chose`, `ChoseUncontested` or `Forced`; only the first renders a
comparison, and it degrades to `ChoseUncontested` rather than inventing a
runner-up at `0.00`. A forced decision renders its cause and nothing numeric,
because nothing numeric was computed — otherwise an operator tunes a threshold
against a number nothing produced.

### Worker identity (`platform/workerid`)

A fleet worker's id is derived from its host's **public IP**, not its hostname:
reputation is per-IP, so a reinstalled host keeps its IP and should keep its
affinity, while a host with a new IP is genuinely a new sender and must not
inherit the old one's assignments. A container hostname changes on
`--force-recreate`, silently stranding the affinity queue keyed on it — a failure
this project has already hit.

The id is a UUIDv5 in the fixed RFC 4122 DNS namespace, so a provisioning script
on another host, in another language, computes the same id from the same IP. The
`workers` row records which source produced it (`ipv4`, `ipv6`, `hostname` for a
NAT'd host with no public address, or `override` for a pinned
`INROAD_WORKER_ID`), so an operator can tell them apart without reading logs.
`role=all` deliberately keeps the hostname, exactly as before per-IP identity
existed.

### The operator view

`GET /fleet/workers`, `GET /fleet/mailboxes/{id}/decisions` and `GET /fleet/jobs`
back the console's **Settings → Fleet** page. All three are admin-session-only:
mounted in the session-only group *and* wrapped in `RequireRole("admin")`, so an
API key or OAuth token is rejected twice over. The worker read is not a fleet
census — it inner-joins through the caller's own assignments, so a worker carrying
none of that workspace's mailboxes does not appear and the deployment's size is
not discoverable. See [invariant 24](/security/) for why worker ids are shown
rather than redacted, and [invariant 70](/security/) for the one field withheld
outright.

### The remote coreapi transport, built

**The worker's own database connection is gone on `role=send`.**
`internal/coreapi/remote` is an HTTP client on the execution plane and a
handler on the control plane's fleet listener, authenticated by the same shared
token the credential broker uses. It now carries every `coreapi` method a
`role=send` worker can reach: the suppression check, the nine **per-message job
reads**, the twenty **claim and outcome writes**, the twelve **manual
reply/compose** calls, the ten **inbound-mail** calls an inbox poll makes, and
the four **worker-infrastructure** calls. Each names one subject by id — no
filter, no pattern, no limit, no cursor — which is the restriction that keeps
the seam from being a query engine over the tenant database.

The six cross-tenant sweeps are deliberately absent and answer
`remote.ErrControlPlaneOnly`. They enumerate every workspace's rows, so a route
for them would hand back exactly the capability the plane split removes; they
run on `role=control`, beside the database, on infrastructure the operator
already trusts with it.

**Credentials do not travel on those routes.** A job response carries a
mailbox's host, port, username and TLS policy and no secret at all; the worker
obtains the decrypted password or access token from the credential broker on the
same listener, by mailbox id. The choice is deliberate: `credbroker` already
exists for brokering a secret to a keyless worker and is already mandatory on
`role=send`, so inlining the secret in a job would have added a second plaintext
channel with its own audit properties to keep in step. The secret fields on the
`coreapi` job types are `json:"-"`, so their absence from the wire is structural
rather than remembered.

It fails closed throughout: a worker that cannot reach the control plane refuses
to work, never falls back to a local read, never assumes "not suppressed", and
never returns a half-built job — a job with an empty body or an unset gate flag
would send the wrong mail rather than none. A lost response is never a double
send: the claim decides what the retry does, not the transport.

**What a compromised fleet host can still reach** is stated in full in
`docs/security.md` invariant 80 rather than left to be inferred. In short: it
can obtain the credential of any mailbox it names, and read the content of any
job it can name an id for. It cannot enumerate anything.

### What is designed, not built

**Per-mailbox credential scoping.** Every `send` worker consumes the shared
`send` queue and may legitimately be handed any mailbox's job — only
`warmup:tick` and `inbox:poll` are routed by assignment — so the credential
broker must answer for any mailbox the token names. Scoping needs per-mailbox
routing for `sequence:advance` first, whose mailbox is not resolved until the
job is hydrated. Worker identity, which that scoping also needs, does now
exist.

**Per-worker fleet tokens.** The fleet token is shared across the fleet, so the
listener cannot tell which host is asking and revoking one revokes all. It is
only worth splitting once the scoping above exists — until then, two hosts with
identical reach would hold two secrets with identical reach.

**Collapsing the `inprocess` source seams.** `inprocess` still carries
`SuppressionSource`, `JobSource`, `OutcomeSource` and `InboxSendSource` — the
four `With...` options that let slices 1–3b move the boundary a group at a
time. Nothing in production installs them any more: a `role=send` worker uses
`*remote.Client` whole, and every other role uses the local path. They are
still exercised by the fault-injection integration tests that prove the
never-double-send properties, so removing them means migrating those tests to
drive `*remote.Client` directly — worth doing, and deliberately not bundled
into the change that moved the boundary.

## System Monorepo Layout

```
Inroad Monorepo
├── cmd/                          --> Binary entrypoints (inroad, worker, migrate, seed)
├── internal/
│   ├── app/                      --> 25 Feature domain slices (auth, campaign, contact, warmup, etc.)
│   ├── platform/                 --> 26 Infra packages (crypto, db, mail, queue, bus, storage)
│   ├── worker/                   --> Execution handlers (sender, sequence, inbox, warmup)
│   └── coreapi/                  --> Control <-> Execution seam interface
├── api/openapi.yaml              --> OpenAPI REST API contract
├── docs/                         --> Standalone Astro Starlight Documentation App
└── web/                          --> React 19 + Vite + Tailwind v4 + RTK Query SPA
```

## Technology Stack

- **Backend:** Go 1.25 · Chi Router · `pgx/v5` · `sqlc` · `golang-migrate` · `asynq` · AES-256-GCM Envelope Cryptography.
- **Frontend:** React 19 · Vite · Tailwind v4 · Redux Toolkit / RTK Query · TanStack Router.
- **MCP Server:** `@modelcontextprotocol/go-sdk` v1.7.0 · OAuth 2.1 Authorization Server (`/oauth2`).
