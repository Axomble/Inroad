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

## P0 — Foundational, cheap, do first (days each)

| # | Item | Effort | Why now |
|---|---|---|---|
| P0.1 | **Release + container-publish CI** — `release.yml` (tagged, `CHANGELOG.md`), `build-push.yml` (GHCR images for api/worker/web) | S | Prerequisite for the installer (P1.1) and for anyone running Inroad without building from source. The reference platform has both; Inroad has neither. |
| P0.2 | **Security CI** — `govulncheck`, `npm audit` / `osv-scanner`, Trivy image scan, plus `.github/dependabot.yml` | S | Called out in the project review as the clearest supply-chain gap. The reference platform ships a dedicated security workflow. |
| P0.3 | **Outbound webhooks** — `internal/app/webhook`: HMAC-signed (`t=…,v1=…` over `t.body`), event-filtered subscriptions, retry with backoff, SSRF guard on the target URL, delivery log | M | The single highest-leverage missing primitive. Hangs off the existing `app/events` bus. Unblocks every integration (P2.x) and matches the incumbent's event webhooks. |
| P0.4 | **Redis in `/readyz`** + a multi-dependency probe | S | From the Redis review: `/readyz` pings Postgres only, so a Redis outage (every login 429s, every enqueue fails) reads as healthy. Grows into the instance-health dashboard (P2.7). |
| P0.5 | **Redis connection config** — accept `redis://` / `rediss://` URL, support password/TLS, one `dialRedis()` constructor | S | From the Redis review: `INROAD_REDIS_ADDR` is address-only; managed Redis (auth/TLS) can't connect. |

## P1 — Core value: the five features that put Inroad in the conversation

| # | Item | Effort | Notes |
|---|---|---|---|
| P1.1 | **One-command installer** — `scripts/install.sh` served from the docs site, checksummed, pulls the P0.1 images, writes a real `.env`, prints a claim link. POSIX sh, `--dry-run`, optional `--wizard` | M | The reference platform's published install script is the model (and its hard-won rules: POSIX not bash, `set -eu`, `main "$@"` last, idempotent, never regenerate a key). Biggest self-host first-impression gap. |
| P1.2 | **Operator CLI** — `cmd/inroadctl`: create user, set password, grant admin, workspace list, instance status; talks to Postgres directly so it works when auth is broken | M | Mirrors the reference platform's operator CLI. Bake it into the api image. |
| P1.3 | **Pre-send email verification** — `internal/app/emailverify`: a `Verifier` seam (accept-interface) with a built-in implementation (syntax → MX → SMTP RCPT probe from a configurable non-sending source → catch-all detection), cached per domain, run at CSV import and at campaign preflight | M | The incumbent bundles it; the reference platform has a dedicated verification module. Pluggable so a third-party provider can be dropped in. Cuts bounce rate before it costs reputation. |
| P1.4 | **Complaint / FBL ingestion + deliverability event ingest API** — ARF parsing on inbound mail, `POST /deliverability/events` (idempotent, API-key scoped) for external processors (SES/SNS, Postmark) | M | Inroad parses DSNs already; this is the complaint half. Feeds the existing deliverability score + circuit breaker. |
| P1.5 | **Contact export** (CSV / XLSX / JSON, scoped to a filter, choose columns) + **CSV import wizard** (preview → column map → dedup strategy, XLSX support) | M | Export is S on its own and conspicuously absent. The wizard is the bigger half. |
| P1.6 | **Audit log** — `internal/app/audit`: append-only, who/what/when, secret values never recorded, workspace-scoped read endpoint + UI | M | Security doc lists it as deferred; table stakes for teams. |
| P1.7 | **Warmup header-loss fallback** — match an inbound warmup message on (envelope-to, provider-assigned Message-ID) when the `X-Inroad-Warmup` header was stripped (Microsoft) | S | Cheap correctness fix; without it warmup from M365 mailboxes under-counts placement. |
| P1.8 | **IMAP/SMTP compatibility hardening** — AUTH method negotiation (CRAM-MD5 → LOGIN → PLAIN), no-CONDSTORE via per-folder UIDNEXT, STATUS fallback when no LIST-STATUS, localized special-use folder names, EHLO with the real sender domain, per-response deadlines, widening backoff on an unreachable server without deactivating the mailbox | M | Each sub-item is a mailbox that otherwise silently fails. The reference platform learned these from real user tickets — copy the list. |

**Exit criterion for P1:** a stranger can `curl | sh` an install, connect a
mailbox that isn't vanilla Gmail, import a verified list, run a campaign, receive
a webhook on every reply, export the results, and see an audit trail — with a CLI
to recover if they lock themselves out.

## P2 — Breadth: most of these needed for reference-platform parity

| # | Item | Effort | Notes |
|---|---|---|---|
| P2.1 | **Hosted lead-capture forms** — form builder (fields, pages, design), a hosted/embeddable public page, submission store, submission → contact (+ custom fields) → optional campaign enrollment | L | The reference platform runs this as a dedicated sub-app + public form server. Can start smaller: one templated form type, JSON field spec, a `formserver` route. It's how contacts get in without a CSV. |
| P2.2 | **Blob-storage seam** — `platform/blobstore`: filesystem default, S3-compatible opt-in | M | Prerequisite for P2.3 and for storing raw message bodies. Mirrors the reference platform's encrypted-body-in-blob-store design. |
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
| P2.14 | **Unified inbox at scale** — folder model (6 provider folders mapped from IMAP special-use / Gmail labels / Graph well-known), scope rail (Unread / Today / Awaiting reply / Snoozed / Scheduled / per-mailbox), server-computed counts, keyset pagination, snooze + draft autosave | L | The place reviews say the incumbent "gets clunky." A chance to be better cheaply. |
| P2.15 | **Workspace export / import** — registry-driven: every org-scoped table declared with scope / order / group / secret-key-domain / blob columns; archive out, archive in on another instance; `inroadctl workspace export\|import` | L | The reference platform's registry-driven export spec is the pattern — including the rule that a migration adding an org table isn't done until it's in the registry. |
| P2.16 | **Account danger zone** — delayed hard-delete with a grace window and a cancel path | S | |
| P2.17 | **Generic OIDC SSO** (+ Apple sign-in) | M | Inroad has Google + passkeys; add a standards OIDC connector. |
| P2.18 | **DLQ replay** — a re-drive action on the failed-task queue (it's already surfaced + paged) | S | |
| P2.19 | **Custom tracking domain per mailbox/campaign** — extend beyond "a verified domain overrides the host" to per-mailbox CNAME config + verification UI | M | |

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

1. **Sprint 1 (P0):** release CI, security CI, webhooks, Redis readiness + config. Ships the plumbing everything else needs.
2. **Sprint 2–3 (P1):** installer + CLI, email verification, complaint ingestion, export/import wizard, audit log, warmup header fix, IMAP hardening. This is the "credible peer" milestone.
3. **Then P2 in dependency order:** blob seam → attachments; webhooks → integrations → meetings; automation canvas → branching/action/switch steps; the rest as capacity allows.
4. **P3 is opportunistic** — pull items forward when a user actually asks.

Re-score `01-feature-matrix.md` at the end of each sprint.
