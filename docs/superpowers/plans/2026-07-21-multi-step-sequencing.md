# Multi-Step Sequencing Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Turn a single-message campaign into a multi-step drip sequence — a campaign owns ordered `sequence_steps`, each targeted contact gets a `sequence_enrollments` cursor that walks the steps on a `wait_days` cadence, one threaded email per step, stopping early on unsubscribe.

**Architecture:** Two new domains (`sequencestep`, `enrollment`) follow the `campaign`/`mailbox` reference pattern (domain-owned `Store` interface, auth-scoped routes, DTOs, tenant checks). A new worker `sequence:advance` handler owns the whole step lifecycle (fetch → personalize → threaded MIME → SMTP → advance) reaching data only through two new `coreapi` methods — it imports zero `db`. The existing `send:email` path and `GetSendJob`/`MarkSend` stay untouched (direct-send test path, no breaking change). A `sequence:sweep_stuck_enrollments` periodic task self-heals missed schedules, mirroring the existing stuck-send sweeper.

**Tech Stack:** Go 1.25 · pgx/sqlc · asynq (`ProcessAt`/`ProcessIn`) · `wneessen/go-mail` (threading headers) · `go-playground/validator/v10` · Postgres 16.

## Global Constraints

- **Module:** `github.com/inroad/inroad`. Go files lowercase, no hyphens; identifiers idiomatic `MixedCaps`. snake_case only at boundaries (JSON, DB columns, env).
- **Toolchain PATH (this machine):** prefix every Go/sqlc Bash command with `export PATH="$PATH:/c/Program Files/Go/bin:$HOME/go/bin"`. Shell state does NOT persist between calls.
- **Architecture (SOLID/Clean):** each domain defines its own `Store` interface; services depend on the interface, not the sqlc struct. `app/*` may import `platform/*`, never the reverse; `app/*` packages don't import each other; workers reach data only via `coreapi`.
- **Type safety:** status columns use Go typed-constant enums (`EnrollmentStatus`, `StopReason`) mirrored by DB `CHECK` constraints. No `interface{}` except `custom_fields` (`map[string]any`). Explicit request/response DTOs; `coreapi.StepSendJob`/`StepResult` are fully-typed bundles.
- **Validation:** every handler calls `validate.Struct(req)` before the service; services enforce tenant-ownership existence checks (cross-workspace reference → 404, never a leak).
- **Tenancy (defense in depth):** every new sqlc query carries a `workspace_id` predicate; every `coreapi` method pins `workspace_id` in the WHERE clause + a belt-and-braces `ErrCrossTenant` assertion.
- **Secrets:** decrypted `SMTPPassword` (`[]byte`) used in-memory only, zeroized after send, never logged.
- **Migrations/queries/gen** live under `internal/platform/db/` (go:embed constraint). New migration is **000007** (000006 is the latest).
- **Commits:** conventional (`feat:`, `test:`, `chore:`). Commit at the end of every task. Work on branch `feature/multi-step-sequencing` (never commit to `main`).

---

## File Structure

- `internal/platform/db/migrations/000007_sequences.{up,down}.sql` — steps, enrollments, `sends` columns, step-idempotency index, backfill
- `internal/platform/db/queries/sequencestep.sql`, `queries/enrollment.sql`; extend `queries/send.sql` → regenerates `gen/`
- `internal/app/sequencestep/{status.go,store.go,service.go,handler.go,routes.go,*_test.go}` — steps CRUD (draft-only, reorder-validated)
- `internal/app/enrollment/{status.go,store.go,service.go,*_test.go}` — enrollment store + `MarkStepSent`/`MarkStepStopped`
- `internal/app/campaign/{service.go,store.go,handler.go,routes.go}` — extend `Launch` (enroll+stagger+enqueue advance) and `GET` (steps + counts)
- `internal/worker/personalize/{personalize.go,*_test.go}` — shared `ContactVars` personalization (`{{custom.<key>}}`); `sender` refactored to use it
- `internal/platform/mail/sender.go` — extend `Message` with `InReplyTo`/`References`
- `internal/platform/queue/queue.go` — `TaskSequenceAdvance`, `TaskSweepEnrollments`, enqueue helpers, scheduler registration
- `internal/coreapi/coreapi.go` (extend) + `internal/coreapi/inprocess/stepsendjob.go` — `GetStepSendJob`/`MarkStepSent`/`MarkStepStopped`/`ListDueEnrollments`
- `internal/worker/sequence/{advance.go,sweeper.go,*_test.go}` — the two handlers
- `internal/worker/handlers.go`, `cmd/worker/main.go` — register handler + scheduler
- `cmd/inroad/main.go` — mount step routes; wire enrollment store into campaign launch
- `api/openapi.yaml` — new endpoints/schemas; regenerate `web/` types

---

## Task 1: Schema, queries, typed enums

**Files:**
- Create: `internal/platform/db/migrations/000007_sequences.up.sql`, `.down.sql`
- Create: `internal/platform/db/queries/sequencestep.sql`, `internal/platform/db/queries/enrollment.sql`
- Modify: `internal/platform/db/queries/send.sql` (step-aware insert + prior-message lookup)
- Generated: `internal/platform/db/gen/*`

**Interfaces:**
- Produces sqlc methods used by later tasks: `CreateStep`, `GetStep`, `ListStepsByCampaign`, `UpdateStep`, `DeleteStep`, `CountStepsByCampaign`, `MaxStepOrder`; `EnrollListMembers`, `GetEnrollment`, `AdvanceEnrollment`, `StopEnrollment`, `CountEnrollmentsByStatus`, `ListDueEnrollments`, `GetStepSendBundle`, `InsertStepSend`, `GetPriorStepMessage`.

- [ ] **Step 1: Write the up migration**

`internal/platform/db/migrations/000007_sequences.up.sql` — copy §3 DDL from the spec verbatim (both tables, their indexes, the four `sends` columns, `idx_sends_campaign_contact_step`, and the backfill `INSERT ... SELECT ... FROM campaigns`).

- [ ] **Step 2: Write the down migration**

`internal/platform/db/migrations/000007_sequences.down.sql`:

```sql
DROP INDEX IF EXISTS idx_sends_campaign_contact_step;
ALTER TABLE sends DROP COLUMN IF EXISTS references_hdr;
ALTER TABLE sends DROP COLUMN IF EXISTS in_reply_to;
ALTER TABLE sends DROP COLUMN IF EXISTS step_order;
ALTER TABLE sends DROP COLUMN IF EXISTS step_id;
DROP TABLE IF EXISTS sequence_enrollments;
DROP TABLE IF EXISTS sequence_steps;
```

- [ ] **Step 3: Run the migration up + down + up to prove it's reversible**

Run:
```bash
export PATH="$PATH:/c/Program Files/Go/bin:$HOME/go/bin"
make migrate-up && make migrate-down && make migrate-up
```
Expected: all three succeed; no error about a missing constraint/column.

- [ ] **Step 4: Write `queries/sequencestep.sql`**

```sql
-- name: CreateStep :one
INSERT INTO sequence_steps (workspace_id, campaign_id, step_order, wait_days, subject, body_text, body_html)
VALUES ($1,$2,$3,$4,$5,$6,$7) RETURNING *;
-- name: GetStep :one
SELECT * FROM sequence_steps WHERE id = $1 AND workspace_id = $2;
-- name: ListStepsByCampaign :many
SELECT * FROM sequence_steps WHERE campaign_id = $1 AND workspace_id = $2 ORDER BY step_order;
-- name: UpdateStep :one
UPDATE sequence_steps SET wait_days = $3, subject = $4, body_text = $5, body_html = $6
WHERE id = $1 AND workspace_id = $2 RETURNING *;
-- name: DeleteStep :exec
DELETE FROM sequence_steps WHERE id = $1 AND workspace_id = $2;
-- name: CountStepsByCampaign :one
SELECT count(*) FROM sequence_steps WHERE campaign_id = $1 AND workspace_id = $2;
-- name: MaxStepOrder :one
SELECT COALESCE(max(step_order), 0)::int FROM sequence_steps WHERE campaign_id = $1 AND workspace_id = $2;
```

- [ ] **Step 5: Write `queries/enrollment.sql`**

```sql
-- name: EnrollListMembers :many
-- Materialize one enrollment per list member for a campaign. Staggered
-- next_due_at is set by the caller per-row (see Task 8) via AdvanceEnrollment;
-- here every new enrollment starts due-now and the launch code reschedules.
INSERT INTO sequence_enrollments (workspace_id, campaign_id, contact_id, next_due_at)
SELECT cam.workspace_id, cam.id, lm.contact_id, now()
FROM campaigns cam
JOIN list_members lm ON lm.list_id = cam.list_id
WHERE cam.id = $1 AND cam.workspace_id = $2
ON CONFLICT (campaign_id, contact_id) DO NOTHING
RETURNING id;
-- name: GetEnrollment :one
SELECT * FROM sequence_enrollments WHERE id = $1 AND workspace_id = $2;
-- name: AdvanceEnrollment :exec
-- Single insertion point for the current_step transition + cadence.
UPDATE sequence_enrollments
SET current_step = $3, next_due_at = $4, status = $5, updated_at = now()
WHERE id = $1 AND workspace_id = $2;
-- name: StopEnrollment :exec
UPDATE sequence_enrollments
SET status = 'stopped', stop_reason = $3, next_due_at = NULL, updated_at = now()
WHERE id = $1 AND workspace_id = $2;
-- name: SetEnrollmentDue :exec
UPDATE sequence_enrollments SET next_due_at = $3, updated_at = now()
WHERE id = $1 AND workspace_id = $2 AND status = 'active';
-- name: CountEnrollmentsByStatus :many
SELECT status, count(*) AS n FROM sequence_enrollments
WHERE campaign_id = $1 AND workspace_id = $2 GROUP BY status;
-- name: ListDueEnrollments :many
-- Sweeper hot path: active enrollments whose next_due_at has passed the
-- reconcile window. Served by the partial idx_enrollments_due. Capped so one
-- sweep tick can't monopolize the worker.
SELECT id, workspace_id FROM sequence_enrollments
WHERE status = 'active' AND next_due_at IS NOT NULL
  AND next_due_at < now() - interval '5 minutes'
ORDER BY next_due_at ASC
LIMIT 500;
```

- [ ] **Step 6: Extend `queries/send.sql` with step-aware inserts + prior-message lookup**

Append:
```sql
-- name: InsertStepSend :one
-- One send per (campaign, contact, step). ON CONFLICT makes the advance
-- handler idempotent: a re-run for the same step returns the existing row.
INSERT INTO sends (workspace_id, campaign_id, contact_id, mailbox_id, to_email,
                   step_id, step_order, in_reply_to, references_hdr)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
ON CONFLICT (campaign_id, contact_id, step_order) DO UPDATE SET to_email = EXCLUDED.to_email
RETURNING id;
-- name: GetStepSendBundle :one
-- Everything the worker needs to send one step-email, workspace-pinned.
SELECT e.id AS enrollment_id, e.workspace_id, e.current_step, e.status,
       cam.id AS campaign_id, cam.mailbox_id,
       ct.id AS contact_id, ct.email AS to_email, ct.first_name, ct.last_name,
       ct.company, ct.custom_fields,
       m.email AS from_email, m.display_name AS from_name,
       m.smtp_host, m.smtp_port, m.smtp_username, m.secret_ciphertext, m.use_tls,
       m.daily_cap, m.ramp_enabled, m.ramp_start_cap, m.ramp_days,
       m.created_at AS mailbox_created_at
FROM sequence_enrollments e
JOIN campaigns cam ON cam.id = e.campaign_id
JOIN contacts ct ON ct.id = e.contact_id
JOIN mailboxes m ON m.id = cam.mailbox_id
WHERE e.id = $1 AND e.workspace_id = $2;
-- name: GetStepByOrder :one
SELECT * FROM sequence_steps WHERE campaign_id = $1 AND workspace_id = $2 AND step_order = $3;
-- name: GetPriorStepMessage :one
-- The most recent sent message for this (campaign, contact) below step_order,
-- used to thread the reply (In-Reply-To / References).
SELECT message_id, references_hdr FROM sends
WHERE campaign_id = $1 AND contact_id = $2 AND step_order < $3 AND status = 'sent'
ORDER BY step_order DESC LIMIT 1;
```

- [ ] **Step 7: Regenerate sqlc + build**

Run:
```bash
export PATH="$PATH:/c/Program Files/Go/bin:$HOME/go/bin"
make sqlc && go build ./...
```
Expected: generation succeeds; `go build` passes (new `gen` methods exist, nothing references them yet).

- [ ] **Step 8: Add typed enums**

Create `internal/app/enrollment/status.go`:
```go
// Package enrollment tracks a contact's position in a campaign's step
// sequence and provides the single stop/advance entry points.
package enrollment

// EnrollmentStatus is the typed enum mirrored by the DB CHECK on
// sequence_enrollments.status.
type EnrollmentStatus string

const (
	StatusActive    EnrollmentStatus = "active"
	StatusCompleted EnrollmentStatus = "completed"
	StatusStopped   EnrollmentStatus = "stopped"
)

// StopReason is why an enrollment halted. "" while active/completed.
type StopReason string

const (
	StopUnsubscribed StopReason = "unsubscribed"
	StopReplied      StopReason = "replied"
	StopBounced      StopReason = "bounced"
	StopFailed       StopReason = "failed"
)
```

- [ ] **Step 9: Commit**

```bash
git add internal/platform/db/migrations/000007_sequences.up.sql internal/platform/db/migrations/000007_sequences.down.sql internal/platform/db/queries/ internal/platform/db/gen/ internal/app/enrollment/status.go
git commit -m "feat(db): 000007 sequences schema, queries, enrollment enums"
```

---

## Task 2: `sequencestep` domain (steps CRUD, draft-only, reorder-validated)

**Files:**
- Create: `internal/app/sequencestep/store.go`, `service.go`, `handler.go`, `routes.go`
- Test: `internal/app/sequencestep/service_test.go`

**Interfaces:**
- Consumes: `gen.SequenceStep`, `gen.Queries` step methods (Task 1), `campaign.Checker`-style ownership (reuse `campaign` store's `Get` via a small `CampaignChecker`).
- Produces: `sequencestep.Service` with `Create/List/Update/Delete`; `NewHandler(svc, jwtSecret).Routes()` mounted at `/api/v1/campaigns/{id}/steps` (Task 12 wiring).

- [ ] **Step 1: Write the store**

`internal/app/sequencestep/store.go`:
```go
package sequencestep

import (
	"context"

	"github.com/google/uuid"
	"github.com/inroad/inroad/internal/platform/db/gen"
)

type CreateInput struct {
	CampaignID uuid.UUID
	StepOrder  int32
	WaitDays   int32
	Subject    string
	BodyText   string
	BodyHTML   string
}

type UpdateInput struct {
	StepID   uuid.UUID
	WaitDays int32
	Subject  string
	BodyText string
	BodyHTML string
}

// Store is the repository interface this domain depends on (defined by the
// consumer for testability).
type Store interface {
	Create(ctx context.Context, ws uuid.UUID, in CreateInput) (gen.SequenceStep, error)
	Get(ctx context.Context, ws, id uuid.UUID) (gen.SequenceStep, error)
	List(ctx context.Context, ws, campaignID uuid.UUID) ([]gen.SequenceStep, error)
	Update(ctx context.Context, ws uuid.UUID, in UpdateInput) (gen.SequenceStep, error)
	Delete(ctx context.Context, ws, id uuid.UUID) error
	MaxStepOrder(ctx context.Context, ws, campaignID uuid.UUID) (int32, error)
}

// CampaignChecker verifies the campaign exists in the workspace and reports
// its status (steps are editable only while the campaign is draft).
type CampaignChecker interface {
	CampaignStatus(ctx context.Context, ws, campaignID uuid.UUID) (string, error)
}

type PgStore struct{ q *gen.Queries }

func NewPgStore(q *gen.Queries) *PgStore { return &PgStore{q: q} }

func (s *PgStore) Create(ctx context.Context, ws uuid.UUID, in CreateInput) (gen.SequenceStep, error) {
	return s.q.CreateStep(ctx, gen.CreateStepParams{
		WorkspaceID: ws, CampaignID: in.CampaignID, StepOrder: in.StepOrder,
		WaitDays: in.WaitDays, Subject: in.Subject, BodyText: in.BodyText, BodyHtml: in.BodyHTML,
	})
}
func (s *PgStore) Get(ctx context.Context, ws, id uuid.UUID) (gen.SequenceStep, error) {
	return s.q.GetStep(ctx, gen.GetStepParams{ID: id, WorkspaceID: ws})
}
func (s *PgStore) List(ctx context.Context, ws, campaignID uuid.UUID) ([]gen.SequenceStep, error) {
	return s.q.ListStepsByCampaign(ctx, gen.ListStepsByCampaignParams{CampaignID: campaignID, WorkspaceID: ws})
}
func (s *PgStore) Update(ctx context.Context, ws uuid.UUID, in UpdateInput) (gen.SequenceStep, error) {
	return s.q.UpdateStep(ctx, gen.UpdateStepParams{
		ID: in.StepID, WorkspaceID: ws, WaitDays: in.WaitDays,
		Subject: in.Subject, BodyText: in.BodyText, BodyHtml: in.BodyHTML,
	})
}
func (s *PgStore) Delete(ctx context.Context, ws, id uuid.UUID) error {
	return s.q.DeleteStep(ctx, gen.DeleteStepParams{ID: id, WorkspaceID: ws})
}
func (s *PgStore) MaxStepOrder(ctx context.Context, ws, campaignID uuid.UUID) (int32, error) {
	return s.q.MaxStepOrder(ctx, gen.MaxStepOrderParams{CampaignID: campaignID, WorkspaceID: ws})
}
```

- [ ] **Step 2: Write the failing service test**

`internal/app/sequencestep/service_test.go`:
```go
package sequencestep

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/inroad/inroad/internal/platform/db/gen"
)

type fakeStore struct {
	maxOrder int32
	steps    []gen.SequenceStep
}

func (f *fakeStore) Create(_ context.Context, ws uuid.UUID, in CreateInput) (gen.SequenceStep, error) {
	st := gen.SequenceStep{ID: uuid.New(), CampaignID: in.CampaignID, StepOrder: in.StepOrder, Subject: in.Subject}
	f.steps = append(f.steps, st)
	return st, nil
}
func (f *fakeStore) Get(context.Context, uuid.UUID, uuid.UUID) (gen.SequenceStep, error) {
	return gen.SequenceStep{}, nil
}
func (f *fakeStore) List(context.Context, uuid.UUID, uuid.UUID) ([]gen.SequenceStep, error) {
	return f.steps, nil
}
func (f *fakeStore) Update(context.Context, uuid.UUID, UpdateInput) (gen.SequenceStep, error) {
	return gen.SequenceStep{}, nil
}
func (f *fakeStore) Delete(context.Context, uuid.UUID, uuid.UUID) error { return nil }
func (f *fakeStore) MaxStepOrder(context.Context, uuid.UUID, uuid.UUID) (int32, error) {
	return f.maxOrder, nil
}

type fakeChecker struct{ status string }

func (c fakeChecker) CampaignStatus(context.Context, uuid.UUID, uuid.UUID) (string, error) {
	return c.status, nil
}

func TestCreateRejectsNonDraftCampaign(t *testing.T) {
	svc := NewService(&fakeStore{}, fakeChecker{status: "running"})
	_, err := svc.Create(context.Background(), uuid.New(), uuid.New(), CreateInput{Subject: "x", BodyText: "y"})
	if err != ErrCampaignNotDraft {
		t.Fatalf("want ErrCampaignNotDraft, got %v", err)
	}
}

func TestCreateAppendsAtNextOrder(t *testing.T) {
	store := &fakeStore{maxOrder: 2}
	svc := NewService(store, fakeChecker{status: "draft"})
	st, err := svc.Create(context.Background(), uuid.New(), uuid.New(), CreateInput{Subject: "x", BodyText: "y"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if st.StepOrder != 3 {
		t.Fatalf("want step_order 3 (max+1), got %d", st.StepOrder)
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run:
```bash
export PATH="$PATH:/c/Program Files/Go/bin:$HOME/go/bin"
go test ./internal/app/sequencestep/ -run TestCreate -v
```
Expected: FAIL — `NewService`, `ErrCampaignNotDraft` undefined.

- [ ] **Step 4: Write the service**

`internal/app/sequencestep/service.go`:
```go
package sequencestep

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/inroad/inroad/internal/platform/db/gen"
)

var (
	ErrNotFound         = errors.New("step not found")
	ErrCampaignNotFound = errors.New("campaign not found")
	ErrCampaignNotDraft = errors.New("steps can only be edited while the campaign is draft")
)

// draftStatus is the campaign status that permits step edits. Kept local so
// this domain doesn't import the campaign package (app/* isolation).
const draftStatus = "draft"

type Service struct {
	store   Store
	checker CampaignChecker
}

func NewService(store Store, checker CampaignChecker) *Service {
	return &Service{store: store, checker: checker}
}

// Create appends a step at max(step_order)+1 after confirming the campaign is
// draft and owned by the workspace.
func (s *Service) Create(ctx context.Context, ws, campaignID uuid.UUID, in CreateInput) (gen.SequenceStep, error) {
	if err := s.requireDraft(ctx, ws, campaignID); err != nil {
		return gen.SequenceStep{}, err
	}
	maxOrder, err := s.store.MaxStepOrder(ctx, ws, campaignID)
	if err != nil {
		return gen.SequenceStep{}, err
	}
	in.CampaignID = campaignID
	in.StepOrder = maxOrder + 1
	return s.store.Create(ctx, ws, in)
}

func (s *Service) List(ctx context.Context, ws, campaignID uuid.UUID) ([]gen.SequenceStep, error) {
	return s.store.List(ctx, ws, campaignID)
}

func (s *Service) Update(ctx context.Context, ws, campaignID uuid.UUID, in UpdateInput) (gen.SequenceStep, error) {
	if err := s.requireDraft(ctx, ws, campaignID); err != nil {
		return gen.SequenceStep{}, err
	}
	st, err := s.store.Update(ctx, ws, in)
	if err != nil {
		return gen.SequenceStep{}, ErrNotFound
	}
	return st, nil
}

func (s *Service) Delete(ctx context.Context, ws, campaignID, stepID uuid.UUID) error {
	if err := s.requireDraft(ctx, ws, campaignID); err != nil {
		return err
	}
	return s.store.Delete(ctx, ws, stepID)
}

func (s *Service) requireDraft(ctx context.Context, ws, campaignID uuid.UUID) error {
	status, err := s.checker.CampaignStatus(ctx, ws, campaignID)
	if err != nil {
		return ErrCampaignNotFound
	}
	if status != draftStatus {
		return ErrCampaignNotDraft
	}
	return nil
}
```

- [ ] **Step 5: Run test to verify it passes**

Run:
```bash
export PATH="$PATH:/c/Program Files/Go/bin:$HOME/go/bin"
go test ./internal/app/sequencestep/ -run TestCreate -v
```
Expected: PASS.

- [ ] **Step 6: Write handler + routes**

`internal/app/sequencestep/handler.go`:
```go
package sequencestep

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/app/auth"
	"github.com/inroad/inroad/internal/platform/db/gen"
	"github.com/inroad/inroad/internal/platform/httpx"
	"github.com/inroad/inroad/internal/platform/validate"
)

type Handler struct {
	svc       *Service
	jwtSecret []byte
}

func NewHandler(svc *Service, jwtSecret []byte) *Handler { return &Handler{svc: svc, jwtSecret: jwtSecret} }

type stepRequest struct {
	WaitDays int32  `json:"wait_days" validate:"gte=0,lte=365"`
	Subject  string `json:"subject" validate:"max=500"`
	BodyText string `json:"body_text"`
	BodyHTML string `json:"body_html"`
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	ws, ok := auth.WorkspaceID(w, r)
	if !ok {
		return
	}
	campaignID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "bad campaign id")
		return
	}
	var req stepRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid json")
		return
	}
	if err := validate.Struct(req); err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.BodyText == "" && req.BodyHTML == "" {
		httpx.Error(w, http.StatusBadRequest, "body_text or body_html required")
		return
	}
	st, err := h.svc.Create(r.Context(), ws, campaignID, CreateInput{
		WaitDays: req.WaitDays, Subject: req.Subject, BodyText: req.BodyText, BodyHTML: req.BodyHTML,
	})
	h.writeStep(w, st, err)
}

func (h *Handler) update(w http.ResponseWriter, r *http.Request) {
	ws, ok := auth.WorkspaceID(w, r)
	if !ok {
		return
	}
	campaignID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "bad campaign id")
		return
	}
	stepID, err := uuid.Parse(chi.URLParam(r, "stepId"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "bad step id")
		return
	}
	var req stepRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid json")
		return
	}
	if err := validate.Struct(req); err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	st, err := h.svc.Update(r.Context(), ws, campaignID, UpdateInput{
		StepID: stepID, WaitDays: req.WaitDays, Subject: req.Subject, BodyText: req.BodyText, BodyHTML: req.BodyHTML,
	})
	h.writeStep(w, st, err)
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	ws, ok := auth.WorkspaceID(w, r)
	if !ok {
		return
	}
	campaignID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "bad campaign id")
		return
	}
	steps, err := h.svc.List(r.Context(), ws, campaignID)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "could not list steps")
		return
	}
	out := make([]stepResponse, 0, len(steps))
	for _, st := range steps {
		out = append(out, toResponse(st))
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (h *Handler) del(w http.ResponseWriter, r *http.Request) {
	ws, ok := auth.WorkspaceID(w, r)
	if !ok {
		return
	}
	campaignID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "bad campaign id")
		return
	}
	stepID, err := uuid.Parse(chi.URLParam(r, "stepId"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "bad step id")
		return
	}
	err = h.svc.Delete(r.Context(), ws, campaignID, stepID)
	switch {
	case errors.Is(err, ErrCampaignNotFound):
		httpx.Error(w, http.StatusNotFound, "campaign not found")
	case errors.Is(err, ErrCampaignNotDraft):
		httpx.Error(w, http.StatusConflict, "campaign not draft")
	case err != nil:
		httpx.Error(w, http.StatusInternalServerError, "could not delete step")
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

func (h *Handler) writeStep(w http.ResponseWriter, st gen.SequenceStep, err error) {
	switch {
	case errors.Is(err, ErrCampaignNotFound):
		httpx.Error(w, http.StatusNotFound, "campaign not found")
	case errors.Is(err, ErrCampaignNotDraft):
		httpx.Error(w, http.StatusConflict, "campaign not draft")
	case errors.Is(err, ErrNotFound):
		httpx.Error(w, http.StatusNotFound, "step not found")
	case err != nil:
		httpx.Error(w, http.StatusInternalServerError, "could not save step")
	default:
		httpx.JSON(w, http.StatusOK, toResponse(st))
	}
}
```

`internal/app/sequencestep/routes.go`:
```go
package sequencestep

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/inroad/inroad/internal/app/auth"
	"github.com/inroad/inroad/internal/platform/db/gen"
)

// Routes mounts step endpoints. Mounted by cmd/inroad at
// /api/v1/campaigns so the {id} param resolves to the campaign.
func (h *Handler) Routes() http.Handler {
	r := chi.NewRouter()
	r.Use(auth.RequireAuth(h.jwtSecret))
	r.Get("/{id}/steps", h.list)
	r.Post("/{id}/steps", h.create)
	r.Put("/{id}/steps/{stepId}", h.update)
	r.Delete("/{id}/steps/{stepId}", h.del)
	return r
}

type stepResponse struct {
	ID        string `json:"id"`
	StepOrder int32  `json:"step_order"`
	WaitDays  int32  `json:"wait_days"`
	Subject   string `json:"subject"`
	BodyText  string `json:"body_text"`
	BodyHTML  string `json:"body_html"`
}

func toResponse(st gen.SequenceStep) stepResponse {
	return stepResponse{
		ID: st.ID.String(), StepOrder: st.StepOrder, WaitDays: st.WaitDays,
		Subject: st.Subject, BodyText: st.BodyText, BodyHTML: st.BodyHtml,
	}
}
```

- [ ] **Step 7: Build + run the package tests**

Run:
```bash
export PATH="$PATH:/c/Program Files/Go/bin:$HOME/go/bin"
go build ./... && go test ./internal/app/sequencestep/ -v
```
Expected: build passes; tests PASS.

- [ ] **Step 8: Commit**

```bash
git add internal/app/sequencestep/
git commit -m "feat(sequencestep): steps CRUD domain (draft-only, workspace-scoped)"
```

---

## Task 3: Shared personalization (`ContactVars`, `{{custom.<key>}}`)

**Files:**
- Create: `internal/worker/personalize/personalize.go`, `personalize_test.go`
- Modify: `internal/worker/sender/sender.go` (call the shared package), delete `internal/worker/sender/personalize.go` + its test after porting cases

**Interfaces:**
- Produces: `personalize.Vars` struct; `personalize.Text(tmpl string, v Vars) string` and `personalize.HTML(tmpl string, v Vars) string`. Consumed by `sender` (Task 3) and `sequence` (Task 9).

- [ ] **Step 1: Write the failing test**

`internal/worker/personalize/personalize_test.go`:
```go
package personalize

import "testing"

func TestCustomFieldSubstitution(t *testing.T) {
	v := Vars{FirstName: "Ada", Email: "ada@x.io", Custom: map[string]string{"city": "London"}}
	got := Text("Hi {{first_name}} from {{custom.city}} <{{email}}>", v)
	want := "Hi Ada from London <ada@x.io>"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestUnknownCustomFieldIsEmpty(t *testing.T) {
	got := Text("X{{custom.missing}}Y", Vars{})
	if got != "XY" {
		t.Fatalf("got %q want %q", got, "XY")
	}
}

func TestHTMLEscapesValues(t *testing.T) {
	got := HTML("Hi {{first_name}}", Vars{FirstName: "<b>Ada</b>"})
	want := "Hi &lt;b&gt;Ada&lt;/b&gt;"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestEmptyFirstNameFallsBackToThere(t *testing.T) {
	if got := Text("Hi {{first_name}}", Vars{}); got != "Hi there" {
		t.Fatalf("got %q", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run:
```bash
export PATH="$PATH:/c/Program Files/Go/bin:$HOME/go/bin"
go test ./internal/worker/personalize/ -v
```
Expected: FAIL — package/functions undefined.

- [ ] **Step 3: Write the implementation**

`internal/worker/personalize/personalize.go`:
```go
// Package personalize substitutes {{...}} template placeholders in email
// subjects/bodies. Shared by the direct sender and the sequence advance
// handler so both apply identical rules.
package personalize

import (
	"html"
	"log/slog"
	"regexp"
	"strings"
)

// Vars are the values available to a template. Custom holds arbitrary
// per-contact fields addressed as {{custom.<key>}}.
type Vars struct {
	FirstName string
	LastName  string
	Email     string
	Company   string
	Custom    map[string]string
}

// leftoverRE matches any placeholder still present after substitution,
// including dotted custom keys. Emitted as a warn so operators spot typos.
var leftoverRE = regexp.MustCompile(`\{\{[a-zA-Z_.]+\}\}`)

// customRE matches {{custom.<key>}} where key is [a-zA-Z0-9_].
var customRE = regexp.MustCompile(`\{\{custom\.([a-zA-Z0-9_]+)\}\}`)

// Text substitutes placeholders for a plain-text body (no escaping).
func Text(tmpl string, v Vars) string { return substitute(tmpl, v, false) }

// HTML substitutes placeholders for an HTML body, escaping every value so a
// hostile contact field can't inject markup.
func HTML(tmpl string, v Vars) string { return substitute(tmpl, v, true) }

func substitute(tmpl string, v Vars, escape bool) string {
	name := v.FirstName
	if strings.TrimSpace(name) == "" {
		name = "there"
	}
	enc := func(s string) string {
		if escape {
			return html.EscapeString(s)
		}
		return s
	}
	// Custom fields first (a fixed key can't collide with custom.*).
	out := customRE.ReplaceAllStringFunc(tmpl, func(m string) string {
		key := customRE.FindStringSubmatch(m)[1]
		return enc(v.Custom[key])
	})
	r := strings.NewReplacer(
		"{{first_name}}", enc(name),
		"{{last_name}}", enc(v.LastName),
		"{{email}}", enc(v.Email),
		"{{company}}", enc(v.Company),
	)
	out = r.Replace(out)
	for _, m := range leftoverRE.FindAllString(out, -1) {
		slog.Warn("unknown template placeholder", "placeholder", m)
	}
	return out
}
```

- [ ] **Step 4: Run test to verify it passes**

Run:
```bash
export PATH="$PATH:/c/Program Files/Go/bin:$HOME/go/bin"
go test ./internal/worker/personalize/ -v
```
Expected: PASS.

- [ ] **Step 5: Refactor `sender` to use the shared package**

In `internal/worker/sender/sender.go`, replace the `personalizeText`/`personalizeHTML` calls with:
```go
vars := personalize.Vars{FirstName: job.FirstName, Email: job.ToEmail}
subject := personalize.Text(job.Subject, vars)
bodyText := withUnsubText(personalize.Text(job.BodyText, vars), job.UnsubURL)
bodyHTML := ""
if job.BodyHTML != "" {
	bodyHTML = withUnsubHTML(personalize.HTML(job.BodyHTML, vars), job.UnsubURL)
}
```
Add the import `"github.com/inroad/inroad/internal/worker/personalize"`. Delete `internal/worker/sender/personalize.go` and `personalize_test.go` (their cases are now covered by the shared package; `withUnsubText`/`withUnsubHTML` stay in `sender.go`).

- [ ] **Step 6: Build + run sender tests**

Run:
```bash
export PATH="$PATH:/c/Program Files/Go/bin:$HOME/go/bin"
go build ./... && go test ./internal/worker/... -v
```
Expected: build passes; sender + personalize tests PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/worker/personalize/ internal/worker/sender/
git commit -m "refactor(worker): shared personalize pkg with {{custom.*}} support"
```

---

## Task 4: Threading headers in `platform/mail`

**Files:**
- Modify: `internal/platform/mail/sender.go`
- Test: `internal/platform/mail/sender_test.go` (add a case; create if absent)

**Interfaces:**
- Produces: `mail.Message` gains `InReplyTo string` and `References string`; `NetSender.Send` sets `In-Reply-To`/`References` headers when non-empty. Consumed by Task 9.

- [ ] **Step 1: Read the current sender to find the header-setting site**

Run:
```bash
export PATH="$PATH:/c/Program Files/Go/bin:$HOME/go/bin"
sed -n '1,120p' internal/platform/mail/sender.go
```
Expected: locate the `Message` struct and where `ListUnsubscribe` is set on the go-mail message.

- [ ] **Step 2: Write the failing test**

`internal/platform/mail/sender_test.go` (add):
```go
func TestMessageCarriesThreadingHeaders(t *testing.T) {
	m := Message{To: "a@b.io", Subject: "Re: Hi", InReplyTo: "<root@x>", References: "<root@x>"}
	if m.InReplyTo != "<root@x>" || m.References != "<root@x>" {
		t.Fatalf("threading fields not set: %+v", m)
	}
}
```
(A structural test — the header wiring is exercised end-to-end by the mailpit integration test in Task 13.)

- [ ] **Step 3: Run test to verify it fails**

Run:
```bash
export PATH="$PATH:/c/Program Files/Go/bin:$HOME/go/bin"
go test ./internal/platform/mail/ -run TestMessageCarriesThreadingHeaders -v
```
Expected: FAIL — unknown fields `InReplyTo`/`References`.

- [ ] **Step 4: Add the fields + header wiring**

In `internal/platform/mail/sender.go`, add to `Message`:
```go
InReplyTo  string
References string
```
Where the go-mail message is built (next to the `List-Unsubscribe` header), add:
```go
if msg.InReplyTo != "" {
	m.SetGenHeader("In-Reply-To", msg.InReplyTo)
}
if msg.References != "" {
	m.SetGenHeader("References", msg.References)
}
```
(Use the same `SetGenHeader`/header API the existing `ListUnsubscribe` line uses — match it exactly.)

- [ ] **Step 5: Run test to verify it passes + build**

Run:
```bash
export PATH="$PATH:/c/Program Files/Go/bin:$HOME/go/bin"
go test ./internal/platform/mail/ -v && go build ./...
```
Expected: PASS; build passes.

- [ ] **Step 6: Commit**

```bash
git add internal/platform/mail/
git commit -m "feat(mail): In-Reply-To/References headers for step threading"
```

---

## Task 5: `enrollment` store + service (`MarkStepSent`/`MarkStepStopped`)

**Files:**
- Create: `internal/app/enrollment/store.go`, `service.go`
- Test: `internal/app/enrollment/service_test.go`

**Interfaces:**
- Consumes: `gen` enrollment methods (Task 1), `enrollment.EnrollmentStatus`/`StopReason` (Task 1).
- Produces: `enrollment.Store` iface; `enrollment.Service` with `Advance(ctx, ws, id, currentStep, nextDueAt, completed)` and `Stop(ctx, ws, id, reason)`. Consumed by the `inprocess` coreapi impl (Task 6) — the coreapi layer calls the enrollment store directly (control-plane code), while the worker reaches it only via `coreapi`.

- [ ] **Step 1: Write the store**

`internal/app/enrollment/store.go`:
```go
package enrollment

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/inroad/inroad/internal/platform/db/gen"
)

type Store interface {
	Enroll(ctx context.Context, ws, campaignID uuid.UUID) ([]uuid.UUID, error)
	Get(ctx context.Context, ws, id uuid.UUID) (gen.SequenceEnrollment, error)
	Advance(ctx context.Context, ws, id uuid.UUID, currentStep int32, nextDueAt time.Time, status EnrollmentStatus) error
	SetDue(ctx context.Context, ws, id uuid.UUID, nextDueAt time.Time) error
	Stop(ctx context.Context, ws, id uuid.UUID, reason StopReason) error
	CountByStatus(ctx context.Context, ws, campaignID uuid.UUID) (map[string]int64, error)
}

type PgStore struct{ q *gen.Queries }

func NewPgStore(q *gen.Queries) *PgStore { return &PgStore{q: q} }

func (s *PgStore) Enroll(ctx context.Context, ws, campaignID uuid.UUID) ([]uuid.UUID, error) {
	return s.q.EnrollListMembers(ctx, gen.EnrollListMembersParams{ID: campaignID, WorkspaceID: ws})
}
func (s *PgStore) Get(ctx context.Context, ws, id uuid.UUID) (gen.SequenceEnrollment, error) {
	return s.q.GetEnrollment(ctx, gen.GetEnrollmentParams{ID: id, WorkspaceID: ws})
}
func (s *PgStore) Advance(ctx context.Context, ws, id uuid.UUID, currentStep int32, nextDueAt time.Time, status EnrollmentStatus) error {
	due := pgtype.Timestamptz{}
	if status == StatusActive {
		due = pgtype.Timestamptz{Time: nextDueAt, Valid: true}
	}
	return s.q.AdvanceEnrollment(ctx, gen.AdvanceEnrollmentParams{
		ID: id, WorkspaceID: ws, CurrentStep: currentStep, NextDueAt: due, Status: string(status),
	})
}
func (s *PgStore) SetDue(ctx context.Context, ws, id uuid.UUID, nextDueAt time.Time) error {
	return s.q.SetEnrollmentDue(ctx, gen.SetEnrollmentDueParams{
		ID: id, WorkspaceID: ws, NextDueAt: pgtype.Timestamptz{Time: nextDueAt, Valid: true},
	})
}
func (s *PgStore) Stop(ctx context.Context, ws, id uuid.UUID, reason StopReason) error {
	return s.q.StopEnrollment(ctx, gen.StopEnrollmentParams{ID: id, WorkspaceID: ws, StopReason: string(reason)})
}
func (s *PgStore) CountByStatus(ctx context.Context, ws, campaignID uuid.UUID) (map[string]int64, error) {
	rows, err := s.q.CountEnrollmentsByStatus(ctx, gen.CountEnrollmentsByStatusParams{CampaignID: campaignID, WorkspaceID: ws})
	if err != nil {
		return nil, err
	}
	out := map[string]int64{}
	for _, r := range rows {
		out[r.Status] = r.N
	}
	return out, nil
}
```

- [ ] **Step 2: Write the failing service test**

`internal/app/enrollment/service_test.go`:
```go
package enrollment

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/inroad/inroad/internal/platform/db/gen"
)

type fakeStore struct {
	advancedStatus EnrollmentStatus
	advancedStep   int32
	stoppedReason  StopReason
}

func (f *fakeStore) Enroll(context.Context, uuid.UUID, uuid.UUID) ([]uuid.UUID, error) { return nil, nil }
func (f *fakeStore) Get(context.Context, uuid.UUID, uuid.UUID) (gen.SequenceEnrollment, error) {
	return gen.SequenceEnrollment{}, nil
}
func (f *fakeStore) Advance(_ context.Context, _, _ uuid.UUID, step int32, _ time.Time, st EnrollmentStatus) error {
	f.advancedStep, f.advancedStatus = step, st
	return nil
}
func (f *fakeStore) SetDue(context.Context, uuid.UUID, uuid.UUID, time.Time) error { return nil }
func (f *fakeStore) Stop(_ context.Context, _, _ uuid.UUID, r StopReason) error {
	f.stoppedReason = r
	return nil
}
func (f *fakeStore) CountByStatus(context.Context, uuid.UUID, uuid.UUID) (map[string]int64, error) {
	return nil, nil
}

func TestMarkStepSentCompletesOnLastStep(t *testing.T) {
	f := &fakeStore{}
	svc := NewService(f)
	// lastStep=true ⇒ status must become completed regardless of nextDueAt.
	if err := svc.MarkStepSent(context.Background(), uuid.New(), uuid.New(), 3, time.Now(), true); err != nil {
		t.Fatal(err)
	}
	if f.advancedStatus != StatusCompleted || f.advancedStep != 3 {
		t.Fatalf("got status=%s step=%d", f.advancedStatus, f.advancedStep)
	}
}

func TestMarkStepSentStaysActiveMidSequence(t *testing.T) {
	f := &fakeStore{}
	svc := NewService(f)
	if err := svc.MarkStepSent(context.Background(), uuid.New(), uuid.New(), 1, time.Now(), false); err != nil {
		t.Fatal(err)
	}
	if f.advancedStatus != StatusActive {
		t.Fatalf("want active, got %s", f.advancedStatus)
	}
}

func TestMarkStepStoppedPassesReason(t *testing.T) {
	f := &fakeStore{}
	svc := NewService(f)
	if err := svc.MarkStepStopped(context.Background(), uuid.New(), uuid.New(), StopUnsubscribed); err != nil {
		t.Fatal(err)
	}
	if f.stoppedReason != StopUnsubscribed {
		t.Fatalf("want unsubscribed, got %s", f.stoppedReason)
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run:
```bash
export PATH="$PATH:/c/Program Files/Go/bin:$HOME/go/bin"
go test ./internal/app/enrollment/ -v
```
Expected: FAIL — `NewService`, `MarkStepSent`, `MarkStepStopped` undefined.

- [ ] **Step 4: Write the service**

`internal/app/enrollment/service.go`:
```go
package enrollment

import (
	"context"
	"time"

	"github.com/google/uuid"
)

type Service struct{ store Store }

func NewService(store Store) *Service { return &Service{store: store} }

// Enroll materializes one enrollment per list member; returns the new ids.
func (s *Service) Enroll(ctx context.Context, ws, campaignID uuid.UUID) ([]uuid.UUID, error) {
	return s.store.Enroll(ctx, ws, campaignID)
}

// MarkStepSent is the single insertion point for the current_step transition
// and cadence. currentStep is the step just sent; nextDueAt is when the next
// step should fire; lastStep true ⇒ the enrollment completes.
func (s *Service) MarkStepSent(ctx context.Context, ws, id uuid.UUID, currentStep int32, nextDueAt time.Time, lastStep bool) error {
	status := StatusActive
	if lastStep {
		status = StatusCompleted
	}
	return s.store.Advance(ctx, ws, id, currentStep, nextDueAt, status)
}

// MarkStepStopped is the single entry point for halting an enrollment. Reply
// and bounce consumers (deferred) call it with their reason.
func (s *Service) MarkStepStopped(ctx context.Context, ws, id uuid.UUID, reason StopReason) error {
	return s.store.Stop(ctx, ws, id, reason)
}

func (s *Service) CountByStatus(ctx context.Context, ws, campaignID uuid.UUID) (map[string]int64, error) {
	return s.store.CountByStatus(ctx, ws, campaignID)
}
```

- [ ] **Step 5: Run test to verify it passes + build**

Run:
```bash
export PATH="$PATH:/c/Program Files/Go/bin:$HOME/go/bin"
go test ./internal/app/enrollment/ -v && go build ./...
```
Expected: PASS; build passes.

- [ ] **Step 6: Commit**

```bash
git add internal/app/enrollment/
git commit -m "feat(enrollment): store + MarkStepSent/MarkStepStopped service"
```

---

## Task 6: `coreapi` `GetStepSendJob`/`MarkStepSent`/`MarkStepStopped`/`ListDueEnrollments`

**Files:**
- Modify: `internal/coreapi/coreapi.go` (add methods + types)
- Create: `internal/coreapi/inprocess/stepsendjob.go`
- Modify: `internal/coreapi/inprocess/inprocess.go` if `client` needs the enrollment store handle (see Step 1)
- Test: `internal/coreapi/inprocess/stepsendjob_test.go` (cadence + threading helpers, no DB)

**Interfaces:**
- Consumes: `enrollment.Service` (Task 5), `gen` step/send methods (Task 1), `crypto.Sealer`, `unsub.MakeToken`, `personalize.Vars` is NOT used here (personalization stays worker-side — coreapi returns raw templates + Vars).
- Produces (added to `coreapi.Client`):
  ```go
  GetStepSendJob(ctx context.Context, enrollmentID, workspaceID string) (StepSendJob, error)
  MarkStepSent(ctx context.Context, enrollmentID, workspaceID string, res StepResult) (Advance, error)
  MarkStepStopped(ctx context.Context, enrollmentID, workspaceID, reason string) error
  ListDueEnrollments(ctx context.Context) ([]DueEnrollment, error)
  ```
  Structs `StepSendJob`, `StepResult`, `Advance`, `ContactVars`, `DueEnrollment` per spec §6.

- [ ] **Step 1: Read the inprocess client struct to learn how deps are held**

Run:
```bash
export PATH="$PATH:/c/Program Files/Go/bin:$HOME/go/bin"
sed -n '1,80p' internal/coreapi/inprocess/inprocess.go
```
Expected: see `client` fields (`q`, `sealer`, `jwtSecret`, `publicURL`) and `New(...)`. If enrollment transitions must run through `enrollment.Service`, thread an `enrollment.Store` (or call `q` directly — the inprocess layer already calls `gen.Queries` directly, so prefer that for consistency and to avoid an app→app import).

- [ ] **Step 2: Add the coreapi types + interface methods**

In `internal/coreapi/coreapi.go`, add to the `Client` interface the four methods above, and add:
```go
type ContactVars struct {
	FirstName, LastName, Email, Company string
	Custom                              map[string]string
}

// StepSendJob is everything the sequence:advance worker needs to send one
// step-email. Personalization is applied worker-side from Vars + the raw
// templates. SMTPPassword is []byte for in-memory zeroization.
type StepSendJob struct {
	EnrollmentID      string
	WorkspaceID       string
	StepOrder         int
	StepID            string
	LastStep          bool
	Suppressed        bool
	EffectiveDailyCap int
	SentToday         int
	ToEmail           string
	Vars              ContactVars
	Subject           string // raw template for this step; "" ⇒ reply-in-thread
	ThreadSubject     string // step-1 subject, for "Re: ..."
	BodyText          string
	BodyHTML          string
	UnsubURL          string
	InReplyTo         string
	References        string
	WaitDaysNext      int // wait_days of the following step; 0 if LastStep
	FromEmail         string
	FromName          string
	SMTPHost          string
	SMTPPort          int
	SMTPUsername      string
	SMTPPassword      []byte
	UseTLS            bool
}

type StepResult struct {
	Status    string // "sent" | "failed"
	MessageID string
	Err       string
}

// Advance tells the worker whether the enrollment finished and, if not, when
// the next step is due.
type Advance struct {
	Completed bool
	NextDueAt time.Time
}

type DueEnrollment struct {
	EnrollmentID string
	WorkspaceID  string
}
```
Add `"time"` to the imports.

- [ ] **Step 3: Write cadence + reply-subject helper tests (no DB)**

`internal/coreapi/inprocess/stepsendjob_test.go`:
```go
package inprocess

import "testing"

func TestReplySubjectUsesThreadSubjectWhenEmpty(t *testing.T) {
	if got := replySubject("", "Intro"); got != "Re: Intro" {
		t.Fatalf("got %q", got)
	}
}

func TestReplySubjectKeepsOwnSubject(t *testing.T) {
	if got := replySubject("Following up", "Intro"); got != "Following up" {
		t.Fatalf("got %q", got)
	}
}

func TestReplySubjectNoDoubleRe(t *testing.T) {
	if got := replySubject("", "Re: Intro"); got != "Re: Intro" {
		t.Fatalf("got %q", got)
	}
}
```

- [ ] **Step 4: Run test to verify it fails**

Run:
```bash
export PATH="$PATH:/c/Program Files/Go/bin:$HOME/go/bin"
go test ./internal/coreapi/inprocess/ -run TestReplySubject -v
```
Expected: FAIL — `replySubject` undefined.

- [ ] **Step 5: Implement `stepsendjob.go`**

`internal/coreapi/inprocess/stepsendjob.go`:
```go
package inprocess

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/platform/db/gen"
	"github.com/inroad/inroad/internal/platform/unsub"
)

// replySubject resolves the subject line for a step. An empty step subject
// means "reply in the thread": prefix the thread's first-step subject with
// "Re: " (idempotently). A non-empty subject is used verbatim.
func replySubject(stepSubject, threadSubject string) string {
	if stepSubject != "" {
		return stepSubject
	}
	if strings.HasPrefix(threadSubject, "Re: ") {
		return threadSubject
	}
	return "Re: " + threadSubject
}

// GetStepSendJob resolves current_step+1, joins the step/campaign/contact/
// mailbox rows, decrypts the SMTP secret, checks suppression + cap, and builds
// threading headers from the prior sent step. workspaceID is pinned in the SQL
// WHERE (defense in depth on top of the unguessable enrollment UUID).
func (c client) GetStepSendJob(ctx context.Context, enrollmentID, workspaceID string) (coreapi.StepSendJob, error) {
	eid, err := uuid.Parse(enrollmentID)
	if err != nil {
		return coreapi.StepSendJob{}, err
	}
	ws, err := uuid.Parse(workspaceID)
	if err != nil {
		return coreapi.StepSendJob{}, err
	}
	b, err := c.q.GetStepSendBundle(ctx, gen.GetStepSendBundleParams{ID: eid, WorkspaceID: ws})
	if err != nil {
		return coreapi.StepSendJob{}, err
	}
	if b.WorkspaceID != ws {
		return coreapi.StepSendJob{}, coreapi.ErrCrossTenant
	}
	nextOrder := b.CurrentStep + 1

	// Resolve the step to send and the total step count (for LastStep).
	step, err := c.q.GetStepByOrder(ctx, gen.GetStepByOrderParams{
		CampaignID: b.CampaignID, WorkspaceID: ws, StepOrder: nextOrder,
	})
	if err != nil {
		return coreapi.StepSendJob{}, err
	}
	maxOrder, err := c.q.MaxStepOrder(ctx, gen.MaxStepOrderParams{CampaignID: b.CampaignID, WorkspaceID: ws})
	if err != nil {
		return coreapi.StepSendJob{}, err
	}
	lastStep := nextOrder >= maxOrder

	// Thread subject = step 1's subject.
	threadStep, err := c.q.GetStepByOrder(ctx, gen.GetStepByOrderParams{
		CampaignID: b.CampaignID, WorkspaceID: ws, StepOrder: 1,
	})
	if err != nil {
		return coreapi.StepSendJob{}, err
	}

	// Threading headers from the prior sent step (empty for step 1).
	var inReplyTo, references string
	if nextOrder > 1 {
		prior, perr := c.q.GetPriorStepMessage(ctx, gen.GetPriorStepMessageParams{
			CampaignID: b.CampaignID, ContactID: b.ContactID, StepOrder: nextOrder,
		})
		if perr == nil {
			inReplyTo = prior.MessageID
			references = strings.TrimSpace(prior.ReferencesHdr + " " + prior.MessageID)
		}
	}

	password, err := c.sealer.Open(b.SecretCiphertext)
	if err != nil {
		return coreapi.StepSendJob{}, err
	}
	suppressed, err := c.q.IsSuppressed(ctx, gen.IsSuppressedParams{WorkspaceID: ws, Lower: b.ToEmail})
	if err != nil {
		return coreapi.StepSendJob{}, err
	}
	sentToday, err := c.q.CountSentToday(ctx, b.MailboxID)
	if err != nil {
		return coreapi.StepSendJob{}, err
	}
	ageDays := int(time.Since(b.MailboxCreatedAt.Time).Hours() / 24)
	cap := effectiveCap(int(b.DailyCap), int(b.RampStartCap), int(b.RampDays), b.RampEnabled, ageDays)
	token := unsub.MakeToken(c.jwtSecret, ws.String(), b.ToEmail)

	// wait_days of the FOLLOWING step, for cadence after this send.
	var waitNext int32
	if !lastStep {
		nextStep, nerr := c.q.GetStepByOrder(ctx, gen.GetStepByOrderParams{
			CampaignID: b.CampaignID, WorkspaceID: ws, StepOrder: nextOrder + 1,
		})
		if nerr == nil {
			waitNext = nextStep.WaitDays
		}
	}

	return coreapi.StepSendJob{
		EnrollmentID: enrollmentID, WorkspaceID: ws.String(),
		StepOrder: int(nextOrder), StepID: step.ID.String(), LastStep: lastStep,
		Suppressed: suppressed, EffectiveDailyCap: cap, SentToday: int(sentToday),
		ToEmail: b.ToEmail,
		Vars: coreapi.ContactVars{
			FirstName: b.FirstName, LastName: b.LastName, Email: b.ToEmail,
			Company: b.Company, Custom: decodeCustom(b.CustomFields),
		},
		Subject: step.Subject, ThreadSubject: threadStep.Subject,
		BodyText: step.BodyText, BodyHTML: step.BodyHtml,
		UnsubURL: c.publicURL + "/u/" + token, InReplyTo: inReplyTo, References: references,
		WaitDaysNext: int(waitNext),
		FromEmail:    b.FromEmail, FromName: b.FromName, SMTPHost: b.SmtpHost, SMTPPort: int(b.SmtpPort),
		SMTPUsername: b.SmtpUsername, SMTPPassword: password, UseTLS: b.UseTls,
	}, nil
}

// MarkStepSent writes the send row result and advances the enrollment cursor
// (or completes it) in one logical step. It computes next_due_at = now +
// WaitDaysNext when more steps remain. Returns Advance so the worker knows
// whether/when to schedule the next sequence:advance.
func (c client) MarkStepSent(ctx context.Context, enrollmentID, workspaceID string, res coreapi.StepResult) (coreapi.Advance, error) {
	eid, err := uuid.Parse(enrollmentID)
	if err != nil {
		return coreapi.Advance{}, err
	}
	ws, err := uuid.Parse(workspaceID)
	if err != nil {
		return coreapi.Advance{}, err
	}
	// The send row is inserted by the worker's InsertStepSend call BEFORE the
	// SMTP attempt (Task 9); here we set its result and advance the cursor.
	// Re-derive the sent step + whether it was the last from the enrollment.
	e, err := c.q.GetEnrollment(ctx, gen.GetEnrollmentParams{ID: eid, WorkspaceID: ws})
	if err != nil {
		return coreapi.Advance{}, err
	}
	sentOrder := e.CurrentStep + 1
	maxOrder, err := c.q.MaxStepOrder(ctx, gen.MaxStepOrderParams{CampaignID: e.CampaignID, WorkspaceID: ws})
	if err != nil {
		return coreapi.Advance{}, err
	}
	lastStep := sentOrder >= maxOrder

	if err := c.q.SetStepSendResult(ctx, gen.SetStepSendResultParams{
		CampaignID: e.CampaignID, ContactID: e.ContactID, StepOrder: sentOrder,
		Status: res.Status, MessageID: res.MessageID, Error: res.Err, WorkspaceID: ws,
	}); err != nil {
		return coreapi.Advance{}, err
	}

	var nextDue time.Time
	status := "active"
	if lastStep {
		status = "completed"
	} else {
		nextStep, nerr := c.q.GetStepByOrder(ctx, gen.GetStepByOrderParams{
			CampaignID: e.CampaignID, WorkspaceID: ws, StepOrder: sentOrder + 1,
		})
		wait := int32(0)
		if nerr == nil {
			wait = nextStep.WaitDays
		}
		nextDue = time.Now().AddDate(0, 0, int(wait))
	}
	due := gen.AdvanceEnrollmentParams{
		ID: eid, WorkspaceID: ws, CurrentStep: sentOrder, Status: status,
	}
	if status == "active" {
		due.NextDueAt = tsz(nextDue)
	}
	if err := c.q.AdvanceEnrollment(ctx, due); err != nil {
		return coreapi.Advance{}, err
	}
	return coreapi.Advance{Completed: lastStep, NextDueAt: nextDue}, nil
}

func (c client) MarkStepStopped(ctx context.Context, enrollmentID, workspaceID, reason string) error {
	eid, err := uuid.Parse(enrollmentID)
	if err != nil {
		return err
	}
	ws, err := uuid.Parse(workspaceID)
	if err != nil {
		return err
	}
	return c.q.StopEnrollment(ctx, gen.StopEnrollmentParams{ID: eid, WorkspaceID: ws, StopReason: reason})
}

func (c client) ListDueEnrollments(ctx context.Context) ([]coreapi.DueEnrollment, error) {
	rows, err := c.q.ListDueEnrollments(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]coreapi.DueEnrollment, len(rows))
	for i, r := range rows {
		out[i] = coreapi.DueEnrollment{EnrollmentID: r.ID.String(), WorkspaceID: r.WorkspaceID.String()}
	}
	return out, nil
}
```

> **Note for the implementer:** `SetStepSendResult` (a workspace-pinned `UPDATE sends SET status/message_id/error/sent_at WHERE campaign_id=$ AND contact_id=$ AND step_order=$ AND workspace_id=$`), `decodeCustom(jsonb) map[string]string`, and `tsz(time.Time) pgtype.Timestamptz` are tiny helpers you add alongside this file (add the query to `send.sql` and regen; put `decodeCustom`/`tsz` in this file). Add their trivial unit coverage to the same test file.

- [ ] **Step 6: Add `SetStepSendResult` query, regen, run tests + build**

Add to `queries/send.sql`:
```sql
-- name: SetStepSendResult :exec
UPDATE sends SET status = $4, message_id = $5, error = $6,
       sent_at = CASE WHEN $4 = 'sent' THEN now() ELSE sent_at END
WHERE campaign_id = $1 AND contact_id = $2 AND step_order = $3 AND workspace_id = $7;
```
Run:
```bash
export PATH="$PATH:/c/Program Files/Go/bin:$HOME/go/bin"
make sqlc && go test ./internal/coreapi/... -v && go build ./...
```
Expected: `replySubject` tests PASS; build passes (all interface methods implemented — if `go build` complains the `client` doesn't satisfy `coreapi.Client`, a method signature is off; fix to match Step 2).

- [ ] **Step 7: Commit**

```bash
git add internal/coreapi/ internal/platform/db/queries/send.sql internal/platform/db/gen/
git commit -m "feat(coreapi): GetStepSendJob/MarkStepSent/MarkStepStopped/ListDueEnrollments"
```

---

## Task 7: Queue tasks + enqueue helpers + scheduler registration

**Files:**
- Modify: `internal/platform/queue/queue.go`
- Test: `internal/platform/queue/queue_test.go` (add payload round-trip)

**Interfaces:**
- Produces: `TaskSequenceAdvance = "sequence:advance"`, `TaskSweepEnrollments = "sequence:sweep_stuck_enrollments"`; `AdvancePayload{EnrollmentID, WorkspaceID}`; `EnqueueAdvance(enrollmentID, ws string) error`, `EnqueueAdvanceAt(enrollmentID, ws string, t time.Time) error`, `EnqueueAdvanceIn(enrollmentID, ws string, d time.Duration) error`; `RegisterSweepEnrollments(sch) error`. Consumed by Tasks 8, 9, 10.

- [ ] **Step 1: Write the failing payload test**

`internal/platform/queue/queue_test.go` (add):
```go
func TestAdvancePayloadRoundTrip(t *testing.T) {
	b, err := json.Marshal(AdvancePayload{EnrollmentID: "e1", WorkspaceID: "w1"})
	if err != nil {
		t.Fatal(err)
	}
	var p AdvancePayload
	if err := json.Unmarshal(b, &p); err != nil {
		t.Fatal(err)
	}
	if p.EnrollmentID != "e1" || p.WorkspaceID != "w1" {
		t.Fatalf("round-trip mismatch: %+v", p)
	}
}
```
(Add `"encoding/json"` to the test imports if absent.)

- [ ] **Step 2: Run test to verify it fails**

Run:
```bash
export PATH="$PATH:/c/Program Files/Go/bin:$HOME/go/bin"
go test ./internal/platform/queue/ -run TestAdvancePayload -v
```
Expected: FAIL — `AdvancePayload` undefined.

- [ ] **Step 3: Add tasks, payloads, helpers, registration**

In `internal/platform/queue/queue.go`:
```go
const TaskSequenceAdvance = "sequence:advance"

// AdvancePayload is the body of a sequence:advance task. WorkspaceID travels
// alongside EnrollmentID so the worker can pin workspace_id in its DB lookups.
type AdvancePayload struct {
	EnrollmentID string `json:"enrollment_id"`
	WorkspaceID  string `json:"workspace_id"`
}

// TaskSweepEnrollments is the periodic reconcile for enrollments whose
// next_due_at passed without a live advance task. Scheduled @every 5m.
const TaskSweepEnrollments = "sequence:sweep_stuck_enrollments"

func (c *Client) EnqueueAdvance(enrollmentID, workspaceID string) error {
	b, err := json.Marshal(AdvancePayload{EnrollmentID: enrollmentID, WorkspaceID: workspaceID})
	if err != nil {
		return err
	}
	_, err = c.inner.Enqueue(asynq.NewTask(TaskSequenceAdvance, b))
	return err
}

func (c *Client) EnqueueAdvanceAt(enrollmentID, workspaceID string, t time.Time) error {
	b, err := json.Marshal(AdvancePayload{EnrollmentID: enrollmentID, WorkspaceID: workspaceID})
	if err != nil {
		return err
	}
	_, err = c.inner.Enqueue(asynq.NewTask(TaskSequenceAdvance, b), asynq.ProcessAt(t))
	return err
}

func (c *Client) EnqueueAdvanceIn(enrollmentID, workspaceID string, d time.Duration) error {
	b, err := json.Marshal(AdvancePayload{EnrollmentID: enrollmentID, WorkspaceID: workspaceID})
	if err != nil {
		return err
	}
	_, err = c.inner.Enqueue(asynq.NewTask(TaskSequenceAdvance, b), asynq.ProcessIn(d))
	return err
}

// RegisterSweepEnrollments registers the periodic due-enrollment sweep.
func RegisterSweepEnrollments(sch *asynq.Scheduler) error {
	_, err := sch.Register("@every 5m", asynq.NewTask(TaskSweepEnrollments, nil))
	return err
}
```

- [ ] **Step 4: Run test to verify it passes + build**

Run:
```bash
export PATH="$PATH:/c/Program Files/Go/bin:$HOME/go/bin"
go test ./internal/platform/queue/ -v && go build ./...
```
Expected: PASS; build passes.

- [ ] **Step 5: Commit**

```bash
git add internal/platform/queue/
git commit -m "feat(queue): sequence:advance + sweep_stuck_enrollments tasks"
```

---

## Task 8: Refactor `campaign.Launch` to enroll + stagger + enqueue advance

**Files:**
- Modify: `internal/app/campaign/store.go` (add `EnrollTx`), `service.go` (Launch uses enrollment path), `handler.go` (response unchanged shape)
- Test: `internal/app/campaign/service_test.go` (update fake, add stagger/enqueue assertions)

**Interfaces:**
- Consumes: `enrollment` enqueue via a new `SequenceEnqueuer` interface `EnqueueAdvanceAt(id, ws string, t time.Time) error` (satisfied by `*queue.Client`), and a store method that enrolls + flips status atomically.
- Produces: `campaign.Service.Launch` now creates enrollments (not per-send rows). `LaunchResult` keeps `TotalSends`→rename semantics to `TotalEnrolled` (add field, keep `TotalSends` alias for the handler's existing JSON contract, see Step 4).

- [ ] **Step 1: Add `EnrollTx` to the campaign store**

In `internal/app/campaign/store.go`, add to the `Store` interface and `PgStore`:
```go
// EnrollTx materializes one sequence_enrollment per list member AND flips the
// campaign to running, atomically. Returns the new enrollment ids. Requires
// the campaign to have >=1 step (enforced by the service before calling).
EnrollTx(ctx context.Context, ws, campaignID uuid.UUID) ([]uuid.UUID, error)
```
```go
func (s *PgStore) EnrollTx(ctx context.Context, ws, campaignID uuid.UUID) ([]uuid.UUID, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	qtx := s.q.WithTx(tx)
	ids, err := qtx.EnrollListMembers(ctx, gen.EnrollListMembersParams{ID: campaignID, WorkspaceID: ws})
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, nil
	}
	if err := qtx.SetCampaignStatus(ctx, gen.SetCampaignStatusParams{
		ID: campaignID, WorkspaceID: ws, Status: string(StatusRunning),
		LaunchedAt: pgtype.Timestamptz{Time: time.Now(), Valid: true},
	}); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return ids, nil
}
```

- [ ] **Step 2: Add a step-count guard + update the failing test**

The service must reject launch when the campaign has no steps (backward-compat: migration 000007 backfilled step 1 for pre-existing campaigns, and `POST /campaigns` callers who never added steps still have the backfilled step only if the campaign predates 000007 — NEW campaigns created after 000007 with no explicit steps need step-1 auto-materialization). Add to `campaign.Store`:
```go
CountSteps(ctx context.Context, ws, campaignID uuid.UUID) (int64, error)
EnsureStep1FromCampaign(ctx context.Context, ws, campaignID uuid.UUID) error
```
`EnsureStep1FromCampaign` runs `INSERT INTO sequence_steps (...) SELECT ... FROM campaigns WHERE id=$1 AND workspace_id=$2 ON CONFLICT (campaign_id, step_order) DO NOTHING` for `step_order=1` (add query `EnsureStep1` to `campaign.sql`). This makes the old `POST /campaigns`→launch path (no explicit steps) still produce a one-step sequence.

Update `service_test.go`'s `fakeStore` to implement the new methods (`CountSteps` returns e.g. 1; `EnsureStep1FromCampaign` returns nil; rename `LaunchTx`→`EnrollTx` in the fake). Add:
```go
func TestLaunchEnrollsAndStaggers(t *testing.T) {
	ids := []uuid.UUID{uuid.New(), uuid.New()}
	store := &fakeStore{status: string(StatusDraft), sendIDs: ids, steps: 2}
	enq := &fakeSeqEnqueuer{}
	svc := NewService(store, okChecker{active: true})
	res, err := svc.Launch(context.Background(), uuid.New(), uuid.New(), enq)
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	if res.TotalEnrolled != 2 || res.EnqueuedCount != 2 {
		t.Fatalf("counts: %+v", res)
	}
	// Stagger: the two advance tasks must be scheduled at increasing times.
	if !enq.times[1].After(enq.times[0]) {
		t.Fatalf("expected staggered schedule, got %v", enq.times)
	}
}
```
with a `fakeSeqEnqueuer` recording `times []time.Time`.

- [ ] **Step 3: Run test to verify it fails**

Run:
```bash
export PATH="$PATH:/c/Program Files/Go/bin:$HOME/go/bin"
go test ./internal/app/campaign/ -run TestLaunchEnrolls -v
```
Expected: FAIL — `TotalEnrolled`, `EnrollTx`, `SequenceEnqueuer` undefined.

- [ ] **Step 4: Rewrite `Launch` for the enrollment path**

In `internal/app/campaign/service.go`, replace `Enqueuer` usage in `Launch` with:
```go
// SequenceEnqueuer schedules a sequence:advance task at a specific time.
// Satisfied by *queue.Client (EnqueueAdvanceAt).
type SequenceEnqueuer interface {
	EnqueueAdvanceAt(enrollmentID, workspaceID string, t time.Time) error
}

// staggerInterval spreads step-1 sends so a launch of N contacts doesn't burst
// the mailbox. Kept small; ramp/cap gating in the worker does the heavy pacing.
const staggerInterval = 2 * time.Second

type LaunchResult struct {
	TotalEnrolled      int
	EnqueuedCount      int
	FailedEnqueueCount int
}

func (s *Service) Launch(ctx context.Context, ws, campaignID uuid.UUID, enq SequenceEnqueuer) (LaunchResult, error) {
	c, err := s.store.Get(ctx, ws, campaignID)
	if err != nil {
		return LaunchResult{}, ErrNotFound
	}
	if c.Status != string(StatusDraft) {
		return LaunchResult{}, ErrAlreadyLaunched
	}
	// Backward compat: a campaign with no explicit steps gets step 1 from its
	// inline subject/body so the old POST /campaigns→launch flow still works.
	n, err := s.store.CountSteps(ctx, ws, campaignID)
	if err != nil {
		return LaunchResult{}, err
	}
	if n == 0 {
		if err := s.store.EnsureStep1FromCampaign(ctx, ws, campaignID); err != nil {
			return LaunchResult{}, err
		}
	}
	ids, err := s.store.EnrollTx(ctx, ws, campaignID)
	if err != nil {
		return LaunchResult{}, err
	}
	if len(ids) == 0 {
		return LaunchResult{}, ErrEmptyList
	}
	res := LaunchResult{TotalEnrolled: len(ids)}
	now := time.Now()
	for i, id := range ids {
		at := now.Add(time.Duration(i) * staggerInterval)
		if err := enq.EnqueueAdvanceAt(id.String(), ws.String(), at); err != nil {
			res.FailedEnqueueCount++
			continue
		}
		res.EnqueuedCount++
	}
	return res, nil
}
```
Add `"time"` to imports. Update `handler.go`'s `launch` JSON to emit `total_enrolled` (keep `queued`, `failed_enqueue_count`); update the handler's `h.enq` field type to `SequenceEnqueuer` and the `NewHandler` signature accordingly. Update `cmd/inroad/main.go` wiring in Task 12.

- [ ] **Step 5: Add the `EnsureStep1` query, regen, run tests**

Add to `queries/campaign.sql`:
```sql
-- name: CountStepsForCampaign :one
SELECT count(*) FROM sequence_steps WHERE campaign_id = $1 AND workspace_id = $2;
-- name: EnsureStep1 :exec
INSERT INTO sequence_steps (workspace_id, campaign_id, step_order, wait_days, subject, body_text, body_html)
SELECT workspace_id, id, 1, 0, subject, body_text, body_html FROM campaigns
WHERE id = $1 AND workspace_id = $2
ON CONFLICT (campaign_id, step_order) DO NOTHING;
```
Wire these into `PgStore.CountSteps`/`EnsureStep1FromCampaign`. Run:
```bash
export PATH="$PATH:/c/Program Files/Go/bin:$HOME/go/bin"
make sqlc && go test ./internal/app/campaign/ -v && go build ./...
```
Expected: PASS; build passes.

- [ ] **Step 6: Commit**

```bash
git add internal/app/campaign/ internal/platform/db/queries/campaign.sql internal/platform/db/gen/
git commit -m "feat(campaign): launch enrolls contacts + staggers sequence:advance"
```

---

## Task 9: Worker `sequence:advance` handler

**Files:**
- Create: `internal/worker/sequence/advance.go`
- Test: `internal/worker/sequence/advance_test.go`

**Interfaces:**
- Consumes: `coreapi.Client` (Task 6), `sender.Sender` (reuse the interface from `internal/worker/sender`), `personalize` (Task 3), `mail.Message` threading fields (Task 4), `queue.AdvancePayload` + `EnqueueAdvanceAt`/`EnqueueAdvanceIn` (Task 7).
- Produces: `sequence.AdvanceHandler(core, sender, enq) func(context.Context, *asynq.Task) error`. The worker must `InsertStepSend` before the SMTP attempt — expose it via a coreapi method `EnsureStepSend(ctx, enrollmentID, ws) (sendID string, err error)` OR fold the insert into `GetStepSendJob` (recommended: fold it in — `GetStepSendJob` inserts the queued `sends` row for the resolved step via `InsertStepSend` and returns; `MarkStepSent` then updates it). **Adopt the fold-in**: add the `InsertStepSend` call to `GetStepSendJob` in Task 6 Step 5 (insert the row for `nextOrder` with threading headers before returning).

- [ ] **Step 1: Write the failing handler test (fake coreapi + fake sender)**

`internal/worker/sequence/advance_test.go`:
```go
package sequence

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/hibiken/asynq"

	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/platform/mail"
	"github.com/inroad/inroad/internal/platform/queue"
)

type fakeCore struct {
	job          coreapi.StepSendJob
	marked       coreapi.StepResult
	advance      coreapi.Advance
	stoppedWith  string
}

func (f *fakeCore) GetStepSendJob(context.Context, string, string) (coreapi.StepSendJob, error) {
	return f.job, nil
}
func (f *fakeCore) MarkStepSent(_ context.Context, _, _ string, res coreapi.StepResult) (coreapi.Advance, error) {
	f.marked = res
	return f.advance, nil
}
func (f *fakeCore) MarkStepStopped(_ context.Context, _, _, reason string) error {
	f.stoppedWith = reason
	return nil
}
func (f *fakeCore) ListDueEnrollments(context.Context) ([]coreapi.DueEnrollment, error) { return nil, nil }

// embed the rest of coreapi.Client with no-ops via a helper (see stubCore).

type fakeSender struct {
	sent mail.Message
	id   string
}

func (f *fakeSender) Send(_ mail.SMTPConfig, m mail.Message) (string, error) {
	f.sent = m
	return f.id, nil
}

type fakeEnq struct{ scheduledAt time.Time }

func (f *fakeEnq) EnqueueAdvanceAt(_, _ string, t time.Time) error { f.scheduledAt = t; return nil }
func (f *fakeEnq) EnqueueAdvanceIn(_, _ string, _ time.Duration) error { return nil }

func task(t *testing.T, p queue.AdvancePayload) *asynq.Task {
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	return asynq.NewTask(queue.TaskSequenceAdvance, b)
}

func TestAdvanceSuppressedStops(t *testing.T) {
	core := &stubCore{fakeCore: fakeCore{job: coreapi.StepSendJob{Suppressed: true}}}
	h := AdvanceHandler(core, &fakeSender{}, &fakeEnq{})
	if err := h(context.Background(), task(t, queue.AdvancePayload{EnrollmentID: "e", WorkspaceID: "w"})); err != nil {
		t.Fatal(err)
	}
	if core.stoppedWith != "unsubscribed" {
		t.Fatalf("want stop unsubscribed, got %q", core.stoppedWith)
	}
}

func TestAdvanceSendsAndSchedulesNext(t *testing.T) {
	next := time.Now().Add(48 * time.Hour)
	core := &stubCore{fakeCore: fakeCore{
		job: coreapi.StepSendJob{ToEmail: "a@b.io", Subject: "Hi", BodyText: "yo",
			EffectiveDailyCap: 100, SentToday: 0, StepOrder: 1, ThreadSubject: "Hi"},
		advance: coreapi.Advance{Completed: false, NextDueAt: next},
	}}
	enq := &fakeEnq{}
	h := AdvanceHandler(core, &fakeSender{id: "<mid@x>"}, enq)
	if err := h(context.Background(), task(t, queue.AdvancePayload{EnrollmentID: "e", WorkspaceID: "w"})); err != nil {
		t.Fatal(err)
	}
	if core.marked.Status != "sent" || core.marked.MessageID != "<mid@x>" {
		t.Fatalf("marked wrong: %+v", core.marked)
	}
	if !enq.scheduledAt.Equal(next) {
		t.Fatalf("next advance not scheduled at NextDueAt: %v vs %v", enq.scheduledAt, next)
	}
}

func TestAdvanceCompletedDoesNotReschedule(t *testing.T) {
	core := &stubCore{fakeCore: fakeCore{
		job:     coreapi.StepSendJob{ToEmail: "a@b.io", Subject: "Bye", BodyText: "end", EffectiveDailyCap: 100},
		advance: coreapi.Advance{Completed: true},
	}}
	enq := &fakeEnq{}
	h := AdvanceHandler(core, &fakeSender{id: "<m>"}, enq)
	if err := h(context.Background(), task(t, queue.AdvancePayload{EnrollmentID: "e", WorkspaceID: "w"})); err != nil {
		t.Fatal(err)
	}
	if !enq.scheduledAt.IsZero() {
		t.Fatalf("completed enrollment must not reschedule, got %v", enq.scheduledAt)
	}
}
```

> **Implementer note:** `stubCore` embeds `fakeCore` and adds no-op implementations of the remaining `coreapi.Client` methods (`MailboxExists`, `GetSendJob`, `MarkSend`, `ListStuckQueuedSends`, `IncrementSendAttempts`) so it satisfies the interface. Define it at the bottom of the test file.

- [ ] **Step 2: Run test to verify it fails**

Run:
```bash
export PATH="$PATH:/c/Program Files/Go/bin:$HOME/go/bin"
go test ./internal/worker/sequence/ -v
```
Expected: FAIL — `AdvanceHandler` undefined.

- [ ] **Step 3: Write the handler**

`internal/worker/sequence/advance.go`:
```go
// Package sequence is the execution-plane multi-step sequencing engine.
package sequence

import (
	"context"
	"encoding/json"
	"time"

	"github.com/hibiken/asynq"

	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/platform/mail"
	"github.com/inroad/inroad/internal/platform/queue"
	"github.com/inroad/inroad/internal/worker/personalize"
)

// Sender sends one email over SMTP (same contract as the direct sender).
type Sender interface {
	Send(cfg mail.SMTPConfig, msg mail.Message) (messageID string, err error)
}

// Enqueuer schedules the next advance. Satisfied by *queue.Client.
type Enqueuer interface {
	EnqueueAdvanceAt(enrollmentID, workspaceID string, t time.Time) error
	EnqueueAdvanceIn(enrollmentID, workspaceID string, d time.Duration) error
}

// capBackoff is how long to wait before retrying an enrollment blocked by the
// mailbox's daily cap (matches the direct sender's 6h re-enqueue).
const capBackoff = 6 * time.Hour

// AdvanceHandler returns an asynq handler for sequence:advance tasks. It owns
// the full step lifecycle: resolve+fetch the due step, personalize, build a
// threaded MIME message, send over SMTP, record the result, and schedule the
// next step (or complete).
func AdvanceHandler(core coreapi.Client, sender Sender, enq Enqueuer) func(context.Context, *asynq.Task) error {
	return func(ctx context.Context, t *asynq.Task) error {
		var p queue.AdvancePayload
		if err := json.Unmarshal(t.Payload(), &p); err != nil {
			return err
		}
		job, err := core.GetStepSendJob(ctx, p.EnrollmentID, p.WorkspaceID)
		if err != nil {
			return err
		}
		defer zeroize(job.SMTPPassword)

		if job.Suppressed {
			return core.MarkStepStopped(ctx, p.EnrollmentID, p.WorkspaceID, string(coreapi.StopReasonUnsubscribed))
		}
		if job.SentToday >= job.EffectiveDailyCap {
			// Over today's cap: retry this enrollment later; leave it active.
			return enq.EnqueueAdvanceIn(p.EnrollmentID, p.WorkspaceID, capBackoff)
		}

		vars := personalize.Vars{
			FirstName: job.Vars.FirstName, LastName: job.Vars.LastName,
			Email: job.Vars.Email, Company: job.Vars.Company, Custom: job.Vars.Custom,
		}
		subject := personalize.Text(subjectFor(job), vars)
		bodyText := withUnsubText(personalize.Text(job.BodyText, vars), job.UnsubURL)
		bodyHTML := ""
		if job.BodyHTML != "" {
			bodyHTML = withUnsubHTML(personalize.HTML(job.BodyHTML, vars), job.UnsubURL)
		}

		msgID, sendErr := sender.Send(
			mail.SMTPConfig{Host: job.SMTPHost, Port: job.SMTPPort, Username: job.SMTPUsername, Password: string(job.SMTPPassword), UseTLS: job.UseTLS},
			mail.Message{
				FromEmail: job.FromEmail, FromName: job.FromName, To: job.ToEmail,
				Subject: subject, BodyText: bodyText, BodyHTML: bodyHTML,
				ListUnsubscribe: job.UnsubURL, InReplyTo: job.InReplyTo, References: job.References,
			},
		)
		if sendErr != nil {
			// Record the failure; MarkStepSent still advances the cursor so a
			// hard-failing step doesn't wedge the enrollment forever.
			if _, err := core.MarkStepSent(ctx, p.EnrollmentID, p.WorkspaceID, coreapi.StepResult{Status: "failed", Err: sendErr.Error()}); err != nil {
				return err
			}
			return sendErr
		}
		adv, err := core.MarkStepSent(ctx, p.EnrollmentID, p.WorkspaceID, coreapi.StepResult{Status: "sent", MessageID: msgID})
		if err != nil {
			return err
		}
		if !adv.Completed {
			return enq.EnqueueAdvanceAt(p.EnrollmentID, p.WorkspaceID, adv.NextDueAt)
		}
		return nil
	}
}

// subjectFor resolves the subject line: an empty step subject means reply in
// the thread ("Re: <thread subject>"), matching coreapi.replySubject.
func subjectFor(job coreapi.StepSendJob) string {
	if job.Subject != "" {
		return job.Subject
	}
	if job.ThreadSubject == "" {
		return ""
	}
	if len(job.ThreadSubject) >= 4 && job.ThreadSubject[:4] == "Re: " {
		return job.ThreadSubject
	}
	return "Re: " + job.ThreadSubject
}

func zeroize(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

func withUnsubText(body, url string) string {
	if url == "" {
		return body
	}
	return body + "\n\n---\nUnsubscribe: " + url
}

func withUnsubHTML(body, url string) string {
	if url == "" {
		return body
	}
	return body + `<hr><p style="font-size:12px;color:#888"><a href="` + url + `">Unsubscribe</a></p>`
}
```

> **Implementer note:** add `StopReasonUnsubscribed StopReason = "unsubscribed"` (and the other reasons) as exported constants on `coreapi` in Task 6, OR pass the literal `"unsubscribed"`. Prefer the typed constant. Reconcile with the `enrollment.StopReason` enum — coreapi is worker-facing so it needs its own copy (app/* isolation); keep the string values identical.

- [ ] **Step 4: Run test to verify it passes + build**

Run:
```bash
export PATH="$PATH:/c/Program Files/Go/bin:$HOME/go/bin"
go test ./internal/worker/sequence/ -v && go build ./...
```
Expected: PASS; build passes.

- [ ] **Step 5: Commit**

```bash
git add internal/worker/sequence/advance.go internal/worker/sequence/advance_test.go internal/coreapi/coreapi.go
git commit -m "feat(worker): sequence:advance handler (send + advance/complete)"
```

---

## Task 10: `sequence:sweep_stuck_enrollments` handler + wiring

**Files:**
- Create: `internal/worker/sequence/sweeper.go`
- Test: `internal/worker/sequence/sweeper_test.go`
- Modify: `internal/worker/handlers.go`, `cmd/worker/main.go`

**Interfaces:**
- Consumes: `coreapi.ListDueEnrollments` (Task 6), `Enqueuer` (Task 9), `queue.RegisterSweepEnrollments` (Task 7).
- Produces: `sequence.SweepHandler(core, enq) func(context.Context, *asynq.Task) error`.

- [ ] **Step 1: Write the failing test**

`internal/worker/sequence/sweeper_test.go`:
```go
package sequence

import (
	"context"
	"testing"

	"github.com/hibiken/asynq"

	"github.com/inroad/inroad/internal/coreapi"
)

type sweepCore struct {
	stubCore
	due []coreapi.DueEnrollment
}

func (s *sweepCore) ListDueEnrollments(context.Context) ([]coreapi.DueEnrollment, error) {
	return s.due, nil
}

type recordEnq struct{ enrollmentIDs []string }

func (r *recordEnq) EnqueueAdvanceAt(id, _ string, _ /*time*/ interface{ }) error { return nil } // replaced below

func TestSweepReenqueuesDue(t *testing.T) {
	core := &sweepCore{due: []coreapi.DueEnrollment{{EnrollmentID: "e1", WorkspaceID: "w"}, {EnrollmentID: "e2", WorkspaceID: "w"}}}
	enq := &sweepEnq{}
	h := SweepHandler(core, enq)
	if err := h(context.Background(), asynq.NewTask("sequence:sweep_stuck_enrollments", nil)); err != nil {
		t.Fatal(err)
	}
	if len(enq.ids) != 2 {
		t.Fatalf("want 2 re-enqueued, got %d", len(enq.ids))
	}
}
```

> **Implementer note:** the `recordEnq` sketch above has a bad signature — define `sweepEnq` implementing the real `Enqueuer` (Task 9) instead:
> ```go
> type sweepEnq struct{ ids []string }
> func (s *sweepEnq) EnqueueAdvanceAt(id, _ string, _ time.Time) error { s.ids = append(s.ids, id); return nil }
> func (s *sweepEnq) EnqueueAdvanceIn(id, _ string, _ time.Duration) error { s.ids = append(s.ids, id); return nil }
> ```
> and add the `"time"` import. The sweeper re-enqueues immediately, so it calls `EnqueueAdvanceIn(id, ws, 0)` (or `EnqueueAdvanceAt` with now) — pick one and match the handler.

- [ ] **Step 2: Run test to verify it fails**

Run:
```bash
export PATH="$PATH:/c/Program Files/Go/bin:$HOME/go/bin"
go test ./internal/worker/sequence/ -run TestSweep -v
```
Expected: FAIL — `SweepHandler` undefined.

- [ ] **Step 3: Write the sweeper**

`internal/worker/sequence/sweeper.go`:
```go
package sequence

import (
	"context"
	"log/slog"

	"github.com/hibiken/asynq"

	"github.com/inroad/inroad/internal/coreapi"
)

// SweepHandler re-enqueues active enrollments whose next_due_at passed the
// reconcile window without a live advance task (launch committed the DB rows
// but Redis enqueue failed, or a scheduled task was lost). Idempotent: a
// duplicate advance is harmless — GetStepSendJob no-ops on a completed/stopped
// enrollment and InsertStepSend is ON CONFLICT.
func SweepHandler(core coreapi.Client, enq Enqueuer) func(context.Context, *asynq.Task) error {
	return func(ctx context.Context, _ *asynq.Task) error {
		rows, err := core.ListDueEnrollments(ctx)
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return nil
		}
		var failures int
		for _, row := range rows {
			if err := enq.EnqueueAdvanceIn(row.EnrollmentID, row.WorkspaceID, 0); err != nil {
				failures++
			}
		}
		slog.Info("sweep_stuck_enrollments", "candidates", len(rows), "reenqueue_failures", failures)
		return nil
	}
}
```

- [ ] **Step 4: Register the handler + scheduler**

`internal/worker/handlers.go` — add the sequence handlers:
```go
mux.HandleFunc(queue.TaskSequenceAdvance, sequence.AdvanceHandler(core, sndr, enq))
mux.HandleFunc(queue.TaskSweepEnrollments, sequence.SweepHandler(core, enq))
```
Add the import `"github.com/inroad/inroad/internal/worker/sequence"`. (`sndr *mail.NetSender` already satisfies both `sender.Sender` and `sequence.Sender`; `enq *queue.Client` satisfies `sequence.Enqueuer`.)

`cmd/worker/main.go` — register the periodic sweep next to `RegisterSweepStuck`:
```go
if err := queue.RegisterSweepEnrollments(sch); err != nil {
	logger.Error("scheduler register (enrollments) failed", "err", err)
	os.Exit(1)
}
```

- [ ] **Step 5: Run tests + build**

Run:
```bash
export PATH="$PATH:/c/Program Files/Go/bin:$HOME/go/bin"
go test ./internal/worker/... -v && go build ./...
```
Expected: PASS; build passes.

- [ ] **Step 6: Commit**

```bash
git add internal/worker/sequence/sweeper.go internal/worker/sequence/sweeper_test.go internal/worker/handlers.go cmd/worker/main.go
git commit -m "feat(worker): sequence sweep_stuck_enrollments periodic reconcile"
```

---

## Task 11: Extend `GET /campaigns/{id}` with steps + enrollment counts

**Files:**
- Modify: `internal/app/campaign/service.go` (add `Detail`), `handler.go` (get returns steps + counts), `routes.go` (response struct)
- Test: `internal/app/campaign/service_test.go`

**Interfaces:**
- Consumes: step list (via a `StepLister` interface `ListSteps(ctx, ws, campaignID) ([]gen.SequenceStep, error)`) and enrollment counts (via `EnrollmentCounter` interface `CountByStatus(ctx, ws, campaignID) (map[string]int64, error)`), injected into the campaign service at construction. Both satisfied by the respective PgStores.
- Produces: `campaignResponse` gains `Steps []stepView` and `Enrollments map[string]int64`.

- [ ] **Step 1: Write the failing test**

Add to `internal/app/campaign/service_test.go`:
```go
func TestDetailIncludesStepsAndCounts(t *testing.T) {
	store := &fakeStore{status: string(StatusRunning), campaigns: map[[2]uuid.UUID]gen.Campaign{}}
	ws, id := uuid.New(), uuid.New()
	store.campaigns[[2]uuid.UUID{ws, id}] = gen.Campaign{ID: id, WorkspaceID: ws, Name: "Q3", Status: "running"}
	svc := NewServiceWithDetail(store, okChecker{active: true},
		fakeStepLister{steps: []gen.SequenceStep{{StepOrder: 1}, {StepOrder: 2}}},
		fakeCounter{counts: map[string]int64{"active": 5, "completed": 1}})
	d, err := svc.Detail(context.Background(), ws, id)
	if err != nil {
		t.Fatalf("Detail: %v", err)
	}
	if len(d.Steps) != 2 || d.Enrollments["active"] != 5 {
		t.Fatalf("detail wrong: %+v", d)
	}
}
```
with `fakeStepLister`/`fakeCounter` fakes.

- [ ] **Step 2: Run test to verify it fails**

Run:
```bash
export PATH="$PATH:/c/Program Files/Go/bin:$HOME/go/bin"
go test ./internal/app/campaign/ -run TestDetail -v
```
Expected: FAIL — `NewServiceWithDetail`, `Detail` undefined.

- [ ] **Step 3: Implement `Detail`**

In `internal/app/campaign/service.go`:
```go
type StepLister interface {
	ListSteps(ctx context.Context, ws, campaignID uuid.UUID) ([]gen.SequenceStep, error)
}
type EnrollmentCounter interface {
	CountByStatus(ctx context.Context, ws, campaignID uuid.UUID) (map[string]int64, error)
}

// CampaignDetail is the extended GET payload.
type CampaignDetail struct {
	Campaign    gen.Campaign
	Steps       []gen.SequenceStep
	Enrollments map[string]int64
}

// Add steps/counters to Service (keep NewService for existing callers; add a
// richer constructor).
func NewServiceWithDetail(store Store, checker Checker, steps StepLister, counts EnrollmentCounter) *Service {
	s := NewService(store, checker)
	s.steps = steps
	s.counts = counts
	return s
}

func (s *Service) Detail(ctx context.Context, ws, id uuid.UUID) (CampaignDetail, error) {
	c, err := s.store.Get(ctx, ws, id)
	if err != nil {
		return CampaignDetail{}, ErrNotFound
	}
	steps, err := s.steps.ListSteps(ctx, ws, id)
	if err != nil {
		return CampaignDetail{}, err
	}
	counts, err := s.counts.CountByStatus(ctx, ws, id)
	if err != nil {
		return CampaignDetail{}, err
	}
	return CampaignDetail{Campaign: c, Steps: steps, Enrollments: counts}, nil
}
```
Add `steps StepLister` and `counts EnrollmentCounter` fields to `Service`. Add a `ListSteps` method to `sequencestep.PgStore` (thin wrapper over `List`) and `CountByStatus` already exists on `enrollment.PgStore`. Update `handler.go`'s `get` to call `svc.Detail` and render steps + `enrollments` in `campaignResponse`; update `toResponse`/`campaignResponse` in `routes.go`.

- [ ] **Step 4: Run test to verify it passes + build**

Run:
```bash
export PATH="$PATH:/c/Program Files/Go/bin:$HOME/go/bin"
go test ./internal/app/campaign/ -v && go build ./...
```
Expected: PASS; build passes.

- [ ] **Step 5: Commit**

```bash
git add internal/app/campaign/
git commit -m "feat(campaign): GET returns steps + enrollment counts"
```

---

## Task 12: Wiring (`cmd/inroad`) + OpenAPI + regenerate frontend types

**Files:**
- Modify: `cmd/inroad/main.go` (construct step/enrollment stores, mount step routes, pass `SequenceEnqueuer` + detail deps to campaign)
- Modify: `api/openapi.yaml`
- Generated: `web/src/store/api.ts` (via the frontend codegen)

**Interfaces:**
- Consumes: everything above.
- Produces: running server exposing the new routes; regenerated typed RTK Query hooks.

- [ ] **Step 1: Wire the stores + routes in `cmd/inroad/main.go`**

Near the existing campaign wiring (`internal/app/campaign` construction, ~line 76):
```go
stepStore := sequencestep.NewPgStore(queries)
enrollStore := enrollment.NewPgStore(queries)
enrollSvc := enrollment.NewService(enrollStore)

campaignSvc := campaign.NewServiceWithDetail(
	campaign.NewPgStore(pool),
	ownershipChecker{mailboxes: mailboxStore, lists: listSvc},
	stepStore,   // StepLister (add ListSteps wrapper)
	enrollStore, // EnrollmentCounter
)
stepSvc := sequencestep.NewService(stepStore, campaignStatusChecker{campaigns: campaign.NewPgStore(pool)})
```
Mount (note: step routes share the `/api/v1/campaigns` prefix; mount the step handler on the SAME mount so `{id}` resolves — either merge into the campaign handler's `Routes()` or mount a second router. Simplest: mount steps at `/api/v1/campaigns` too — chi allows overlapping mounts only if paths differ, so instead add the step routes INTO `campaign.Handler.Routes()` by giving the campaign handler a reference to the step handler, OR mount steps under a distinct subrouter. **Recommended:** register step routes inside `campaign.Handler.Routes()` by passing the `*sequencestep.Handler` into `campaign.NewHandler`.):
```go
router.Mount("/api/v1/campaigns", campaign.NewHandler(campaignSvc, cfg.JWTSecret, enq, sequencestep.NewHandler(stepSvc, cfg.JWTSecret)).Routes())
```
Add a `campaignStatusChecker` adapter (mirrors `ownershipChecker`) implementing `sequencestep.CampaignChecker.CampaignStatus` via `campaign` store's `Get`. Add imports for `sequencestep` and `enrollment`.

- [ ] **Step 2: Update `campaign.Handler.Routes()` to include step routes**

In `internal/app/campaign/handler.go`/`routes.go`, add a `steps *sequencestep.Handler` field to `Handler`, extend `NewHandler`, and in `Routes()` register the step sub-paths by delegating (`r.Get("/{id}/steps", h.steps.List)` etc. — export the step handler methods or add a `Mount` helper). Keep the enqueuer type as `SequenceEnqueuer` (Task 8).

- [ ] **Step 3: Build + run the full unit suite**

Run:
```bash
export PATH="$PATH:/c/Program Files/Go/bin:$HOME/go/bin"
go build ./... && make test
```
Expected: build passes; all unit tests PASS.

- [ ] **Step 4: Update `api/openapi.yaml`**

Add paths: `POST/GET /campaigns/{id}/steps`, `PUT/DELETE /campaigns/{id}/steps/{stepId}`; extend the `Campaign` GET response schema with `steps[]` and `enrollments` (map of status→count); update `POST /campaigns/{id}/launch` response to `{queued, total_enrolled, failed_enqueue_count}`. Add `SequenceStep` schema. Match the field names in the handlers' JSON tags exactly.

- [ ] **Step 5: Regenerate frontend types**

Run:
```bash
cd web && npm run codegen   # or the project's openapi codegen script (see package.json)
```
Expected: `web/src/store/api.ts` regenerates with the new endpoints/types; `web` still type-checks (`npm run build` or `tsc --noEmit`). **Types only — no UI work in this plan.**

- [ ] **Step 6: Commit**

```bash
git add cmd/inroad/main.go internal/app/campaign/ api/openapi.yaml web/src/store/
git commit -m "feat(api): mount step routes; openapi + regenerated web types"
```

---

## Task 13: Integration tests (deliverability proof)

**Files:**
- Create: `internal/worker/sequence/sequence_integration_test.go` (build-tagged `//go:build integration`, mirroring `internal/worker/sender/send_integration_test.go`)

**Interfaces:**
- Consumes: dockerized Postgres (`make db-up`) + mailpit SMTP (same harness as the existing send integration test).

- [ ] **Step 1: Read the existing send integration test to reuse its harness**

Run:
```bash
export PATH="$PATH:/c/Program Files/Go/bin:$HOME/go/bin"
sed -n '1,80p' internal/worker/sender/send_integration_test.go
```
Expected: learn the mailpit setup, DB seeding helpers, and build tag.

- [ ] **Step 2: Write the backward-compat + multi-step + stop tests**

`internal/worker/sequence/sequence_integration_test.go` — three test functions using the harness from Step 1:
- `TestBackwardCompatSingleStep`: create campaign via the store with inline subject/body, no explicit steps → launch → assert one enrollment, step 1 auto-materialized (`CountStepsForCampaign==1`), run `AdvanceHandler` once → mailpit received 1 message, `sends` row `status='sent'`, enrollment `status='completed'`.
- `TestTwoStepThreadedSequence`: campaign + 2 steps (step 2 `wait_days=0`, empty subject) → launch → run advance twice (drain the queue / call handler with the enrollment id) → mailpit received 2 messages, the 2nd has a non-empty `In-Reply-To`, enrollment `completed`.
- `TestStopOnUnsubscribeSkipsRemainingSteps`: after step 1, insert a suppression row for the contact → run advance → enrollment `stopped` with `stop_reason='unsubscribed'`, no 2nd message in mailpit.
- `TestCrossTenantStepFetchReturns404`: (unit-level, can live in `sequencestep` instead) workspace A fetching workspace B's step id → not found.

(Write each with explicit assertions; reuse the seed helpers — do not invent new DB plumbing.)

- [ ] **Step 3: Run the integration suite**

Run:
```bash
export PATH="$PATH:/c/Program Files/Go/bin:$HOME/go/bin"
make db-up && make migrate-up && make test-integration
```
Expected: all three sequence integration tests PASS against dockerized PG + mailpit.

- [ ] **Step 4: Live proof (manual, real provider)**

Against the live mailbox (`ahmed@axomble.com`): create a 2-step campaign via the API, launch, confirm a real test inbox physically receives step 1, then step 2 as a threaded reply. Assert `sends` rows `status='sent'` with non-empty `message_id`; unsubscribe from step 1's email and confirm step 2 is skipped.

- [ ] **Step 5: Commit**

```bash
git add internal/worker/sequence/sequence_integration_test.go
git commit -m "test(sequence): backward-compat, threaded multi-step, stop-on-unsub integration"
```

---

## Self-Review Notes (gaps flagged for execution)

These are the spots where the plan leans on my inferred spec (§0 assumptions) rather than your locked text — verify at the task gate:

- **Task 1 / A1:** `idx_sends_campaign_contact_step` unique index + `InsertStepSend`'s `ON CONFLICT (campaign_id, contact_id, step_order)`. Confirm this is the idempotency key you want (vs. keeping created_at-based).
- **Task 6 / A3 fold-in:** I fold `InsertStepSend` into `GetStepSendJob` so the `sends` row exists before SMTP and `MarkStepSent` updates it. If your spec has `advance` insert the row explicitly, move it into `advance.go`.
- **Task 8 / A6:** backward-compat `EnsureStep1FromCampaign` on launch for campaigns with zero steps. Confirm new (post-000007) campaigns should auto-materialize step 1 from inline subject/body.
- **Task 8 / A4:** `staggerInterval = 2s`. Confirm the stagger policy (or swap for business-hours pacing seam).
- **Task 9:** on hard SMTP failure I still call `MarkStepSent(failed)` to advance the cursor (avoid wedging). Your spec's `MarkStepStopped(reason='failed')` semantics may prefer stopping the enrollment instead — reconcile against the "exact MarkStepStopped call-time semantics" you referenced.
- **Naming:** `coreapi.StopReason*` constants duplicate `enrollment.StopReason` values (app/* isolation forbids the import). Keep the string values identical; a drift here is a silent bug.
