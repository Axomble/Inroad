# 03 — Replicate vs Improve: Decisions

The opinionated part. For each capability the Reference platform has and Inroad
lacks (or does partially), a verdict, rough effort, and priority — mapped onto
Inroad's single-Go-stack, deliverability-first goals.

**Legal recap:** Reference platform is Apache 2.0 → porting Go code is allowed with
attribution. Twenty is AGPL → clean-room reimplement only, never copy. See
[README](README.md#licensing--read-before-copying-anything).

### Legend

- **Verdict** — **Replicate** (port the approach, adapt to our code) · **Do-better**
  (reimplement, improved for our architecture/tests) · **Skip** (not worth it) ·
  **Defer** (real value, wrong time).
- **Effort** — S ≈ days · M ≈ 1–2 weeks · L ≈ several weeks · XL ≈ months.
- **Priority** — P0 (do now, cheap+foundational) · P1 (core value) · P2 (breadth)
  · P3 (later).

---

## P0 — Quick foundational wins (do these first; days each)

| Capability | Verdict | Effort | Why now |
|---|---|---|---|
| **One-command `make dev`** (services + migrate + seed + api + worker + web) | Replicate | S | Biggest first-impression / contributor-velocity win for the effort. We already have all the pieces; wire one target. |
| **CI + golangci-lint + trivy** | Do-better | S | We have *none*. This is the clearest gap vs them. Do it better by enabling a real linter set (they disabled most of theirs) and keeping our test suite green in CI. |
| **`Idempotency-Key` on mutations** (general middleware) | Replicate | S | We already have send idempotency; generalize to a Postgres-backed header store. Cheap correctness win, prerequisite for a public API. |

---

## P1 — Core deliverability & adoption (the stuff that makes cold email land and gets users in)

| Capability | Verdict | Effort | Why / how |
|---|---|---|---|
| **OAuth mailbox connection (Gmail API / MS Graph)** | Do-better | L | Most users want to connect Gmail/M365 in one click, not paste SMTP creds. Single biggest **adoption** unlock. Store refresh tokens under the per-workspace DEK (below). |
| **Reply classification** (positive/negative/neutral/auto_reply/OOO/unsubscribe) | Do-better | M | Mostly **deterministic** (RFC 3834 headers + keyword lexicon); the LLM layer is optional. Unlocks OOO-safe stop-on-reply and future reply branching. High value, no AI dependency. Port their layered approach. |
| **Natural-cadence send scheduler** (multiplicative pace variation, non-grid jitter, distribution curve, sub-minute humanization so sends never land on `:00`) | Do-better | M | Core anti-fingerprint deliverability. Our sender currently sends on cap + simple spacing. This is the highest-leverage sending change. |
| **Scheduling windows** (weekly, per-day, multi-interval, tz-aware) | Do-better | M | Table-stakes for cold email; pairs with the cadence engine. |
| **Sender pools + rotation modes** (tag/explicit/all; LRU/round-robin/weighted) | Do-better | M | Enables multi-mailbox campaigns (the normal case). Weighted-by-health is the good default. |
| **Open/click tracking** (pixel + click redirect, opaque tickets, dedup, MPP/scanner filtering, IP hashing) | Do-better | M | A headline feature we entirely lack. Build in **Go** (endpoint or tiny service) — not Rust; our volume doesn't need it. Needs blob/asset handling → do blob-storage seam first. |
| **Domain authentication check (SPF/DKIM/DMARC)** | Replicate | S | Cheap DNS lookups, high user-trust signal, informational (non-blocking) to start. Great value/effort ratio. |
| **Unified inbox** (store polled IMAP messages + 3-column read/reply view) | Do-better | L | We already poll IMAP and *throw the messages away*. Storing + surfacing them is the highest-leverage missing **product** surface. Start read-only (thread view + reply), grow later. |
| **Per-workspace DEK + `KeyProvider` seam** | Do-better | M | Per-tenant crypto isolation; local master key stays the default; cloud KMS becomes a drop-in. Ship it **with tests** (their equivalent has none). See [02 §4](02-architecture.md). |
| **Blob storage seam (filesystem / S3-compatible)** | Do-better | S | Prerequisite for tracking assets, attachments, and email-body offload. Small interface, add before the features that need it. |

---

## P2 — Product breadth (after the core lands)

| Capability | Verdict | Effort | Why / how |
|---|---|---|---|
| **CRM: Deals + Pipelines + Stages + contact activity timeline** | Do-better | L | The "few CRM features" goal. Reimplement **clean-room** (Twenty is AGPL). Keep it **outreach-attached** like Reference (deals carry campaign + mailbox attribution) rather than Twenty's general object platform. Skip Company/custom-objects/views for v1. |
| **Public REST API + scoped API keys** | Do-better | M | We have the internal OpenAPI already; add key auth (scoped bitmask, rate limit, usage log). Unlocks integrations DIY. |
| **Outbound webhooks** (HMAC-signed, event-filtered, retried, SSRF-guarded) | Replicate | M | Port their signing scheme (`t=…,v1=…` over `t.body`, replay window, retry ladder). Reuse/extend our SSRF guard ([02 §6](02-architecture.md)). Lets users pipe events to their own CRM without us building connectors. |
| **A/B testing** (per-step variants, weighted split, winner analysis) | Do-better | M | Standard cold-email capability; needs the tracking signals (P1) first. |
| **Spintax** (`{a\|b\|c}`, subject/body, CSS/JSON-safe) | Replicate | S | Small, self-contained, reduces content fingerprinting. |
| **Pre-send email verification** (syntax → MX → SMTP RCPT → catch-all) | Replicate | M | Cuts hard bounces. Run from the API (non-sending IP), never the worker. Pluggable external verifier later. |
| **Complaint ingestion + suppress-on-complaint** | Do-better | M | Needs FBL/ARF parsing. Completes the deliverability signal set with bounces we already handle. |
| **Auto-pause campaign on bounce/complaint spike** | Replicate | S | Cheap reputation guardrail once we track the rates. |
| **Deliverability dashboard** (0–100 score, rates, at-risk lists) | Do-better | M | Do *after* the signals exist (tracking, domain auth, complaints). Port their score model. |
| **2FA (TOTP) + OAuth social sign-in** | Do-better | M | Security/adoption. Passkeys can follow. |
| **`/metrics` (Prometheus) + optional error sink (Sentry)** | Do-better | S | Beats their thin o11y; small effort. |
| **Contact import wizard** (preview → map → commit, dedup strategy) + export | Do-better | M | Upgrade our CSV import to a mapped wizard; add export. |
| **Categories/tags** + faceted contact search | Do-better | M | Foundation the CRM + inbox labels reuse. |

---

## P3 / Defer — real value, wrong time

| Capability | Verdict | Why deferred |
|---|---|---|
| **Pooled warmup** (free/premium pools, AI content bank, verification tokens, health/appeal) | Defer | Genuinely valuable and our warmup is ramp-only — but pooled warmup needs a **base of participating mailboxes** to be meaningful (the cold-start problem our own PRD flags). Ramp + the deliverability signals above matter more first. Large, multi-part build. |
| **Realtime (presence, live updates)** | Defer / Do-better-in-Go | When we build it, use SSE or a Go WebSocket hub — **not** a separate Elixir service. Live campaign stats + inbox badge first; presence/cursors much later. |
| **Automations (visual trigger→condition→action builder)** | Defer | XL. Sequences + reply branches cover most of the value; a general automation graph is a post-PMF investment. |
| **AI features** (assistant, drafts, research, AI steps/variables, MCP) | Defer | XL and greenfield. The one exception already promoted to P1 is **reply classification**, which is mostly non-AI. Revisit AI once the core product is sticky. |
| **AI credits ledger + billing/Stripe** | Defer | Cloud-only. Self-host unlocks everything; no billing needed until there's a Cloud offering. |
| **Admin console** (platform back-office) | Defer | Only matters at multi-tenant operator scale. |
| **Sandbox simulator** ("plays the internet") | Defer (nice-to-have) | Excellent for demos + integration tests without real mailboxes; build once the core send/track/reply loop is stable. |
| **Meetings, lead sync, Zapier/Make apps, referral program** | Defer | Long-tail; webhooks (P2) unblock DIY versions. |

---

## Skip — do not build (with reasons)

| Thing | Why skip |
|---|---|
| **Event bus + schema registry (Kafka/Avro)** | asynq/Redis is the right tool at our scale; their own compose defaults away from it. Pure complexity for us. |
| **GCP Cloud Tasks scheduler path** | Cloud coupling; the Postgres/Redis clock we have is fine. |
| **Per-VPS fleet: autoscaler, provisioning, SSH orchestration, worker tiers** | Their answer to running thousands of mailboxes across many IPs. Premature for single-node self-hosting; their own auto-provisioning is permanently dry-run. |
| **Polyglot services (Rust/Elixir/Swift)** | One Go stack is our deliberate operational advantage. Build tracking + realtime in Go until a *measured* hot path proves otherwise. |
| **Physical worker/DB split (now)** | Keep the `coreapi` seam clean, but only cut Postgres off the worker when we run on untrusted/multi-IP hosts. |

---

## Suggested sequencing

1. **Wave 0 (P0, ~1 week):** `make dev`, CI + lint, idempotency middleware. Foundation + credibility.
2. **Wave 1 (P1 deliverability core):** DEK seam + blob seam → OAuth mailboxes → reply classification → natural-cadence scheduler + scheduling windows → sender pools/rotation → open/click tracking → domain-auth check → unified inbox (read-only). This is the wave that makes Inroad a *real* cold-email product that lands in the inbox.
3. **Wave 2 (P2 breadth):** public API + webhooks → CRM (deals/pipelines/timeline, clean-room) → A/B + spintax → email verification + complaints + auto-pause → deliverability dashboard → 2FA/social → `/metrics`.
4. **Wave 3 (P3/Defer):** pooled warmup, realtime-in-Go, automations, AI, sandbox — as PMF and demand justify.

Each Wave-1/2 item is its own spec → plan → implementation cycle. When you pick the
first one, I'll run it through brainstorming into a design doc and an implementation
plan.

---

## One honest caveat

Even fully executed, this is a multi-quarter roadmap and the Reference platform is a
moving target with a team behind it. Inroad's winning move is **not** feature parity
— it's being the **best-tested, easiest-to-self-host, single-stack** cold-email core
that lands in the inbox, then growing breadth deliberately. Every "Skip" above is a
place we deliberately stay simpler. Protect that.
