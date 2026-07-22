# Reply & Bounce Detection Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Periodically poll each active mailbox's IMAP INBOX, match inbound replies (threading headers) and delivery-status notifications (DSNs) back to the sends that caused them, and halt the affected enrollment — hard bounces also suppress the address workspace-wide.

**Architecture:** A periodic `inbox:sweep` fans out an `inbox:poll` per active mailbox (mirrors `sequence:sweep_stuck_enrollments`→`sequence:advance`). `inbox:poll` fetches messages above a per-mailbox UID cursor via a new `mail.InboxReader` (SSRF-guarded, go-imap v1.2.1), runs pure DSN + reply matchers, and acts through new `coreapi` methods — the worker imports zero `db`.

**Tech Stack:** Go 1.25 · pgx/sqlc · asynq · `emersion/go-imap v1.2.1` · `net/mail`+`mime/multipart` (DSN) · Postgres.

## Global Constraints

- Module `github.com/inroad/inroad`. Go files lowercase; identifiers idiomatic MixedCaps; snake_case only at JSON/DB/env boundaries.
- `app/*` imports `platform/*`, never the reverse; `app/*` packages don't import each other; **workers reach data only via `coreapi` (zero `db` import)**; workers MAY use `platform/mail` for mail I/O (like the sender does).
- Each domain owns its `Store` interface; services depend on the interface. Every new sqlc query carries a `workspace_id` predicate; every `coreapi` method that fetches a row by id pins `workspace_id` + a belt-and-braces `ErrCrossTenant` assertion (mirror `GetStepSendJob`).
- Toolchain PATH (this machine): prefix EVERY Go/sqlc command with `export PATH="$PATH:/c/Program Files/Go/bin:$HOME/go/bin"`. Shell state does not persist between calls. Work in the worktree `C:/Users/Ahmed/OneDrive/Desktop/personal-projects/Inroad-reply` (branch `feature/reply-bounce-detection`).
- IMAP dial ONLY through `mail`'s `vetAddr` SSRF guard + TLS (reuse `net_tester.go`'s pattern). Decrypted IMAP password is `[]byte`, in-memory only, zeroized after dial, never logged. Never log message bodies (log message-ids + classification only).
- `emersion/go-imap` is **v1.2.1** (v1 API: `client.Dial`/`DialTLS`, `c.StartTLS`, `c.Login`, `c.Select` → `*imap.MailboxStatus` with `.UidValidity`, `c.UidFetch(seqset, items, ch)` delivering `*imap.Message` on a channel). Do NOT assume v2.
- Bounce policy: hard (5.x.x) ⇒ stop enrollment (StopBounced) + SuppressEmail(workspace,email); soft (4.x.x) ⇒ log only. Reply match = threading headers only; skip `Auto-Submitted: auto-*` and DSNs. Poll `@every 3m`, INBOX only, ≤200 msgs/poll.
- Conventional commits. Commit at the end of every task. Never commit to `main`.

---

## Task 1: Migration 000010 + queries (cursor, active mailboxes, send-by-message-id, suppress-email)

**Files:**
- Create: `internal/platform/db/migrations/000010_inbox_cursor.{up,down}.sql`
- Modify: `queries/mailbox.sql`, `queries/send.sql`, `queries/suppression.sql` → regen `gen/`

**Interfaces produced:** sqlc methods `SetInboxCursor`, `GetInboxPollRow` (or reuse a mailbox getter that now returns the cursor cols), `ListActiveMailboxes`, `GetSendByMessageID`, `SuppressEmail`.

- [ ] **Step 1: Confirm migration head is 000009, write the up migration**

Run `ls internal/platform/db/migrations/ | grep -oE '^[0-9]+' | sort -u | tail -1` → must be `000009`. Then `000010_inbox_cursor.up.sql`:
```sql
ALTER TABLE mailboxes
    ADD COLUMN inbox_last_seen_uid BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN inbox_uid_validity  BIGINT NOT NULL DEFAULT 0;
```
`.down.sql`:
```sql
ALTER TABLE mailboxes DROP COLUMN IF EXISTS inbox_uid_validity;
ALTER TABLE mailboxes DROP COLUMN IF EXISTS inbox_last_seen_uid;
```

- [ ] **Step 2: Queries**

`queries/mailbox.sql` (append):
```sql
-- name: ListActiveMailboxes :many
SELECT id, workspace_id FROM mailboxes WHERE status = 'active';
-- name: SetInboxCursor :exec
UPDATE mailboxes SET inbox_last_seen_uid = $3, inbox_uid_validity = $4
WHERE id = $1 AND workspace_id = $2;
```
(For the poll job, check whether an existing `GetMailbox`/`Get` already selects `SELECT *` — if so it now includes the two cursor columns and needs no new query; otherwise add `GetInboxPollRow` selecting the IMAP fields + cursor. Confirm before adding.)
`queries/send.sql` (append):
```sql
-- name: GetSendByMessageID :one
-- Match an inbound reply/bounce back to the send that caused it, workspace-scoped.
SELECT id, enrollment_id, contact_id, to_email FROM sends
WHERE workspace_id = $1 AND message_id = $2 AND message_id <> '' LIMIT 1;
```
(Confirm the real column names on `sends`: `enrollment_id` may be nullable for the legacy direct-send path — the query still works; the handler treats a null enrollment as "no enrollment to stop".)
`queries/suppression.sql` (append IF no equivalent exists — check first):
```sql
-- name: SuppressEmail :exec
-- Add an address to the workspace suppression list (idempotent). reason e.g. 'bounced'.
INSERT INTO suppressions (workspace_id, email, reason)
VALUES ($1, $2, $3)
ON CONFLICT (workspace_id, email) DO NOTHING;
```
(Match the ACTUAL suppressions table columns — read the migration/existing suppression queries first; if unsubscribe already inserts, mirror that insert with a 'bounced' reason.)

- [ ] **Step 3: Regen + build**

`export PATH=... && make sqlc && go build ./...` — generation + build pass.

- [ ] **Step 4: Commit** — `feat(db): 000010 inbox UID cursor + reply/bounce match queries`

---

## Task 2: DSN parser (pure function)

**Files:** Create `internal/worker/inbox/dsn.go`, `dsn_test.go`.

**Interfaces produced:**
```go
type BounceKind int
const (NotABounce BounceKind = iota; SoftBounce; HardBounce)
type DSNResult struct {
    Kind             BounceKind
    OriginalMessageID string // from the returned message headers, "" if absent
    FailedRecipient   string // from Final-Recipient, "" if absent
    StatusCode        string // e.g. "5.1.1"
}
// ParseDSN inspects a parsed message; returns NotABounce if it isn't a DSN.
func ParseDSN(hdr mail.Header, contentType string, body []byte) DSNResult
```

- [ ] **Step 1: Write failing tests with fixtures**

`dsn_test.go` — table test with real-world DSN samples (embed as string constants): a hard bounce (`Content-Type: multipart/report; report-type=delivery-status`, a `message/delivery-status` part with `Status: 5.1.1`, and a returned-headers part with `Message-ID: <orig@x>`); a soft bounce (`Status: 4.2.2` mailbox full); a normal (non-DSN) email → `NotABounce`; a mailer-daemon email without a structured report → best-effort (NotABounce or Soft, assert your chosen behavior). Assert `Kind`, `StatusCode`, `OriginalMessageID`.
```go
func TestParseDSNHardBounce(t *testing.T) {
    r := ParseDSN(hdr(hardDSN), ctOf(hardDSN), []byte(hardDSN))
    if r.Kind != HardBounce || r.StatusCode != "5.1.1" || r.OriginalMessageID != "<orig@x>" {
        t.Fatalf("got %+v", r)
    }
}
```

- [ ] **Step 2: Run, verify fail** — `go test ./internal/worker/inbox/ -run TestParseDSN -v` → FAIL (undefined).

- [ ] **Step 3: Implement `ParseDSN`**

Detect DSN: `mime.ParseMediaType(contentType)` → `multipart/report` with `report-type=delivery-status` (or From contains `mailer-daemon`/`postmaster`). Walk the multipart body (`multipart.NewReader` with the boundary); in the `message/delivery-status` part read the per-recipient fields (`Final-Recipient`, `Action`, `Status`); classify by the first digit of `Status` (`5`→Hard, `4`→Soft); in the `message/rfc822`(-headers) part parse headers and read `Message-ID`. Return `DSNResult`. Keep it pure (no I/O). Be defensive: missing parts → best-effort, never panic.

- [ ] **Step 4: Run, verify pass; Step 5: Commit** — `feat(inbox): DSN parser (hard/soft classification + original message-id)`

---

## Task 3: Reply matcher (pure function)

**Files:** Create `internal/worker/inbox/reply.go`, `reply_test.go`.

**Interfaces produced:**
```go
// MessageIDs extracts candidate message-ids from In-Reply-To + References.
func MessageIDs(hdr mail.Header) []string
// IsAutoReply reports whether the message is an auto-responder (RFC 3834) and
// should NOT be treated as an engaged reply.
func IsAutoReply(hdr mail.Header) bool
```

- [ ] **Step 1: Failing tests** — `In-Reply-To: <a@x>` + `References: <b@y> <a@x>` → `["<a@x>","<b@y>","<a@x>"]` (dedupe optional, document); `Auto-Submitted: auto-replied` → `IsAutoReply` true; absent → false; also treat `Precedence: auto_reply`/`bulk`? (document the exact set matched). 

- [ ] **Step 2: fail; Step 3: implement** — split In-Reply-To/References on whitespace, keep `<...>` tokens; `IsAutoReply` = `Auto-Submitted` present and != `no`. Keep pure.

- [ ] **Step 4: pass; Step 5: commit** — `feat(inbox): reply matcher (message-id extraction + auto-reply skip)`

---

## Task 4: `mail.InboxReader` — go-imap v1 UID fetch

**Files:** Create `internal/platform/mail/inbox.go`, `inbox_test.go`. Modify `mail.go` (interface + InboundMessage type) if needed.

**Interfaces produced:**
```go
type InboundMessage struct {
    UID         uint32
    Header      mail.Header // parsed RFC5322 header (From, In-Reply-To, References, Auto-Submitted, Content-Type, Message-ID)
    ContentType string
    Body        []byte      // full raw message (for DSN multipart parsing)
}
type InboxReader interface {
    // Fetch returns messages with UID > sinceUID from INBOX (cap maxN), plus the
    // mailbox's current UIDVALIDITY. cfg reuses IMAPConfig.
    Fetch(cfg IMAPConfig, sinceUID uint32, maxN int) (msgs []InboundMessage, uidValidity uint32, err error)
}
// NetInboxReader is the production impl (SSRF-guarded, go-imap v1.2.1).
type NetInboxReader struct { Timeout time.Duration; AllowPrivate bool }
```

- [ ] **Step 1: Read the existing dial to copy exactly**

`sed -n '1,130p' internal/platform/mail/net_tester.go` — reuse `vetAddr(cfg.Host, cfg.Port, allowedIMAPPorts, t.AllowPrivate)`, the `client.Dial`+`StartTLS` (143) vs `client.DialTLS` (implicit) branch, and `c.Login`. Match it exactly.

- [ ] **Step 2: Implement `Fetch` (go-imap v1.2.1 API)**

```go
func (r *NetInboxReader) Fetch(cfg IMAPConfig, sinceUID uint32, maxN int) ([]InboundMessage, uint32, error) {
    addr, err := vetAddr(cfg.Host, cfg.Port, allowedIMAPPorts, r.AllowPrivate)
    if err != nil { return nil, 0, err }
    c, err := dialIMAP(addr, cfg) // extract the Dial/StartTLS-vs-DialTLS branch from TestIMAP into a shared helper
    if err != nil { return nil, 0, err }
    defer c.Logout()
    if err := c.Login(cfg.Username, cfg.Password); err != nil { return nil, 0, fmt.Errorf("imap login: %w", err) }
    mbox, err := c.Select("INBOX", true) // read-only
    if err != nil { return nil, 0, err }
    uidValidity := mbox.UidValidity
    // UID range (sinceUID+1):* — new mail only.
    seqset := new(imap.SeqSet)
    seqset.AddRange(sinceUID+1, 0) // 0 = "*"
    section := &imap.BodySectionName{} // BODY[] full message
    items := []imap.FetchItem{imap.FetchUid, section.FetchItem()}
    ch := make(chan *imap.Message, 32)
    done := make(chan error, 1)
    go func() { done <- c.UidFetch(seqset, items, ch) }()
    var out []InboundMessage
    for m := range ch {
        raw := m.GetBody(section)
        if raw == nil { continue }
        body, _ := io.ReadAll(raw)
        msg, _ := mail.ReadMessage(bytes.NewReader(body)) // header parse; tolerate error
        var h mail.Header; if msg != nil { h = msg.Header }
        out = append(out, InboundMessage{UID: m.Uid, Header: h, ContentType: h.Get("Content-Type"), Body: body})
    }
    if err := <-done; err != nil { return nil, uidValidity, fmt.Errorf("imap fetch: %w", err) }
    // Return the LOWEST maxN UIDs so the cap never skips mail: sort ascending and
    // truncate. The handler advances the cursor only to the max UID of THIS
    // returned batch, so anything beyond the cap is picked up on the next tick.
    sort.Slice(out, func(i, j int) bool { return out[i].UID < out[j].UID })
    if len(out) > maxN { out = out[:maxN] }
    return out, uidValidity, nil
}
```
Extract the dial branch shared with `TestIMAP` into `dialIMAP(addr, cfg)` so both use one SSRF-guarded path (refactor `TestIMAP` to call it — keep its behavior/tests green). NOTE: this collects the range then truncates to the lowest maxN — correct (no skips) at typical inbox volumes; a UID-SEARCH-first optimization is a deferred refinement, not needed for v1.

- [ ] **Step 3: Test**

`inbox_test.go`: unit-test what's testable without a server — the `sinceUID+1:*` seqset construction and the SSRF rejection (mirror `net_tester_test.go`'s `TestIMAPRejectsPrivateWhenDisallowed`: `Fetch` on a private host with `AllowPrivate=false` → error, no dial). Full fetch is exercised in the Task 9 integration test. Run `go test ./internal/platform/mail/ -v` (existing IMAP tests must stay green after the `dialIMAP` refactor).

- [ ] **Step 4: Commit** — `feat(mail): InboxReader — SSRF-guarded IMAP UID fetch (go-imap v1)`

---

## Task 5: `coreapi` methods + inprocess impl

**Files:** Modify `internal/coreapi/coreapi.go`; create `internal/coreapi/inprocess/inboxpoll.go`; test `inboxpoll_test.go`.

**Interfaces produced (added to `coreapi.Client`):**
```go
type MailboxRef struct{ ID, WorkspaceID string }
type InboxPollJob struct {
    Host string; Port int; Username string; Password []byte; UseTLS bool
    LastSeenUID uint32; UIDValidity uint32
}
type SendRef struct{ SendID, EnrollmentID, ContactEmail string } // EnrollmentID "" if none
ListActiveMailboxes(ctx) ([]MailboxRef, error)
GetInboxPollJob(ctx, mailboxID, workspaceID string) (InboxPollJob, error)
SetInboxCursor(ctx, mailboxID, workspaceID string, lastSeenUID, uidValidity uint32) error
FindSendByMessageID(ctx, workspaceID, messageID string) (SendRef, error) // ErrNoMatch if none
MarkReplied(ctx, enrollmentID, workspaceID string) error
MarkBounced(ctx, enrollmentID, workspaceID, email string, hard bool) error
```

- [ ] **Step 1: Read `inprocess` deps + the existing `GetStepSendJob`** to mirror decrypt (`sealer.Open`) + the `ErrCrossTenant` assertion pattern.
- [ ] **Step 2: Failing test** `inboxpoll_test.go` — decrypt/zeroize + cross-tenant assertions with a fake `gen.Queries`-style seam OR test the pure mapping; mirror `stepsendjob_test.go`.
- [ ] **Step 3: Implement** — each method workspace-pinned; `GetInboxPollJob` decrypts the IMAP secret via the sealer; `FindSendByMessageID` uses `GetSendByMessageID` (Task 1), returns `ErrNoMatch` on `pgx.ErrNoRows`; `MarkReplied` → enrollment `MarkStepStopped(StopReplied)`; `MarkBounced` → `MarkStepStopped(StopBounced)` and if `hard` also `SuppressEmail`. Add `ErrNoMatch`.
- [ ] **Step 4: build+test; Step 5: commit** — `feat(coreapi): inbox poll + reply/bounce match methods (inprocess)`

---

## Task 6: Queue tasks + enqueue helpers + scheduler registration

**Files:** Modify `internal/platform/queue/queue.go`, `queue_test.go`.

**Interfaces produced:** `TaskInboxSweep="inbox:sweep"`, `TaskInboxPoll="inbox:poll"`; `EnqueueInboxPoll(mailboxID, workspaceID string) error`; `RegisterInboxSweep(sch) error` (`@every 3m`). Payload struct `InboxPollPayload{MailboxID, WorkspaceID string}` (JSON), mirroring the existing advance payload.

- [ ] Steps: failing test asserting the task names + payload round-trip (mirror `queue_test.go`), implement mirroring `EnqueueAdvance`/`RegisterSweepEnrollments`, build+test, commit `feat(queue): inbox:sweep + inbox:poll tasks and scheduler registration`.

---

## Task 7: `inbox:poll` + `inbox:sweep` handlers

**Files:** Create `internal/worker/inbox/{poll.go, sweep.go, poll_test.go, sweep_test.go}`.

**Interfaces:** Consumes `coreapi.Client`, `mail.InboxReader`, Task 2/3 parsers, `queue`. Produces `Register(mux, core, reader, enq)` wiring both handlers (mirror `sequence` package's handler registration).

- [ ] **Step 1: Failing tests with fakes** (`poll_test.go`) — a fake `InboxReader` returning canned `InboundMessage`s + a fake `coreapi`:
  - a message whose References matches a seeded send (not auto-reply) → `MarkReplied` called with that enrollment; cursor advanced to max UID.
  - an `Auto-Submitted: auto-replied` message matching a send → `MarkReplied` NOT called.
  - a hard DSN referencing a send → `MarkBounced(hard=true)`; a soft DSN → neither Mark called (log only).
  - a message matching nothing → ignored; cursor still advances.
  - UIDVALIDITY changed vs job → re-baseline path: `SetInboxCursor` with the new validity + current max, no Mark calls.
  `sweep_test.go` — `inbox:sweep` calls `ListActiveMailboxes` and enqueues one `inbox:poll` per mailbox.

- [ ] **Step 2: fail; Step 3: implement**

`poll.go`: unmarshal payload → `GetInboxPollJob` → `defer zeroize(job.Password)` → `reader.Fetch(cfg, job.LastSeenUID, 200)` → if `uidValidity != job.UIDValidity && job.UIDValidity != 0` re-baseline (SetInboxCursor to max UID + new validity, return) → else for each message: `ParseDSN` first (hard→MarkBounced true+FindSend by OriginalMessageID; soft→log); if not a bounce and not `IsAutoReply`, for each `MessageIDs(hdr)` try `FindSendByMessageID` → on match `MarkReplied`, break → track maxUID over PROCESSED messages → `SetInboxCursor(maxUID, uidValidity)`. Since `Fetch` returns the lowest maxN UIDs sorted, maxUID = the last processed message's UID; the remainder (if the batch hit the cap) is picked up next tick. If zero messages were returned, leave the cursor unchanged. Log counts (replies/bounces/skipped), never bodies.
`sweep.go`: `ListActiveMailboxes` → `EnqueueInboxPoll` each. Mirror the enrollment sweeper.

- [ ] **Step 4: pass; Step 5: commit** — `feat(inbox): poll + sweep handlers (reply/bounce detection, cursor advance)`

---

## Task 8: Wiring (`cmd/worker` + handlers.go)

**Files:** Modify `internal/worker/handlers.go`, `cmd/worker/main.go`.

- [ ] Register the two handlers on the mux (construct `mail.NewNetInboxReader(cfg.MailAllowPrivateHosts)`), and `queue.RegisterInboxSweep(sch)` on the scheduler alongside the existing sweeps. Build. Run `go build ./... && go test ./...`. Commit `feat(worker): register inbox poll/sweep + scheduler`.

---

## Task 9: Integration tests

**Files:** Create `internal/worker/inbox/inbox_integration_test.go` (`//go:build integration`).

- [ ] **Step 1** — mirror the existing integration harness (`internal/worker/sequence/sequence_integration_test.go`). Against dockerized Postgres + an IMAP fixture (greenmail-style test server, or seed via the sequence test's mail setup if one exists): seed a campaign+enrollment+send with a known `message_id`; deliver an inbound reply referencing it to the mailbox; run the poll handler with the real `NetInboxReader` + inprocess coreapi → assert enrollment `stopped/replied` + cursor advanced; deliver a hard DSN → `stopped/bounced` + suppression row; soft DSN → unchanged; re-poll → no-op.
- [ ] **Step 2 (compile only — docker down)** — `go vet -tags=integration ./... && go build -tags=integration ./...` must be clean. Report execution NOT-RUN(no docker) with the run command: `make db-up && go test -tags=integration ./internal/worker/inbox/... -v`.
- [ ] **Step 3: Full verify + commit** — `go build ./... && go vet ./... && gofmt -l internal cmd && go test ./...`; commit `test(inbox): integration — reply/bounce/cursor flows (compile-verified)`.
