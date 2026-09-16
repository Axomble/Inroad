# 04 — Parity plan

The sequenced route to "on par or better than the reference platform and the
hosted incumbent," mapped onto Inroad's single-Go-module architecture. Derived
from the gaps in [`01-feature-matrix.md`](01-feature-matrix.md),
[`02-reference-platform.md`](02-reference-platform.md) and
[`03-hosted-incumbent.md`](03-hosted-incumbent.md).

**Effort:** S ≈ days · M ≈ 1–2 weeks · L ≈ several weeks · XL ≈ months.
**Priority:** P0 foundational/cheap · P1 core value · P2 breadth · P3 later.

Nothing here regresses the things Inroad is already ahead on (see
`02-reference-platform.md` §4) — determinism, exclusion-constraint windows, the
pulse read-model, test depth, first-class Company.

---

## Reconciled against the tree — 2026-09-15 @ `3d0273e`

Every item below was checked against the actual package, route and migration
trees rather than against this document's own wording. That distinction
mattered: searching for a plan item's phrasing finds nothing when the code
shipped under a different name, which is how six completed items kept reading as
outstanding. **The plan was stale in both directions, and nearly sent work to
rebuild code that already ships.**

**Marked outstanding, actually done:** P0.3 (the webhooks UI exists, with a
route and four test files), P1.1 (installer), P1.2 (operator CLI), P1.4 (ARF
parsing), P1.7 (warmup header-loss fallback).

**Described wrongly:** P2.2 — the filesystem implementation and the from-env
factory both ship; only the first caller is missing. P2.21 — the write side is
wired at six call sites, but there is no read query, so it is a write-only
ledger. P2.7 — `inroadctl status` already covers the CLI half.

**Undocumented epic.** Nine commits (`3346b93`…`3d0273e`) built a multi-IP
sending fleet: control/send worker roles with role-scoped queues, worker
identity derived from the host's public IP, risk-band placement, an append-only
fleet decision log, and per-worker provider-signal collection. P3.9 asks for
much of this and still reads as blocked on P3.8. It is not — it was built
without waiting for the literal HTTP split, which remains outstanding.

~~**Confirmed dead code**~~ **RESOLVED 2026-09-16.**
`ListFleetDecisionsForMailbox` (`internal/platform/db/gen/fleet.sql.go`) had no
production caller — only an integration test. The Fleet operator view (#210)
gave it the reader it was written for: `internal/app/fleet/store.go:90` calls it
behind the domain's `Store` seam, and both files record that history in place.

One earlier claim of mine was wrong and is retracted: `internal/platform/jobrun`
is **not** dead. There is no `jobrun.Loop`; the API is `jobrun.Record`, and it
has six production call sites. The name came from a peer system, not from ours.

Two items remain uncertain and are deliberately not marked: **P3.6** (whether
`platform/warmup/content.go` implements the full generation pipeline or just
predates it) and **P2.19**'s baseline claim that a verified domain already
overrides the host — nothing in `internal/worker/sequence/advance.go`
corroborates it.

---

## P0 — Foundational, cheap, do first (days each)

> **✅ P0 IS COMPLETE.** All five landed in the P0 sprint (#171, merged
> 2026-09-07), the same PR that introduced this document — which is why the
> matrix's first pass scored them as missing. Verified against `d4f5720`.

| # | Item | Effort | Why now |
|---|---|---|---|
| ~~P0.1~~ ✅ | ~~**Release + container-publish CI** — `release.yml` (tagged, `CHANGELOG.md`), `build-push.yml` (GHCR images for api/worker/web)~~ **DONE** — both workflows ship, multi-arch, with `platform/version` stamped at link time, `checksums.txt`, and a Keep-a-Changelog `CHANGELOG.md` parsed for the release body. | S | Prerequisite for the installer (P1.1) and for anyone running Inroad without building from source. The reference platform has both; ~~Inroad has neither~~ Inroad now has both. |
| ~~P0.2~~ ✅ | ~~**Security CI** — `govulncheck`, `npm audit` / `osv-scanner`, Trivy image scan, plus `.github/dependabot.yml`~~ **DONE** — `security.yml` (govulncheck reachable-symbol + `npm audit --audit-level=high` + Trivy FS for vuln/secret/misconfig) on PR, push and weekly; `dependabot.yml` covers gomod, npm, actions and the Dockerfiles. Informs rather than gates, deliberately. | S | Called out in the project review as the clearest supply-chain gap. |
| ~~P0.3~~ ⚠️ | ~~**Outbound webhooks** — `internal/app/webhook`: HMAC-signed (`t=…,v1=…` over `t.body`), event-filtered subscriptions, retry with backoff, SSRF guard on the target URL, delivery log~~ **BACKEND DONE** — `app/webhook`, `worker/webhook/deliver.go`, `platform/webhookwire` (`sign.go` + `ssrf.go`), migration `20260907132955_webhook`, and `/webhook-endpoints` ×5 incl. `rotate-secret`, `ping`, `deliveries`. ~~**Remaining: the UI**~~ **DONE 2026-09-15** — `web/src/features/webhooks/` ships `webhooks-page.tsx`, `webhook-endpoint-row.tsx`, `delivery-log.tsx`, `secret-reveal.tsx` and `webhook-copy.ts` with four test files, routed at `web/src/routes/app.settings.webhooks.tsx`. **P0.3 is fully done**; the ⚠️ above is stale. | M | The single highest-leverage missing primitive. Unblocks every integration (P2.x). |
| ~~P0.4~~ ✅ | ~~**Redis in `/readyz`** + a multi-dependency probe~~ **DONE** — `/readyz` pings Postgres *and* Redis under a 2s budget. Still grows into P2.7. | S | A Redis outage fails every login closed at the rate limiter, yet a Postgres-only probe reported ready. |
| ~~P0.5~~ ✅ | ~~**Redis connection config** — accept `redis://` / `rediss://` URL, support password/TLS, one `dialRedis()` constructor~~ **DONE** — `internal/platform/redisconn`. | S | `INROAD_REDIS_ADDR` was address-only; managed Redis (auth/TLS) could not connect. |

## P1 — Core value: the five features that put Inroad in the conversation

| # | Item | Effort | Notes |
|---|---|---|---|
| ~~P1.1~~ ✅ | ~~**One-command installer** — `scripts/install.sh` served from the docs site, checksummed, pulls the P0.1 images, writes a real `.env`, prints a claim link. POSIX sh, `--dry-run`, optional `--wizard` | M | The reference platform's published install script is the model (and its hard-won rules: POSIX not bash, `set -eu`, `main "$@"` last, idempotent, never regenerate a key). Biggest self-host first-impression gap.~~ **DONE 2026-09-15** — `scripts/install.sh` (POSIX `sh`, `set -eu`, `main "$@"` last, idempotent `.env`, `--dry-run`, `--dir`), `deploy/docker/docker-compose.prod.yml`, referenced from the deploy docs. Two deliberate deviations: no `--wizard`, and it prints an `inroadctl create-user` command rather than a claim link. **Blocked in practice until the GHCR packages are made public** — a real `curl | sh` currently fails `unauthorized` on image pull. **Confirmed 2026-09-16**: anonymous manifest pulls of `inroad-api`, `inroad-worker` and `inroad-web` all return HTTP 403. The fix is a repo settings change (each package → Package settings → Change visibility → Public), not code; the `git clone` + `docker compose up` path in the README is unaffected because it builds locally. |
| ~~P1.2~~ ✅ | ~~**Operator CLI** — `cmd/inroadctl`: create user, set password, grant admin, workspace list, instance status; talks to Postgres directly so it works when auth is broken | M | Mirrors the reference platform's operator CLI. Bake it into the api image.~~ **DONE 2026-09-15** — `cmd/inroadctl/` with `create-user`, `set-password` (stdin or hidden prompt, never argv), `grant-role`, `workspaces`, `users`, `status`; baked into `Dockerfile.api`. `status` also covers much of P2.7's CLI half: Postgres reachability, migration version + dirty flag, live-worker count, Redis. |
| P1.3 | **Pre-send email verification** — `internal/app/emailverify`: a `Verifier` seam (accept-interface) with a built-in implementation (syntax → MX → SMTP RCPT probe from a configurable non-sending source → catch-all detection), cached per domain, run at CSV import and at campaign preflight | M | The incumbent bundles it; the reference platform has a dedicated verification module. Pluggable so a third-party provider can be dropped in. Cuts bounce rate before it costs reputation. |
| P1.4 | **Complaint / FBL ingestion + deliverability event ingest API** — ~~`POST /deliverability/events` (idempotent, API-key scoped) for external processors (SES/SNS, Postmark)~~ **the ingest API already ships** and a complaint already suppresses + feeds the score and breaker. ~~**Remaining: ARF parsing on inbound mail**~~ **DONE** — `internal/worker/inbox/arf.go` implements RFC 5965 `ParseARF`, with unit, integration and poll-path tests. **P1.4 is fully done.** | ~~M~~ **S** | Downgraded: only the inbound-mail half is left. |
| P1.5 | **Contact export** (CSV / XLSX / JSON, scoped to a filter, choose columns) + **CSV import wizard** (preview → column map → dedup strategy, XLSX support) | M | Export is S on its own and conspicuously absent. The wizard is the bigger half. |
| P1.6 | **Audit log** — `internal/app/audit`: append-only, who/what/when, secret values never recorded, workspace-scoped read endpoint + UI | M | Security doc lists it as deferred; table stakes for teams. |
| ~~P1.7~~ ✅ | ~~**Warmup header-loss fallback** — match an inbound warmup message on (envelope-to, provider-assigned Message-ID) when the `X-Inroad-Warmup` header was stripped (Microsoft) | S | Cheap correctness fix; without it warmup from M365 mailboxes under-counts placement.~~ **DONE** — `internal/worker/inbox/poll.go`'s `recoverWarmupSendID`, with `poll_warmupfallback_test.go`. |
| P1.8 | **IMAP/SMTP compatibility hardening** — AUTH method negotiation (CRAM-MD5 → LOGIN → PLAIN), no-CONDSTORE via per-folder UIDNEXT, STATUS fallback when no LIST-STATUS, localized special-use folder names, EHLO with the real sender domain, per-response deadlines, widening backoff on an unreachable server without deactivating the mailbox | M | Each sub-item is a mailbox that otherwise silently fails. The reference platform learned these from real user tickets — copy the list. |

**Exit criterion for P1:** a stranger can `curl | sh` an install, connect a
mailbox that isn't vanilla Gmail, import a verified list, run a campaign, receive
a webhook on every reply, export the results, and see an audit trail — with a CLI
to recover if they lock themselves out.

## P2 — Breadth: most of these needed for reference-platform parity

| # | Item | Effort | Notes |
|---|---|---|---|
| P2.1 | **Hosted lead-capture forms** — form builder (fields, pages, design), a hosted/embeddable public page, submission store, submission → contact (+ custom fields) → optional campaign enrollment | L | The reference platform runs this as a dedicated sub-app + public form server. Can start smaller: one templated form type, JSON field spec, a `formserver` route. It's how contacts get in without a CSV. |
| P2.2 | **Blob-storage seam** — ~~`platform/blobstore`:~~ `platform/storage` **already exists** with the `Provider` interface and a complete, tested S3 implementation — but **nothing imports it**, there is no filesystem implementation, and no env var reaches `platform/config`, so today it is dead code. ~~**Remaining: the filesystem default + a from-env factory + the first caller.**~~ **CORRECTED 2026-09-15** — `fs.go` (271 lines, path-traversal guarded), `factory.go`'s `FromEnv`, and every `INROAD_STORAGE_FS_ROOT`/`INROAD_S3_*` config var all ship. **Only the first caller is missing**, and `config.go:58` says so itself. This is the highest leverage-per-effort item on the list: it is what unblocks P2.3. | ~~M~~ **S** | Prerequisite for P2.3 and for storing raw message bodies. Either finish it here or delete it and re-add it with its first caller — leaving an unwired package contradicts the repo's own no-dead-code rule. |
| P2.3 | **Attachments** — per-step and campaign-wide files, workspace storage quota, quota-race-safe upload, carried through the send + test-send + preview paths | M | Needs P2.2. |
| P2.4 | **Visual automation canvas** — `internal/app/automation`: trigger (reply / bounce / unsub / meeting / form / inbound webhook / warmup-health-change / campaign-step) → IF condition → action (tag, task, deal, notify-webhooks, run-sub-automation, stop). jsonb config per node with a Go struct + validation on write and a `CHECK` on the discriminator | L | The reference platform's jsonb branching-tree config is the model. This is the big one; it unlocks P2.5, P3.2, P3.3. |
| P2.5 | **Branch-on-behaviour + action steps + switch steps in sequences** | L | Conditional edges between steps (opened / clicked / replied ± N-day window, random split); non-email nodes. Pairs with P2.4's node model. |
| P2.6 | **Native integrations** — Slack (P2 first), then HubSpot / Pipedrive / Calendly / Cal.com; ship thin **Zapier** and **Make** apps over the public API | L | All depend on P0.3 (webhooks). Calendly/Cal.com also give you **Meetings** (P2.8). |
| P2.7 | **Instance-health dashboard / `inroadctl doctor`** — multi-dependency probe (Postgres, Redis, queue depth, worker heartbeat, blob store, outbound SMTP), surfaced in-app and on the CLI | M | Extends P0.4. |
| P2.8 | **Meetings** — Calendly / Cal.com inbound webhooks (+ manual entry), `.ics`, status lifecycle, campaign/mailbox attribution, as a `meetings` record type in `features/records` | M | |
| P2.9 | **Notifications system** — in-app feed + email digest + Slack, per-category opt-in; distinct from the pulse card (which stays the at-a-glance surface) | M | |
| P2.10 | **Dynamic segments** — make list membership a stored query that keeps enrolling into linked campaigns; keep static lists too | M | |
| P2.11 | **Per-mailbox rolled human workday** — once per mailbox per local day, roll+store {start, finish, lunch, daily target, hourly ceiling} from ranges; scheduler reads the stored plan; "today's workday" shown on the mailbox | M | Inroad has windows + seeded cadence; this adds the stored-daily-plan layer so "why is nothing sending right now" has a concrete answer. Can only ever lower volume / delay. |
| P2.12 | **Seed inbox-placement testing** — send tokenised copies to a configured set of seed mailboxes across providers, classify inbox vs spam per provider, surface as its own dashboard section + a pre-scale check | M | Reuses the warmup spam-detection path. Distinct from the warmup-derived signal Inroad already has. |
| P2.13 | **Inbox agent** — auto-draft a reply to every classified inbound, held in the existing approval queue, off the reply hook | M | Inroad has on-demand AI drafts + the approval queue; this makes it always-on. HITL stays the default. |
| P2.14 | **Unified inbox at scale** — ~~scope rail (Unread / Today / Awaiting reply / Snoozed / Scheduled / per-mailbox), server-computed counts, keyset pagination, snooze + draft autosave~~ **all of that shipped** (`all/unread/today/this_week/awaiting_reply/snoozed`, an unknown scope 400s, rail counts share the list's clock, `/inbox/drafts`, `/snooze`, `/schedule-reply`, `/outbox` cancel, thread labels). **Remaining: the folder model** — 6 provider folders mapped from IMAP special-use / Gmail labels / Graph well-known, plus moves. | ~~L~~ **M** | The place reviews say the incumbent "gets clunky." A chance to be better cheaply. |
| P2.15 | **Workspace export / import** — registry-driven: every org-scoped table declared with scope / order / group / secret-key-domain / blob columns; archive out, archive in on another instance; `inroadctl workspace export\|import` | L | The reference platform's registry-driven export spec is the pattern — including the rule that a migration adding an org table isn't done until it's in the registry. |
| P2.16 | **Account danger zone** — delayed hard-delete with a grace window and a cancel path | S | |
| P2.17 | **Generic OIDC SSO** (+ Apple sign-in) | M | Inroad has Google + passkeys; add a standards OIDC connector. |
| ~~P2.18~~ ✅ | ~~**DLQ replay** — a re-drive action on the failed-task queue (it's already surfaced + paged)~~ **DONE** — `POST /dead-letters/{id}/replay` plus `/discard`, wired to the settings route. | S | |
| P2.19 | **Custom tracking domain per mailbox/campaign** — extend beyond "a verified domain overrides the host" to per-mailbox CNAME config + verification UI | M | |
| P2.20 | **Advisor-lite: deterministic sending-posture detectors** — ~30 pure-Go, no-model checks (cap too high for the ramp, warmup off while sending, no follow-up steps, unsub header disabled, list exhaustion, narrow send window, spammy copy, SPF/DKIM drift) surfaced inline on the row they concern | M | **Added 2026-09-08.** The reference platform has since shipped this as a first-class domain, and it is the cheapest differentiation available: no LLM dependency, no new infrastructure, and `app/pulse` is already the delivery vehicle (severity-sorted, promotes the worst problem first). It fits the honest-metrics posture Inroad is already ahead on. AI narration can layer on later behind the existing seam. |
| P2.21 ◐ | **Scheduled-job run ledger** — **WRITE SIDE DONE 2026-09-15**, read side missing — one table every periodic loop writes an ok/error row to (`worker/maintenance`, `domainauth`, `recipientesp`, `deliverability`, the warmup pollers), with last-run / last-error / duration surfaced in-app, and a run-now request the owning process picks up on its next tick | S–M | **Added 2026-09-08**, from the competitor's `scheduled_job_runs` (see `02-reference-platform.md` §4b). Turns "is the sweep actually running" from a log-grep into a query. Composes directly with P2.7's instance-health dashboard and is the single cheapest operability win on this list. **Reconciled 2026-09-15:** `internal/platform/jobrun`'s `Record` decorator is wired at six real call sites (`internal/worker/handlers.go`, `internal/worker/inbox/register.go`), writing to `scheduled_job_runs` via migration `20260908123022`. **But the queries are `InsertScheduledJobRun` and `PurgeScheduledJobRuns` only — there is no List/read query at all**, so nothing surfaces last-run/last-error/duration and the run-now request does not exist. It is a write-only ledger. Remaining: a read query plus one endpoint or CLI command. |
| ~~P2.22~~ ✅ | ~~**Warmup containment must key on the address, not the mailbox row**~~ **DONE 2026-09-15** (#204) — the carry-forward lookup is now keyed on the address, with a migration (`20260914152247_warmup_containment_follows_address`) and a dedicated `containment_integration_test.go`. Original entry below, kept for the reasoning. — deleting a mailbox and re-adding the same address launders its containment history. `mailboxes.id` is `gen_random_uuid()`, so a re-added address gets a **new** id, while `warmup_state_transitions` (the thing that carries containment across a re-entry) is keyed on `mailbox_id`. The old transitions are orphaned and the address returns to `probation` — quarantine and blocked included. Re-key the carry-forward lookup on `(workspace_id, email)`, which already has a unique index (`mailboxes_workspace_email_key`) and is the real stable identity | S–M | **Added 2026-09-14, corrected same day.** The first version of this entry claimed disable→re-enable erased the penalty. **It does not** — `UpsertWarmupParticipant` deliberately reads the last sealed lane out of `warmup_state_transitions`, which has no FK to `mailboxes` precisely so it survives the participant `DELETE`, and carries `quarantine`/`blocked` forward. That path is correctly handled and well commented. Only the **mailbox-deletion** path is open, because it changes the key rather than removing the row. Narrower than first written, still a real laundering route, and cheap to close. |
| P2.23 | **Seed inbox-placement testing** — send tokenized copies of a real template through a real sender to a panel of controlled seed mailboxes, then classify Inbox / Spam / Promotions from the synced folder flags, surfaced per campaign and per mailbox | M–L | **Added 2026-09-14.** Measures the outcome customers actually buy; bounce and complaint rates only proxy it. Doubles as a placement-quality signal no competitor-independent source gives us. Capture Gmail's `CATEGORY_*` tab labels explicitly — without them a Promotions-tab message reads as "inbox" and the feature quietly overstates itself. Name the package `inboxtest`, never `placement`: fleet placement already owns that word. |

## P3 — Later: AI depth, platform, adjacent products

| # | Item | Effort | Notes |
|---|---|---|---|
| P3.1 | **AI contact research** — outbound web-fetch capability + a citation model + opener generation; gated behind the BYO provider key like the rest of AI | L | Needs a sanctioned egress path. |
| P3.2 | **AI steps in automations/sequences** — agent / generate / classify / extract nodes | M | Needs P2.4. |
| P3.3 | **AI variables** — per-recipient copy computed at send time, cached, with a spend cap | M | Needs P2.4 + a send-time hook. |
| P3.4 | **Adaptive follow-up timing** — tune step delays from observed engagement/response patterns | M | The incumbent markets this; keep the deterministic default and make adaptation opt-in. |
| P3.5 | **Autopilot inbox mode** — opt-in auto-send for the inbox agent, with rate/scope guardrails and a kill switch; HITL remains default | M | Competitive checkbox vs the incumbent's AI reply agent. |
| P3.6 | **Warmup content generation pipeline** — behind the existing `ContentGenerator` seam: AI batch build, schema validation, robotic-language + safety lint, usage-balanced draw, auto-retire at ≥15% spam over ≥20 samples | L | |
| P3.7 | **Warmup ban status + appeals flow** | M | |
| P3.8 | **Physical control/execution split** — give `coreapi` an HTTP transport, stand up an internal-API listener, worker gets a client not a `pgxpool`, DEK decryption moves behind it | L | The `coreapi` seam was built for this ("in-process now, HTTP later"). Do it when multi-VPS sending is actually needed. |
| P3.9 | **Mailbox → worker assignment engine** — assign by health band + load across the per-IP queues that already exist; fleet health quarantine + rebalance | L | Needs P3.8 to be worth much. Skip VPS auto-provisioning (dry-run even in the reference platform). |
| P3.10 | **Admin console** — separate SPA / route group: users, workers, campaigns, impersonation, platform analytics; separate permission model | XL | Only for a multi-tenant hosted deployment. |
| P3.11 | **Website-visitor tracking** | L | Adjacent product; only if there's demand. |
| P3.12 | **Login-risk / anomaly scoring, new-device alerts, session+location listing** | M | |
| P3.13 | **Billing** (Stripe, plans, trial, feature gating, referral/discount) | L | Only if Inroad runs a hosted tier. Self-host unlocks everything. |

## Explicitly not doing

| Item | Why |
|---|---|
| B2B lead database (~450M contacts) | A different company. Integrate the user's own source instead. |
| Done-for-you domain / mailbox provisioning | An ops business with lock-in — the opposite of Inroad's pitch. Document buying/configuring domains well instead. |
| Cross-customer warmup network | Can't pool strangers' mailboxes safely across self-hosted instances. Make the workspace-local pool excellent. |
| Second event-bus implementation (Kafka) | The `bus.Dispatcher` seam exists; a second impl is YAGNI until a deployment needs it. |
| Extra-language services (e.g. Rust / BEAM) | Trades "single Go module, easy to run" for scale Inroad doesn't have. Revisit realtime only past ~10k concurrent connections. |
| Native mobile app | The SPA is responsive; a second client for marginal reach. |
| Managed deliverability-as-a-service | A support org, not a feature. Offer it as paid support later. |

## Suggested sequencing

1. ~~**Sprint 1 (P0):** release CI, security CI, webhooks, Redis readiness + config. Ships the plumbing everything else needs.~~ **✅ SHIPPED** in #171 (2026-09-07), less the webhook UI.
2. **Sprint 2–3 (P1) — current sprint.** installer + CLI, email verification, ~~complaint ingestion~~ inbound ARF only, export/import wizard, audit log, warmup header fix, IMAP hardening. This is the "credible peer" milestone.
3. **Then P2 in dependency order:** blob seam → attachments; webhooks → integrations → meetings; automation canvas → branching/action/switch steps; the rest as capacity allows.
4. **P3 is opportunistic** — pull items forward when a user actually asks.

Re-score `01-feature-matrix.md` at the end of each sprint.

### The immediate list, in order (as of 2026-09-08 @ `d4f5720`)

Small enough to finish before the next re-score, and each one closes a row
rather than starting a surface:

1. **Webhook UI** (S) — finishes P0.3; the backend is already paid for.
2. **Blob seam: filesystem impl + config factory** (S) — or delete
   `platform/storage`. Do not leave it unwired a third sprint.
3. **Warmup header-loss fallback** (S, P1.7) — a correctness bug, not a feature:
   warmup from Microsoft mailboxes under-counts placement today, and placement
   drives the health state machine.
4. **Contact export** (S, half of P1.5) — the most conspicuous single absence in
   the CRM.
5. **Inbound ARF parsing** (S, rest of P1.4).
6. **Audit log** (M, P1.6) — the security doc has listed it as deferred since
   the beginning.
7. **Operator CLI** (M, P1.2), then **the installer** (M, P1.1) — in that order,
   because the installer wants a working `inroadctl` to print a claim link.
8. **Pre-send email verification** (M, P1.3) and **IMAP/SMTP hardening**
   (M, P1.8) — the two that most change whether a stranger's first campaign
   works.
