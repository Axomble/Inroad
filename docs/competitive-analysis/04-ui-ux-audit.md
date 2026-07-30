# 04 — UI/UX Audit: Reference Console vs Inroad

A page-by-page comparison of the **interface**, not the backend. Feature parity at
the capability level is in [01-feature-inventory.md](01-feature-inventory.md); this
document is about information architecture, finding/navigation affordances,
interaction quality, and visual identity.

> **Naming rule (see [README](README.md)):** the competitor is "the Reference
> platform" / "Reference". Its name does not appear here.

## Method and its limits

Grounded in three Reference screenshots — **Inbox**, **Campaigns**, **Accounts** —
compared against Inroad screenshots captured from the running app on `main`
(`docs/images/*.png`).

**Read this honestly:** those three screenshots are the Reference platform's
best-foot-forward views with round demo numbers. I cannot see their loading, error,
empty, or mobile states, cannot confirm every sidebar item is implemented, and
cannot judge their perceived performance. I am comparing **our real app** to **their
marketing-quality stills**. Where I say they're ahead, it's on something structural
that a screenshot can prove; where I say we're ahead, it's on something in our code.

---

## 1. The global chrome — the biggest single gap

This is where the difference in "feels like a real console" is concentrated, and
almost none of it is backend work.

| Element | Reference | Inroad | Verdict |
|---|---|---|---|
| Global search + `⌘K` | In the header, on every page | **Nothing.** Zero search inputs in `web/src` | ❌ missing |
| Nav grouping | 4 groups — ungrouped Inbox, then `EMAIL`, `CRM`, `RESOURCES` — uppercase tracked labels, hairline separators | Flat list of 5 items | ❌ missing |
| Live counts in nav | Right-aligned on nearly every row (`Accounts 12`, `Contacts 1.2k`, `Deals 18`) | None | ❌ missing |
| Live telemetry card | Pinned above nav: `● LIVE 38ms`, `12 mailboxes / 9 active`, `Inbox 3 unread`, `Today 0/600` | None | ❌ missing |
| Presence | Avatar stack in header (two collaborators) | None | ❌ missing (needs realtime) |
| Notifications | Bell in header | None | ❌ missing |
| Breadcrumb / back | `‹ Inbox`, `‹ Campaigns` next to the workspace switcher | None | ❌ missing |
| User identity | Sidebar footer: avatar + name + email, above `Settings` | Header avatar initials only — no name, no email | ⚠️ partial |
| Workspace switcher | Header, with workspace mark | Header, with workspace mark | ✅ parity |
| Plan badge | `PRO` | N/A — everything unlocked | ✅ by design |

**The finding that matters:** our own design system already specifies most of this.
[docs/frontend-design.md](../frontend-design.md) §4 says the sidebar is *"grouped
nav with tracked-uppercase section labels and hairline separators. Live counts sit
right-aligned per row."* **We wrote the spec and never built it.** This gap is
unimplemented design, not absent design thinking — which makes it cheap to close.

---

## 2. Page: Inbox

**Inroad has no inbox at all.** This is the single largest product gap in the three
pages, and it's listed as roadmap in the README.

What theirs contains, as a build checklist:

| Feature | Notes |
|---|---|
| Three-pane layout | Filter rail → message list → thread reader |
| Saved views | All · Unread · Today · This week · **Awaiting reply** · Snoozed · Scheduled, each with a count |
| Per-mailbox scoping | `MAILBOXES` rail section, per-address unread counts |
| Categories | `Interested 14` / `Pricing 6` / `Follow up 9`, colored dots, mirrored as per-message dots in the list |
| Date grouping | `TODAY 3` / `YESTERDAY 1` headers |
| Message rows | Avatar initials, sender, subject, snippet, relative time (`12m`, `1h`), unread rail, per-message count badge |
| Thread reader | `3 messages · 2 participants`, per-message avatar/name/email/`to you`/timestamp |
| Thread actions | Snooze, archive, overflow; `Reply` / `Forward` |
| Inline reply affordance | *"Hover any message to reply directly."* |
| Keyboard | Persistent hint bar: `j move · ↵ open · esc close · / search` |
| Live indicator | `● live` on the list header |
| Summary counts | `3 unread · 7 awaiting · 12 today · 28 this week` above the list |

**The strategic point:** we already own the hard half of this. Our inbox worker does
reply matching to the originating send (`FindSendByMessageID`), threading, DSN/bounce
parsing, and — critically — **deterministic reply classification** into seven classes
(`positive` / `negative` / `neutral` / `auto_reply` / `out_of_office` / `unsubscribe`
/ `unknown`) with the deciding layer and a confidence. Their `CATEGORIES` rail is a
view over exactly that kind of signal.

So the gap here is **a read-model plus a view**, not new intelligence. We are missing
the easy half of the feature whose hard half we've already shipped and tested. That
asymmetry is the most actionable thing in this audit.

---

## 3. Page: Campaigns

| Feature | Reference | Inroad | Verdict |
|---|---|---|---|
| Status stat strip | 5 stats, each with a **sub-caption** (`sending now`, `resumable`, `not started`, `finished`) | 4 stats, **no sub-captions** | ⚠️ partial |
| Search within list | `Search campaigns…` | None | ❌ missing |
| Sort | `Newest ⌄` | None | ❌ missing |
| Folders / grouping | `Folders` button + `All folders ⌄` filter | None | ❌ missing |
| Per-status row icon | Distinct leading glyph per status | Status text only | ❌ missing |
| Stable short id | Mono `a3f1c8b2` beside the name | None | ❌ missing |
| Description / subtitle | `Tier-1 accounts · 5 steps` | Subject line | ✅ parity (different content) |
| Created date | Calendar icon + `Jun 12` | None on the row | ❌ missing |
| Inline primary action | Play / pause per row | `Launch` on drafts | ✅ parity |
| Row overflow menu | `⋯` | `⋮` | ✅ parity |
| Sequence editor | Not visible in these screenshots | Steps, delays, merge fields, drag-reorder, draft-only structural edits | ✅ **ours is visible and good** |
| Engagement panel | Not visible | Sent/opens/clicks/replies/bounces/unsubs with honest *indicative* vs *reliable* labelling | ✅ **ours, and better-qualified** |

### Our own flaw here, independent of them

**Campaign detail renders inline, above the list, and isn't routed.**
[campaigns-page.tsx:57](../../web/src/features/campaigns/campaigns-page.tsx#L57)
renders `<CampaignDetail>` between the `StatStrip` and the `<ul>`, driven by
`useState`. Consequences:

1. **No URL per campaign** — can't link, bookmark, or share a campaign; browser back
   doesn't close it.
2. **The list gets pushed down** — you click a row and the thing you clicked
   scrolls out of view beneath a tall detail panel.
3. **`Close` is the only exit**, and it sits mid-page rather than where a panel
   close belongs.
4. **Two affordances, one behaviour, two names** — the row click toggles detail, and
   the overflow menu calls the same thing `View stats` / `Hide stats`. Per our own
   copy rule (*"an action keeps the same name through the whole flow"*), that's a
   naming inconsistency.

A route (`/app/campaigns/$id`) or a right-hand drawer both fix all four. Theirs
implies a detail route via the `‹ Campaigns` breadcrumb.

---

## 4. Page: Accounts vs our Mailboxes + Warmup

Their **Accounts** page is one screen that answers *"which mailboxes can I trust to
send today?"* We split that answer across two pages, and neither one fully answers it.

| Feature | Reference | Inroad | Verdict |
|---|---|---|---|
| Stat strip | `TOTAL 42 connected` / `HEALTHY 31 sending now` / `WARMING 8 ramping up` / `NEEDS ATTENTION 3 paused or failing` | Mailboxes: Total/Active/Paused/Error, no sub-captions. Warmup: Pool/Healthy/Watch/At risk, **with** sub-captions | ⚠️ split + inconsistent |
| **Real table header row** | `ACCOUNT · WARMUP · HEALTH` | **None** — `60/day` and `ACTIVE` appear with no column labels | ❌ missing |
| **Bulk select** | Row checkboxes + select-all in header | None anywhere in the app | ❌ missing |
| Search | `Search by email…` | None | ❌ missing |
| Scope filter | `All accounts ⌄` | None | ❌ missing |
| **Numeric health score** | `HEALTHY 96` / `AT RISK 64` / `ISSUE 22` | Bucket only (`HEALTHY`), no number | ⚠️ partial |
| Warmup state in the row | `20 / 50` with ramp glyph, or `Off`, or `Paused` | On the **Warmup page only** | ⚠️ wrong page |
| Measured placement | Not shown on this screen | `inbox 7d 0% · spam 7d 0%` per mailbox | ✅ **ours, and more honest** |
| Health *reason* | Not shown | `HealthReason` persisted and surfaced (`NOT ENOUGH HISTORY YET`) | ✅ **ours** |
| `IN CAMPAIGN` badge | Yes — shows what a mailbox is committed to | None | ❌ missing |
| Per-provider identity | Not visible | `SMTP` / `Gmail` / `Microsoft 365` chips + host:port | ✅ **ours** |
| Empty state | Not visible | *"Warmup needs at least 2 mailboxes to exchange mail. Enable warmup on another mailbox below…"* | ✅ **ours is genuinely good** |

**The IA critique:** the mailbox is the unit of trust in this product. Its identity
lives on one page and its health and ramp live on another, so the operator has to
hold state in their head and switch pages to answer one question. Their single table
is the better information architecture, and adopting it costs us no new backend —
both pages already read the data.

---

## 5. Cross-cutting flaws in ours

| # | Flaw | Evidence | Severity |
|---|---|---|---|
| 1 | **No finding affordances at all** — no global search, no per-list search, no sort, no filter | Zero `placeholder="Search…"` and no sort/filter controls in `web/src` | **High** — invisible at 3 campaigns / 24 contacts, fatal at their 1.2k contacts / 42 mailboxes |
| 2 | **No keyboard model** — no `⌘K`, no `j/k/↵` list nav, no hint bar | No `onKeyDown` outside a test file | **High** for a scan-and-operate console |
| 3 | Campaign detail inline + unrouted | §3 above | **High** |
| 4 | Mailbox trust state split across two pages | §4 above | Medium |
| 5 | No column headers on any list | Mailboxes/contacts rows | Medium |
| 6 | `Stat.sub` exists but only Warmup uses it | `page.tsx:80` supports `sub`; only `warmup-page` passes it | **Low effort, real gain** |
| 7 | Contacts pagination has no total | `limit + 1` lookahead in `contacts-page.tsx:125` — can't render `248` or page numbers | Medium |
| 8 | Header avatar flashes `?` on hard reload | Visible in captured screenshots; initials resolve only after `/auth/me` settles | Low but cheap to fix |
| 9 | No bulk actions | No checkbox on any row | Low — defer until a bulk action exists worth having |

---

## 6. Where we are actually better

Not consolation prizes — these are real and worth protecting:

1. **Control language.** Our tactile buttons (inset highlight, hard bottom lip, lift
   on hover, recess on press, lime glow on primary) are a distinctive, coherent
   interaction signature. Theirs is competent flat blue-on-white — well executed,
   but indistinguishable from a dozen other tools.
2. **Semantic colour discipline.** Orange means warmup and nothing else, product-wide.
   Theirs uses blue for everything, so no colour carries meaning.
3. **Honest metrics.** We label opens *indicative* and clicks *reliable*. Nothing in
   their screenshots admits that open tracking is unreliable.
4. **Measured, explained health.** We show inbox-vs-spam placement over 7 days plus a
   health *reason*; their score is a number without visible reasoning.
5. **Safety copy.** *"Structural changes are draft-only"* on a disabled control, with
   the reason in the tooltip, is better than anything visible in theirs.
6. **Typography identity.** Geist Sans + Geist Mono, self-hosted, consistent across
   every OS. Theirs reads as Inter-or-system.

---

## 7. Verdict

**Do not redesign.** The visual language is not the problem — it is more distinctive
than theirs. The problem is **information density and the total absence of
finding/navigation affordances**. Their console tells you what to do next; ours makes
you go look.

Three tiers, in the order they should be done:

### Tier 1 — build the spec we already wrote (~1–2 days, zero new backend)
1. Grouped sidebar nav with uppercase section labels + right-aligned live counts.
   *Already specified in `frontend-design.md` §4.*
2. Pass `sub` on every `Stat` on every page — the primitive already takes it.
3. Column headers on the mailbox and contacts lists.
4. Merge warmup ramp + health into the mailbox row, so one page answers "can I send
   today". Keep the Warmup page for pool-level detail.
5. Fix the `?` avatar flash.

### Tier 2 — the console mechanics (~1 week, minor backend)
6. Route campaign detail as `/app/campaigns/$id`, or move it into a right drawer.
   Rename `View stats` to match.
7. Search + sort on campaigns, mailboxes, contacts. Client-side first; server-side
   when counts justify it.
8. `⌘K` command palette (navigate + act) and `j/k/↵` list navigation with a hint bar.
9. Return a real `total` from the contacts endpoint so pagination can show counts.

### Tier 3 — the actual product gap (needs its own spec)
10. **The unified inbox.** We own the hard half already: reply→send matching,
    threading, DSN parsing, and seven-class deterministic classification. What's
    missing is a messages read-model and the three-pane view. This is the item that
    would change how the product *feels*, and the only one here that deserves a
    full spec before any code.

**Deliberately not doing:** presence avatars and a live telemetry card (both need a
realtime layer we haven't built), bulk select (wait until a bulk action earns it),
campaign folders (premature at single-digit campaign counts), and a numeric health
score (our bucket + reason + measured placement is more useful than a number whose
formula nobody trusts).
