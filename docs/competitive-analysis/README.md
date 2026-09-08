# Competitive analysis

Where Inroad stands against the two products it competes with, and what it would
take to be **on par or better** than both. This directory is checked into a
public repo, so the two competitors are referred to by role, never by name.

- **The reference platform** — an open-source (Apache 2.0), self-hostable Go +
  React cold-email and warmup platform in exactly Inroad's category, with the
  same architectural instincts (control/execution split, envelope-encrypted
  secrets, pooled warmup, deterministic reply classification) and a **much wider
  product surface** today. This is the direct competitor. Analysed from its
  published source (`main`, reconciled 2026-09-07).
- **The hosted incumbent** — the closed-source SaaS market leader in cold email.
  Not self-hostable, not open. The comparison is about **product capability and
  the moat that isn't code** (a ~450M-contact lead database, a multi-million-
  mailbox deliverability network, done-for-you domain provisioning). Analysed
  from public 2026 third-party reviews and the vendor's own marketing.

## The documents

| File | What it covers |
|---|---|
| [`01-feature-matrix.md`](01-feature-matrix.md) | Full capability matrix: Inroad vs the reference platform vs the hosted incumbent, area by area, with Inroad's status verified against `main`. |
| [`02-reference-platform.md`](02-reference-platform.md) | The direct competitor. Architecture differences, everything it ships that Inroad does not, and a replicate / do-better / skip verdict on each. |
| [`03-hosted-incumbent.md`](03-hosted-incumbent.md) | The market leader. What it actually sells, which parts are copyable capability and which are a data/network moat, and what "match the incumbent" can and cannot mean for a self-hosted tool. |
| [`04-parity-plan.md`](04-parity-plan.md) | Prioritised roadmap to on-par-or-better, mapped onto Inroad's single-Go-module architecture. **Start here** — it carries the done/not-done marks and the immediate ordered list. |

An older `05-gap-audit-2026-08-04.md` may exist in a working tree; it is
git-excluded, not part of this set, and its statuses predate the unified inbox,
CRM, A/B, spintax, webhooks and the P0 sprint. Do not score against it.

## Headline verdict (2026-09-08)

**What changed since 2026-09-07:** the P0 sprint (#171) merged this analysis and
the P0 work in the same PR, so the first pass scored Inroad as missing five
things it shipped that day. Re-reconciled against `d4f5720`: **all of P0 is
done** (release CI, security CI, outbound webhooks server-side, Redis readiness,
Redis URL config), and four rows were simply mis-scored — lead-flow throttle,
the deliverability event ingest API, the inbox scope rail, and inbox
drafts/snooze/scheduled-reply were all already present. DLQ replay (P2.18) is
done too. The reference platform's `main` moved 76 commits in the same day and
now has an admin-console backend; see `02-reference-platform.md` §4b.

Struck-through text throughout this directory marks a verdict that has been
superseded, so the record of what moved survives the rewrite.

## Headline verdict (2026-09-07, superseded)

**Core sending engine:** Inroad is at parity with the reference platform and
ahead of the incumbent on engineering rigour — deterministic seeded cadence,
per-enrollment rotation that preserves thread identity, health-gating applied to
in-flight threads, send windows made unrepresentable-if-overlapping by a DB
exclusion constraint. None of the three does the *mechanism* of cold sending
better than Inroad.

**Product surface:** Inroad trails the reference platform by a wide margin and
the incumbent by a wider one. Since the last analysis Inroad has closed most of
the sending-sophistication gap (sender pools, rotation, cadence, windows, A/B,
spintax, pooled warmup, tracking with bot classification, domain auth,
deliverability dashboard, unified inbox, CRM with deals/pipelines, realtime, an
AI agent with an approval queue, an MCP server, an OAuth2 provider). What remains
missing is **breadth**, not depth:

- pre-send email verification (MX → RCPT → catch-all)
- ~~complaint / FBL ingestion~~ inbound ARF parsing only (the ingest API ships) and seed inbox-placement testing
- ~~outbound webhooks~~ **shipped server-side; UI outstanding** + an integrations ecosystem (CRM sync, chat, iPaaS connectors)
- hosted lead-capture forms and website-visitor tracking
- a visual automation canvas with branch-on-behaviour, action steps and switch steps
- a physical DB-less worker fleet with orchestration / provisioning
- an admin console, an audit log, a notifications system
- a one-command installer and an operator CLI
- ~~a native mobile app~~ (deliberately skipped — see `04-parity-plan.md`)

**The part code can't close (incumbent only):** the ~450M-contact lead database,
the multi-million-mailbox deliverability feedback network, brand trust with
mailbox providers, and done-for-you domain provisioning. Inroad can match the
incumbent's *software*; it cannot manufacture its *data*. Positioning against the
incumbent is "you own the infrastructure and the credentials, and there is no
per-mailbox or per-lead meter," not "we have more leads."

## How to keep this current

Re-reconcile the Inroad column against `main` whenever a feature cycle lands, and
re-pull the reference platform's source and the incumbent's public material on
the same pass. Put the reconciliation date at the top of `01-feature-matrix.md`.
An earlier version of this analysis rotted because it was dated once and never
revisited.
