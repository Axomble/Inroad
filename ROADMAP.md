# Roadmap

**Last reconciled:** 2026-10-20 against `main` @ `5058c66`.

This is a point-in-time plan, not a backlog. The backlog lives in
[`docs/competitive-analysis/04-parity-plan.md`](docs/competitive-analysis/04-parity-plan.md);
this file says what we are doing next and why, and is re-reconciled against the
tree rather than against its own previous contents.

That distinction is not pedantry. The parity plan drifted far enough once that
five shipped features still read as outstanding, and work was nearly dispatched
to rebuild the webhooks UI, the scheduled-job ledger and the storage seam — all
of which already existed. **Any plan here is wrong until checked against the
code.**

---

## Where we are

**The multi-IP sending fleet is architecturally complete.** The execution plane
is now a containment boundary rather than an operational lever: a `role=send`
worker holds no master key, opens no database connection, and refuses to start
if given either. It reaches relational data only through an authenticated HTTP
seam.

| Shipped | What it gave us |
|---|---|
| Worker roles + role-scoped queues | A worker can run per-message work without the control plane's sweeps |
| IP-derived worker identity | A worker's identity survives restarts and maps to the IP whose reputation it carries |
| Scored placement + rotation | Placement composes additively; a mailbox moves only under an urgency gate |
| Provider signals + decision log | Per-worker auth/throttle counters, and "why is this mailbox here?" is answerable |
| Credential brokering | A stolen worker disk or `.env` decrypts nothing |
| `coreapi` remote transport | The pool is gone on `role=send` |
| IMAP/SMTP hardening + poll backoff | AUTH negotiation, a real test harness, and an outage no longer costs ~60 reconnects/hour |
| Operator CLI, installer, audit log, operator view | An instance can be recovered, installed, audited and inspected |

---

## In flight

Merge order matters here, because one of these breaks the build if taken out of
turn.

1. **#248** — optimistic concurrency for step branch writes. Mergeable.
2. **#245** — yes/no condition nodes on the canvas. Mergeable, and **stacks on
   #248**: its frontend consumes an API that only exists there. Merging it first
   breaks the build.
3. **#244** — configurable retention. Conflicting, and genuinely so (overlapping
   edits in `.env.example`, `capabilities_test.go`, `maintenance/cleanup.go`).
   Needs a real merge, not a rebuild.
4. Dependabot (**#259**, **#260**) — mergeable.

---

## Next, in order

### 1. Make the installer usable — **GHCR packages are still private**

`scripts/install.sh` and `docker-compose.prod.yml` shipped in #199. An anonymous
pull of `ghcr.io/axomble/inroad-api` returns **401**, so the documented
`curl | sh` path cannot work for anyone. This is a settings toggle, not code,
and it is the only item standing between a merged feature and a real user.

### 2. P1.3 — pre-send email verification

Design in progress. Decisions already taken: block `invalid`, warn `risky`;
provider verification auto-on-import but opt-in; cache keyed by address, with an
`invalid` verdict writing a suppression so the existing send-time gate (remote
workers included) does the blocking.

**Open question that must be answered in the schema, not deferred:** nothing in
this repo ever deletes from `suppression`. The verification row carries an
expiry; the suppression it writes does not. An address suppressed because its
domain was briefly NXDOMAIN is therefore suppressed *permanently*, and the only
evidence expires. Either a sweep re-verifies expired `invalid` verdicts and
lifts the suppression, or `undeliverable` suppressions carry their own expiry.

### 3. P1.5 — the CSV import wizard

Export ships. Import is a single-click upload with no preview, column mapping or
dedup strategy, and no XLSX.

### 4. Surface poll failures to users

Poll backoff persists a failure count and classification, and logs it — visible
to an operator with database or log access, **not to a user**. Pairs with the
mailbox-error tile that is permanently 0 because nothing writes `status='error'`
(and nothing should: `ListActiveMailboxes` and `MailboxExists` both gate on
`active`, so writing it would stop sending too).

### 5. Fleet remainder

- `sequence:advance` is still unpinned — a decision about where sender selection
  lives, since the mailbox does not exist at enqueue time for an initial step.
- The four `inprocess.WithRemote*` options have no production caller; they drive
  the fault-injection suites. Deleting them means migrating those suites to
  `*remote.Client` — worth doing, and worth doing separately from the thing they
  prove.
- OAuth token refresh still uses `http.DefaultTransport`. Deliberate — it moves
  to the control plane — but it should get chosen timeouts regardless.
- `GetInboxPollJob` failures are not backed off, so a revoked refresh token
  still hammers the provider's token endpoint every sweep.

---

## QA as a Petri agent

**Intent:** run exploratory QA as a separate agent in
[Petri](https://github.com/safety-research/petri), alongside — not instead of —
the existing suites.

### Why a separate agent, and why Petri

Today's QA agent (`.claude/agents/qa.md`) *executes* tests: 135 Go integration
files, 9 Playwright specs, the Vitest suite. It verifies what we thought to
assert. It cannot find what nobody thought to assert.

Petri's shape fits the gap: an auditor agent drives a target through many seeded
scenarios **in parallel**, and a judge scores the resulting transcripts. Applied
to a product rather than a model, that is exploratory QA at a scale a human
tester cannot reach — many sessions, varied, each one scored.

**Inroad is unusually well suited to this** because it already exposes a typed
agent surface: `internal/app/mcpserver` with a documented tool reference. A
Petri auditor can drive the product through its own MCP tools rather than
through a browser, which makes scenarios cheap to seed and transcripts easy to
judge.

### What it would look for

The failures this session actually produced are the template — every one was
invisible to the existing suites because it was never asserted:

- **Capabilities silently not registered.** A wrapper satisfying an interface
  but not the concrete type compiled, started, and registered no test-send
  handler, no breaker, no dead-letter capture. Nothing failed.
- **A sentinel that never matched.** Two `errors.New` values meant an
  `errors.Is` check had never once been true in production; every operator undo
  was dead-lettered instead of ending quietly.
- **Behaviour that looks like success.** A localized junk folder meant warmup
  reported a *clean inbox rate* for a mailbox whose mail was going to spam.
- **A connect test and a sender that disagreed** about the same mailbox: the
  test passed, every send failed.

A judge rubric aimed at "did the system report success while doing nothing",
"did two paths disagree about the same object", and "did a failure surface
anywhere a user could see it" would have caught all four.

### Sequencing

1. **Spike first.** Stand up Petri against the MCP surface with a handful of
   seeds and see whether the transcripts are judgeable. Output is an answer, not
   infrastructure.
2. **If it holds:** a seed corpus derived from the parity plan's user journeys,
   plus one seed per class of bug above.
3. **Then CI**, nightly rather than per-PR — exploratory runs are not a gate,
   they are a net.

**Unverified:** Petri's current API, its judge configuration, and whether it
drives a non-model target cleanly all need checking against the upstream repo
before any of this is scheduled. Treat the above as intent, not a design.

---

## Standing hazards

Two environmental issues cost real debugging time and will cost it again:

- **DB↔host clock skew fails tests on any branch.** Measured between −1711 ms
  and −37 ms on one machine in a day. When the sign flips positive, claims defer
  and clock-sensitive tests fail — on `main` as readily as on a branch. Prefer
  injected clocks; compute scheduling deltas in SQL, not in Go.
- **The `golangci-lint` action cache can report findings in files a branch never
  touched**, including paths that no longer exist. `golangci-lint cache clean`
  before trusting a local run; re-run the job before trusting a CI one.

Also worth knowing: the dev Postgres container exits on its own. A wall of
`connection refused` is a dead container, not a regression.
