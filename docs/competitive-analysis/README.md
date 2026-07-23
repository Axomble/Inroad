# Competitive Analysis — Feature & Architecture Study

This folder documents a mature, same-category open-source platform (referred to
throughout as **"the Reference platform"** / **"Reference"** — its name is
deliberately omitted from every document here) and maps its capabilities against
**Inroad**, so we can decide, feature by feature, what to **replicate**, what to
**do better**, and what to **skip**.

> **Naming rule for this folder:** never write the Reference platform's real name,
> product-specific identifiers (token prefixes, env-var prefixes, hostnames), or
> its repo path in any file here. Code paths (e.g. `internal/app/campaign`) are
> fine — they don't identify the product.

## The documents

| Doc | What it covers |
|-----|----------------|
| [01-feature-inventory.md](01-feature-inventory.md) | Every feature the Reference platform ships, grouped by area, each tagged with Inroad's current status (✅ have / ⚠️ partial / ❌ missing). |
| [02-architecture.md](02-architecture.md) | The architectural patterns where Reference is genuinely stronger, each with an honest "replicate as-is / do better / not worth it for us" call. |
| [03-replicate-vs-improve.md](03-replicate-vs-improve.md) | The decision table: per capability, a verdict (**Replicate / Do-better / Skip / Defer**), rough effort, and priority, mapped onto Inroad's single-Go-stack architecture. |

## Executive summary

The Reference platform is **~13× the size of Inroad** (~150k Go LOC across ~64
domains, plus separate services in three other languages) and is a far more
complete product. Inroad today is a well-tested **core**: multi-workspace auth,
SMTP/IMAP mailbox connection, multi-step sequencing with threading, cap/ramp-paced
sending, DSN bounce + reply detection, suppression, and one-click unsubscribe.

Where the Reference platform is ahead falls into three buckets:

1. **Product breadth** — unified inbox, a real CRM (deals/pipelines/tasks/notes/
   meetings), AI features, integrations/webhooks/public API, billing/credits,
   realtime collaboration, an admin console, open/click tracking, pooled warmup.
2. **Sending sophistication** — sender pools + rotation modes, ESP matching,
   scheduling windows, anti-fingerprint send-time humanization, A/B testing,
   spintax, lead-flow throttling, seed inbox-placement testing.
3. **Infrastructure maturity** — a physical control/execution-plane split (workers
   hold no DB connection), provider-swappable infra (event bus, KMS, blob store,
   scheduler), and per-organization data-encryption keys with a KMS option.

Inroad's counter-strengths are real and worth protecting: **a single Go stack**
(they run Go + Rust + Elixir + Swift), **much higher test density on the paths that
matter**, and **tighter module boundaries**. The goal of this study is not to
become them — it's to cherry-pick the capabilities that move deliverability and
product value, and implement them the Inroad way.

## Licensing — read before copying anything

This determines *how* we can "replicate."

| Source | License | What we may do |
|--------|---------|----------------|
| **Reference platform** | **Apache 2.0** | ✅ We may **port or adapt its Go code** directly into Inroad (also Apache 2.0), provided we preserve attribution — keep the upstream copyright/`NOTICE` and license text for any substantial copied portion. |
| **Twenty (CRM reference)** | **AGPL v3** (some files Enterprise-licensed) | ❌ We may **not copy its code**. AGPL is strong copyleft with a network clause — pulling AGPL code into Inroad would force our *entire* platform, including any hosted Cloud offering, to be released under AGPL. Twenty may only be a **clean-room inspiration** source: study the concepts, reimplement independently in our own Apache-2.0 code. (It's also a different stack — NestJS/React/GraphQL — so we'd reimplement regardless.) |

**Practical rule:** Reference platform → porting is allowed with attribution;
ideas from Twenty → reimplement clean-room, never paste. When in doubt on
licensing for a specific chunk, treat it as clean-room reimplementation — ideas
and architecture aren't copyrightable, specific code expression is.

## How to read this

Start with [03-replicate-vs-improve.md](03-replicate-vs-improve.md) for the
decisions, then use [01](01-feature-inventory.md) and [02](02-architecture.md) as
the backing detail. Everything is grounded in the Reference platform's own product
docs and source; Inroad status reflects the code as of this branch.
