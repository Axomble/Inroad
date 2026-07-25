# Reply Classification Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: subagent-driven-development. Steps use checkbox (`- [ ]`).

**Goal:** Classify inbound replies (positive/negative/neutral/auto_reply/out_of_office/unsubscribe/unknown) and act — fix the OOO trap, suppress reply-unsubscribes, tag replies with sentiment.

**Architecture:** A pure `internal/platform/replyclassify` package (Layer 1 headers + Layer 2 lexicon, deterministic/offline; Layer 3 model = injected optional seam, unwired). The inbox `processMessage` classifies each matched reply and routes: automated → don't stop (OOO fix); unsubscribe → suppress+stop; else → stop tagged. Inspired by Warmbly (Apache-2.0), reimplemented with fixes (the "not interested" bug, no global state, boundary matching) + Inroad test density.

**Tech Stack:** Go 1.25 · net/mail · pgx/sqlc · React SPA.

## Global Constraints

- Toolchain: prefix EVERY Go/sqlc command with `export PATH="$PATH:/c/Program Files/Go/bin:$HOME/go/bin"` in the SAME command. Do NOT `set -a && . ./.env`.
- Go files lowercase; identifiers MixedCaps; snake_case only at DB/env/JSON boundaries. Frontend kebab-case; apply React best practices (typed RTK via lib/rtk-error, code-split routes, one badge/pill component).
- Every tenant query workspace_id-pinned; classification is pure/side-effect-free; suppression reuses the existing workspace-scoped path.
- Worker reaches data only via coreapi (zero db import); the classifier is pure (no db/worker deps).
- Migrations/queries under internal/platform/db/; regen with `sqlc generate`. Migration head is `000013`; new migration is `000014`.
- Conventional commits; do NOT commit (coordinator commits per task). Verify before done: `go build ./...`, `go vet ./...`, `gofmt -l internal cmd` (only 4 known pre-existing files), `go test ./...`.
- **Attribution:** this ports/adapts Warmbly (Apache-2.0). Preserve attribution — a package-doc note crediting Warmbly's `replyclassify` + a root `NOTICE` entry. Do NOT paste verbatim where we intentionally improve (lexicon ordering, DI, boundary matching).

**Reference (read first):** Warmbly `C:/Users/Ahmed/OneDrive/Desktop/personal-projects/warmbly/internal/app/replyclassify/{classifier,headers,lexicon,model}.go` (inspiration); Inroad `internal/worker/inbox/{poll.go,reply.go,dsn.go}`, `internal/coreapi/coreapi.go` + `internal/coreapi/inprocess/reply*.go`/`inbox*.go`, `internal/app/enrollment/status.go`.

---

### Task 1: `internal/platform/replyclassify` — layered classifier (pure)

**Files:** Create `internal/platform/replyclassify/{classifier,headers,lexicon,model}.go` + `_test.go` for each; create/append root `NOTICE`.

**Interfaces (Produces):**
- Class/Source consts (the 7 classes + 4 sources); `Input{Headers map[string][]string; Subject, BodyText string}`; `Result{Class, Source string; Confidence float64}`.
- `ModelClassifier` interface `Classify(ctx, subject, body string) (class string, ok bool)`.
- `New(model ModelClassifier) *Classifier`; `(*Classifier).Classify(ctx, in) Result` (model may be nil → Layer 3 off).
- `IsAutomated(class string) bool` (auto_reply || out_of_office).

**Improvements over Warmbly (MUST implement, each with a test):**
- **Layer 2 order = compliance → negative → positive** (Warmbly does compliance→positive→negative). AND positive keywords are **negation-aware**: a positive hit preceded within ~3 words by `not/n't/no/never/isn't/won't/can't` does NOT fire. This fixes "not interested" (contains positive "interested") mis-classifying as positive — it must be `negative`.
- **Boundary-aware matching** for short/ambiguous tokens: match on word boundaries (regexp `\b`+token or a tokenized scan) for `stop`, `no need`, `go away`; phrase keywords (`stop emailing`) may use contains.
- **No global state:** the optional model is a struct field injected via `New`, not a package global + mutex.
- Layer 1: also treat `List-Unsubscribe`+`Auto-Submitted`/`X-Auto-Response-Suppress`/`Feedback-ID` as automated signals (superset of Warmbly).

- [ ] **Step 1: failing tests** — table-driven per layer. Cover: OOO subject w/o Auto-Submitted → out_of_office; Auto-Submitted: auto-replied → OOO, auto-generated → auto_reply; Precedence bulk → auto_reply; multipart/report delivery-status → auto_reply; mailer-daemon From → auto_reply; **"not interested" → negative** (regression); "unsubscribe"/"remove me" → unsubscribe (compliance-first, even if also negative words present); "sounds great, let's chat" → positive; **"nonstop"/"stopped by" → NOT unsubscribe** (boundary); empty/ambiguous → unknown (Source ""); nil model → middle=unknown no I/O; a fake ModelClassifier returns positive on the middle.
- [ ] **Step 2:** run → FAIL.
- [ ] **Step 3:** implement `classifier.go` (pipeline: Layer1 → Layer2 → Layer3(if model!=nil) → unknown), `headers.go` (case-insensitive lookup, superset signals), `lexicon.go` (compliance→negative→positive, negation-aware positive, boundary matching), `model.go` (the `ModelClassifier` seam + `IsAutomated`). Package doc credits Warmbly (Apache-2.0); add a `NOTICE` entry.
- [ ] **Step 4:** run → PASS; `go build ./... && go vet ./... && gofmt -l internal/platform/replyclassify`.
- [ ] **Step 5:** commit `feat(replyclassify): layered deterministic reply classifier (inspired by Warmbly, Apache-2.0)`.

---

### Task 2: Migration `000014` + enrollment columns + `unsubscribed` stop reason

**Files:** Create `internal/platform/db/migrations/000014_reply_class.{up,down}.sql`; modify `internal/platform/db/queries/enrollment.sql`; `internal/app/enrollment/status.go`; regen gen/.

**Interfaces (Produces):** `sequence_enrollments` gains `reply_class text`, `reply_source text`, `reply_confidence real`, `replied_at timestamptz` (all nullable); a CHECK pins `reply_class` to the 7 classes (or NULL). New queries `SetEnrollmentReplyClass` and (reuse/extend) the stop path. `enrollment.StopReason` gains `StopUnsubscribed = "unsubscribed"`.

- [ ] **Step 1:** `000014_reply_class.up.sql` — `ALTER TABLE sequence_enrollments ADD COLUMN reply_class text, ADD COLUMN reply_source text, ADD COLUMN reply_confidence real, ADD COLUMN replied_at timestamptz;` + `ALTER TABLE ... ADD CONSTRAINT sequence_enrollments_reply_class_chk CHECK (reply_class IS NULL OR reply_class IN ('positive','negative','neutral','auto_reply','out_of_office','unsubscribe','unknown'));` down: drop constraint + columns.
- [ ] **Step 2:** queries: `SetEnrollmentReplyClass` (workspace-pinned UPDATE of the 4 columns + replied_at=now()); confirm the existing stop query accepts `unsubscribed`. `enrollment/status.go`: add `StopUnsubscribed`.
- [ ] **Step 3:** `sqlc generate && go build ./...`; confirm gen methods.
- [ ] **Step 4:** migration reversibility (Docker up — coordinator will confirm Docker first): `migrate up && (echo y|migrate down) && migrate up`. If DB down, DEFERRED.
- [ ] **Step 5:** commit `feat(db): 000014 enrollment reply_class columns + unsubscribed stop reason`.

---

### Task 3: coreapi — MarkReplied(tagged) + MarkUnsubscribed + RecordReplyClass

**Files:** modify `internal/coreapi/coreapi.go`; `internal/coreapi/inprocess/` reply/inbox impls; fakes in worker inbox tests.

**Interfaces (Produces):**
- `MarkReplied(ctx, enrollmentID, workspaceID, replyClass, replySource string, confidence float64) error` — stop (reason replied) + store class/source/confidence/replied_at.
- `MarkUnsubscribed(ctx, enrollmentID, workspaceID, email string) error` — suppress the address (reuse the suppression insert used by MarkBounced) + stop (reason unsubscribed) + store class=unsubscribe.
- `RecordReplyClass(ctx, enrollmentID, workspaceID, class, source string, confidence float64) error` — store class WITHOUT stopping (for auto_reply/out_of_office).
- All workspace-pinned; enrollmentID may be "" (legacy direct-send) → store nothing / no-op safely.

- [ ] **Step 1:** failing inprocess tests (or extend): MarkReplied stores class + stops; MarkUnsubscribed suppresses + stops; RecordReplyClass stores + leaves status active.
- [ ] **Step 2:** implement in inprocess (reuse enrollment state machine + suppression). Update the `coreapi.Client` interface + ALL fakes (worker inbox test fakes).
- [ ] **Step 3:** `go build ./... && go vet ./... && go test ./internal/coreapi/... ./internal/worker/...`.
- [ ] **Step 4:** commit `feat(coreapi): tagged MarkReplied + MarkUnsubscribed + RecordReplyClass`.

---

### Task 4: Integrate the classifier into `processMessage`

**Files:** modify `internal/worker/inbox/poll.go` (processMessage + PollHandler/register to hold a `*replyclassify.Classifier`), `register.go`; tests `poll_test.go`.

- [ ] **Step 1:** failing tests: an OOO reply (subject "Out of Office", no Auto-Submitted) → enrollment NOT stopped, RecordReplyClass called with out_of_office; an "unsubscribe" reply → MarkUnsubscribed; a "not interested" reply → MarkReplied class=negative; a plain "sounds great" → MarkReplied class=positive; DSN bounce still → MarkBounced (unchanged).
- [ ] **Step 2:** in `processMessage`, after `FindSendByMessageID` matches, build `replyclassify.Input{Headers: msg.Header (as map), Subject, BodyText: msg.Body}` and classify; route per §5 of the spec (automated→RecordReplyClass, unsubscribe→MarkUnsubscribed, else→MarkReplied tagged). Keep the DSN-first branch. Construct the `Classifier` (New(nil) — Layer 3 unwired) in `inbox.Register`/PollHandler and thread it in. Note: `mail.InboundMessage.Header` is a `net/mail.Header` (map[string][]string) — adapt to the classifier Input.
- [ ] **Step 3:** `go build ./... && go vet ./... && go test ./internal/worker/inbox/...` (+ existing reply/bounce tests green).
- [ ] **Step 4:** commit `feat(inbox): classify matched replies — OOO-safe stop, unsubscribe suppression, tagged`.

---

### Task 5: Frontend — reply-class pill on the campaign/contact view

**Files:** web/src/features/campaigns/ (or contacts) — a `ReplyClassPill` component + surface it where enrollment/contact reply status shows; extend the relevant API type if `reply_class` is exposed via an endpoint (check the campaign detail/contacts API; if not exposed, add it to the response DTO in the backend handler — coordinate: that's a small backend edit, keep it in this task).

- [ ] Apply React best practices (one pill component, typed, no eager-import regressions). Test the pill rendering per class. Verify `cd web && npm run lint && npm run build && npx vitest run`.
- [ ] commit `feat(web): reply-class pill on campaign/contact view`.

---

### Task 6: Docs + attribution

**Files:** `docs/architecture.md`, `docs/security.md`, root `NOTICE` (verify Task 1 added it).

- [ ] architecture.md: the reply pipeline (match → classify → route); security.md: reply-unsubscribe suppression (compliance) + OOO-trap fix note; confirm `NOTICE` credits Warmbly (Apache-2.0). commit `docs(replyclassify): reply pipeline + compliance + attribution`.

---

## Self-Review

- Spec coverage: §4 classifier→T1, §5 integration→T4, §6 data/coreapi→T2+T3, §7 frontend→T5, §8 compliance→T3+T4, §9 tests→per task, §10 order→tasks. Covered.
- Improvements over Warmbly each have a task + regression test (not-interested bug→T1, no-global-state→T1, boundary→T1, OOO-trap→T4, reply-unsubscribe→T3/T4).
- Migration `000014` is next free (head `000013`). Attribution (Apache-2.0) in T1 + T6.
- Type consistency: `MarkReplied` gains (class, source, confidence); `MarkUnsubscribed`/`RecordReplyClass` new — T3 updates the interface + ALL fakes; T4 calls them.
