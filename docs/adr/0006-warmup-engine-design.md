# 0006 — Warm-up engine: workspace-local pool, static content behind an AI seam

**Status:** Accepted

## Context
The "warm-up" feature was a no-op tick on a ramping daily cap — reputation-building
in name only. A credible engine needs mailboxes to actually exchange mail, engage
with it (read, rescue-from-spam, reply), and adapt to measured inbox placement.
Two forks dominated the design:

1. **Whose mailboxes warm whom?** A shared cross-tenant pool (mailboxes across
   workspaces warm each other) helps small workspaces but pushes real mail across
   tenant boundaries — breaking the strict `workspace_id`-pinning invariant and
   adding an abuse/isolation surface. A workspace-local pool keeps every query
   pinned but requires ≥2 opted-in mailboxes to do anything.
2. **Where does warm-up content come from?** An AI content bank (batch-generated,
   lint-gated) is richer but adds an external dependency, cost, and non-determinism
   to the core path — at odds with the repo's offline, testable, self-hostable bias
   (cf. the deterministic reply classifier).

## Decision
- **Workspace-local pool.** Mailboxes warm only opted-in peers in the same
  workspace. Every warm-up query stays `workspace_id`-pinned and no mail crosses
  tenants; a workspace with <2 participants idles (surfaced in the UI).
- **Curated static content behind an injected `warmup.ContentGenerator` seam.** The
  shipped path is deterministic and offline; an AI generator drops into the seam
  later without touching callers — the same "interface + one impl" pattern as
  `coreapi` (ADR 0003) and the reply-classifier's `ModelClassifier`.
- **Full recipient-side engagement in v1** (rescue-from-spam, mark-read, threaded
  reply via a `mail.Engager` seam), because engagement — not just sending — is what
  builds reputation. Placement is attributed to the **sender**, feeding a
  `healthy → watch → throttled → paused` health state machine that dampens the ramp.
- The engine rides the existing seams — send transport + SSRF guard, claim-before-send
  idempotency, the `coreapi` boundary — rather than reinventing them.

## Consequences
- Strict tenant isolation and a fully offline, testable core path; no AI dependency,
  cost, or external service required to self-host warm-up.
- A single-mailbox workspace cannot warm up at all. If solo warm-up is ever needed,
  the answer is an explicitly opt-in shared pool behind the same signed-token +
  isolation guards — not relaxing the default.
- Content is less varied than an AI bank until the generator seam is filled; the
  static library is themed and humanized to stay believable at low volume.
- Landed alongside two pieces of distributed-worker groundwork (a transport-bus seam
  over asynq per ADR 0002, and per-IP worker routing) that are transport-neutral and
  reusable by campaign sends.

## Amendment (2026-08) — the reputation network

The decision above stands and is not rewritten: the pool is still workspace-local,
content still comes from the injected `warmup.ContentGenerator`, engagement is still
recipient-side, and placement is still attributed to the sender. What changed is the
*safety model* built on top of it, in three increments. Each has its own design
document; this note exists so a reader of the ADR is not left with a picture that
predates them.

- **[Reputation network design](../warmup-reputation-network-design.md)** — the
  frame. It names the four questions the engine must answer separately, and its §2
  is an honest account of where the engine described above stopped short.
- **Phase 0** (`warmup_observations`, `warmup_state_transitions`, the `unknown`
  health state) made evidence append-only and every automatic decision explainable
  from it, and established that missing evidence reads as `unknown` rather than as
  health.
- **[Phase 1](../superpowers/specs/2026-08-12-warmup-reputation-phase-1-design.md)**
  — the local safety controller.
- **[Pair leases](../superpowers/specs/2026-08-13-warmup-pair-leases-design.md)** —
  a send may not fire under a policy that no longer holds.

What that changed about the original decision:

- **One health state became two axes.** The
  `healthy → watch → throttled → paused` machine above answered both "how does this
  mailbox's mail perform" and "who may it exchange traffic with", so a mailbox with
  no evidence and a mailbox with bad evidence were indistinguishable to the pool. A
  separate `lane` now answers the second question, and health drives lane demotion
  while lane never drives health.
- **Evidence became statistical, and populations stayed apart.** Rates are compared
  as Wilson lower bounds against a minimum sample, so a thin sample cannot pause a
  mailbox; campaign and warmup bounces keep their own denominators, because pooling
  them let synthetic warmup traffic dilute a real campaign rate below its threshold.
- **The engine gained authority over cold sending.** A lane may now withhold new
  campaign leads from a mailbox AND from its organizational domain, which is why the
  denominators above had to be right first: a wrong one stops the wrong campaign.
- **A send now carries a lease.** Lane compatibility checked at scheduling time
  could go stale before the message left; the send row is the lease and is
  revalidated at claim.

None of this reopened the two forks the original decision settled. Cross-tenant
pooling and a `Coordinator` remain out of scope, and every added query is still
`workspace_id`-pinned — including the domain half of the campaign gate, so one
tenant's containment cannot reach another's mailboxes on the same domain.
