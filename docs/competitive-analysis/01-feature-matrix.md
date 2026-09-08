# 01 — Feature matrix

**Reconciled:** 2026-09-08 against Inroad `main` @ `d4f5720`, the reference
platform's published source (`main` @ its 2026-09-08 head), and the hosted
incumbent's public 2026 material.

> **Previous reconciliation:** 2026-09-07 @ `a124e98`. That pass was written
> *before* the P0 sprint (#171) merged, so it scored Inroad without the release
> CI, security CI, outbound webhooks, Redis readiness and Redis-URL work that
> landed in the very same PR. Rows corrected on this pass carry the stale mark
> ~~struck out~~ next to the verified one, so what moved is visible rather than
> silently rewritten.

Columns: **Ref** = the reference platform (open-source peer). **Incmb** = the
hosted incumbent (closed SaaS).

Legend: ✅ have · ⚠️ partial · ❌ missing · — not applicable (e.g. billing on a
self-hosted tool).

Where Inroad and the reference platform disagree only on depth, the note says so.
The incumbent is a hosted SaaS; its "✅" means the capability is sold, not that it
is self-hostable. Competitor-internal file paths are not reproduced here; only
Inroad's own paths are cited.

---

## 1. Campaigns & sequences

| Capability | Inroad | Ref | Incmb | Notes |
|---|:--:|:--:|:--:|---|
| Multi-step sequences, per-step delay, draft-gated structural edits | ✅ | ✅ | ✅ | Inroad core. |
| Campaign creation wizard (Basics / Schedule / Sending / First email) | ⚠️ | ✅ | ✅ | Inroad has a create form + activation checklist, not a stepped wizard. |
| Visual flow canvas (nodes + connectors decide routing) | ❌ | ✅ | ✅ | Inroad is a linear step list. Ref: collaborative canvas, up to 50 steps. |
| Branch on behaviour (opened / clicked / replied ± N days, random split) | ❌ | ✅ | ✅ | Inroad has stop-on-reply / stop-on-suppression only. |
| Action steps (add/remove tag, create task/deal, unsubscribe, notify, run automation) | ❌ | ✅ | ⚠️ | Non-email nodes that perform a control-plane action then route on. |
| Switch steps (multi-case routing; value or AI decider) | ❌ | ✅ | ⚠️ | |
| A/B variants per step, weighted split, per-arm stats | ✅ | ✅ | ✅ | Inroad: deterministic per-enrollment assignment, per-variant send/reply counts. |
| A/B winner analysis (confidence, auto-promote) | ⚠️ | ✅ | ✅ | ~~Inroad measures per-variant; no automated winner call.~~ Inroad **does** name a winner (`campaign/results.go`: highest reply rate, floor of 200 sends on one arm, 1.25× relative margin, `WinnerNote` explaining every abstention). Still ⚠️ only because there is no auto-promote. |
| Sender pool + rotation (round-robin / LRU / weighted) | ✅ | ✅ | ✅ | Inroad: all three, pure `platform/rotation`, per-enrollment not per-send. |
| ESP / provider matching (Gmail→Gmail, off/prefer/strict) | ✅ | ✅ | ✅ | Inroad: `recipientesp` sweep caches recipient provider by MX; never dials on the send path. |
| Timezone-aware send windows, per-weekday, multi-interval | ✅ | ✅ | ✅ | Inroad: IANA zone, overlaps unrepresentable via GiST exclusion constraint. |
| Natural-cadence scheduler (distribution curve, off-grid jitter, sub-minute humanisation) | ✅ | ✅ | ⚠️ | Inroad `platform/cadence`: every jitter a seeded hash of stable ids, so a retry recomputes the identical instant. |
| Per-mailbox human workday (randomised start/finish/lunch/hourly ceiling) | ⚠️ | ✅ | ⚠️ | Ref: per-mailbox rolled workday in the mailbox's own tz. Inroad has the window + cadence but not the rolled-workday model. |
| Stacked limits (mailbox cap ∧ campaign daily limit ∧ ramp ∧ health) | ✅ | ✅ | ✅ | Inroad: campaign limit can only lower throughput. |
| Campaign ramp-up (separate from warmup ramp) | ⚠️ | ✅ | ✅ | Inroad ramps the mailbox cap; no campaign-level ramp curve. |
| Lead-flow throttle (max new leads/day, prioritise follow-ups) | ~~❌~~ ✅ | ✅ | ✅ | **Was mis-scored.** `campaign/schedulehandler.go` carries `max_new_leads_per_day` alongside `daily_limit`, settable and clearable per campaign. |
| Preflight validation (scored readiness report, no send) | ✅ | ✅ | ⚠️ | Inroad `campaign/preflight.go`: tracking domain, unsub header, window, A/B config, custom-field refs. |
| Spintax (`{a\|b\|c}`, nested, subject+body) | ✅ | ✅ | ✅ | Inroad `platform/spintax`. |
| Personalisation / merge fields (`{{first_name}}`, custom fields) | ✅ | ✅ | ✅ | Inroad has fields + fallbacks; both competitors add conditionals + helpers. |
| Attachments (per-step or campaign-wide) | ❌ | ✅ | ✅ | Ref: per-step files, storage-quota enforced. |
| One-time / broadcast email (single message, no follow-ups) | ⚠️ | ✅ | ✅ | Inroad sends per-campaign; no dedicated one-off path. |
| AI-written / AI-adapted copy at send time | ⚠️ | ✅ | ✅ | Inroad has an AI agent that can edit steps; no per-recipient AI variables. |

## 2. Sending, reliability, deliverability

| Capability | Inroad | Ref | Incmb | Notes |
|---|:--:|:--:|:--:|---|
| Idempotent sends + stuck-send / stuck-enrollment sweepers | ✅ | ✅ | ✅ | Inroad: DB row-claim is the guarantee; queue dedup is defence in depth. |
| Dead-letter queue: surface + page + inspect | ✅ | ✅ | ⚠️ | Inroad `app/deadletter` + cursor-paged UI; payloads are row pointers, never content. |
| DLQ replay endpoint | ~~❌~~ ✅ | ⚠️ | ❌ | ~~Inroad has visibility, no re-drive button.~~ **Done** — `POST /dead-letters/{id}/replay` and `/discard`, surfaced on the settings route. Closes P2.18. |
| Bounce detection (DSN parse, hard → suppress, soft → keep) | ✅ | ✅ | ✅ | Inroad: RFC 3462/3464. |
| Complaint / FBL / ARF ingestion | ~~❌~~ ⚠️ | ✅ | ✅ | ~~Inroad suppresses on hard bounce + unsubscribe only.~~ **Half done.** The *ingest* half ships: `POST /deliverability/events` takes complaints from an external processor, idempotent on `provider_event_id`, and a complaint suppresses and feeds the score + breaker. **Still missing:** ARF/`Feedback-Type` parsing on inbound mail — `worker/inbox/dsn.go` only mentions ARF to avoid misreading a feedback report as a bounce. So an FBL that arrives *as mail* is not ingested. |
| One-click unsubscribe (RFC 8058, `List-Unsubscribe-Post`) | ✅ | ✅ | ✅ | |
| Workspace suppression list, signed unsub tokens, enforced at send | ✅ | ✅ | ✅ | |
| Open / click tracking, signed tickets, per-campaign toggle | ✅ | ✅ | ✅ | Inroad `platform/track`. |
| Bot / machine-open classification (MPP, proxies, scanners, prefetch) | ✅ | ✅ | ✅ | Inroad `platform/botfilter`: pure, no I/O; opens labelled "indicative", clicks "reliable". |
| Custom tracking domain (per-mailbox / per-campaign CNAME) | ⚠️ | ✅ | ✅ | Inroad: only a verified domain overrides the default host. |
| Domain authentication (SPF / DKIM / DMARC check, scheduled + on-demand) | ✅ | ✅ | ✅ | Inroad `app/sendingdomain` + `domainauth` sweep, hourly vs 24h staleness window. |
| Deliverability dashboard (0–100 score, bounce/complaint/spam trends, at-risk list) | ✅ | ✅ | ✅ | Inroad `app/deliverability` + `features/deliverability`. |
| Auto-pause campaign on bounce / complaint spike (circuit breaker) | ✅ | ✅ | ✅ | Inroad `deliverability:evaluate` task, evaluated after a send finalises, never inside the send txn. |
| Seed inbox-placement testing (tokenised copy to seed mailboxes, per-provider split) | ⚠️ | ✅ | ✅ | Inroad reports inbox-vs-spam from the warmup path only, not a dedicated seed network. |
| Pre-send email verification (syntax → MX → SMTP RCPT → catch-all), off a non-sending IP | ❌ | ✅ | ✅ | Inroad validates syntax at import only. Ref: a dedicated verification module. Incmb: multi-step + AI scoring. |
| Deliverability event ingest API (`POST /deliverability/events`, idempotent) | ~~❌~~ ✅ | ✅ | ✅ | **Was mis-scored** — the route exists and is idempotent on `provider_event_id`. For external pipelines (e.g. an SES bounce processor). |
| IMAP/SMTP compatibility breadth (no-CONDSTORE, LIST-STATUS fallback, AUTH negotiation, localized folders) | ⚠️ | ✅ | ✅ | Ref has done a lot of hardening here, driven by real user tickets; Inroad's IMAP path is narrower. |

## 3. Warmup

| Capability | Inroad | Ref | Incmb | Notes |
|---|:--:|:--:|:--:|---|
| Ramp-based warmup (linear cap increase over N days) | ✅ | ✅ | ✅ | Inroad `platform/warmup` drives the real `warmup:tick` send. |
| Pooled warmup (real threaded mail between participating mailboxes) | ✅ | ✅ | ✅ | Inroad: workspace-local pool, no cross-tenant flow. Ref: tiered pools by plan. Incmb: a large shared cross-customer network. |
| Recipient-side engagement (rescue from spam, mark read, in-thread reply) | ✅ | ✅ | ✅ | Inroad: signed `X-Inroad-Warmup` token; junk scan; rescue is itself a positive placement signal. |
| Provider-header-loss fallback (match on envelope-to + assigned Message-ID) | ⚠️ | ✅ | ✅ | Ref explicitly handles Microsoft stripping custom headers. Inroad relies on the token header. |
| Warmup content generation (AI conversation plans, schema-validated, safety-linted, auto-retired) | ⚠️ | ✅ | ✅ | Inroad: curated static library behind a `ContentGenerator` seam; AI generator is a drop-in but no batch/lint/auto-retire bank. |
| Warmup health states + auto-throttle | ✅ | ✅ | ✅ | Inroad: `healthy → watch → throttled → paused`, timed pause windows, clean-window recovery (not snap-to-healthy). |
| Warmup placement rolled up per recipient domain | ⚠️ | ✅ | ✅ | Inroad attributes placement to the sender for health; not yet per recipient domain. |
| Warmup ban status + appeals flow | ❌ | ✅ | ⚠️ | |

## 4. Unified inbox

| Capability | Inroad | Ref | Incmb | Notes |
|---|:--:|:--:|:--:|---|
| Unified inbox across all mailboxes, 3-column, threaded | ✅ | ✅ | ✅ | Inroad `app/inbox` + `features/inbox`; reply goes out through the mailbox that owns the thread. |
| Folder model (Inbox / Sent / Drafts / Archive / Spam / Trash) mapped from provider | ⚠️ | ✅ | ✅ | Inroad stores replies/bounces; Ref mirrors all six provider folders + moves. |
| Scope views (Unread / Today / Awaiting reply / Snoozed / Scheduled / per-mailbox / per-tag) | ~~⚠️~~ ✅ | ✅ | ✅ | ~~Inroad has a thread list + search; not the full scope rail.~~ **Done** — `inbox/handler.go` has `all / unread / today / this_week / awaiting_reply / snoozed`, an unknown scope 400s rather than silently returning an unscoped page, and the rail counts share the clock with the list they link to. Scheduled lives on its own `/inbox/outbox` route rather than in the rail. |
| Reply classification taxonomy (positive/negative/neutral/auto/OOO/unsub) | ✅ | ✅ | ✅ | Inroad `platform/replyclassify`: RFC 3834 headers → keyword lexicon, deterministic, no network call. |
| User-definable reply labels + match rules + automation | ✅ | ✅ | ⚠️ | Inroad `app/replylabel`. |
| Draft autosave, scheduled sends, undo-send window, snooze | ~~⚠️~~ ✅ | ✅ | ✅ | ~~Inroad has deferred/cancellable pending replies (row-pointer design); no autosave/snooze UI.~~ **Done** — `/inbox/drafts`, `/inbox/threads/{id}/snooze`, `/inbox/threads/{id}/schedule-reply`, and `/inbox/outbox/{pendingId}` for cancel-before-send, plus user labels on threads. |
| AI-drafted replies in the thread, held for approval | ✅ | ✅ | ✅ | Inroad: AI drafts in thread view, approval queue with diff preview. |
| Inbox agent (auto-draft on every inbound, never sends) | ⚠️ | ✅ | ✅ | Inroad drafts on demand; no always-on inbox agent off the reply hook. |
| Full-text search across message bodies within scope | ✅ | ✅ | ✅ | |

## 5. CRM

| Capability | Inroad | Ref | Incmb | Notes |
|---|:--:|:--:|:--:|---|
| Contacts + typed custom fields | ✅ | ✅ | ✅ | Inroad: per-workspace field definitions with real types, mapped on CSV import, validated at preflight. |
| Server-side contact search at scale (trigram index, keyset pagination) | ✅ | ✅ | ⚠️ | Inroad: a page 200k rows in costs the same as the first. |
| Faceted search (custom-field operators, ranges, campaign membership, facet counts) | ⚠️ | ✅ | ⚠️ | Inroad: substring search only, no facets. |
| Companies | ✅ | ✅ | ❌ | Inroad has a first-class Company record; the incumbent's CRM does not. |
| Deals + pipelines + stages (board + table, drag) | ✅ | ✅ | ✅ | Inroad `app/crm` + `features/crm`; deals carry campaign + source-mailbox attribution. |
| CRM tasks + task types (priority, status, assignee, due) | ✅ | ✅ | ⚠️ | Inroad `features/records`: tasks modelled polymorphically over contact/company/deal. |
| Notes (attributed, timeline) | ✅ | ✅ | ⚠️ | Inroad `features/records`. |
| Unified activity feed / merged timeline | ✅ | ✅ | ⚠️ | Inroad: polymorphic activity feed, one impl serves every record type; each record shows who created it (agent / member / auto-capture). |
| Meetings (Calendly / Cal.com webhooks, `.ics`, statuses, attribution) | ❌ | ✅ | ⚠️ | |
| Import wizard (preview → map → commit, dedup strategy, CSV/TSV/XLSX) | ⚠️ | ✅ | ✅ | Inroad: CSV import with skip/duplicate reporting; no preview/mapping wizard, CSV only. |
| Export (CSV / XLSX / JSON, scoped, custom columns) | ❌ | ✅ | ✅ | |
| Segments / saved audiences that keep enrolling | ⚠️ | ✅ | ✅ | Inroad has lists; Ref has dynamic segments linked to campaigns. |
| Lead sync (Google Sheets on-demand pull) | ❌ | ✅ | ⚠️ | |
| Built-in B2B lead database | ❌ | ❌ | ✅ | Incmb: a large proprietary contact database. Neither open-source tool has this. |

## 6. AI

| Capability | Inroad | Ref | Incmb | Notes |
|---|:--:|:--:|:--:|---|
| In-app AI assistant (streaming, typed tools, acts as the user, approval-gated) | ✅ | ✅ | ✅ | Inroad `app/agentchat` + `agentrun` + `agenttool`; every write goes through an approval queue with a diff preview; BYO provider key, feature off until a key is added. |
| AI reply drafting / rewrite in the inbox | ✅ | ✅ | ✅ | |
| Inbox agent (auto-draft replies held for approval) | ⚠️ | ✅ | ✅ | See §4. |
| AI contact research (web read → cited facts + openers) | ❌ | ✅ | ✅ | |
| AI steps inside sequences / automations (agent / generate / classify / extract) | ❌ | ✅ | ✅ | |
| AI variables (per-recipient copy computed at send time) | ❌ | ✅ | ✅ | |
| AI skills (reusable markdown playbooks) | ❌ | ✅ | ⚠️ | |
| MCP server (expose workspace as MCP tools) | ✅ | ✅ | ⚠️ | Inroad `app/mcpserver` — same typed tools as the in-app agent. |
| Connect external MCP tools | ❌ | ✅ | ❌ | |
| Deterministic classification stays the default (works with no key) | ✅ | ✅ | ❌ | Inroad + Ref: pull the key and the pipeline still classifies. The incumbent's is model-backed. |
| AI credits ledger + per-member spend caps | — | ✅ | ✅ | Cloud/billing concern; N/A for self-host but Ref ships it for its hosted mode. |

## 7. Integrations, webhooks, public API

| Capability | Inroad | Ref | Incmb | Notes |
|---|:--:|:--:|:--:|---|
| Public REST API, OpenAPI-typed | ✅ | ✅ | ✅ | Inroad `api/openapi.yaml`, ~~154~~ **159** paths, frontend types generated from it. |
| Scoped API keys (bitmask / attenuated authority) | ✅ | ✅ | ✅ | Inroad `app/apikey`: `campaigns:read` / `:write` / `:send` etc. |
| API-key rate limiting + IP allowlist + usage analytics | ⚠️ | ✅ | ✅ | Inroad: Redis fixed-window limiter, fail-closed; no per-key IP allowlist or usage analytics. |
| OAuth 2.x provider for third-party apps (PKCE, consent) | ✅ | ✅ | ⚠️ | Inroad `app/oauthprovider`. |
| Outbound webhooks (HMAC-signed, event-filtered, retried, SSRF-guarded) | ~~❌~~ ⚠️ | ✅ | ✅ | ~~Only inbound provider webhooks exist in Inroad today. **Highest-leverage missing integration primitive.**~~ **Backend done in the P0 sprint** — `app/webhook` (endpoints, event filter, delivery log, cursor paging), `worker/webhook/deliver.go` (retry/backoff), `platform/webhookwire` (`sign.go` + `ssrf.go`), migration `20260907132955_webhook`, and five REST paths incl. `rotate-secret`, `ping` and `deliveries`. ⚠️ **not ✅ because there is no UI** — no `web/src/features/webhooks`, no route; the only frontend trace is the generated `store/api.ts`. Endpoint management is API-only today. |
| Native integrations (CRM sync, chat, scheduling) | ❌ | ✅ | ✅ | e.g. HubSpot / Salesforce / Pipedrive / Slack / Calendly. |
| iPaaS connectors (Zapier / Make / n8n) | ❌ | ✅ | ✅ | Ref ships thin Zapier + Make apps over its public API plus an n8n guide. |
| Hosted lead-capture forms (builder, embed, submission → contact → campaign) | ❌ | ✅ | ✅ | Ref runs this as a dedicated sub-app + public form server. |
| Website-visitor tracking / deanonymisation | ❌ | ✅ | ✅ | A separate visitor-tracking product in both competitors. |
| Developer realtime WebSocket (scoped, documented) | ✅ | ✅ | ⚠️ | Inroad: `wsticket` codec + workspace fan-out hub + documented transport. |

## 8. Realtime & collaboration

| Capability | Inroad | Ref | Incmb | Notes |
|---|:--:|:--:|:--:|---|
| Live-updating dashboard (counts, feeds, health) over a push transport | ✅ | ✅ | ⚠️ | Inroad: WebSocket hub with per-workspace monotonic seq + bounded replay + gap-free reconnect. |
| Live "pulse" / status card (severity-sorted, one O(1) read-model) | ✅ | ⚠️ | ❌ | Inroad-specific: `app/pulse` drives sidebar card + nav counts + today's-sends meter. |
| Multiplayer canvas (presence, cursors, cursor chat, live edits) | ❌ | ✅ | ❌ | Ref runs a dedicated realtime service for this. |
| Notifications system (in-app feed + email digest + Slack, per-category) | ❌ | ✅ | ✅ | |
| `⌘K` command palette + keyboard list nav | ✅ | ✅ | ✅ | Inroad ships both per README. |

## 9. Accounts, auth, multi-tenancy

| Capability | Inroad | Ref | Incmb | Notes |
|---|:--:|:--:|:--:|---|
| Multi-workspace, roles (owner / admin / member), invites, switcher | ✅ | ✅ | ✅ | |
| Email + password, verification, reset | ✅ | ✅ | ✅ | |
| JWT access + rotating refresh tokens, reuse detection, family revocation | ✅ | ✅ | ✅ | Inroad `app/auth`. |
| TOTP 2FA with recovery codes | ✅ | ✅ | ✅ | |
| Passkeys / WebAuthn | ✅ | ✅ | ⚠️ | Inroad `app/passkey`. |
| Social sign-in (Google) | ✅ | ✅ | ✅ | Inroad `identity/google.go`. |
| Social sign-in (Apple) + OIDC / SAML SSO | ❌ | ✅ | ⚠️ | Ref has a generic OIDC connector + Apple. |
| Pre-auth rate limiting (IP + account keyed, fail-closed) | ✅ | ✅ | ✅ | Inroad `platform/throttle`. |
| Login risk / anomaly scoring, new-device alerts, session/location listing | ❌ | ✅ | ⚠️ | |
| Granular permission model (org roles ⟂ API scopes ⟂ admin perms) | ⚠️ | ✅ | ⚠️ | Inroad: 3 coarse roles + API scopes; Ref: a permission bitmask + custom roles. |
| Account danger zone (delayed hard-delete + grace window) | ❌ | ✅ | ⚠️ | |
| Envelope-encrypted secrets (per-workspace DEK wrapped by KEK, crypto-shred on delete) | ✅ | ✅ | — | Inroad + Ref both. |
| Workspace export / import (portable archive, move to another instance) | ❌ | ✅ | — | Ref: registry-driven export covering every org-scoped table, plus a CLI export/import. |

## 10. Fleet / worker orchestration

| Capability | Inroad | Ref | Incmb | Notes |
|---|:--:|:--:|:--:|---|
| Control plane / execution plane seam | ⚠️ | ✅ | ✅ | Inroad has the *interface* (`internal/coreapi`) but the worker is in-process with a Postgres pool — logical split only. Ref's worker holds **no Postgres connection**. |
| DB-less worker, per-worker event-bus topic, reaches relational data over HTTP | ❌ | ✅ | ✅ | The security story: a compromised VPS worker can't dump the tenant DB. |
| Mailbox → worker assignment (tier + risk band + load, risk segregation) | ❌ | ✅ | ✅ | Inroad: single worker, per-IP queue routing exists but no assignment engine. |
| Fleet health quarantine + rebalancer (drain hot workers) | ❌ | ✅ | ✅ | |
| Worker autoscaler + auto-provisioning (VPS) | ❌ | ⚠️ | ✅ | Ref's provisioning is dry-run by default. |
| Worker auto-update (release channel, signed) | ❌ | ✅ | — | |
| Mailbox connect: OAuth (Gmail API / MS Graph) + SMTP/IMAP with live validation | ✅ | ✅ | ✅ | Inroad: all three behind one `MultiSender`; live connection test before persist; TLS-by-default with an explicit persisted opt-out. |

## 11. Admin & ops

| Capability | Inroad | Ref | Incmb | Notes |
|---|:--:|:--:|:--:|---|
| Platform admin console (users / workers / campaigns, impersonation, analytics) | ❌ | ✅ | ✅ | Ref: a separate admin SPA + backend domain. |
| Org audit trail (who did what, secret values never recorded) | ❌ | ✅ | ⚠️ | Inroad's security doc lists this as deferred. |
| Idempotency-Key on all mutations (general middleware) | ✅ | ✅ | ⚠️ | Inroad: Postgres-backed `httpx.Idempotency`, workspace-scoped. |
| One-command dev (services + migrate + seed + api + worker + web) | ✅ | ✅ | — | Both. |
| One-command self-host install (pulls release images, no clone, wizard) | ❌ | ✅ | — | Ref: a published, checksummed install script with a `--wizard`. Inroad: compose file, several steps. |
| Operator CLI that talks to the DB directly (works when sign-in doesn't) | ❌ | ✅ | — | Inroad has `cmd/migrate` + `cmd/seed` only. |
| Health probes: `/healthz` + `/readyz` | ✅ | ✅ | — | ~~Inroad: `/readyz` pings Postgres (not Redis — see the Redis review).~~ **Fixed in the P0 sprint** — `cmd/inroad/main.go` now gates readiness on Postgres **and** Redis with a 2s budget, because a Redis outage fails every sign-in closed at the rate limiter while a Postgres-only probe still reported ready. Closes P0.4. |
| Prometheus metrics | ✅ | ✅ | — | Inroad `platform/metrics`. |
| Instance-health dashboard / doctor command | ❌ | ✅ | — | |
| Distributed tracing (OpenTelemetry) | ❌ | ⚠️ | — | Neither strong. |
| CI: build + lint + test | ✅ | ✅ | — | ~~Inroad: 1 workflow~~ Inroad now runs **4** workflows, same count as Ref: `ci.yml` (lint, unit, integration, frontend, e2e), `security.yml`, `release.yml`, `build-push.yml`. |
| CI: vuln scanning (govulncheck / npm audit / Trivy / CodeQL) | ~~❌~~ ✅ | ✅ | — | ~~Ref: a dedicated security workflow.~~ **Done** — `.github/workflows/security.yml` runs govulncheck (reachable-symbol), `npm audit --audit-level=high`, and a Trivy FS scan (HIGH/CRITICAL; vuln + secret + misconfig) per PR, on push to main and weekly, plus `.github/dependabot.yml` for gomod/npm/actions/Docker. Deliberately *informs* rather than gates, since an external advisory DB can turn it red with no code change. Closes P0.2. |
| CI: release + container publish workflow | ~~❌~~ ✅ | ✅ | — | ~~Ref: tagged releases + published images. Inroad: none, no `CHANGELOG`.~~ **Done** — `build-push.yml` publishes multi-arch (amd64/arm64) `ghcr.io/<owner>/inroad-{api,worker,web}` (`edge` on main; `x.y.z`/`x.y`/`latest`/`sha-…` on a `v*` tag); `release.yml` builds `inroad`/`worker`/`migrate` for linux amd64+arm64, macOS arm64 and Windows amd64 with `checksums.txt`; `platform/version` is stamped at link time and logged at startup; `CHANGELOG.md` (Keep a Changelog) is parsed for the release body. Closes P0.1. |
| Sandbox simulator ("plays the internet": deliver / open / click / reply through real code paths) | ✅ | ✅ | — | Inroad `internal/sandbox` (persona, deliver, simulate, timeline). |

## 12. Billing (hosted-mode only — N/A for self-host)

| Capability | Inroad | Ref | Incmb |
|---|:--:|:--:|:--:|
| Stripe subscriptions, plan tiers, checkout / portal / proration | ❌ | ✅ | ✅ |
| Feature gating by plan | ❌ | ✅ | ✅ |
| Free trial auto-provisioned | ❌ | ✅ | ✅ |
| Discount codes + referral program | ❌ | ✅ | ✅ |
| Done-for-you domain + pre-warmed mailbox provisioning | ❌ | ❌ | ✅ |

## 13. Clients

| Capability | Inroad | Ref | Incmb | Notes |
|---|:--:|:--:|:--:|---|
| Web SPA | ✅ | ✅ | ✅ | Inroad: React 19 + Vite + Tailwind v4 + RTK Query + TanStack Router. |
| Native mobile app | ❌ | ✅ | ✅ | Ref ships a native iOS app. |
| Marketing site in-repo | ❌ | ✅ | — | |

---

## Score summary

| Area | Inroad vs Ref | Inroad vs Incumbent |
|---|---|---|
| Core sending engine | **par** (Inroad ahead on determinism/rigour) | **ahead** |
| Deliverability tooling | behind (verification, inbound ARF, seed net) | behind (verification, placement testing) |
| Warmup | par (Ref ahead on content generation + header-loss fallback) | behind (no large shared network) |
| Unified inbox | ~~slightly behind (folder model, scopes)~~ **par** — the scope rail, snooze, drafts, scheduled reply and thread labels all landed; only the six-folder provider mirror is outstanding | behind |
| CRM | par (Inroad ahead: first-class Company; Ref ahead: meetings, export, facets) | **ahead** |
| AI | behind (research, AI steps, AI variables) | behind |
| Integrations / webhooks / forms | ~~**well behind**~~ behind — webhooks (the prerequisite) now exist server-side; forms, native integrations and iPaaS do not | **well behind** |
| Realtime | par (different shapes) | ahead |
| Auth / security | par (Ref ahead: SSO, risk scoring, audit, danger zone) | ahead |
| Fleet orchestration | **well behind** | behind |
| Admin / ops / install | behind (no admin console, audit, installer, operator CLI) ~~, release CI~~ — **release + security CI now shipped** | n/a |
| Lead data / DFY | n/a | **not closeable by code** |

**Net:** Inroad is a credible peer of the reference platform on the engine and
the CRM, clearly behind on breadth (integrations, forms, AI depth, fleet, admin,
install story). Against the incumbent it wins on engine, CRM, ownership and
pricing model, and loses on the data moat and the deliverability network — the
latter being the part no amount of code closes.

---

## Verified still missing (2026-09-08 @ `d4f5720`)

The not-done half, stated as plainly as the done half. Each was checked against
the tree on this pass, not carried over from the previous doc.

**Nothing shipped for these — no package, no route, no config:**

| Gap | Plan item | Evidence of absence |
|---|---|---|
| One-command self-host installer | P1.1 | `scripts/` holds only `dev-up.sh`, `dev-down.sh`, `dev.ps1` |
| Operator CLI | P1.2 | `cmd/` is `inroad`, `worker`, `migrate`, `seed` — no `inroadctl` |
| Pre-send email verification | P1.3 | No `app/emailverify`; every `email_verif*` hit in the tree is the auth-token flow |
| Inbound ARF / FBL-as-mail parsing | P1.4 (half) | ARF appears once in `worker/inbox/dsn.go`, as a comment about *not* misreading it |
| Contact export (CSV/XLSX/JSON) | P1.5 | No export path under `/contacts`; no XLSX anywhere in the Go tree |
| Audit log | P1.6 | No `app/audit`; every `audit` hit is the agent-approval trail |
| Warmup header-loss fallback | P1.7 | `worker/warmup` matches on the `X-Inroad-Warmup` token only — no envelope-to + assigned-Message-ID path |
| Attachments | P2.3 | No `attachment_*` column, task or route |
| Automation canvas · branch-on-behaviour · action/switch steps | P2.4–2.5 | No `app/automation`; sequences stay a linear list |
| Native integrations · iPaaS · hosted forms · visitor tracking | P2.1, P2.6, P3.11 | No integration/form domain either side of the seam |
| Meetings | P2.8 | No `meetings` record type |
| Notifications system | P2.9 | No `app/notification`; `platform/notify` is transactional mail only |
| Dynamic segments | P2.10 | Static `app/list` only |
| Per-mailbox rolled human workday | P2.11 | Windows + seeded cadence, no stored daily plan |
| Seed inbox-placement testing | P2.12 | Placement is warmup-derived; no seed-mailbox set |
| Always-on inbox agent | P2.13 | Drafting is on-demand off the thread view |
| Workspace export / import | P2.15 | No registry, no archive path |
| Account danger zone | P2.16 | No delayed-delete grace window |
| Generic OIDC / Apple sign-in | P2.17 | Google + passkeys only; no `oidc` in the tree |
| Per-mailbox/campaign tracking domain | P2.19 | No `tracking_domain` column — a verified domain overrides the host, nothing narrower |
| API-key IP allowlist + usage analytics | §7 | Redis fixed-window limiter only |
| Campaign creation wizard | §1 | No wizard component; the campaign route is a tabbed detail page |
| Faceted contact search | §5 | No facet code; substring search only |
| CSV import wizard (preview → map → commit) | P1.5 | `app/contact/import.go` is headless |
| Physical control/execution split · fleet assignment · admin console | P3.8–3.10 | Worker still holds a `pgxpool` |
| Advisor-lite posture detectors | P2.20 | No detector layer; every `advisor` hit in the tree is the word "advisory" in a comment |
| Scheduled-job run ledger | P2.21 | ~10 periodic loops, none of which records a run row |
| Templates library (shared merge-variable catalog, content/spam score) | — | No template domain; templating is merge-token expansion inside `app/campaign` |
| Contact-timezone sending | — | Contacts carry no timezone column; windows are campaign-tz |
| Analytics depth (daily/hourly rollups, campaign comparison) | — | `app/reporting` is per-campaign performance only |

**Started but not wired — worth a decision, not just a backlog row:**

- `internal/platform/storage` defines the blob-storage `Provider` seam (P2.2)
  with a complete S3 implementation and tests, but **nothing imports it**, there
  is no filesystem implementation, and no `S3_*`/`STORAGE_*` env var reaches
  `platform/config`. As it stands it is dead code by the `CLAUDE.md` rule
  ("no dead code, unused exports"). Either finish P2.2 (add the filesystem
  default + config factory) on the way to attachments, or delete it and re-add
  it with its first caller.
- Outbound webhooks have a complete backend and no UI (see §7). The capability
  is real for an API consumer and invisible to an operator.
