# Reply & Bounce Detection Design

**Goal:** Close the sequencing loop — stop emailing a contact once they reply or
their address hard-bounces. A periodic IMAP poll of each active mailbox matches
inbound replies (via threading headers) and delivery-status notifications (DSNs)
back to the sends that caused them, halting the affected enrollment and (for
hard bounces) suppressing the address workspace-wide.

**Builds on:** the merged `main` (sequencing + auth). Reuses the pre-cut seams:
`enrollment.StopReplied`/`StopBounced` + `MarkStepStopped` (single stop entry
point), `sends.message_id`/`references_header` (threading), the SSRF-guarded
IMAP dial in `platform/mail`, the asynq `@every` sweeper pattern, and the
`suppression` domain. Branch: `feature/reply-bounce-detection` off `main`.

**Tech stack:** Go 1.25 · pgx/sqlc · asynq · `emersion/go-imap` (already a dep) ·
`net/mail` + `mime/multipart` for DSN parsing · Postgres. Worker reaches data
only via `coreapi`.

---

## 0. Assumptions (confirmed with product owner)

| # | Decision | Rationale |
|---|---|---|
| A1 | **Detect & act only.** Poll IMAP, match replies + bounces, stop the enrollment, suppress hard bounces. NO reply-content storage, NO inbox UI, NO sentiment. | Smallest slice delivering the deliverability/etiquette win; a unified inbox is a later phase. |
| A2 | **Bounce policy:** classify hard (5.x.x) vs soft (4.x.x). Hard ⇒ stop enrollment (`StopBounced`) + add the address to the workspace suppression list (global). Soft ⇒ log only, no stop/suppress. | Deliverability-correct; reuses the suppression domain. |
| A3 | **Reply match = threading headers only.** Inbound `In-Reply-To`/`References` matched against `sends.message_id` (workspace+mailbox scoped) ⇒ `MarkStepStopped(StopReplied)`. No fuzzy From-address matching in v1. | Precise, low false-positive; it's the threading our sent mail establishes. |
| A4 | **Skip auto-replies.** A message carrying `Auto-Submitted: auto-*` (RFC 3834) or that is itself a DSN is NOT treated as an engaged reply, so out-of-office responders don't halt sequences. | Avoids false stops; the rare "stop" typed into an OOO is an accepted miss. |
| A5 | **Per-mailbox UID cursor** (`last_seen_uid`, `uid_validity`) on `mailboxes`; only messages with `UID > last_seen_uid` are processed; a UIDVALIDITY change re-baselines (records the new validity, resumes from current max — does not reprocess history). | Idempotent, bounded per poll; standard IMAP incremental fetch. |
| A6 | **Poll cadence `@every 3m`**, INBOX only, capped fetch per poll (e.g. ≤200 messages) so one tick can't monopolize the worker. Tunable. | Mirrors the send/enrollment sweeper cadence + LIMIT discipline. |

**Non-goals (later phases):** unified inbox UI, reply storage/sentiment,
positive-reply classification, fuzzy From matching, per-message webhooks,
polling non-INBOX folders.

---

## 1. Architecture

Two asynq tasks, mirroring `sequence:sweep_stuck_enrollments` → `sequence:advance`:

- **`inbox:sweep`** (periodic, `@every 3m`, registered on the scheduler): calls
  `coreapi.ListActiveMailboxes()` and enqueues one `inbox:poll` per mailbox.
  Idempotent — re-enqueuing a mailbox already polling is harmless (cursor guards
  reprocessing).
- **`inbox:poll {mailbox_id, workspace_id}`**: the per-mailbox handler.
  1. `job = coreapi.GetInboxPollJob(mailboxID, workspaceID)` — returns decrypted
     IMAP creds (`[]byte`, zeroized after), host/port/TLS, and the cursor
     (`last_seen_uid`, `uid_validity`).
  2. Dial IMAP through the existing SSRF guard + TLS (`platform/mail`), SELECT
     INBOX. If the server's `UIDVALIDITY` != stored `uid_validity` → re-baseline
     (set cursor to current max UID, persist, done this tick).
  3. `UID FETCH (last_seen_uid+1):*` headers + structure (cap ≤200); for each
     message run §2. Track the max UID seen.
  4. `coreapi.SetInboxCursor(mailboxID, workspaceID, maxUID, uidValidity)`.

The worker imports zero `db`; all data access is the new `coreapi` methods (§4).

## 2. Per-message processing (bounce first, then reply)

For each fetched message (parsed with `net/mail` + `mime`):

1. **Bounce (DSN) check:** is it a delivery-status report? Heuristics combined:
   `Content-Type: multipart/report; report-type="delivery-status"` OR From is
   `MAILER-DAEMON`/`postmaster`. If so, parse the `message/delivery-status` part
   for the `Status:` field and the failed recipient / original `Message-ID`
   (from the returned `message/rfc822`(-headers) part).
   - `Status` `5.x.x` (or no class but clearly permanent) ⇒ **hard**: resolve the
     original send by that Message-ID (or recipient+mailbox), call
     `coreapi.MarkBounced(sendID or enrollmentID, hard=true)` which
     `MarkStepStopped(StopBounced)` + `SuppressContact(workspace, email, "bounced")`.
   - `4.x.x` ⇒ **soft**: `log.Info` only. No stop/suppress.
   - Unparseable DSN ⇒ log + skip (flagged for a future heuristic pass).
   A DSN is never also treated as a reply (return after handling).
2. **Reply check:** collect message-ids from `In-Reply-To` + `References`. If any
   matches a `sends.message_id` in this mailbox's workspace ⇒ it's a reply to our
   sequence. **Unless** the message has `Auto-Submitted: auto-replied`/`auto-generated`
   (RFC 3834) → skip (auto-responder). Otherwise
   `coreapi.MarkReplied(enrollmentID, workspaceID)` ⇒ `MarkStepStopped(StopReplied)`.
3. No match ⇒ ignore (not ours).

`MarkStepStopped` is idempotent, so a message reprocessed after a crash mid-poll
(cursor not yet advanced) causes no double effect.

## 3. Data model — migration `000010`

```sql
ALTER TABLE mailboxes
    ADD COLUMN inbox_last_seen_uid BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN inbox_uid_validity  BIGINT NOT NULL DEFAULT 0;
```
`down`: drop the two columns. No other schema change — the enrollment
`status='stopped', stop_reason` and the suppression row are the record of what
happened (A1: no reply storage). (Confirm at implementation whether `suppression`
already has an insert path keyed by (workspace, email, reason); if it only has the
unsubscribe path, add a `SuppressEmail` query/method — reuse the table.)

## 4. `coreapi` extension (worker stays DB-free)

Added to `coreapi.Client`:
```go
ListActiveMailboxes(ctx) ([]MailboxRef, error)          // {ID, WorkspaceID}
GetInboxPollJob(ctx, mailboxID, workspaceID) (InboxPollJob, error)
SetInboxCursor(ctx, mailboxID, workspaceID string, lastSeenUID, uidValidity uint32) error
FindSendByMessageID(ctx, workspaceID, messageID string) (SendRef, error) // {SendID, EnrollmentID, ContactEmail}; ErrNoMatch if none
MarkReplied(ctx, enrollmentID, workspaceID string) error                 // -> MarkStepStopped(StopReplied)
MarkBounced(ctx, enrollmentID, workspaceID, email string, hard bool) error // hard -> MarkStepStopped(StopBounced)+SuppressEmail
```
`InboxPollJob` bundles: IMAP host/port/username, decrypted `Password []byte`
(zeroized by the worker after dial), `UseTLS`, `LastSeenUID`, `UIDValidity`.
Every method is workspace-pinned in SQL + a belt-and-braces `ErrCrossTenant`
assertion where a row is fetched by id (mirrors `GetStepSendJob`).

## 5. New / changed files

- `internal/platform/mail/inbox.go` (+test) — `InboxReader` interface + a
  go-imap-backed impl: `Fetch(cfg, sinceUID) ([]InboundMessage, uidValidity, error)`
  reusing the SSRF-guarded dial from `net_tester.go`. `InboundMessage` = parsed
  headers (From, In-Reply-To, References, Auto-Submitted, Content-Type) + the
  DSN parts. This is the only new IMAP-fetch code.
- `internal/worker/inbox/{poll.go, dsn.go, reply.go, sweep.go, *_test.go}` — the
  handlers + pure-function DSN parser + reply matcher (unit-tested with fixtures,
  no network).
- `internal/platform/db/migrations/000010_inbox_cursor.{up,down}.sql`;
  `queries/mailbox.sql` (cursor get/set, list-active) + `queries/send.sql`
  (`GetSendByMessageID`) + `queries/suppression.sql` (`SuppressEmail` if absent) → regen.
- `internal/coreapi/coreapi.go` + `inprocess/inboxpoll.go` — the 6 methods.
- `internal/platform/queue/queue.go` — `TaskInboxSweep`/`TaskInboxPoll`, enqueue
  helpers, `RegisterInboxSweep`.
- `internal/worker/handlers.go`, `cmd/worker/main.go` — register handlers + scheduler.

## 6. Security & tenancy

- IMAP dial only through `mail`'s SSRF guard (block loopback/link-local/multicast;
  private gated by `INROAD_MAIL_ALLOW_PRIVATE_HOSTS`) + TLS enforced; dial the
  vetted IP, hostname as SNI (DNS-rebinding closed) — reuse `net_tester.go`'s path.
- Decrypted IMAP password is `[]byte`, in-memory only, zeroized after dial, never
  logged. No message bodies logged (log message-ids + classification only).
- `FindSendByMessageID` is workspace-scoped: a spoofed inbound `Message-ID` can
  only ever match a send in the polling mailbox's own workspace, so a reply/bounce
  can't cross tenants or stop another workspace's enrollment.
- Hard-bounce `SuppressEmail` is workspace-scoped (suppresses only within the
  workspace that owns the mailbox).
- A malicious inbound message can at worst stop/suppress an enrollment/address in
  its own workspace (a self-inflicted DoS on one's own campaign) — acceptable and
  bounded.

## 7. Verification

- **Unit:** DSN parser (hard 5.x.x, soft 4.x.x, malformed, multiple providers'
  formats via fixtures); reply matcher (threading hit, no-match, Auto-Submitted
  skip, DSN-not-treated-as-reply); cursor/UIDVALIDITY re-baseline logic; the poll
  handler with a fake `InboxReader` + fake `coreapi` (no network/DB).
- **Integration (`//go:build integration`):** against dockerized Postgres +
  a test IMAP server (or a seeded maildir / greenmail-style fixture): seed a
  send with a known message_id, inject an inbound reply referencing it → poll →
  enrollment `stopped/replied`; inject a hard-DSN → enrollment `stopped/bounced`
  + suppression row present; soft-DSN → no change; cursor advances; re-poll is a
  no-op (idempotent).
- **Manual:** point at a real mailbox with a real reply/bounce (post-merge, when
  a mail environment is available).

## 8. Open reconciliation notes

- Confirm the exact IMAP client API surface `net_tester.go` uses (`emersion/go-imap`
  version) and whether it exposes `UID FETCH` + `UIDVALIDITY`; if the pinned
  version is v1 vs v2 the fetch code differs — the implementer verifies against
  the actual dep before writing `inbox.go`.
- Confirm the `suppression` insert path: reuse the existing table + add a
  `SuppressEmail(workspace, email, reason)` query if only the unsubscribe path exists.
