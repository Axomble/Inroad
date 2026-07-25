# Send-Path Hardening — Phase A: Delivery Correctness

**Date:** 2026-07-26
**Branch:** `feature/send-path-hardening` (off `main`, migration head `000014`)
**Status:** Design — approved (scope: Phase A; cap/pacing is a separate Phase B)

## 1. Goal & the bug

The backend audit found a **CRITICAL** double-send: in both send handlers
(`worker/sequence/advance.go`, `worker/sender/sender.go`) the real SMTP call
`sender.Send()` runs **before** the send is recorded. The deterministic-id
`ON CONFLICT DO NOTHING` dedupes the *row*, not the *delivery*, so a retried
asynq job (redelivery after a post-send crash) or a concurrent sweeper-vs-lazy-chain
advance **delivers the same email twice**. Comments claim idempotency; they mean
row-idempotent, not delivery-idempotent.

Phase A makes delivery idempotent and fixes the related correctness/security gaps.
**Out of scope (Phase B):** atomic daily-cap reservation and `min_interval_seconds`
pacing — a distinct per-mailbox rate primitive.

## 2. Claim-before-send (the core fix)

Restructure both handlers to **claim → send → finalize**. The `sends` row is the
claim; it moves through `sending → sent | failed`.

### 2.1 Migration `000015`
- Extend the `sends.status` CHECK to include `'sending'`:
  `CHECK (status IN ('queued','sending','sent','failed','skipped'))`.
- Add `claimed_at timestamptz` (nullable) — the lease timestamp for crash reclaim.
- Down: drop `claimed_at`; restore the prior CHECK **exactly** (no `'sending'`).
  (Any rows left in `'sending'` at downgrade are a pre-existing operational concern;
  the down may `UPDATE ... SET status='failed' WHERE status='sending'` before
  re-adding the CHECK so the constraint re-applies cleanly. Document it.)

### 2.2 Step path — `ClaimStepSend` + `FinalizeStepSend`
`GetStepSendJob` stays read-only and unchanged (it already derives the deterministic
`sendID` the tracking tokens embed). New queries:

- **`ClaimStepSend`** — insert-or-reclaim, workspace-pinned:
  ```sql
  INSERT INTO sends (id, workspace_id, campaign_id, contact_id, mailbox_id, to_email,
                     step_order, references_header, status, claimed_at)
  VALUES ($1,$2,$3,$4,$5,$6,$7,$8,'sending', now())
  ON CONFLICT (campaign_id, contact_id, step_order) WHERE step_order IS NOT NULL
  DO UPDATE SET status='sending', claimed_at=now(), error=''
    WHERE sends.status = 'sending'
      AND sends.claimed_at < now() - make_interval(secs => sqlc.arg(lease_seconds))
  RETURNING id;
  ```
  Returns a row iff we won a fresh insert **or** reclaimed a *stale* `'sending'`
  (crash recovery). Returns nothing when the row is `'sent'` (already delivered),
  `'failed'` (permanent), or a *fresh* `'sending'` (another worker owns it) → **skip
  the send, return nil**.
- **`FinalizeStepSend`** (success or permanent failure) — `UPDATE sends SET
  status=$status, message_id=$mid, error=$err, sent_at = CASE WHEN $status='sent'
  THEN now() ELSE sent_at END WHERE id=$1 AND workspace_id=$2`. Runs in the SAME tx
  as the enrollment cursor advance (as `MarkStepSent` does today).
- **`ReleaseStepSend`** (retryable failure) — `UPDATE sends SET claimed_at =
  'epoch' WHERE id=$1 AND workspace_id=$2 AND status='sending'` — marks the lease
  immediately expired so the asynq retry reclaims promptly, without touching a
  finalized row.

`coreapi.Client` gains: `ClaimStepSend(ctx, job) (claimed bool, err error)`,
`FinalizeStepSend(ctx, job, res) (Advance, error)` (replaces the record+advance half
of `MarkStepSent`), `ReleaseStepSend(ctx, job) error`. Keep them workspace-pinned.

### 2.3 `AdvanceHandler` new flow
1. `GetStepSendJob` → guards (`Skip` / suppressed / cap≤0 / over-cap) unchanged, **before** claim.
2. `claimed, err := core.ClaimStepSend(...)`; if `!claimed` → return nil (someone else owns/sent it).
3. Build MIME (uses the already-derived `SendID`), `msgID, sendErr := sender.Send(...)`.
4. `sendErr == nil` → `FinalizeStepSend(status=sent)` + advance cursor → schedule next / done.
5. `sendErr != nil && mail.Retryable(sendErr)` → `ReleaseStepSend` + **return sendErr** (asynq retries).
6. `sendErr != nil && !Retryable` → `FinalizeStepSend(status=failed)` + advance cursor (fail-forward for permanent).

### 2.4 Direct path (`sender/sender.go`) — same shape
The `send:email` path is currently dormant (`EnqueueSends` only in tests) but shares
the `sends` table, so harden it for consistency and to remove the latent risk (audit F5).
Its rows pre-exist as `'queued'`, so the claim is an UPDATE:
`UPDATE sends SET status='sending', claimed_at=now() WHERE id=$1 AND workspace_id=$2
AND (status='queued' OR (status='sending' AND claimed_at < now()-lease)) RETURNING id`.
Same finalize/release/retry policy. `MarkSend` splits into claim + finalize/release.

### 2.5 Lease
Default lease = 5 minutes (crash recovery window; longer than any single send timeout
of 30s, shorter than the 5-min enrollment sweeper so a genuinely crashed send is
re-driven). Config-surface it (`INROAD_SEND_CLAIM_LEASE`, default 5m) or a package const.

## 3. Transient-vs-permanent classification — `mail.Retryable(err) bool`

New helper in `internal/platform/mail`. **Retryable (transient):** `net.Error` with
`Timeout()`; connection refused / reset / EOF during dial; a `gomail` SMTP `4xx`
reply (greylisting, rate-limit). **Not retryable:** `5xx` SMTP (bad recipient, policy),
auth failure, message-build errors, SSRF `vetAddr` rejection. **Default (unknown /
ambiguous, incl. errors after DATA where delivery may have happened): NOT retryable**
— retrying a possibly-delivered message risks a double, so we fail-forward instead.
This is a deliberate "never double, occasionally drop a rare ambiguous send" tradeoff;
document it. Unit-test the classification table.

## 4. asynq idempotency config (defense in depth over the claim)

In `queue.go`, add to the send/advance enqueues:
- `asynq.TaskID(...)` keyed so a sweeper re-enqueue of an already-pending advance
  dedups (e.g. `advance:<enrollmentID>:<dueUnix>`), without blocking a genuinely new
  advance (new due time) — the claim remains the correctness guarantee; this just cuts
  wasted concurrent advances.
- `asynq.MaxRetry(N)` (e.g. 5) — bound retries so a permanently-failing task doesn't
  cycle ~25× by default.
- `asynq.Retention(...)` for post-run inspection (short).
Keep the existing `ProcessIn`/scheduled behavior.

## 5. SMTP TLS default (security — Invariant 6)

`internal/platform/mail/sender.go` + `net_tester.go`: `UseTLS` is a plain bool that
defaults to `false` (JSON zero-value), so an omitted field silently dials cleartext and
does `SMTPAuthPlain` — the mailbox password crosses the wire in the clear, violating
"TLS enforced by default; plaintext only on explicit opt-out." Fix: make TLS the
default. Prefer opportunistic/mandatory STARTTLS; treat plaintext as an explicit,
deliberate opt-out (e.g. default the effective policy to `TLSMandatory` for 587/25/2525
unless an explicit `allow_plaintext` opt-out is set). Port 465 stays implicit-TLS. IMAP
is already correct. Update the connect-test to reject silent-plaintext the same way.
(Coordinate the request-field change with the mailbox connect DTO; keep it backward-safe.)

## 6. pgx pool sizing (perf)

`internal/platform/db/db.go` `Connect`: set `cfg.MaxConns` explicitly to at least
`WorkerConcurrency + headroom` and a non-zero `cfg.MinConns`, so workers don't starve on
`pool.Acquire` when concurrency is raised toward the ≥50-mailbox NFR. Derive from a
config value (thread `WorkerConcurrency`/a `DBMaxConns` into `Connect`, or set a sane
floor and document the relationship). Don't override an explicit `pool_max_conns` in the
DSN if present.

## 7. Security / invariants (must hold)

- Every new query (claim/finalize/release) workspace-pinned; a cross-tenant/foreign id
  claims/updates zero rows. Finalize + cursor advance stay in one tx.
- No secret handling changes; passwords still zeroized after send.
- The TLS-default change strengthens Invariant 6; no plaintext without explicit opt-out.

## 8. Testing

- **Unit (worker, fake core+sender):**
  - **Double-send regression (the headline test):** invoke `AdvanceHandler` twice for
    the same enrollment/step with a call-counting fake sender; the second claim fails →
    assert `Send` called **exactly once**. Same for `sender.Handler`.
  - Transient error → `Release` called + handler returns the error (asynq will retry);
    `Send` not finalized as failed.
  - Permanent error → finalize `failed` + cursor advances (fail-forward).
  - `mail.Retryable` classification table (timeout/4xx→true; 5xx/auth/build→false; unknown→false).
- **Integration (Postgres):**
  - Claim state machine: fresh insert claims; a `'sent'` row → claim returns false;
    a stale `'sending'` (claimed_at old) → reclaim true; a fresh `'sending'` → false.
  - Crash-recovery: claim, don't finalize, advance clock past lease → re-claim succeeds.
  - Workspace scoping: a foreign ws claiming/finalizing touches zero rows.
- **Regression:** existing send/sequence/inbox suites stay green.

## 9. Delivery order

1. Migration `000015` (status `'sending'` + `claimed_at`) + queries (`ClaimStepSend`,
   `FinalizeStepSend`, `ReleaseStepSend`, direct-path claim) + `sqlc generate`.
2. coreapi: `ClaimStepSend`/`FinalizeStepSend`/`ReleaseStepSend` (+ direct-path equivalents);
   split `MarkStepSent`/`MarkSend`.
3. `mail.Retryable` + SMTP TLS default.
4. Rewire `AdvanceHandler` + `sender.Handler` to claim→send→finalize/release; asynq
   TaskID/MaxRetry; pool sizing.
5. Tests (unit double-send regression + classification; integration claim/crash/tenant).
6. Verify (lint 0, build, vet, unit + integration green); docs note in `docs/security.md`
   (Invariant 6 tightened; delivery-idempotent send claim).
