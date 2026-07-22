# Multi-Step Sequencing — Design Spec (PROPOSAL)

**Date:** 2026-07-21
**Status:** Working spec — §12–15 author's text; §0–11 grounded in the code, with author-confirmed corrections (see §0). Author confirmed **A3 = advance sends directly** as a self-contained path (not delegation to `send:email`), `GetSendJob`/`MarkSend` retained for the direct-send test path, `now + 6h` cap backoff, `MarkStepStopped` as the single stop entry point, and reorder-validation tightening on steps. Remaining ⟨A#⟩ tags are still-inferred and catchable at per-task review gates.
**Source PRD:** `PRD.md` (sequences/branching); extends `2026-07-20-minimal-campaign-send-design.md`
**Builds on:** `campaign`/`contact`/`list`/`suppression` domains, `sends` pipeline, `coreapi` seam (ADR-0003), `crypto.Sealer`, `platform/mail`, migrations through `000006`

---

## 0. Assumptions to confirm (read this first)

These are the points I inferred. If your real §1–11 differs, correct these and the rest of the spec follows:

| # | Decision I assumed | Alternatives / risk |
|---|---|---|
| A1 | **Send idempotency key becomes `(campaign_id, contact_id, step_order)`** via a new partial unique index; the legacy `ON CONFLICT (campaign_id, contact_id)` in `EnqueueSends` is retired (it no longer matches a constraint after 000006 anyway). | Could instead keep created_at-based uniqueness and rely on the enrollment cursor for idempotency. Affects the migration + `send.sql`. |
| A2 | **Enrollment states = `active \| completed \| stopped`**; `stop_reason ∈ ('','unsubscribed','replied','bounced','failed')`. `current_step` = highest step_order sent (0 = none yet). | §13 says "active + waiting" — you may want a distinct `waiting` state. |
| A3 | ✅ **CONFIRMED. `sequence:advance` does the SMTP send itself** (mirroring `sender.Handler`) via `GetStepSendJob`/`MarkStepSent` — a self-contained path. `GetSendJob`/`MarkSend` stay for the direct-send test path (no breaking change). | — locked. |
| A4 | **Cadence:** `next_due_at = <send time of step N> + step(N+1).wait_days`. Step 1 has `wait_days=0` and fires at launch, **staggered** by `index × 2s` ⟨tunable⟩ to avoid a burst. | You may want business-day math or a per-mailbox stagger window (§14 P0.5 seam). |
| A5 | **Threading:** step >1 with an **empty subject** ⇒ reply in thread (`Re: <step-1 subject>`, `In-Reply-To`/`References` = prior send's `message_id`); non-empty subject ⇒ new subject, still threaded. | You may want explicit per-step "reply vs new thread" flags. |
| A6 | **Backfill:** for every existing campaign, migration 000007 inserts a `step_order=1` step copying `subject/body_text/body_html`; already-launched campaigns are **not** retro-enrolled (their sends already exist). New launches build enrollments. | Retro-enrollment of in-flight campaigns is possible but risky pre-cutover. |
| A7 | Personalization gains `{{custom.<key>}}` resolving from `contacts.custom_fields` JSONB, plus `{{last_name}}`/`{{company}}`. Unknown `{{custom.x}}` → empty string + warn log (matches existing `warnUnknownPlaceholders`). | — |

---

## 1. Purpose & Scope

Extend the proven single-message send into a **multi-step drip sequence**. A campaign owns an ordered list of **steps**; each targeted contact gets an **enrollment** that walks the steps on a cadence, one email per step, stopping early on unsubscribe (reply/bounce are deferred but pre-wired). Backend only — verified by API + a live multi-step send. UI is a separate track (types regenerated from OpenAPI only).

**Non-goals (this increment):** reply detection, bounce classification, conditional branching, spintax, business-hours pacing, public API keys — all deferred with seams (§14).

## 2. Locked / Proposed Decisions

| Decision | Choice |
|---|---|
| Steps | `sequence_steps` — ordered per campaign (`step_order` 1..N), each with its own subject/body + `wait_days` gap before it fires |
| Enrollment | `sequence_enrollments` — one per (campaign, contact); a cursor (`current_step`, `next_due_at`, `status`, `stop_reason`) |
| Per-step send | Still **one `sends` row + one task per step-send** — reuse the send spine, add `step_id`/`step_order` + threading columns |
| Advance driver | Worker `sequence:advance` task per enrollment, scheduled at `next_due_at` (asynq `ProcessAt`) ⟨A3⟩ |
| Self-heal | Periodic `sequence:sweep_stuck_enrollments` re-enqueues active enrollments whose `next_due_at` has passed (mirrors the existing stuck-send sweeper) |
| Stop machinery | Single entry point `MarkStepStopped(enrollmentID, reason)`; unsubscribe wired now, reply/bounce deferred |
| Backward compat | `POST /campaigns` (subject/body) unchanged; launch auto-materializes step 1 if no explicit steps exist |
| Threading | `message_id` (exists) + new `in_reply_to`/`references` columns stored per send for future reply-matching |
| Personalization | `{{first_name}}`, `{{email}}`, `{{last_name}}`, `{{company}}`, `{{custom.<key>}}` |
| Worker DB access | Still zero `db` — two new `coreapi` methods only |

## 3. Data Model — migration `000007_sequences.up.sql`

All tenant tables carry `workspace_id UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE`; PKs `UUID DEFAULT gen_random_uuid()`; timestamps `TIMESTAMPTZ`.

```sql
-- Ordered steps of a campaign's sequence.
CREATE TABLE sequence_steps (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id  UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    campaign_id   UUID NOT NULL REFERENCES campaigns(id) ON DELETE CASCADE,
    step_order    INT  NOT NULL CHECK (step_order >= 1),
    wait_days     INT  NOT NULL DEFAULT 0 CHECK (wait_days >= 0), -- gap AFTER prior step before this one sends
    subject       TEXT NOT NULL DEFAULT '',   -- empty on step>1 ⇒ reply in thread (⟨A5⟩)
    body_text     TEXT NOT NULL DEFAULT '',
    body_html     TEXT NOT NULL DEFAULT '',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (campaign_id, step_order)
);
CREATE INDEX idx_sequence_steps_campaign ON sequence_steps (campaign_id, step_order);

-- One enrollment per (campaign, contact): the cursor that walks the steps.
CREATE TABLE sequence_enrollments (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id  UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    campaign_id   UUID NOT NULL REFERENCES campaigns(id) ON DELETE CASCADE,
    contact_id    UUID NOT NULL REFERENCES contacts(id) ON DELETE CASCADE,
    status        TEXT NOT NULL DEFAULT 'active'
                    CHECK (status IN ('active','completed','stopped')),
    stop_reason   TEXT NOT NULL DEFAULT ''
                    CHECK (stop_reason IN ('','unsubscribed','replied','bounced','failed')),
    current_step  INT  NOT NULL DEFAULT 0,   -- highest step_order sent; 0 = nothing sent yet
    next_due_at   TIMESTAMPTZ,               -- when the next step is due; NULL once completed/stopped
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (campaign_id, contact_id)
);
-- Advance loop + sweeper only ever scan active, due rows. Partial index stays tiny (§13).
CREATE INDEX idx_enrollments_due ON sequence_enrollments (next_due_at) WHERE status = 'active';
CREATE INDEX idx_enrollments_campaign ON sequence_enrollments (campaign_id, status);

-- Step linkage + threading on the existing sends table.
ALTER TABLE sends ADD COLUMN step_id     UUID REFERENCES sequence_steps(id) ON DELETE SET NULL;
ALTER TABLE sends ADD COLUMN step_order  INT  NOT NULL DEFAULT 1;
ALTER TABLE sends ADD COLUMN in_reply_to TEXT NOT NULL DEFAULT '';
ALTER TABLE sends ADD COLUMN references_hdr TEXT NOT NULL DEFAULT '';

-- ⟨A1⟩ Step-level idempotency: one send per (campaign, contact, step). Partial-safe,
-- coexists with the partition-ready (campaign_id, contact_id, created_at) key.
CREATE UNIQUE INDEX idx_sends_campaign_contact_step
    ON sends (campaign_id, contact_id, step_order);

-- ⟨A6⟩ Backfill: every existing campaign gets a step_order=1 mirroring its inline message.
INSERT INTO sequence_steps (workspace_id, campaign_id, step_order, wait_days, subject, body_text, body_html)
SELECT workspace_id, id, 1, 0, subject, body_text, body_html FROM campaigns;
```

**Down migration:** drop `idx_sends_campaign_contact_step`, the four `sends` columns, then `sequence_enrollments`, `sequence_steps`.

**Scalability rationale:** `sequence_enrollments` is the new high-growth table (500K rows at target scale, §13). Its only hot query — "active enrollments due now" — is served entirely by the partial `idx_enrollments_due`. `sends` growth (2.5M) is unchanged in shape; the new step columns are fixed-width/small-text and don't perturb the existing hot indexes.

## 4. Type Safety

- New typed enums mirrored by CHECK constraints:
  ```go
  type EnrollmentStatus string // "active" | "completed" | "stopped"
  type StopReason string       // "" | "unsubscribed" | "replied" | "bounced" | "failed"
  ```
- `sequence_steps`/`sequence_enrollments` sqlc models are the persistence types; domains expose small `Store` interfaces (dependency inversion, matching `campaign.Store`).
- `coreapi.StepSendJob` and `coreapi.StepResult` are fully-typed bundles (no loose maps), parallel to `SendJob`/`SendResult`.

## 5. Validation

| Route | Field validation | Ownership / semantic checks |
|---|---|---|
| `POST /campaigns/{id}/steps` | `step_order` ≥ 1; `wait_days` ≥ 0; `subject` ≤ 500; `body_text` OR `body_html` non-empty **unless** step_order>1 with intentional reply | campaign belongs to workspace (404); campaign `status='draft'` to edit steps (409); `step_order` unique in campaign (409) |
| `PUT /campaigns/{id}/steps/{stepId}` | same field rules | step + campaign belong to workspace (404 — cross-tenant → 404 per §12) |
| `DELETE /campaigns/{id}/steps/{stepId}` | — | ownership (404); draft only (409) |
| `GET /campaigns/{id}` (extended) | — | ownership (404); returns steps[] + enrollment counts |
| `POST /campaigns/{id}/launch` (refactored) | — | ownership (404); `draft` (409); list non-empty AND ≥1 step (422) |

Services enforce tenant-ownership existence checks; a step/enrollment referenced across tenants yields 404, never a leak.

## 6. `coreapi` Extension (worker stays DB-free)

```go
// Added to coreapi.Client:
GetStepSendJob(ctx, enrollmentID, workspaceID string) (StepSendJob, error)
MarkStepSent(ctx, enrollmentID, workspaceID string, res StepResult) (Advance, error)
MarkStepStopped(ctx, enrollmentID, workspaceID, reason string) error
ListDueEnrollments(ctx) ([]DueEnrollment, error) // sweeper self-heal

type StepSendJob struct {
    EnrollmentID string
    WorkspaceID  string
    StepOrder    int    // the step about to send (current_step+1)
    StepID       string
    LastStep     bool   // no further steps after this one
    Suppressed   bool
    // message (personalized fields resolved worker-side)
    ToEmail   string
    Vars      ContactVars   // first/last/email/company + custom map
    Subject   string        // resolved for this step (may be "" ⇒ reply-in-thread ⟨A5⟩)
    ThreadSubject string     // step-1 subject, for building "Re: ..."
    BodyText  string
    BodyHTML  string
    UnsubURL  string
    InReplyTo string        // message_id of the prior step's send ("" for step 1)
    References string
    // cap gate + transport (same as SendJob)
    EffectiveDailyCap int
    SentToday         int
    FromEmail, FromName, SMTPHost string
    SMTPPort int
    SMTPUsername string
    SMTPPassword []byte
    UseTLS bool
}

type StepResult struct { Status, MessageID, Err string } // status: sent|failed
type Advance struct { Completed bool; NextDueAt time.Time } // MarkStepSent tells the worker whether/when to reschedule
type ContactVars struct { FirstName, LastName, Email, Company string; Custom map[string]string }
type DueEnrollment struct { EnrollmentID, WorkspaceID string }
```

`inprocess.GetStepSendJob`: resolves `current_step+1`, joins step→campaign→contact→mailbox, decrypts SMTP secret, checks suppression, computes cap, builds unsub URL + threading headers from the prior send's `message_id`. **`MarkStepSent`** (one transaction) writes the `sends` row result, advances `current_step`, and computes `next_due_at` for the following step (or sets `status='completed', next_due_at=NULL`) — this is the **single insertion point** for `current_step` transitions and cadence (§14 branching/pacing seams). `SMTPPassword` decrypted in-memory only, never logged.

## 7. Send / Advance Flow

1. **Launch** (`POST /campaigns/{id}/launch`, one transaction): flip campaign `running`; `INSERT` one `sequence_enrollments` row per list member (`current_step=0`, `status=active`, `next_due_at = now() + index×stagger` ⟨A4⟩) `ON CONFLICT (campaign_id, contact_id) DO NOTHING`; enqueue one `sequence:advance` task per new enrollment via `ProcessAt(next_due_at)`.
2. **`sequence:advance` handler** (mirrors `sender.Handler`):
   - `job = GetStepSendJob(enrollmentID, ws)`. If enrollment already stopped/completed → no-op.
   - If `job.Suppressed` → `MarkStepStopped(reason='unsubscribed')`, done.
   - If `SentToday ≥ EffectiveDailyCap` → bump attempts / re-enqueue `ProcessIn(6h)` (reuse the cap-defer pattern), leave enrollment active.
   - Else personalize (subject + bodies, `{{custom.*}}`), append unsub footer, build MIME with threading headers, `Send()`; then `adv = MarkStepSent(result)`. If `adv.Completed` → done; else enqueue the next `sequence:advance` at `adv.NextDueAt`.
3. **Completion:** enrollment flips `completed` when the last step sends; campaign flips `done` when no `active` enrollments remain (computed on `GET /campaigns/{id}` and/or after `MarkStepSent`).
4. **`sequence:sweep_stuck_enrollments`** (periodic, e.g. `@every 5m`): `ListDueEnrollments` returns active rows with `next_due_at < now() - window`; re-enqueue `sequence:advance` for each (idempotent, like the send sweeper).

## 8. Personalization (`{{custom.<key>}}`)

Extend `internal/worker/sender/personalize.go`: replace the fixed 2-key replacer with a `ContactVars`-driven pass. Known keys (`first_name`, `last_name`, `email`, `company`) substitute their values; `{{custom.<key>}}` looks up `Custom[key]` (empty + warn if absent). HTML path still `html.EscapeString`s every substituted value. Leftover `{{...}}` still warns via `warnUnknownPlaceholders` (regex widened to allow `.`).

## 9. Threading Headers — `platform/mail`

Extend `mail.Message` with `InReplyTo`, `References` (and keep `ListUnsubscribe`). `NetSender.Send` sets the `In-Reply-To` and `References` headers when non-empty and returns the generated `Message-ID` (already stored in `sends.message_id`). Step-1 sends leave them empty; the `inprocess` layer populates them for step>1 from the prior send's `message_id` and accumulated references (⟨A5⟩).

## 10. New Domains & Files

- `internal/app/sequencestep/` — `Store` iface + `PgStore`, service, handler, routes (steps CRUD, draft-only edits, tenant checks).
- `internal/app/enrollment/` — enrollment `Store` + service (`Enroll`, `MarkStepSent`, `MarkStepStopped`), status enum.
- `internal/app/campaign/` — extend `Launch` to enroll + stagger + enqueue `sequence:advance`; extend `GET` response with steps + enrollment counts.
- `internal/worker/sequence/` — `sequence:advance` handler + `sequence:sweep_stuck_enrollments` handler.
- `internal/platform/queue/queue.go` — `TaskSequenceAdvance`, `TaskSweepEnrollments`, `EnqueueAdvance`/`EnqueueAdvanceAt`, `RegisterSweepEnrollments`.
- `internal/coreapi/coreapi.go` + `inprocess/` — `GetStepSendJob`/`MarkStepSent`/`MarkStepStopped`/`ListDueEnrollments` (+ `stepsendjob.go`).
- `internal/platform/db/migrations/000007_sequences.{up,down}.sql` + `queries/{sequencestep,enrollment}.sql`, extend `send.sql`.
- `internal/worker/handlers.go`, `cmd/worker/main.go` (register handler + scheduler), `api/openapi.yaml`.

Follows the `campaign`/`mailbox` reference pattern throughout.

## 11. Verification

1. **Backward compat:** create a campaign the old way (`POST /campaigns` subject/body), launch → enrollment created, step 1 auto-materialized, real send lands, `sends.status='sent'`.
2. **Multi-step:** create campaign + 2 explicit steps (step 2 `wait_days=0` for test speed, empty subject), launch → step 1 sends, enrollment advances, step 2 sends as a threaded reply (`In-Reply-To` set); enrollment `completed`.
3. **Stop-on-unsubscribe:** unsubscribe after step 1 → enrollment `stopped` (`stop_reason='unsubscribed'`), step 2 never sends.
4. **Cross-tenant (§12):** workspace A cannot fetch/edit workspace B's steps → 404; every new sqlc query carries a `workspace_id` predicate.
5. **Sweeper:** an active enrollment with a past `next_due_at` and no live task gets re-enqueued.
6. **Unit:** cadence math, personalization incl. `{{custom.*}}`, threading-header composition, state-machine transitions. **Integration:** launch→advance→advance against dockerized Postgres + mailpit.

---

## 12. Test Cases (author's text)

- Backward compat: create campaign the old way (`POST /campaigns` with subject/body), launch, verify enrollment + step 1 auto-created + successful send.

### Security / cross-tenant
- Workspace A cannot fetch/edit workspace B's steps → 404.
- Every new sqlc query includes `workspace_id` predicate (defense-in-depth pattern from the review fixes).

## 13. Scale Notes (author's text)

At 100 mailboxes × 5000 contacts × 5 steps per campaign, 30-day cadence:
- `sequence_enrollments`: 500K rows; partial `idx_enrollments_due` stays small (active + waiting only).
- `sends`: 2.5M rows over campaign life; already partition-ready from migration 000006.
- Peak scheduled asynq tasks: ≤ 500K at any moment. asynq handles this; sweeper self-heals if it doesn't.
- No new Redis / Postgres capacity requirements.

## 14. Deferred (with pre-cut seams) (author's text)

| Deferred item | Seam already in place |
|---|---|
| Reply detection (P0.2) | Threading headers stored; `sends.message_id` indexed already; `enrollment.stop_reason='replied'` machinery ready |
| Stop-on-reply consumer (P0.3) | `MarkStepStopped(reason)` is the one entry point; reply-event handler calls it |
| Bounce classifier (P0.4) | `MarkStepStopped(reason='bounced')` already wired; classifier only needs to decide when to call |
| Business-hours pacing (P0.5) | `next_due_at` computation is the single insertion point — one helper wraps it to shift into legal windows |
| Conditional branching (Phase 1) | `current_step` transition happens in one place (`MarkStepSent`); a branch resolver can inject a different `next_step_order` |
| Spintax (P4) | Personalization pass is one function; a spintax expander wraps it |
| Public API keys (P3) | Endpoints are auth-scoped; API-key auth plugs in at the middleware layer |

## 15. Build Order (author's text — input to plan)

1. Migration 000007 (steps, enrollments, sends columns, backfill) + sqlc queries.
2. `sequence_steps` domain (Store + service + handlers + routes + tests).
3. Personalization expansion (`ContactVars`, `{{custom.<key>}}`).
4. Threading header helpers in `platform/mail` (compose `Re: `, `References`, `In-Reply-To`).
5. `sequence_enrollments` store + service + `MarkStepSent`/`MarkStepStopped`.
6. `coreapi` `GetStepSendJob` / `MarkStepSent` + `inprocess` impl.
7. Refactor `POST /campaigns/{id}/launch` to insert enrollments + stagger + enqueue `sequence:advance`.
8. Worker `sequence:advance` handler + register in `handlers.go` + wire in `cmd/worker/main.go`.
9. `sequence:sweep_stuck_enrollments` periodic task + register in the scheduler.
10. `GET /campaigns/{id}` extended with steps + enrollment counts.
11. OpenAPI update; regenerate frontend types (types only — UI is separate track).
12. Unit + integration tests per §12.
