# Product Requirements Document
## [Working Title: "Outpost"] — Self-Hosted Cold Email & Mailbox Warmup Platform

**Status:** Draft v1
**Owner:** Ahmed Mustufa
**Document type:** Full product PRD — open source core + cloud commercial offering
**Last updated:** July 2026

> Naming note: "Outpost" is used as a placeholder throughout this document pending final brand decision. Replace via find-and-replace once finalized.

---

## 1. Executive Summary

Outpost is a self-hostable cold email sequencing and mailbox warmup platform, positioned as an open-core alternative to Instantly, Smartlead, and Lemlist. It ships in two forms:

- **Outpost Core (open source, self-hosted):** free, runs on the operator's own infrastructure, full data ownership, no vendor lock-in.
- **Outpost Cloud (commercial, hosted):** managed version with a shared warmup pool, billing, and zero ops burden, sold on a flat-fee or tiered subscription model.

The wedge is not "cheaper Instantly." It is: (1) full infrastructure ownership for technical buyers who don't want their sending data living in a vendor's database, and (2) a compliance-adjacent trust position for regulated-industry buyers (initially healthcare operations, via Axomble's existing relationships) who want outbound tooling from a vendor they already trust with sensitive data handling.

---

## 2. Problem Statement

Cold email/outbound teams currently choose between:
- **Proprietary SaaS platforms** (Instantly, Smartlead, Lemlist) — good deliverability infrastructure and warmup networks, but recurring per-seat/per-volume costs that scale awkwardly, no data ownership, and no ability to customize sequencing logic beyond what the vendor's UI exposes.
- **Mature open-source marketing tools** (Mautic, Postal, Listmonk) — real infrastructure, but none combine multi-step cold sequencing, deliverability tracking, and automated warmup in one coherent, easy-to-run package. Mautic has sequencing but is heavy (PHP/MySQL, 4-8GB RAM) and prone to characteristic operational failures (cron jobs silently stopping, DKIM misconfiguration). Postal handles sending infrastructure but isn't a sequencing tool.
- **Early open-source attempts** (e.g., Warmbly) — architecturally reasonable ideas, but immature, unproven at the "does the core product actually work" level (e.g., broken registration flow), and built with an operational complexity (multi-language, multi-runtime) that's hard for a solo team to finish and support.

There is no mature, easy-to-run, single-stack, open-source-first platform that does sequencing + sending + tracking + warmup together.

---

## 3. Goals

1. Ship a working, self-hostable v1 covering mailbox connection, sequencing, sending, and deliverability tracking within 3-4 months of solo part-time development.
2. Validate product-market fit by dogfooding on Axomble's own outbound campaigns before selling to anyone.
3. Reach a sellable Cloud offering once a functioning shared warmup pool exists (requires a minimum viable customer base — see Section 12).
4. Build a defensible, narrow wedge (compliance-adjacent healthcare/regulated-industry outbound buyers) rather than competing head-on with Instantly/Smartlead on price or general-market volume.

## 4. Non-Goals (v1)

- Not competing on lead database size/quality (Instantly's 450M+ contact database is out of scope; assume operator brings their own list).
- Not building multi-channel outreach (LinkedIn, cold calling, SMS) in v1 — email-only.
- Not building a white-label agency management layer in v1 (multi-client sub-accounts deferred to Cloud v2).
- Not attempting to match Instantly/Smartlead's warmup pool scale in v1 — warmup ships as a basic pacing/ramp system first, real pooled warmup is a Cloud-era feature once there's a customer base to pool.
- Not supporting on-prem mail server hosting (Postal-style outbound MTA) — Outpost connects to existing mailboxes (Gmail, M365, generic SMTP/IMAP) rather than replacing the mail server itself.

---

## 5. Target Users & Personas

### Persona A — "The Self-Hoster" (Open Source adopter)
Technical operator (founder, growth engineer, dev-savvy marketer) who wants full control of sending infrastructure and doesn't want customer/lead data living in a third-party SaaS database. Comfortable with Docker, willing to trade convenience for ownership. Primary channel: GitHub, Hacker News, self-hosting communities (r/selfhosted).

### Persona B — "The Regulated-Industry Operator" (Cloud, compliance-adjacent wedge)
Operations lead at a healthcare organization (or similarly regulated small business) doing B2B outbound (e.g., partnership development, referral network growth) who is currently using or considering Instantly/Smartlead but has an existing trust relationship with Axomble on compliance matters. Values a vendor who already understands their regulatory context, even though the outbound tool itself doesn't touch PHI.

### Persona C — "The Agency Reseller" (Cloud v2+)
Runs cold email campaigns for multiple clients, wants white-label sub-accounts and per-client billing tiers without Instantly/Smartlead's per-seat cost scaling. Deferred beyond v1 but should inform data model decisions (tenant isolation) early.

---

## 6. Competitive Landscape Summary

| Platform | Model | Strength | Weakness (for our wedge) |
|---|---|---|---|
| Instantly | Proprietary SaaS | Huge warmup network, lead DB, brand trust, 40,000+ customers | Expensive at scale, no data ownership, generic (no regulated-industry trust angle) |
| Smartlead | Proprietary SaaS | Strong API, agency features | Per-client agency fees add up, same data ownership gap |
| Lemlist | Proprietary SaaS | Multichannel, personalization | Per-seat pricing expensive past 5 users |
| Mautic | Open source | Mature, huge community, full marketing automation | Heavy (4-8GB RAM), operationally fragile (cron/DKIM issues), not built for cold-email-specific pacing/warmup |
| Postal | Open source | Robust mail delivery infra | Not a sequencing/campaign tool by itself |
| Warmbly | Open source | Good architectural ideas (control/execution plane split) | Immature — broken registration, low adoption, complex multi-language stack for a small team to finish |

---

## 7. Product Principles

1. **Own your data, own your infrastructure** — every core feature must work fully self-hosted with zero required calls to third-party services (aligned with the "self-host by default, cloud services pluggable" pattern proven by Warmbly's design, executed more completely).
2. **One stack, deeply understood** — Go backend end-to-end for v1; no premature polyglot architecture. Split out a service into another language only when production data proves a specific bottleneck.
3. **Deliverability first, feature breadth second** — a smaller feature set that reliably lands in the inbox beats a large feature set that gets flagged as spam.
4. **Dogfood before you sell** — every feature ships to Axomble's own outbound campaigns before it's marketed to a single external customer.
5. **Honest about warmup limitations at launch** — v1 warmup is pacing/ramp-based, not pooled. This is disclosed openly rather than oversold, consistent with what an early-stage self-hosted tool can honestly deliver.

---

## 8. High-Level Architecture

```
┌─────────────────────────────────────────────────────────┐
│                     React Dashboard                      │
│              (TypeScript, Vite, Tailwind)                 │
└───────────────────────┬─────────────────────────────────┘
                         │ REST + WebSocket
┌───────────────────────▼─────────────────────────────────┐
│                    Go Backend API                         │
│         (net/http or Gin, single deployable binary)       │
│  - Auth (JWT, OAuth for mailbox connections)               │
│  - Campaign / Sequence CRUD                                │
│  - Contact / List management                               │
│  - Mailbox connection management                           │
│  - Webhook/event ingestion                                  │
└───────┬──────────────────────────┬────────────────────────┘
        │                          │
┌───────▼────────┐        ┌────────▼─────────┐
│   PostgreSQL    │        │   Redis (Queue)   │
│  (system of      │        │  (BullMQ-style     │
│   record)        │        │   job queue via     │
│                  │        │   asynq or similar)  │
└──────────────────┘        └────────┬─────────┘
                                       │
                             ┌─────────▼──────────┐
                             │   Go Worker Pool     │
                             │  - SMTP send workers  │
                             │  - IMAP poll workers   │
                             │  - Warmup pacing engine│
                             │  - Bounce/reply parser  │
                             └───────────────────────┘
```

**Stack decisions:**
- Backend + workers: **Go** (single language, single deployable binary per component)
- Database: **PostgreSQL** (system of record — contacts, campaigns, mailboxes, events)
- Queue/cache: **Redis** (job queue for sends, rate limiting, pacing state)
- Frontend: **React + TypeScript + Tailwind**
- Tracking pixel/redirect endpoint: served by the Go API initially; split into a dedicated lightweight Go service only if request volume demands it
- Realtime dashboard updates: plain WebSocket (`gorilla/websocket`) from the Go backend — no separate Elixir/Phoenix service needed at v1 scale
- Deployment: Docker Compose for self-hosters; single binary + systemd for worker fleets at Cloud scale

---

## 9. Feature Specification

### 9.1 Mailbox Connection & Management

**9.1.1 Gmail / Google Workspace connection**
- One-click OAuth 2.0 flow (Google API scopes: `gmail.send`, `gmail.readonly` for reply detection)
- Store refresh token encrypted at rest (see Section 10, envelope encryption)
- Automatic token refresh handling with alerting on refresh failure
- Support for multiple Gmail mailboxes per workspace/tenant

**9.1.2 Microsoft 365 / Outlook connection**
- OAuth 2.0 via Microsoft Graph API (scopes: `Mail.Send`, `Mail.Read`)
- Same encrypted storage and refresh handling as Gmail

**9.1.3 Generic SMTP/IMAP connection**
- Manual entry: host, port, username, app password (or password), TLS/STARTTLS settings
- Connection test on save (send a test SMTP handshake + IMAP login before persisting)
- Support for Zoho, Fastmail, self-hosted mail servers, or any standards-compliant provider

**9.1.4 Mailbox health & status**
- Per-mailbox dashboard: connection status, daily send count vs. cap, last successful send, last IMAP poll, any auth errors
- Manual pause/resume per mailbox
- Mailbox removal with confirmation (does not delete historical send/event data)

**9.1.5 Per-mailbox sending limits**
- Configurable daily send cap per mailbox
- Configurable send spacing (minimum interval between sends from the same mailbox)
- Business-hours-only sending option (per mailbox timezone)
- Ramp schedule: new mailboxes start at a low daily cap and increase automatically over a configurable number of days (default 30-day ramp, editable)

---

### 9.2 Campaign & Sequencing Engine

**9.2.1 Campaign creation**
- Campaign name, description, associated contact list(s)
- Assign one or more sending mailboxes to a campaign (round-robin or weighted distribution across assigned mailboxes)
- Campaign-level daily send cap independent of individual mailbox caps

**9.2.2 Sequence builder**
- Multi-step sequences (unlimited steps, practically bounded by UI usability — target UI supports at least 10 steps cleanly)
- Each step: subject line, body (plain text + HTML), delay since previous step (in days/hours)
- Personalization variables: `{{first_name}}`, `{{company}}`, `{{custom_field_n}}` — pulled from contact record
- Spintax support for subject/body variation (basic `{option1|option2|option3}` syntax) to reduce identical-content fingerprinting across sends

**9.2.3 Conditional branching**
- Branch on: reply received / no reply / email opened / link clicked / bounced
- Example: "If no reply after Step 2, wait 3 days then send Step 3B; if replied, stop sequence and flag contact for manual follow-up"
- Stop-on-reply as a global default (configurable per campaign) to avoid double-emailing engaged contacts

**9.2.4 Scheduling & pacing**
- Campaign start/end dates
- Per-day send volume caps at the campaign level, distributed across the sequence's active contacts
- Time-zone-aware sending windows (respect contact's inferred or specified timezone where available)

**9.2.5 A/B testing (v2, not v1)**
- Split-test subject lines or body variants across a contact list, report reply-rate delta
- Deferred to post-v1 to keep initial scope focused

---

### 9.3 Contact & List Management

**9.3.1 Contact import**
- CSV import with column mapping UI
- Deduplication on email address within a list and across lists (configurable)
- Required fields: email; optional: first name, last name, company, custom fields (arbitrary key-value)

**9.3.2 List management**
- Static lists (manually curated) and dynamic segments (filter by custom field, engagement status, campaign history)
- Suppression list — global do-not-contact list honored across all campaigns (unsubscribe requests, hard bounces, manual opt-outs)
- Unsubscribe link auto-inserted in every sent email (footer), single-click unsubscribe honored immediately and permanently

**9.3.3 Contact activity timeline**
- Per-contact view: every email sent, opened, clicked, replied, bounced, across all campaigns they've been part of

---

### 9.4 Deliverability & Tracking

**9.4.1 Open tracking**
- Invisible tracking pixel embedded per sent email
- Deduplication logic to avoid inflated open counts from email client pre-fetching (rate-limit repeated opens from the same IP within a short window before counting a "new" open)
- Toggle to disable open tracking entirely per campaign (some deliverability-conscious operators disable pixel tracking, as it can itself be a spam signal — document this tradeoff in-product)

**9.4.2 Click tracking**
- Link rewriting through a redirect service that logs the click then forwards to the original URL
- Same deduplication approach as opens

**9.4.3 Reply detection**
- IMAP polling of each connected mailbox's inbox (and sent folder, for thread-matching)
- Reply matching via `In-Reply-To` / `References` headers against sent message IDs
- Sentiment/intent flagging (v2): basic keyword classification (interested / not interested / out-of-office / unsubscribe-request) to help prioritize manual review — deferred, not v1 core

**9.4.4 Bounce handling**
- Hard bounce detection (permanent failure — SMTP 5xx) → auto-add to suppression list, mark contact undeliverable
- Soft bounce detection (temporary failure — SMTP 4xx) → retry with backoff, alert if repeated across N attempts
- Bounce rate monitoring per mailbox with automatic pause if bounce rate exceeds a configurable threshold (protects sender reputation)

**9.4.5 Spam/reputation signals dashboard**
- Per-mailbox: bounce rate, spam complaint rate (where reportable via provider feedback loops), open rate trend
- Alerting when a mailbox's metrics degrade past configurable thresholds, with automatic pause option

---

### 9.5 Warmup System

**9.5.1 v1 — Ramp-based warmup (no pooling)**
- New mailbox starts at a low daily send volume (default: 5-10/day)
- Automatic daily increase per a configurable ramp curve (linear or step-function) up to the mailbox's target daily cap over a configurable period (default 30 days)
- Documentation guidance for manual warmup best practices (send-and-reply exchanges with a small set of known-good addresses) for operators who want to supplement ramp-based warmup manually in v1

**9.5.2 v2/Cloud — Pooled warmup**
- Deferred until Outpost Cloud has a sufficient base of participating mailboxes to make pooled warmup statistically meaningful (see Section 12, minimum viable pool size)
- Design (informed by, not copied from, Warmbly's architecture): per-IP/per-mailbox reputation scoring, automatic exclusion of mailboxes showing spam patterns or repeated bounce/complaint signals, isolated pools for free vs. paid tiers to prevent abuse from free-tier accounts degrading paid-tier reputation

---

### 9.6 Analytics & Reporting

**9.6.1 Campaign-level dashboard**
- Sent / delivered / opened / clicked / replied / bounced counts and rates, over time (daily granularity)
- Funnel visualization: sent → opened → clicked → replied

**9.6.2 Mailbox-level dashboard**
- Volume sent over time per mailbox, health status, ramp progress (if in warmup)

**9.6.3 Export**
- CSV export of any report view
- (v2) Scheduled email digest reports

---

### 9.7 Team & Access Control (Cloud-relevant, basic version in Core)

**9.7.1 Multi-tenancy**
- Workspace concept — each self-hosted install can support multiple workspaces (teams) with isolated data, to support the Agency Reseller persona later without a schema rewrite
- Row-level tenant isolation in Postgres (tenant_id on all core tables), following the same pattern used in prior FertilityOS multi-tenant work

**9.7.2 Roles & permissions**
- Owner, Admin, Member roles at workspace level (v1)
- Granular per-resource permissions (v2, if agency use case demands it)

**9.7.3 Audit log**
- Record of mailbox connections/disconnections, campaign creation/deletion, user invites/removals, sensitive settings changes

---

### 9.8 Security & Compliance

**9.8.1 Encryption**
- Envelope encryption for stored OAuth tokens and SMTP/IMAP credentials: a local master key (self-hosted default) or KMS-backed key (Cloud), wrapping per-tenant data keys, so raw credentials never sit in Postgres unencrypted
- TLS enforced for all SMTP/IMAP connections; reject plaintext auth by default with an explicit opt-out for legacy providers

**9.8.2 Data ownership & export**
- Full data export (contacts, campaigns, events) available at any time in open formats (CSV/JSON) — no vendor lock-in, consistent with the open-source-first positioning
- Account/workspace deletion permanently purges all data on request

**9.8.3 Compliance posture**
- Outpost itself does not process PHI and is not positioned as a HIPAA-covered tool — this should be stated clearly and honestly in marketing material to avoid implying a compliance claim the product doesn't make
- For the regulated-industry wedge, the trust signal is the vendor relationship (Axomble's existing compliance credibility), not a claim about the tool's own regulatory certification — this distinction must be clear in all sales/marketing content

---

### 9.9 Billing (Cloud only)

**9.9.1 Plan tiers (illustrative, pricing TBD based on unit economics modeling)**
- Self-serve tiers based on mailbox count and/or monthly send volume
- Usage overage handling (soft cap with alert vs. hard cap — decide based on customer feedback)

**9.9.2 Payment processing**
- Stripe integration for subscription billing (Cloud only — Core/self-hosted has no billing dependency at all, keeping the open-source version fully free of any required third-party service calls)

---

### 9.10 API & Integrations

**9.10.1 Public REST API**
- Full CRUD for campaigns, contacts, mailboxes, sequences via API (parity with dashboard UI actions)
- API key management per workspace

**9.10.2 Webhooks**
- Outbound webhooks for key events (email sent, opened, clicked, replied, bounced) so operators can pipe events into their own CRM (e.g., Twenty CRM, given the operator's existing setup) or data warehouse

**9.10.3 CRM sync (v2)**
- Native two-way sync connectors (starting with Twenty CRM, given direct relevance) — deferred past v1, but webhook support in v1 unblocks a DIY version of this immediately

---

## 10. Data Model (Initial Outline)

Core entities:
- `workspaces` (tenant root)
- `users` (workspace members, roles)
- `mailboxes` (connection type, encrypted credentials/tokens, send caps, ramp state, health status)
- `contacts` (email, custom fields as JSONB, suppression status)
- `lists` (static or dynamic segment definition)
- `campaigns` (name, assigned mailboxes, caps, schedule)
- `sequences` (ordered steps belonging to a campaign)
- `sequence_steps` (subject, body, delay, branch conditions)
- `sends` (individual send events: contact, step, mailbox, timestamp, status)
- `events` (opens, clicks, replies, bounces — polymorphic, references `sends`)
- `suppression_list` (global do-not-contact, reason, source)
- `audit_log` (actor, action, target, timestamp)

All tenant-scoped tables carry `workspace_id` with row-level isolation enforced at the application layer (and optionally Postgres RLS policies for defense in depth).

---

## 11. Non-Functional Requirements

| Category | Requirement |
|---|---|
| Performance | Support at least 50 concurrent mailboxes sending/polling per single-node deployment without degradation (v1 target; scale-out via additional worker nodes beyond this) |
| Reliability | Send queue must survive process restarts (Redis-persisted job state); no silently dropped sends |
| Security | All credentials encrypted at rest; TLS-only SMTP/IMAP by default |
| Deployability | Single `docker-compose up` for a complete self-hosted install; documented systemd-based install path for production |
| Observability | Structured logging (JSON) for all send/poll/error events; health check endpoints for each service component |
| Portability | No hard dependency on any single cloud provider for Core; pluggable blob storage (filesystem default, S3-compatible optional) |

---

## 12. Phased Roadmap

**Phase 0 — Internal dogfood (Months 1-3)**
- Mailbox connection (Gmail, M365, SMTP/IMAP)
- Basic sequencing (linear steps, no branching yet)
- Sending + bounce handling
- Open/click tracking
- Deploy against Axomble's own outbound; Meena and Ahmed as first real users

**Phase 1 — Open source v1 launch (Months 3-5)**
- Conditional branching in sequences
- Reply detection
- Ramp-based warmup
- Contact/list management, suppression list
- Public GitHub launch, self-hosted only, no cloud offering yet

**Phase 2 — Cloud beta (Months 5-8, contingent on Phase 1 traction)**
- Multi-tenancy hardening, billing integration
- Minimum viable pooled warmup — requires a minimum participating mailbox base (rough target: 50-100 actively warming mailboxes before pooling produces a believable traffic pattern; below this, keep ramp-based warmup even on Cloud and disclose this honestly)
- Onboard first paying customers from Axomble's existing healthcare-ops relationships

**Phase 3 — Expansion (Month 8+)**
- Agency/reseller multi-client support
- CRM native sync connectors
- A/B testing, sentiment classification on replies

---

## 13. Success Metrics

**Phase 0 (dogfood):**
- Sequences run reliably for 4+ weeks on Axomble's own outbound without manual intervention for bugs
- Bounce rate stays under 2% on Axomble's own sending mailboxes

**Phase 1 (open source launch):**
- GitHub stars / self-hosted install count (leading indicator of technical credibility, not revenue)
- Zero critical (data-loss or credential-leak class) bugs reported in first 60 days

**Phase 2 (Cloud):**
- First 10 paying customers from the regulated-industry wedge
- Customer-reported deliverability (inbox placement) at parity with or better than their prior Instantly/Smartlead setup

---

## 14. Open Questions / Risks

1. **Warmup pool cold-start problem** — addressed by explicitly not overselling pooled warmup until Cloud has enough participating mailboxes; needs a concrete decision on what "enough" means before Phase 2 begins.
2. **Brand separation from Axomble's compliance identity** — needs a decision (sub-brand vs. same brand) before any public-facing launch, to avoid diluting Axomble's core compliance positioning with a general-purpose outbound tool.
3. **License choice for Core** (Apache 2.0 vs. AGPL-3.0) — affects whether competitors can host Outpost Core commercially against you; needs a decision before public repo launch, not after.
4. **Solo-builder bandwidth** — this is a nights/weekends build alongside a full-time role (Verto/TFP), Axomble outbound, Dokko, and a Germany relocation application cycle. Phase timelines above assume part-time pace; should be revisited monthly against actual progress rather than treated as fixed.
5. **Regulatory framing risk** — must avoid any marketing language that implies Outpost itself is HIPAA-compliant or PHI-safe, since it does not process PHI; the trust angle is vendor relationship, not product certification, and this line must stay clear in all copy.

---

## 15. Appendix — Terminology

- **Warmup pool:** a shared network of mailboxes exchanging low-volume, natural-looking email traffic with each other to build sender reputation before high-volume sending begins.
- **Ramp:** a gradual increase in a single mailbox's daily send volume over time, without pooling.
- **Sequence:** an ordered set of email steps with timing and branching logic, sent to a contact over the life of a campaign.
- **Suppression list:** a permanent do-not-contact registry honored across all campaigns for a given workspace.
