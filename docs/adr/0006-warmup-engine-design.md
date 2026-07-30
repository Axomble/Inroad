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
