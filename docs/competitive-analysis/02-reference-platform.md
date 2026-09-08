# 02 — The reference platform (direct competitor)

**Source:** the reference platform's published source, `main`, re-pulled and
re-read 2026-09-08 (previous read: 2026-09-07 — the head moved by 76 commits in
that single day, which is itself the most useful datum in this document).
**License:** Apache 2.0. Porting the *approach* is fine with attribution; copying
source verbatim needs the notice. This document describes patterns, not code to
copy, and does not reproduce competitor-internal file paths.

The reference platform and Inroad are the same product category with the same
architectural instincts. It is further along on **product breadth** and
**operability**; Inroad is at least even on the **sending engine** and **CRM
core**, and cleaner in a few places (heavier tests, first-class Company record,
the pulse read-model, determinism guarantees written into the cadence code).

If Inroad is going to be "on par or better," this is the bar to clear, because
everything it has is buildable — it's all in a repo that can be read.

---

## 1. Shape of the two codebases

Counts re-measured on both trees 2026-09-08; the stale figure is struck where
it moved.

| | Inroad | Reference platform |
|---|---|---|
| Language spread | Go + React (one Go module) | Go + React ×3 SPAs (dashboard, admin, forms) + a BEAM realtime service + a Rust tracking service + a native iOS app + a marketing site |
| Backend domains | 33 | ~~~100~~ **89** (measured, not estimated) |
| Services | api, worker | ~~api, consumer, worker, tracking, forms, realtime, updater~~ **10 binaries**: backend, consumer, worker, forms, tracking, realtime, updater, sandbox, migrate, seed — plus an operator CLI |
| Migrations | ~~156~~ **158 files / 79 versions** | ~~~130~~ **135** |
| Go test files | ~~353~~ **360** | ~~~230~~ **237** |
| CI workflows | ~~1~~ **4** (ci, build/push, release, security) | 4 (ci, image build/push, release, security) |
| Docs | Astro/Starlight, ~25 pages | ~70 pages (guides / fundamentals / api / self-hosting) |
| Self-host entry | compose file + several `make` targets | one published, checksummed install script that pulls release images (no clone, no compiler), with a `--wizard` — and as of 2026-09-07 a commit explicitly rewriting the guide "around what was tested" |
| Operator tooling | `cmd/migrate`, `cmd/seed` | a first-class operator CLI that talks to the DB directly and works when login is broken |

Read that as: **Inroad tests each feature harder and documents its invariants at
length; the reference platform ships roughly three times the features and a real
install/operate story.** Its own contributor notes explicitly discourage long
code comments, which is the opposite of Inroad's house style — a values
difference, not a defect on either side.

## 2. Architecture: where the reference platform is genuinely ahead

### 2.1 Physical control/execution plane split

Its worker **holds no database connection at all**. It subscribes to an
event-bus topic named for its own identity, and reaches the two pieces of
relational data it needs (the per-org decrypted DEK, the Message-ID map) over the
control plane's internal HTTP API with a bearer token.

Inroad has the *seam* — `internal/coreapi` — but the worker runs in-process with
a `pgxpool`. The split is logical, enforced by convention and a lint rule, not by
the network.

**What the physical split buys:** a worker host that gets popped can't dump the
tenant database. For a product whose workers deliberately run on cheap untrusted
VPSs across many IPs, that's a real security property, and it's the single
biggest architectural gap.

**Verdict: do-better, deferred.** Inroad's `coreapi` interface was designed for
exactly this ("in-process now, HTTP later" — see `CLAUDE.md`). The lift is: give
`coreapi` an HTTP transport, stand up an internal-API listener in `cmd/inroad`,
give the worker a client instead of a pool, move DEK decryption behind it. Do it
when someone actually needs multi-host sending; until then the logical split
keeps the code honest for free.

### 2.2 IP-as-identity worker fleet

Worker identity is derived deterministically from the host's public IPv4 — same
IP means same worker (reputation survives a reinstall), new IP means a fresh
identity. Add throughput by adding hosts, each a distinct network identity. An
admin drives every worker over SSH from the dashboard (install, update image,
rotate keys, tail logs), host key pinned on first contact. Worker config
(event-bus + cache + internal-API settings) lives in a named profile; a worker
whose applied config predates the profile is flagged stale and rewritten over
SSH in one click.

Inroad has per-IP queue routing (`w:<id>` queues, weighted priority) but no
assignment engine, no provisioning, no fleet health/rebalance.

**Verdict: replicate the model, skip the auto-provisioning.** The queue routing
is the hard half and it's done. Mailbox→worker assignment by health band + load
is a medium job and worth it. VPS auto-provisioning is force-dry-run even in the
reference platform — skip it.

### 2.3 Infra abstraction with two default stacks

Every infra dependency sits behind an interface with a from-env factory. The
default all-local stack is a lightweight message bus + JSON + local-AES +
filesystem blobs; the opt-in cloud stack swaps in Kafka + a schema registry +
a cloud KMS + object storage. Same binaries, both ways.

Inroad is asynq/Redis only, behind a `bus.Dispatcher` seam that currently has
exactly one implementation.

**Verdict: skip for now.** The seam exists; a second implementation is YAGNI
until there's a deployment that needs one. Don't build Kafka support
speculatively.

### 2.4 Polyglot services

Rust for the tracking endpoint (unauth, blast-volume, wants to be tiny and
fast), a BEAM service for realtime fanout and the multiplayer canvas (built for
millions of stateful connections).

**Verdict: skip.** Inroad's tracking endpoint is already allocation-free pure Go
(`platform/botfilter` is explicit about this), and its realtime hub — one
`PSubscribe` per process, in-process fan-out, bounded replay, monotonic seq — is
a solid single-node design. Adding two more languages to a "single Go module"
project trades the thing that makes Inroad easy to run for scale it doesn't have
yet. Revisit realtime only if concurrent connections reach the tens of thousands.

## 3. Features the reference platform ships that Inroad does not

Grouped by how much they matter for "on par." Each has a verdict for
`04-parity-plan.md`.

### Tier 1 — table stakes for the category; Inroad looks incomplete without them

| Feature | Verdict |
|---|---|
| ~~**Outbound webhooks** (HMAC-signed, event-filtered, retried, SSRF-guarded)~~ | ~~**Replicate, P0.**~~ **✅ DONE server-side** (#171): `app/webhook` + `worker/webhook` + `platform/webhookwire`. Only the management UI is outstanding. |
| **Pre-send email verification** (syntax → MX → SMTP RCPT → catch-all, from a non-sending IP) | **Do-better, P1.** Pluggable provider seam + a built-in SMTP-probe implementation. Cuts bounce rate before it costs reputation. |
| **Complaint / FBL / ARF ingestion** + a deliverability event ingest API | **Replicate, P1.** Inroad parses DSNs already; add ARF parsing and an idempotent ingest endpoint for external pipelines. |
| **Hosted lead-capture forms** (builder, embed, submission → contact → campaign) | **Replicate, P1.** A whole sub-app in the reference platform, but high-value: it's how contacts get *in* without a CSV. |
| **Native integrations + iPaaS connectors** (CRM sync, chat, scheduling; Zapier/Make/n8n) | **Replicate, P2.** Depends on webhooks (P0). Start with Slack + a generic webhook; the iPaaS packages are thin wrappers over the public API. |
| **Attachments** (per-step / campaign-wide, storage-quota enforced) | **Replicate, P2.** Needs a blob-storage seam (filesystem default, S3 opt-in) Inroad doesn't have yet. |

### Tier 2 — meaningful product depth; parity needs most of these

| Feature | Verdict |
|---|---|
| **Visual automation canvas** (trigger → condition → action, IF branches, error path) | **Do-better, P2.** Big. The jsonb branching-tree config model (typed at the app boundary, `CHECK` on the discriminator) is the pattern to copy. |
| **Branch-on-behaviour in sequences** (opened/clicked/replied ± N-day, random split) | **Replicate, P2.** Inroad has the events; it needs conditional edges between steps. |
| **Action steps + switch steps** in sequences | **Replicate, P2.** Pairs with the canvas. |
| **AI contact research** (web read → cited facts + openers) | **Defer, P3.** Needs an outbound web-fetch capability and a citation model; valuable but not parity-critical. |
| **AI steps in sequences/automations** + **AI variables** (per-recipient copy at send time) | **Defer, P3.** Requires the automation canvas first. |
| **Inbox agent** (auto-draft a reply to every inbound, held for approval) | **Replicate, P2.** Inroad has on-demand AI drafts + the approval queue; wire it to the reply hook. |
| **Meetings** (Calendly / Cal.com webhooks, `.ics`, attribution) | **Replicate, P2.** Inbound webhook + a `meetings` record type in `features/records`. |
| **CSV import wizard** (preview → map → commit, dedup strategy, XLSX) | **Replicate, P2.** Inroad has headless CSV import; add the mapping UI + XLSX. |
| **Contact export** (CSV/XLSX/JSON, scoped, custom columns) | **Replicate, P1.** Cheap, and its absence is conspicuous. |
| **Dynamic segments** that keep enrolling into linked campaigns | **Do-better, P2.** Inroad has static lists; make list membership a stored query. |
| **Notifications system** (in-app feed + email digest + Slack, per-category) | **Replicate, P2.** |
| **Per-mailbox rolled human workday** (randomised start/finish/lunch/hourly ceiling, in the mailbox's tz) | **Do-better, P2.** Inroad has windows + seeded cadence; add the once-per-local-day rolled-and-stored workday so "why is nothing sending" has an answer. |
| **Warmup content generation pipeline** (AI plans, schema-validated, safety-linted, usage-balanced, auto-retired at ≥15% spam) | **Do-better, P3.** Inroad has the `ContentGenerator` seam and a static library; build the batch/lint/auto-retire bank behind it. |
| **Warmup header-loss fallback** (match on envelope-to + provider-assigned Message-ID when the token header is stripped) | **Replicate, P1.** Cheap correctness fix; without it warmup from Microsoft mailboxes under-counts. |
| **IMAP/SMTP compatibility hardening** (no-CONDSTORE via UIDNEXT, STATUS fallback, AUTH negotiation CRAM-MD5→LOGIN→PLAIN, localized folder names, EHLO with real domain, per-response deadlines) | **Replicate, P1.** The reference platform learned these from real user tickets. Each is a small, self-contained fix and each one is a mailbox that otherwise silently fails. |

### Tier 3 — operability & platform; needed to be a *product*, not just an engine

| Feature | Verdict |
|---|---|
| **One-command installer** (published script, pulls release images, checksummed, wizard) | **Replicate, P1.** Biggest first-impression gap for self-hosters. |
| **Operator CLI** (create account, set password, grant admin, instance status — direct to DB) | **Replicate, P1.** `cmd/inroadctl`. Recovery when login is broken. |
| ~~**Release + container-publish CI** (tagged releases, published images, `CHANGELOG`)~~ | ~~**Replicate, P0.**~~ **✅ DONE** (#171): `release.yml` + `build-push.yml` + `platform/version` + `CHANGELOG.md`. |
| ~~**Security CI** (govulncheck, npm audit, Trivy) + Dependabot~~ | ~~**Replicate, P0.**~~ **✅ DONE** (#171): `security.yml` + `dependabot.yml`. |
| **Admin console** (users / workers / campaigns, impersonation, platform analytics) | **Defer, P3.** Only matters for a multi-tenant hosted deployment. |
| **Org audit trail** (who did what, secrets never recorded) | **Replicate, P1.** Security doc lists it as deferred; it's table stakes for teams. |
| **Workspace export / import** (portable archive, move instances) | **Do-better, P2.** The reference platform's registry-driven spec (every org table declared with scope/order/group/secret-domain) is the pattern. |
| **Account danger zone** (delayed hard-delete + grace window) | **Replicate, P2.** |
| **SSO / OIDC + Apple sign-in** | **Replicate, P2.** Inroad has Google + passkeys; add generic OIDC. |
| **Login-risk / anomaly scoring, new-device alerts** | **Defer, P3.** |
| **Instance-health dashboard / doctor command** | **Replicate, P2.** Extends `/readyz` into a multi-dependency probe (add Redis — see the Redis review). |
| **Website-visitor tracking** | **Defer, P3.** Adjacent product, not core. |
| **Native mobile app** | **Skip.** The SPA is responsive; a native app is a second client to maintain for marginal reach. Revisit only if there's demand. |

## 4. Where Inroad is ahead — keep these

- **Determinism written into the engine.** `platform/cadence` recomputes the
  identical send instant on retry from a seeded hash of stable ids; the DB
  row-claim is documented as the delivery-idempotency guarantee with queue dedup
  as explicit defence-in-depth. The reference platform's docs describe similar
  behaviour but Inroad's is enforced and tested at the unit level.
- **Send windows unrepresentable-if-overlapping** via a GiST exclusion
  constraint — illegal state made impossible at the schema, not validated in app
  code.
- **First-class Company record.** The reference platform's CRM (like the
  incumbent's) has no Company object; Inroad models contacts, companies and deals
  polymorphically with one notes/tasks/activity implementation serving all three.
- **The pulse read-model.** One O(1) aggregate driving the sidebar card + nav
  counts + today's-sends meter, severity-sorted, "promotes the worst problem
  first." The reference platform has notifications but not this single-glance "is
  everything okay" surface.
- **Test depth.** 360 test files over 33 domains vs 237 over 89 — ~11 per domain
  against ~2.7. Per-feature, Inroad is far better covered: the
  branch/empty/error cases, tenant-scoping guards on every query (`4321fe9`),
  the "no `@/features/*` import" enforcement test.
- **Heavily documented invariants.** `docs/security.md`, the migration-numbering
  rationale, the coreapi seam contract. A new contributor can learn *why* from
  the tree.

Do not regress any of these while chasing breadth.

## 4b. What moved on the competitor's side between the two reads

Re-pulling `main` for the 2026-09-08 pass brought 76 commits dated 2026-09-07/08
alone. Two of them change this document's conclusions rather than its details:

- **The admin console is no longer a "defer, P3" observation about someone
  else's roadmap — its backend just landed.** Roughly forty admin endpoints in
  one pass: mailbox sync-governor state with clear-throttle and restart-backfill,
  in-flight send reservations, cross-workspace dead letters with replay, task
  failures, webhook delivery health with reclaim, fleet capacity, a control-loop
  decision log, dedicated worker bindings, operator-driven workspace export and
  import, per-org API keys and webhooks, warmup abuse history, and signups by
  acquisition channel. It also added a `scheduled_job_runs` table that **every**
  backend and consumer loop now records through, with a run-now request picked up
  within fifteen seconds.

  The transferable idea is not the console. It is `scheduled_job_runs`: a single
  table every periodic loop writes an ok/error row to, which turns "is the
  domain-auth sweep actually running" from a log-grep into a query. Inroad has
  ~10 such loops (`worker/maintenance`, `domainauth`, `recipientesp`,
  `deliverability`, the warmup pollers) and no equivalent surface. It is an S-M
  job, it composes with P2.7's instance-health dashboard, and it is worth
  pulling forward ahead of anything in P3.

- **Their self-hosting story got another pass** ("make self-hosting work end to
  end and rewrite the guide around what was tested"), and mailbox connectivity
  keeps absorbing real-user breakage: a third security mode for a mail server on
  the same machine, IMAP folder identity by name rather than `UIDVALIDITY`
  (RFC 3501 never promised uniqueness across folders), SMTP auth negotiation
  handed the normalized host, Message-ID matching with *and* without angle
  brackets, and an in-house SMTP verifier gaining HELO/MAIL-FROM passthrough
  plus an optional third-party verification key.

  Every one of those is a P1.8 line item, and the Message-ID bracket fix is a
  bug Inroad can have today without knowing it. **This is the strongest evidence
  for the P1.8 verdict below: do not derive that list from first principles when
  it can be read off a competitor's commit log.**

Read together: the operability gap is not closing on its own, and the parts of
it that matter most to a self-hoster (does my mailbox connect, is my scheduled
job running, can I recover without the UI) are exactly the parts Inroad has
deferred.

## 5. The honest summary

The reference platform is what Inroad looks like after two more years of product
work: same bones, three times the surface, a real operate story. Nothing it has
is unbuildable — but the gap is roughly **20–30 features and a platform layer**,
not a weekend. `04-parity-plan.md` sequences that.

The competitive question isn't "can Inroad match it feature-for-feature" (yes,
eventually) — it's "which of these features does an Inroad user actually need
first," and the answer is: webhooks, email verification, forms, the installer,
and release CI. Those five put Inroad in the same conversation.
