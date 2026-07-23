# 01 — Feature Inventory

Every feature the Reference platform ships, grouped by area, each tagged with
Inroad's current status:

- ✅ **have** — implemented in Inroad today
- ⚠️ **partial** — some of it exists, meaningful gaps remain
- ❌ **missing** — not in Inroad

Paths in *Source* lines are Reference-platform code paths (they don't identify the
product). "Inroad:" notes map to our codebase.

---

## 1. Campaigns

| Feature | Status | Notes |
|---|---|---|
| Campaign lifecycle (draft → active → paused/finished), start/stop | ✅ have | Inroad has draft → running → done + launch. We lack the paused *variants* (`paused_no_accounts`, `paused_trial_expired`) and auto-pause triggers. |
| Campaign wizard (Basics / Schedule / Sender pool / First email) | ⚠️ partial | Inroad has a create form; no multi-step wizard, no schedule/sender-pool steps. |
| Self-healing reconciler (re-seed stalled campaigns) | ✅ have | Inroad's enrollment sweeper is the equivalent. |
| **Sender pool selection** (by tag / explicit list / all active) | ❌ missing | Inroad assigns exactly one mailbox per campaign. Reference UNIONs tag-based + explicit pools with per-sender weight/enabled. |
| **Rotation modes** (least-recently-used / round-robin / weighted) | ❌ missing | Weighted base = `remaining × (1 + log2(warmupAgeDays+1))`, ties broken by UUID for determinism. |
| **ESP / provider matching** (off / prefer / strict; Gmail→Gmail) | ❌ missing | Recipient provider resolved from cached `esp_provider` or a domain→provider string map; never dials MX on the hot path. |
| **Stacked sending limits** (mailbox cap ∧ campaign limit ∧ ramp) | ⚠️ partial | Inroad enforces a per-mailbox daily cap. Reference stacks three limits with `min()` (mailbox-first safety), campaign limit can only *lower* the cap. |
| **Daily campaign ramp-up** (ramp_start/increment/ceiling, per UTC day) | ⚠️ partial | Inroad has a warmup ramp on the cap; no separate *campaign-level* ramp. |
| **Lead-flow throttle** (max new leads/day, prioritize-new toggle) | ❌ missing | Caps brand-new contacts/day while follow-ups keep flowing. |
| Lead statuses (Queued/Processing/Done/Replied/Bounced/Unsub) | ✅ have | Inroad tracks enrollment status equivalently. |
| **Scheduling windows** (weekly, per-day, multi-interval, tz-aware) | ❌ missing | 7-element per-weekday window array; even intra-window distribution + jitter; respects each mailbox's local business hours. |
| Advanced-outreach override block (bounce pipeline, DLQ, A/B, intent, send-time opt) | ❌ missing | Per-campaign + org-level tunables. |
| **Preflight validation** (scored readiness report, no send) | ❌ missing | Checks tracking domain, unsub header, daily limit, schedule window, A/B config; returns score + per-check remediation. |
| Test email / template preview (rendered, no side effects) | ⚠️ partial | Inroad renders in the sequence-step UI; no dedicated test-send endpoint. |
| **Attachments** (campaign-wide or per-step, ≤15 MB, type-filtered) | ❌ missing | Worker fetches bytes from blob storage by key at send time. |
| Live activity feed + "Needs attention" | ⚠️ partial | Inroad has send stats; no live feed (no realtime layer). |

---

## 2. Sequences & Steps

| Feature | Status | Notes |
|---|---|---|
| Multi-step sequences (subject / body / per-step delay) | ✅ have | Inroad core capability; up to N steps, draft-gated editing. |
| **Visual flow canvas** (node graph, connections = routing) | ❌ missing | Reference builds sequences on a collaborative canvas; Inroad is a linear step list. |
| Email steps + composer (rich text, preview via real engine) | ✅ have | Inroad has subject/body_text/body_html per step. |
| **Action steps** (add/remove tag, create task/deal, unsubscribe, notify, run automation, wait, end) | ❌ missing | Non-email nodes that perform a control-plane action then route on. |
| **Switch steps** (multi-case routing; AI or value decider) | ❌ missing | Value decider is deterministic (template + `/regex/`); AI decider is 1 credit/contact. |
| Wait / spacing (delay as a property of the target step) | ✅ have | Inroad stores per-step delay. |
| Follow-up threading (`In-Reply-To`/`References`, same subject) | ✅ have | Inroad threads follow-ups; changing subject starts a new thread. |
| **A/B variants** (up to 5 per step, weighted split, per-arm stats) | ❌ missing | Original is a weighted control arm; inactive variants drop from split without deletion. |
| **A/B winner analysis** (winner_id, winning_rule, confidence, auto-promote) | ❌ missing | Min sample size 30; confidence bands. |
| **Branching on behavior** (opened/clicked/replied ± N-day window, reply intent, random split) | ❌ missing | Inroad has stop-on-reply/stop-on-suppression only; no conditional branches. |
| **Instant vs at-next-step branches** | ❌ missing | Reply-intent/open/click branches can fire the instant the event lands. |
| Reply branches & routing (reply triggers the path of the *specific* email answered) | ⚠️ partial | Inroad stops the enrollment on reply; no reply *branch* routing. |
| Stop-on-reply (route-aware) | ✅ have | Inroad stops on human reply. |
| **Reply classification** (positive/negative/neutral/auto_reply/OOO/unsubscribe/unknown) | ❌ missing | Layered: RFC 3834 headers → keyword lexicon → optional model. This is high value and mostly deterministic — see doc 03. |
| **Reply templates** (org-shared snippets, `{{.Key}}`, content score) | ❌ missing | |

---

## 3. Sending & Scheduling

| Feature | Status | Notes |
|---|---|---|
| Per-mailbox daily-cap enforcement + idempotent sends | ✅ have | Inroad enforces caps and dedupes sends. |
| Stuck-send / stuck-enrollment sweepers | ✅ have | Inroad has both. |
| **Natural-cadence scheduler** (multiplicative pace variation, non-grid jitter, morning/afternoon distribution curve, sub-minute humanization so sends never land on `:00`) | ❌ missing | This is the anti-fingerprint engine. Inroad sends on cap + simple spacing. High deliverability value — see doc 03. |
| Health-gated cold sending (watch ×0.7, throttled ×0.5, quarantined/blocked = stop) | ❌ missing | Shares the exact warmup health state so cold + warmup schedulers can't drift. |
| Compose/one-off send with mailbox scoring picker | ⚠️ partial | Inroad sends per-campaign; no ad-hoc compose. |
| **Send modes** (instant / smart / scheduled) + **undo-send window** | ❌ missing | Instant sends parked briefly (5–120s) so they're cancelable. |
| Daily-throttle abuse guard (per-(scope,resource,UTC-day) Redis INCR) | ❌ missing | Caps creation rate on "unlimited" resources. |
| Timezone service (IANA resolution) | ⚠️ partial | Inroad has no tz-aware sending. |
| **Task reliability / DLQ** (retry budget, dead-letter queue, replay) | ⚠️ partial | Inroad retries via asynq + sweepers; no explicit DLQ surface or replay endpoint. |
| **Spintax** (`{a\|b\|c}`, subject+body, nested, CSS/JSON-safe) | ❌ missing | Per-recipient wording variation to avoid identical batches. |
| Personalization / expression templating (`{{first_name}}`, `{{custom.<key>}}`) | ✅ have | Inroad has merge fields. Reference adds fallbacks, conditionals, math/string helpers, spaced-field names. |

---

## 4. Warmup

| Feature | Status | Notes |
|---|---|---|
| Ramp-based warmup (linear cap increase over N days) | ✅ have | Inroad's ramp math is live (`inprocess/ramp.go`); the `warmup:tick` handler itself is a no-op. |
| **Pooled warmup** (mailboxes exchange natural mail to build reputation) | ❌ missing | The big one. Reference segregates **free vs premium pools**; recipient-only participation; avoids reciprocal-pair loops. |
| **Warmup content generation** (offline AI bank of conversation plans) | ❌ missing | Batch-generated, lint-gated, auto-retire risky threads; send path draws least-used thread atomically; falls back to a static library. |
| Warmup reply behavior & threading (configurable reply rate, resumes exact plan) | ❌ missing | |
| **Verification token + inbound processing** (hidden token, rescue-from-spam, file to a dedicated folder) | ❌ missing | Rescuing warmup mail from spam is itself a positive placement signal. |
| Warmup routing rules (provider/domain/tld preferences) | ❌ missing | |
| **Warmup health states** (healthy/watch/throttled/quarantined/blocked) + spam score + auto-block | ❌ missing | Score model with sample floors; expired block → probation, not snap-to-healthy. |
| Warmup ban status & appeals | ❌ missing | |
| Warmup placement-by-domain signal | ❌ missing | Every verified delivery reports inbox vs spam, rolled up per recipient domain — a free continuous placement signal. |

---

## 5. Deliverability & Tracking

| Feature | Status | Notes |
|---|---|---|
| **Deliverability dashboard** (0–100 score, bounce/complaint/spam/suppressed, over-time chart, at-risk lists) | ❌ missing | Composite score: −40 bounce (max@10%), −30 complaint (max@0.30%), −40 spam-placement (max@40%). |
| Mailbox health bands (Healthy/Watch/Quarantine/Blocked/Catastrophic) | ❌ missing | Drives pool + cold-send treatment. |
| **Seed inbox-placement testing** (tokenized copy to controlled seed mailboxes, per-provider classification) | ❌ missing | Reuses the same spam-flag detection as warmup ingest. |
| **Domain authentication** (SPF/DKIM/DMARC check, daily + on-demand) | ❌ missing | Informational today (doesn't block send). Cheap, high-signal — see doc 03. |
| **Open/click tracking** (pixel + click redirect, opaque tickets, anti-inflation dedup, MPP/scanner detection, IP hashing) | ❌ missing | Reference runs this as a separate Rust service. Inroad has none. |
| Tracking injection + **custom tracking domain** (per-mailbox/campaign CNAME) | ❌ missing | Only a *verified* domain overrides the default host (SSRF-adjacent guard). |
| One-click unsubscribe (RFC 8058, `List-Unsubscribe`) | ✅ have | Inroad has this. |
| Pre-send email verification (syntax → MX → SMTP RCPT → catch-all), from non-sending IP | ❌ missing | Reference has it (pluggable ZeroBounce/etc. in prod). Inroad validates syntax only at import. |
| Suppression list (auto on hard bounce / complaint / unsub) | ⚠️ partial | Inroad suppresses on hard bounce + unsubscribe. No **complaint** ingestion (needs FBL/ARF). |
| Bounce detection (5.x.x permanent → suppress; 4.x.x transient → keep) | ✅ have | Inroad parses DSN (RFC 3462/3464) and distinguishes hard/soft. |
| Deliverability event ingest API (`POST /deliverability/events`, idempotent) | ❌ missing | For external pipelines (e.g. an SES bounce processor). |
| Auto-pause campaign on bounce/complaint spike | ❌ missing | Thresholds ~8% bounce / ~1.5% complaint. |

---

## 6. Fleet / Worker Orchestration

| Feature | Status | Notes |
|---|---|---|
| Worker execution plane | ⚠️ partial | Inroad has a worker binary, but it's **in-process with a Postgres pool** (logical split only). Reference runs one-per-VPS, DB-less, per-worker topic. See doc 02. |
| **Mailbox→worker assignment** (tier + risk band + load, risk segregation) | ❌ missing | Inroad has a single worker; no assignment. |
| Worker health quarantine (fleet band evaluator, ~5min) | ❌ missing | |
| Fleet rebalancer (drain hot workers onto cold peers) | ❌ missing | |
| Fleet autoscaler + auto-provisioning (Hetzner) | ❌ missing | Note: real provisioning is force-dry-run even in the Reference platform. |
| SSH-driven worker lifecycle (install/update/rotate keys, TOFU pinning) | ❌ missing | |
| Worker auto-update (GitHub release webhook, channels) | ❌ missing | |
| Mailbox connection: **OAuth (Gmail API / MS Graph)** | ❌ missing | Inroad supports **generic SMTP/IMAP only**. |
| Mailbox connection: SMTP/IMAP with live validation | ✅ have | Inroad live-tests SMTP + IMAP before persisting. |

---

## 7. CRM

The Reference CRM is an **outreach-attached CRM**, not a general object platform.
Objects present: **Pipelines, Stages, Deals, CRM Tasks, Task Types, Notes,
Activities/Timeline, Meetings, Categories/tags**. (Compared to Twenty: **no**
first-class Company object, **no** custom objects, **no** saved views/view builder,
**no** custom fields on deals/tasks.)

| Feature | Status | Notes |
|---|---|---|
| Contacts + custom fields (string k/v) | ✅ have | Inroad has contacts + lists; custom fields via JSONB. |
| Contact 360 (engagement summary + suppression state) | ⚠️ partial | Inroad has per-contact data; no hydrated 360 view. |
| Faceted contact search (custom-field ops, campaign membership, ranges, facet counts) | ❌ missing | Inroad has basic list-scoped listing. |
| Import wizard (preview → map → commit, dedup strategy, CSV/TSV/XLSX) | ⚠️ partial | Inroad has CSV import; no preview/mapping wizard, CSV only. |
| Export (CSV/XLSX/JSON, scoped, custom columns) | ❌ missing | |
| Categories / tags (colored, reused as inbox labels) | ❌ missing | |
| Lead sync (Google Sheets, on-demand pull) | ❌ missing | |
| **Deals & Pipelines** (stages, board+table, drag, faceted search, summary rollups) | ❌ missing | Deals carry `campaign_id` + `source_mailbox_id` attribution — outreach-native. |
| **CRM Tasks & Task Types** (priority, status, assignee/team, due, faceted search) | ❌ missing | |
| **Contact Notes** (attributed, timeline) | ❌ missing | |
| **Activity log + merged timeline** (structured enum + human-readable feed) | ❌ missing | |
| **Meetings** (Calendly/Cal.com webhooks or manual, statuses, `.ics`, attribution) | ❌ missing | |

> For the "few CRM features from Twenty" goal: the cheapest high-value CRM set is
> **Deals + Pipelines + Stages + a contact activity timeline** (outreach-attached,
> like Reference), reimplemented clean-room. Twenty's Company/custom-object/view
> machinery is out of scope and AGPL-encumbered.

---

## 8. Unified Inbox (Unibox)

| Feature | Status | Notes |
|---|---|---|
| **Unified inbox across all mailboxes** (read/sort/label/reply, 3-column) | ❌ missing | Inroad polls IMAP for reply/bounce detection but **discards the messages** — no store, no view. Highest-leverage missing product surface. |
| Scope views (All/Unread/Today/Awaiting reply/Snoozed/Scheduled/per-mailbox/per-tag) | ❌ missing | |
| Threading, keyboard nav, seen/unread sync | ❌ missing | |
| Reply/forward/compose from thread, apply template, booking link | ❌ missing | |
| Auto mailbox selection (conversation affinity → budget → domain-auth health) | ❌ missing | |
| Draft autosave, scheduled sends, undo-send, snoozing | ❌ missing | |

---

## 9. AI Features

All run on a tool registry executed as the invoking user with their permission
mask, over a provider abstraction (OpenAI/Anthropic/OpenRouter/Groq/Ollama/custom).

| Feature | Status | Notes |
|---|---|---|
| AI assistant (streaming chat, takes permitted actions, approval-gated) | ❌ missing | |
| AI reply drafts / writing assistant (inline edit, rewrite) | ❌ missing | |
| Inbox agent (auto-draft replies, held for approval, never sends) | ❌ missing | Runs in the consumer off the reply hook. |
| AI contact research (web read → cited facts + openers) | ❌ missing | |
| AI skills (reusable markdown playbooks) | ❌ missing | |
| AI steps in automations/sequences (agent/generate/classify/extract) | ❌ missing | |
| AI variables (per-recipient copy at send time) | ❌ missing | |
| AI credits ledger + spend controls | ❌ missing | Only relevant if we build paid AI. |
| MCP server (expose our data as MCP tools) + connect external MCP tools | ❌ missing | |

> AI is entirely greenfield for Inroad. Most of it is **Defer** (see doc 03); the
> one cheap, high-value, mostly-deterministic piece is **reply classification**
> (§2), which needs no LLM for the common cases.

---

## 10. Integrations, Webhooks & Automations

| Feature | Status | Notes |
|---|---|---|
| Integration connections (HubSpot/Salesforce/Pipedrive/Close/Slack/Discord/Calendly) | ❌ missing | |
| **Outbound webhooks** (HMAC-signed, event-filtered, retried, SSRF-guarded) | ❌ missing | Signing `t=…,v1=…` over `t.body`, 5-min replay window, 8 retries. Good pattern to adopt early. |
| **Public REST API + API keys** (scoped bitmask, IP allowlist, rate limit, usage analytics) | ⚠️ partial | Inroad has an internal REST API (OpenAPI-typed) but no public API-key auth. |
| OAuth 2.1 for third-party apps (PKCE, DCR, app-level webhooks) | ❌ missing | |
| Automations (visual trigger → condition → action graph) | ❌ missing | |
| Zapier / Make / n8n apps | ❌ missing | |

---

## 11. Realtime / Collaboration

| Feature | Status | Notes |
|---|---|---|
| Live presence, cursors, cursor chat, live-updating everything | ❌ missing | Reference runs a separate Elixir/Phoenix service. Inroad SPA is request/response. |
| Notifications (in-app feed + email digest + push + Slack, per-category) | ❌ missing | |
| Developer realtime WebSocket (scoped) | ❌ missing | |

---

## 12. Accounts, Auth & Multi-tenancy

| Feature | Status | Notes |
|---|---|---|
| Multi-workspace identity, switch, roles | ✅ have | Inroad: owner/admin/member. Reference: editable custom roles (up to 25/org), a 16-bit org permission bitmask, multi-role union. |
| Email + password sign-in | ✅ have | Reference adds a two-step emailed-code confirm + Turnstile captcha. |
| JWT access + rotating refresh tokens, reuse detection, session mgmt | ✅ have | Inroad has rotation + reuse detection + family revocation. Reference adds device/location session listing + new-device alerts. |
| Email verification, password reset, invites | ✅ have | Both. |
| **2FA (TOTP)** with recovery codes | ❌ missing | |
| **Passkeys / WebAuthn** | ❌ missing | |
| **OAuth social sign-in** (Google, Apple) | ❌ missing | |
| Granular permission model (org roles vs API scopes vs admin perms — 3 systems) | ⚠️ partial | Inroad has 3 coarse roles. |
| API keys with scopes | ❌ missing | See §10. |
| Account/org danger zone (delayed hard-delete + grace window) | ❌ missing | |

---

## 13. Billing & Credits

| Feature | Status | Notes |
|---|---|---|
| Stripe subscriptions, plan tiers, checkout/portal/proration | ❌ missing | Cloud-only concern; self-host unlocks all gates. |
| Feature gating by plan tier | ❌ missing | |
| Free trial (14-day, auto-created) | ❌ missing | |
| AI credits ledger (monthly + purchased pools, metered) | ❌ missing | Only if we build paid AI. |
| Credit watch (low-balance alerts + auto top-up) | ❌ missing | |
| Discount codes + referral program | ❌ missing | |

---

## 14. Admin & Ops

| Feature | Status | Notes |
|---|---|---|
| Platform admin console (user/worker/warmup/campaign mgmt, impersonation, analytics) | ❌ missing | Separate 22-bit admin permission model. |
| Admin outreach mailer (separate from campaign send path) | ❌ missing | |
| System status probes | ⚠️ partial | Inroad has `/healthz`; no multi-dependency probe dashboard. |
| **Org audit trail** (who did what, secret values never recorded) | ❌ missing | Reference platform notes this as a good pattern; Inroad's security doc also lists audit log as deferred. |
| Idempotency keys on mutations | ⚠️ partial | Inroad has send idempotency; no general `Idempotency-Key` header support. |
| Rate limiting (per-user, plan-aware) | ⚠️ partial | Inroad has minimal rate limiting; security doc lists it as deferred. |

---

## 15. Developer Experience & Ops niceties

| Feature | Status | Notes |
|---|---|---|
| **One-command dev** (`make dev`: services + migrate + seed + all processes) | ❌ missing | Inroad needs 4–5 make targets + npm. Cheap, high-impact — see doc 03. |
| **Sandbox simulator** ("plays the internet": delivers, opens, clicks, replies through real code paths) | ❌ missing | Excellent for demos + integration testing without real mailboxes. |
| CI (build/lint/release workflows) | ❌ missing | Inroad has **no** CI. Reference has 3 workflows + golangci-lint + trivy. |
| Provider-swappable infra (event bus / KMS / blob / scheduler) | ⚠️ partial | See doc 02. |

---

## Summary counts

Of the Reference platform's catalogued capabilities, Inroad today is roughly:

- **✅ have:** the sequencing/sending/bounce/suppression core, multi-workspace auth, SMTP/IMAP connect, ramp warmup.
- **⚠️ partial:** deliverability (bounce/suppress only), warmup (ramp, no pool), CRM (contacts/lists only), analytics (send stats only), API (internal only), worker split (logical only).
- **❌ missing:** unified inbox, open/click tracking, pooled warmup, full CRM, AI, integrations/webhooks/public API, realtime, billing, admin console, and most sending-sophistication features (sender pools, rotation, ESP matching, scheduling windows, A/B, spintax, send-time humanization, seed placement, domain auth).

See [03-replicate-vs-improve.md](03-replicate-vs-improve.md) for what to do about each.
