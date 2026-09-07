# 03 — The hosted incumbent (market leader)

**Source:** public material, September 2026 — third-party reviews and the
vendor's own marketing. The incumbent is a hosted SaaS: not open, not
self-hostable, so this is a capability and positioning comparison, not a code
read. The product is referred to by role, not by name.

The question "is Inroad on par with the incumbent" has two halves:

1. **The software.** Campaigns, warmup, unified inbox, CRM, verification, API.
   Here Inroad can reach parity, and already beats it on some axes.
2. **The moat that isn't software.** A ~450M-contact lead database, a
   multi-million-mailbox deliverability network, done-for-you domain
   provisioning, brand relationships with the large mailbox providers. Inroad
   cannot build these, and shouldn't try.

Positioning follows from that: **Inroad competes with the incumbent on ownership
and cost structure, not on data.**

---

## 1. What the incumbent actually sells (2026)

| Module | What it is | Inroad equivalent |
|---|---|---|
| **Email accounts** | Connect unlimited sending mailboxes, rotate sends across them, per-account caps | ✅ sender pools + rotation + stacked caps |
| **Warmup** | Auto-on once an inbox connects; ramps volume, simulates opens/replies/spam-rescue using a **large private network of real accounts** | ⚠️ pooled warmup, but workspace-local — no cross-customer network |
| **AI sequences** | Multi-step builder; AI adapts copy per lead and tunes follow-up timing/content from responses | ⚠️ sequences + A/B + spintax + an AI agent that can edit steps; no per-lead AI copy or adaptive timing |
| **Unified inbox** | One inbox across all mailboxes, filtering, suggested actions, ~4-class reply classification (~92% accuracy reported), "usable at 30 mailboxes" | ✅ unified inbox + deterministic 6-class classification + custom label rules; UX unproven at scale |
| **AI reply agent** | Auto-responds to replies, human-in-the-loop **or** autopilot mode | ⚠️ AI drafts + approval queue (HITL only, by design — sending is never auto-approved) |
| **Pipeline CRM** | Deal / opportunity tracking, step-by-step open/reply/opportunity reporting; "not a HubSpot replacement" | ✅ deals + pipelines + stages + tasks + notes + activity + **companies** (the incumbent's CRM has no Company object) |
| **Email verification** | Multi-step + AI scoring; catches spam traps, abuse, catch-all | ❌ syntax check at import only |
| **Inbox placement** | Seed tests, SpamAssassin scoring, blacklist monitoring, rules to pause/re-warm | ⚠️ warmup-path placement signal + a deliverability score + a circuit breaker; no seed-test product |
| **Lead database** | ~450M B2B contacts, filterable, enrich-and-load into a campaign | ❌ — and not planned |
| **Website visitors** | Deanonymise site traffic into leads | ❌ |
| **Done-for-you services** | The vendor registers domains, provisions mailboxes, sells pre-warmed accounts | ❌ — self-host means you bring your own |
| **API + webhooks** | Programmatic campaigns/leads/analytics/verification/placement; real-time event webhooks; **lower tiers rate-capped ~100 req/min** | ✅ OpenAPI REST + scoped keys + OAuth2 provider; ❌ outbound webhooks |
| **Analytics** | Step-by-step open/reply/opportunity; campaign compare | ✅ cross-campaign reporting, per-step stats, deliverability trends |

## 2. Pricing — the positioning lever

The incumbent's 2026 outreach plans run roughly **$47 / $97 / $358 per month**
across three tiers, with annual discounts. But the real bill is commonly
**$100–1,500+/mo** once you add:

- inbox hosting (they resell Google Workspace seats)
- done-for-you domains and pre-warmed accounts
- lead-database access if you have no data source of your own
- higher API limits (the entry tier's ~100 req/min "kills any serious
  integration")

And a structural catch: **domains and mailboxes provisioned through the
done-for-you service are retained by the vendor and are not transferable if you
leave.**

**Inroad's counter-position:**

> Bring the mailboxes you already own. No per-mailbox slot fee, no per-active-lead
> meter, no API rate tier, no vendor lock on your domains. Run it on a $20 VPS.
> The credentials never leave your infrastructure.

That is a real, defensible pitch to the segment that finds the incumbent's total
cost of ownership and lock-in unacceptable — agencies, high-volume senders, the
privacy-conscious, anyone in a regulated context. It is **not** a pitch to
someone who needs the incumbent's lead database to function.

## 3. Where Inroad already wins

- **Data ownership & no lock-in.** Mailbox credentials are envelope-encrypted on
  your infra; deleting a workspace crypto-shreds them. Nothing is "retained by
  the vendor."
- **Cost model.** Flat infra cost vs a metered stack that scales with mailboxes
  and leads.
- **CRM has Companies.** The incumbent's doesn't. Inroad's polymorphic
  notes/tasks/activity model is more complete for actual account-based work.
- **Deterministic reply classification.** Works with no AI key and makes no
  network call. The incumbent's is model-backed — pull the plug and it stops.
- **Transparency.** Open source, auditable, self-hostable. The incumbent is a
  black box.
- **Engine rigour.** Seeded-deterministic cadence, per-enrollment rotation that
  preserves thread identity, exclusion-constraint send windows. The incumbent's
  cadence is "smart send" with no published guarantees.
- **API access model.** Scoped keys + a real OAuth2 provider with consent, no
  request-rate tier gating integration work.

## 4. Where Inroad must improve to claim "on par on the software"

Ranked; these feed `04-parity-plan.md`.

1. **Pre-send email verification.** The incumbent bundles it; its absence in
   Inroad is the most visible single gap. `04` P1.
2. **Outbound webhooks + API events.** The incumbent has real-time event
   webhooks; Inroad has none. `04` P0.
3. **Seed inbox-placement testing.** A dedicated per-provider placement product,
   not just the warmup-derived signal. `04` P2.
4. **Adaptive / AI sequence copy** (per-lead variables, response-tuned timing).
   Inroad has the agent and the events; it lacks the send-time hook. `04` P3.
5. **Always-on inbox agent with an autopilot option.** Inroad is deliberately
   HITL-only. Keep HITL as the default, but an opt-in auto-send mode (with
   guardrails) is a competitive checkbox. `04` P3.
6. **Unified inbox at scale.** Prove and tune the inbox UX past ~50 mailboxes —
   the exact place reviews say the incumbent "gets clunky." A chance to be
   *better*, cheaply, with keyset pagination and server-computed counts (Inroad
   already does both elsewhere). `04` P2.

## 5. Where Inroad should NOT try to match the incumbent

- **Lead database.** Building or licensing a ~450M-contact B2B dataset is a
  different company. Integrate with the user's own source instead (CSV, Sheets
  sync, a webhook from their enrichment tool).
- **Done-for-you domain/mailbox provisioning.** Reselling Google Workspace seats
  and registering domains is an ops business with lock-in baked in — the opposite
  of Inroad's pitch. Document *how* to buy and configure domains well instead.
- **A shared cross-customer warmup network.** The incumbent's network works
  because it's a large SaaS with one trust boundary. A self-hosted tool can't
  pool mailboxes across strangers' instances safely. Inroad's workspace-local
  pool is the correct design for the model; make it excellent rather than
  networked.
- **Managed deliverability-as-a-service.** That's a support org, not a feature.
  It can be offered as paid support later, without building it into the product.

## 6. Verdict

**On the software:** Inroad is ~70% of the way to the incumbent's capability
surface, ahead on CRM depth, ownership, determinism and API model, behind on
verification, webhooks, placement testing, and AI-adaptive copy. Closing items
1–3 in §4 gets it to "credible peer on features."

**On the moat:** not closeable, and that's fine. Inroad's answer to "the
incumbent has ~450M leads and a huge mailbox network" is "and it also has your
domains, your credentials, a metered bill, and a rate-capped API — Inroad has
none of those." Different customer, deliberately.
