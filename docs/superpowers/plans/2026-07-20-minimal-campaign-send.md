# Minimal Campaign Send — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A real email leaves a connected mailbox and lands in an inbox, driven by a minimal campaign (CSV-imported contacts → campaign → worker send), with unsubscribe.

**Architecture:** New domains `contact`, `list`, `campaign`, `suppression` follow the `mailbox` reference pattern (domain-owned `Store` interface, auth-scoped routes, DTOs, tenant checks). The worker gains a `send:email` handler that reaches data and decrypted credentials only through two new `coreapi` methods (`GetSendJob`/`MarkSend`) — it imports zero `db`. Sends are one DB row + one asynq task per `send_id` (restart-safe, idempotent via a DB unique constraint).

**Tech Stack:** Go 1.25 · pgx/sqlc · asynq · `wneessen/go-mail` · `go-playground/validator/v10` · HMAC-SHA256 (unsubscribe) · Postgres 16.

## Global Constraints

- **Module:** `github.com/inroad/inroad`. Go files lowercase; identifiers idiomatic `MixedCaps`. snake_case only at boundaries (JSON, DB columns, env).
- **Toolchain PATH (this machine):** prefix every Go/sqlc Bash command with `export PATH="$PATH:/c/Program Files/Go/bin:$HOME/go/bin"`. Shell state does not persist between calls.
- **Architecture (SOLID/Clean):** each domain defines its own `Store` interface; services depend on the interface, not the sqlc-backed struct. Small interfaces at seams. `app/*` may import `platform/*`, never reverse; `app/*` packages don't import each other; workers reach data only via `coreapi`.
- **Type safety:** status columns use Go typed-constant enums (`CampaignStatus`, `SendStatus`, `SuppressionReason`) mirrored by DB `CHECK` constraints. No `interface{}` except `custom_fields` (`map[string]any`). Explicit request/response DTOs.
- **Validation:** every handler validates its request via `validate.Struct` before the service; services enforce tenant-ownership existence checks (cross-workspace reference → 404). Values copied verbatim from the spec's per-route table.
- **Secrets:** decrypted `SMTPPassword` is used in-memory only, never logged.
- **Tenancy:** every query scoped by `workspace_id` from the JWT (`auth.UserFromContext`).
- **Commits:** conventional (`feat:`, `test:`, `chore:`). Commit at the end of every task.
- **Migrations/queries/gen** live under `internal/platform/db/` (go:embed constraint).

---

## File Structure

- `internal/platform/db/migrations/000003_campaign_send.{up,down}.sql`
- `internal/platform/db/queries/{contact,list,campaign,send,suppression}.sql` → regenerates `gen/`
- `internal/platform/validate/validate.go`
- `internal/platform/mail/sender.go`
- `internal/app/list/{store.go,service.go,handler.go,routes.go,*_test.go}`
- `internal/app/contact/{store.go,service.go,import.go,handler.go,routes.go,*_test.go}`
- `internal/app/campaign/{status.go,store.go,service.go,handler.go,routes.go,*_test.go}`
- `internal/app/suppression/{token.go,store.go,handler.go,routes.go,*_test.go}`
- `internal/coreapi/coreapi.go` (extend), `internal/coreapi/inprocess/inprocess.go` (extend), `internal/coreapi/inprocess/sendjob.go`
- `internal/platform/queue/queue.go` (add `send:email` task)
- `internal/worker/sender/{sender.go,personalize.go,*_test.go}`, `internal/worker/handlers.go` (register)
- `internal/platform/config/config.go` (add `PublicURL`)
- `cmd/inroad/main.go` (mount new routes), `api/openapi.yaml`

---

## Task 1: Schema, queries, typed enums, deps

**Files:**
- Create: `internal/platform/db/migrations/000003_campaign_send.up.sql`, `.down.sql`
- Create: `internal/platform/db/queries/{contact,list,campaign,send,suppression}.sql`
- Generated: `internal/platform/db/gen/*`

**Interfaces:**
- Produces: tables + sqlc methods used by all later tasks. Key generated methods (names from query annotations below): `CreateList`, `ListLists`, `GetList`, `UpsertContact`, `AddListMember`, `ListContactsByList`, `CountListMembers`, `CreateCampaign`, `GetCampaign`, `ListCampaigns`, `SetCampaignStatus`, `EnqueueSends` (bulk insert), `GetSendBundle`, `SetSendResult`, `CountSentToday`, `ListQueuedSendIDs`, `CountSendsByStatus`, `AddSuppression`, `IsSuppressed`.

- [ ] **Step 1: Write the up migration**

`internal/platform/db/migrations/000003_campaign_send.up.sql` — copy the DDL from the spec §3 verbatim (contacts, lists, list_members, campaigns, sends, suppression, with all indexes and CHECK constraints).

- [ ] **Step 2: Write the down migration**

```sql
DROP TABLE IF EXISTS suppression;
DROP TABLE IF EXISTS sends;
DROP TABLE IF EXISTS campaigns;
DROP TABLE IF EXISTS list_members;
DROP TABLE IF EXISTS lists;
DROP TABLE IF EXISTS contacts;
```

- [ ] **Step 3: Write query files**

`queries/list.sql`:
```sql
-- name: CreateList :one
INSERT INTO lists (workspace_id, name) VALUES ($1, $2) RETURNING *;
-- name: GetList :one
SELECT * FROM lists WHERE id = $1 AND workspace_id = $2;
-- name: ListLists :many
SELECT * FROM lists WHERE workspace_id = $1 ORDER BY created_at DESC;
-- name: CountListMembers :one
SELECT count(*) FROM list_members WHERE list_id = $1;
```

`queries/contact.sql`:
```sql
-- name: UpsertContact :one
INSERT INTO contacts (workspace_id, email, first_name, last_name, company)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (workspace_id, lower(email))
DO UPDATE SET first_name = EXCLUDED.first_name
RETURNING id, (xmax = 0) AS inserted;
-- name: AddListMember :exec
INSERT INTO list_members (list_id, contact_id) VALUES ($1, $2)
ON CONFLICT (list_id, contact_id) DO NOTHING;
-- name: ListContactsByList :many
SELECT c.* FROM contacts c
JOIN list_members lm ON lm.contact_id = c.id
WHERE lm.list_id = $1 AND c.workspace_id = $2
ORDER BY c.created_at DESC
LIMIT $3 OFFSET $4;
```
> `UpsertContact` returns `inserted` via the `xmax = 0` trick so the importer can count new-vs-duplicate.

`queries/campaign.sql`:
```sql
-- name: CreateCampaign :one
INSERT INTO campaigns (workspace_id, name, mailbox_id, list_id, subject, body_text, body_html)
VALUES ($1,$2,$3,$4,$5,$6,$7) RETURNING *;
-- name: GetCampaign :one
SELECT * FROM campaigns WHERE id = $1 AND workspace_id = $2;
-- name: ListCampaigns :many
SELECT * FROM campaigns WHERE workspace_id = $1 ORDER BY created_at DESC;
-- name: SetCampaignStatus :exec
UPDATE campaigns SET status = $3, launched_at = COALESCE(launched_at, $4)
WHERE id = $1 AND workspace_id = $2;
-- name: CountSendsByStatus :many
SELECT status, count(*) AS n FROM sends WHERE campaign_id = $1 GROUP BY status;
```

`queries/send.sql`:
```sql
-- name: EnqueueSends :many
INSERT INTO sends (workspace_id, campaign_id, contact_id, mailbox_id, to_email)
SELECT c.workspace_id, $1, lm.contact_id, cam.mailbox_id, ct.email
FROM campaigns cam
JOIN campaigns c ON c.id = cam.id
JOIN list_members lm ON lm.list_id = cam.list_id
JOIN contacts ct ON ct.id = lm.contact_id
WHERE cam.id = $1 AND cam.workspace_id = $2
ON CONFLICT (campaign_id, contact_id) DO NOTHING
RETURNING id;
-- name: GetSendBundle :one
SELECT s.id AS send_id, s.workspace_id, s.to_email, s.mailbox_id,
       ct.first_name, cam.subject, cam.body_text, cam.body_html,
       m.email AS from_email, m.display_name AS from_name,
       m.smtp_host, m.smtp_port, m.smtp_username, m.secret_ciphertext, m.use_tls,
       m.daily_cap, m.ramp_enabled, m.ramp_start_cap, m.ramp_days, m.created_at AS mailbox_created_at
FROM sends s
JOIN campaigns cam ON cam.id = s.campaign_id
JOIN contacts ct ON ct.id = s.contact_id
JOIN mailboxes m ON m.id = s.mailbox_id
WHERE s.id = $1;
-- name: SetSendResult :exec
UPDATE sends SET status = $2, message_id = $3, error = $4,
       sent_at = CASE WHEN $2 = 'sent' THEN now() ELSE sent_at END
WHERE id = $1;
-- name: CountSentToday :one
SELECT count(*) FROM sends
WHERE mailbox_id = $1 AND status = 'sent' AND sent_at::date = (now() AT TIME ZONE 'utc')::date;
-- name: CountQueuedByCampaign :one
SELECT count(*) FROM sends WHERE campaign_id = $1 AND status = 'queued';
-- name: GetCampaignIDForSend :one
SELECT campaign_id, workspace_id FROM sends WHERE id = $1;
```

`queries/suppression.sql`:
```sql
-- name: AddSuppression :exec
INSERT INTO suppression (workspace_id, email, reason) VALUES ($1, $2, $3)
ON CONFLICT (workspace_id, lower(email)) DO NOTHING;
-- name: IsSuppressed :one
SELECT EXISTS (SELECT 1 FROM suppression WHERE workspace_id = $1 AND lower(email) = lower($2));
```

- [ ] **Step 4: Fetch deps + generate**

Run:
```bash
export PATH="$PATH:/c/Program Files/Go/bin:$HOME/go/bin"
go get github.com/wneessen/go-mail@latest github.com/go-playground/validator/v10@latest
sqlc generate
go build ./internal/platform/db/...
```
Expected: `gen/{contact,list,campaign,send,suppression}.sql.go` created; build OK.

- [ ] **Step 5: Verify migration applies (integration)**

Run (needs `make db-up` / dev DB on :5433):
```bash
export PATH="$PATH:/c/Program Files/Go/bin:$HOME/go/bin"
export INROAD_JWT_SECRET=0123456789abcdef0123456789abcdef INROAD_MASTER_KEY=MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY= INROAD_DATABASE_URL=postgres://inroad:inroad@localhost:5433/inroad?sslmode=disable
go run ./cmd/migrate up
```
Expected: `migrate up ok`.

- [ ] **Step 6: Commit**

```bash
git add internal/platform/db sqlc.yaml go.mod go.sum
git commit -m "feat: add campaign-send schema, queries, and generated types"
```

---

## Task 2: Validation wrapper

**Files:**
- Create: `internal/platform/validate/validate.go`, `internal/platform/validate/validate_test.go`

**Interfaces:**
- Produces: `validate.Struct(v any) error` returning `nil` or `*validate.Error` (has `Fields map[string]string`). `validate.IsValidationError(err) (*Error, bool)` for handlers to map to 400.

- [ ] **Step 1: Write the failing test**

```go
package validate

import "testing"

type sample struct {
	Email string `validate:"required,email"`
	Name  string `validate:"required,max=5"`
}

func TestStructReportsFieldErrors(t *testing.T) {
	err := Struct(sample{Email: "nope", Name: "toolong"})
	if err == nil {
		t.Fatal("expected validation error")
	}
	ve, ok := IsValidationError(err)
	if !ok {
		t.Fatalf("expected *Error, got %T", err)
	}
	if _, has := ve.Fields["Email"]; !has {
		t.Errorf("expected Email field error, got %v", ve.Fields)
	}
}

func TestStructPassesValid(t *testing.T) {
	if err := Struct(sample{Email: "a@b.com", Name: "ok"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/platform/validate/` → FAIL (`undefined: Struct`).

- [ ] **Step 3: Implement**

```go
// Package validate wraps go-playground/validator with a single shared instance
// and a field-keyed error type handlers can map to HTTP 400.
package validate

import (
	"fmt"
	"strings"

	"github.com/go-playground/validator/v10"
)

var v = validator.New(validator.WithRequiredStructEnabled())

// Error is a validation failure keyed by struct field name.
type Error struct{ Fields map[string]string }

func (e *Error) Error() string {
	parts := make([]string, 0, len(e.Fields))
	for f, m := range e.Fields {
		parts = append(parts, fmt.Sprintf("%s: %s", f, m))
	}
	return "validation failed: " + strings.Join(parts, "; ")
}

// Struct validates v by its `validate` tags. Returns nil or *Error.
func Struct(s any) error {
	err := v.Struct(s)
	if err == nil {
		return nil
	}
	var verrs validator.ValidationErrors
	if !asValidationErrors(err, &verrs) {
		return err
	}
	fields := make(map[string]string, len(verrs(verrs)))
	for _, fe := range verrs {
		fields[fe.Field()] = fe.Tag()
	}
	return &Error{Fields: fields}
}

func verrs(v validator.ValidationErrors) validator.ValidationErrors { return v }

func asValidationErrors(err error, target *validator.ValidationErrors) bool {
	if ve, ok := err.(validator.ValidationErrors); ok {
		*target = ve
		return true
	}
	return false
}

// IsValidationError reports whether err is a *Error.
func IsValidationError(err error) (*Error, bool) {
	e, ok := err.(*Error)
	return e, ok
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./internal/platform/validate/` → PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/platform/validate
git commit -m "feat: add request validation wrapper"
```

---

## Task 3: List domain

**Files:**
- Create: `internal/app/list/{store.go,service.go,handler.go,routes.go,service_test.go}`

**Interfaces:**
- Consumes: `gen` (CreateList/GetList/ListLists/CountListMembers), `auth`, `httpx`, `validate`.
- Produces: `list.NewPgStore(*gen.Queries) *PgStore` (implements `list.Store`); `list.NewService(Store) *Service` with `Create(ctx, wsID uuid.UUID, name string) (gen.List, error)`, `List(ctx, wsID) ([]gen.List, error)`, `Get(ctx, wsID, id) (gen.List, error)`, `MemberCount(ctx, id) (int64,error)`; sentinel `list.ErrNotFound`; `list.NewHandler(*Service, jwtSecret []byte)` with `Routes()` mounting `POST /` and `GET /`.

- [ ] **Step 1: Write the failing service test** (fake store, no DB)

```go
package list

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/inroad/inroad/internal/platform/db/gen"
)

type fakeStore struct{ created gen.List }

func (f *fakeStore) Create(_ context.Context, ws uuid.UUID, name string) (gen.List, error) {
	f.created = gen.List{ID: uuid.New(), WorkspaceID: ws, Name: name}
	return f.created, nil
}
func (f *fakeStore) List(context.Context, uuid.UUID) ([]gen.List, error) { return []gen.List{f.created}, nil }
func (f *fakeStore) Get(context.Context, uuid.UUID, uuid.UUID) (gen.List, error) { return f.created, nil }
func (f *fakeStore) CountMembers(context.Context, uuid.UUID) (int64, error) { return 0, nil }

func TestCreateList(t *testing.T) {
	svc := NewService(&fakeStore{})
	l, err := svc.Create(context.Background(), uuid.New(), "Prospects")
	if err != nil || l.Name != "Prospects" {
		t.Fatalf("Create: %v %+v", err, l)
	}
}
```

- [ ] **Step 2: Run to verify fail**

Run: `go test ./internal/app/list/` → FAIL.

- [ ] **Step 3: Implement `store.go`**

```go
// Package list manages contact lists and membership.
package list

import (
	"context"

	"github.com/google/uuid"
	"github.com/inroad/inroad/internal/platform/db/gen"
)

type Store interface {
	Create(ctx context.Context, workspaceID uuid.UUID, name string) (gen.List, error)
	List(ctx context.Context, workspaceID uuid.UUID) ([]gen.List, error)
	Get(ctx context.Context, workspaceID, id uuid.UUID) (gen.List, error)
	CountMembers(ctx context.Context, id uuid.UUID) (int64, error)
}

type PgStore struct{ q *gen.Queries }

func NewPgStore(q *gen.Queries) *PgStore { return &PgStore{q: q} }

func (s *PgStore) Create(ctx context.Context, ws uuid.UUID, name string) (gen.List, error) {
	return s.q.CreateList(ctx, gen.CreateListParams{WorkspaceID: ws, Name: name})
}
func (s *PgStore) List(ctx context.Context, ws uuid.UUID) ([]gen.List, error) {
	return s.q.ListLists(ctx, ws)
}
func (s *PgStore) Get(ctx context.Context, ws, id uuid.UUID) (gen.List, error) {
	return s.q.GetList(ctx, gen.GetListParams{ID: id, WorkspaceID: ws})
}
func (s *PgStore) CountMembers(ctx context.Context, id uuid.UUID) (int64, error) {
	return s.q.CountListMembers(ctx, id)
}
```

- [ ] **Step 4: Implement `service.go`**

```go
package list

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/inroad/inroad/internal/platform/db/gen"
)

var ErrNotFound = errors.New("list not found")

type Service struct{ store Store }

func NewService(store Store) *Service { return &Service{store: store} }

func (s *Service) Create(ctx context.Context, ws uuid.UUID, name string) (gen.List, error) {
	return s.store.Create(ctx, ws, name)
}
func (s *Service) List(ctx context.Context, ws uuid.UUID) ([]gen.List, error) {
	return s.store.List(ctx, ws)
}
func (s *Service) Get(ctx context.Context, ws, id uuid.UUID) (gen.List, error) {
	return s.store.Get(ctx, ws, id)
}
func (s *Service) MemberCount(ctx context.Context, id uuid.UUID) (int64, error) {
	return s.store.CountMembers(ctx, id)
}
```

- [ ] **Step 5: Implement `handler.go` + `routes.go`**

`handler.go`:
```go
package list

import (
	"encoding/json"
	"net/http"

	"github.com/inroad/inroad/internal/app/auth"
	"github.com/inroad/inroad/internal/platform/httpx"
	"github.com/inroad/inroad/internal/platform/validate"
)

type Handler struct {
	svc       *Service
	jwtSecret []byte
}

func NewHandler(svc *Service, jwtSecret []byte) *Handler { return &Handler{svc: svc, jwtSecret: jwtSecret} }

type createRequest struct {
	Name string `json:"name" validate:"required,min=1,max=200"`
}
type listResponse struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	ws, ok := workspaceID(w, r)
	if !ok {
		return
	}
	var req createRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid json")
		return
	}
	if err := validate.Struct(req); err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	l, err := h.svc.Create(r.Context(), ws, req.Name)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "could not create list")
		return
	}
	httpx.JSON(w, http.StatusOK, listResponse{ID: l.ID.String(), Name: l.Name})
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	ws, ok := workspaceID(w, r)
	if !ok {
		return
	}
	ls, err := h.svc.List(r.Context(), ws)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "could not list")
		return
	}
	out := make([]listResponse, 0, len(ls))
	for _, l := range ls {
		out = append(out, listResponse{ID: l.ID.String(), Name: l.Name})
	}
	httpx.JSON(w, http.StatusOK, out)
}
```

`routes.go` (shared `workspaceID` helper defined here):
```go
package list

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/app/auth"
	"github.com/inroad/inroad/internal/platform/httpx"
)

func (h *Handler) Routes() http.Handler {
	r := chi.NewRouter()
	r.Use(auth.RequireAuth(h.jwtSecret))
	r.Post("/", h.create)
	r.Get("/", h.list)
	return r
}

// workspaceID extracts and parses the workspace id from the JWT claims.
func workspaceID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	claims, ok := auth.UserFromContext(r.Context())
	if !ok {
		httpx.Error(w, http.StatusUnauthorized, "unauthorized")
		return uuid.Nil, false
	}
	id, err := uuid.Parse(claims.WorkspaceID)
	if err != nil {
		httpx.Error(w, http.StatusUnauthorized, "bad workspace")
		return uuid.Nil, false
	}
	return id, true
}
```

- [ ] **Step 6: Run tests + build**

Run: `go test ./internal/app/list/ && go build ./internal/app/list/` → PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/app/list
git commit -m "feat: add list domain"
```

---

## Task 4: Contact domain + CSV import

**Files:**
- Create: `internal/app/contact/{store.go,service.go,import.go,handler.go,routes.go,import_test.go}`

**Interfaces:**
- Consumes: `gen` (UpsertContact/AddListMember/ListContactsByList), `list` (ownership check via `list.Service.Get`), `auth`, `httpx`, `validate`.
- Produces: `contact.NewService(Store, *list.Service)`; `(*Service) ImportCSV(ctx, ws, listID uuid.UUID, r io.Reader) (ImportResult, error)` where `ImportResult{Imported, Skipped, Duplicates int}`; `(*Service) ListByList(ctx, ws, listID uuid.UUID, limit, offset int32) ([]gen.Contact, error)`; handler `POST /lists/{id}/import` (multipart) + `GET /contacts`.

- [ ] **Step 1: Write the failing import test** (fake store; parse logic is the unit under test)

```go
package contact

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/inroad/inroad/internal/platform/db/gen"
)

type fakeStore struct{ upserts int }

func (f *fakeStore) Upsert(_ context.Context, _ uuid.UUID, _ UpsertInput) (uuid.UUID, bool, error) {
	f.upserts++
	return uuid.New(), true, nil
}
func (f *fakeStore) AddToList(context.Context, uuid.UUID, uuid.UUID) error { return nil }
func (f *fakeStore) ListByList(context.Context, uuid.UUID, uuid.UUID, int32, int32) ([]gen.Contact, error) {
	return nil, nil
}

func TestImportCSVParsesHeaderAndSkipsBadRows(t *testing.T) {
	svc := &Service{store: &fakeStore{}}
	csv := "email,first_name\nalice@x.com,Alice\nnot-an-email,Bob\nbob@x.com,Bob\n"
	res, err := svc.importRows(context.Background(), uuid.New(), uuid.New(), strings.NewReader(csv))
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if res.Imported != 2 || res.Skipped != 1 {
		t.Fatalf("got %+v, want Imported=2 Skipped=1", res)
	}
}

func TestImportCSVRejectsMissingEmailColumn(t *testing.T) {
	svc := &Service{store: &fakeStore{}}
	if _, err := svc.importRows(context.Background(), uuid.New(), uuid.New(), strings.NewReader("name\nAlice\n")); err == nil {
		t.Fatal("expected error for missing email column")
	}
}
```

- [ ] **Step 2: Run to verify fail**

Run: `go test ./internal/app/contact/` → FAIL.

- [ ] **Step 3: Implement `store.go`**

```go
// Package contact manages contacts and CSV import into lists.
package contact

import (
	"context"

	"github.com/google/uuid"
	"github.com/inroad/inroad/internal/platform/db/gen"
)

type UpsertInput struct{ Email, FirstName, LastName, Company string }

type Store interface {
	// Upsert returns the contact id and whether it was newly inserted.
	Upsert(ctx context.Context, workspaceID uuid.UUID, in UpsertInput) (uuid.UUID, bool, error)
	AddToList(ctx context.Context, listID, contactID uuid.UUID) error
	ListByList(ctx context.Context, workspaceID, listID uuid.UUID, limit, offset int32) ([]gen.Contact, error)
}

type PgStore struct{ q *gen.Queries }

func NewPgStore(q *gen.Queries) *PgStore { return &PgStore{q: q} }

func (s *PgStore) Upsert(ctx context.Context, ws uuid.UUID, in UpsertInput) (uuid.UUID, bool, error) {
	row, err := s.q.UpsertContact(ctx, gen.UpsertContactParams{
		WorkspaceID: ws, Email: in.Email, FirstName: in.FirstName, LastName: in.LastName, Company: in.Company,
	})
	if err != nil {
		return uuid.Nil, false, err
	}
	return row.ID, row.Inserted, nil
}
func (s *PgStore) AddToList(ctx context.Context, listID, contactID uuid.UUID) error {
	return s.q.AddListMember(ctx, gen.AddListMemberParams{ListID: listID, ContactID: contactID})
}
func (s *PgStore) ListByList(ctx context.Context, ws, listID uuid.UUID, limit, offset int32) ([]gen.Contact, error) {
	return s.q.ListContactsByList(ctx, gen.ListContactsByListParams{ListID: listID, WorkspaceID: ws, Limit: limit, Offset: offset})
}
```

- [ ] **Step 4: Implement `import.go` (the parse logic)**

```go
package contact

import (
	"context"
	"encoding/csv"
	"errors"
	"io"
	"net/mail"
	"strings"

	"github.com/google/uuid"
)

const maxImportRows = 50000

type ImportResult struct {
	Imported   int `json:"imported"`
	Skipped    int `json:"skipped"`
	Duplicates int `json:"duplicates"`
}

// importRows parses a headered CSV and upserts each valid row into the list.
// Columns are detected by header name (email required). Invalid emails are
// skipped and counted, never fatal.
func (s *Service) importRows(ctx context.Context, ws, listID uuid.UUID, r io.Reader) (ImportResult, error) {
	cr := csv.NewReader(r)
	cr.LazyQuotes = true
	cr.FieldsPerRecord = -1

	header, err := cr.Read()
	if err != nil {
		return ImportResult{}, errors.New("empty or unreadable CSV")
	}
	col := map[string]int{}
	for i, h := range header {
		col[strings.ToLower(strings.TrimSpace(h))] = i
	}
	emailIdx, ok := col["email"]
	if !ok {
		return ImportResult{}, errors.New("CSV must have an 'email' column")
	}

	var res ImportResult
	rows := 0
	for {
		rec, err := cr.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			res.Skipped++
			continue
		}
		rows++
		if rows > maxImportRows {
			return res, errors.New("CSV exceeds 50000 rows")
		}
		email := field(rec, emailIdx)
		if _, perr := mail.ParseAddress(email); perr != nil || email == "" {
			res.Skipped++
			continue
		}
		in := UpsertInput{
			Email:     email,
			FirstName: field(rec, col["first_name"]),
			LastName:  field(rec, col["last_name"]),
			Company:   field(rec, col["company"]),
		}
		id, inserted, err := s.store.Upsert(ctx, ws, in)
		if err != nil {
			res.Skipped++
			continue
		}
		if inserted {
			res.Imported++
		} else {
			res.Duplicates++
		}
		if err := s.store.AddToList(ctx, listID, id); err != nil {
			// membership failure is non-fatal for the row's count
			continue
		}
	}
	return res, nil
}

func field(rec []string, idx int) string {
	if idx < 0 || idx >= len(rec) {
		return ""
	}
	return strings.TrimSpace(rec[idx])
}
```
> Note: `col["first_name"]` returns `0` when absent (Go map zero value). Guard by only reading optional columns present in `col`; simplest is to default missing optional indices to `-1`:

Adjust after building `col`: for the optional names, if absent set to -1. Add right after `col` is built:
```go
	for _, name := range []string{"first_name", "last_name", "company"} {
		if _, ok := col[name]; !ok {
			col[name] = -1
		}
	}
```

- [ ] **Step 5: Implement `service.go`**

```go
package contact

import (
	"context"
	"io"

	"github.com/google/uuid"
	"github.com/inroad/inroad/internal/app/list"
	"github.com/inroad/inroad/internal/platform/db/gen"
)

type Service struct {
	store Store
	lists *list.Service
}

func NewService(store Store, lists *list.Service) *Service { return &Service{store: store, lists: lists} }

// ImportCSV verifies the list belongs to the workspace, then imports rows.
func (s *Service) ImportCSV(ctx context.Context, ws, listID uuid.UUID, r io.Reader) (ImportResult, error) {
	if _, err := s.lists.Get(ctx, ws, listID); err != nil {
		return ImportResult{}, list.ErrNotFound
	}
	return s.importRows(ctx, ws, listID, r)
}

func (s *Service) ListByList(ctx context.Context, ws, listID uuid.UUID, limit, offset int32) ([]gen.Contact, error) {
	return s.store.ListByList(ctx, ws, listID, limit, offset)
}
```

- [ ] **Step 6: Implement `handler.go` + `routes.go`**

`handler.go` — `POST /lists/{id}/import` (multipart, 10 MB cap) and `GET /contacts?list=&limit=&offset=`:
```go
package contact

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/app/auth"
	"github.com/inroad/inroad/internal/app/list"
	"github.com/inroad/inroad/internal/platform/httpx"
)

const maxUploadBytes = 10 << 20 // 10 MB

type Handler struct {
	svc       *Service
	jwtSecret []byte
}

func NewHandler(svc *Service, jwtSecret []byte) *Handler { return &Handler{svc: svc, jwtSecret: jwtSecret} }

func (h *Handler) Routes() http.Handler {
	r := chi.NewRouter()
	r.Use(auth.RequireAuth(h.jwtSecret))
	r.Post("/lists/{id}/import", h.importCSV)
	r.Get("/contacts", h.listContacts)
	return r
}

func (h *Handler) importCSV(w http.ResponseWriter, r *http.Request) {
	ws, ok := wsID(w, r)
	if !ok {
		return
	}
	listID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "bad list id")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes)
	file, _, err := r.FormFile("file")
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "missing 'file' upload")
		return
	}
	defer file.Close()

	res, err := h.svc.ImportCSV(r.Context(), ws, listID, file)
	if errors.Is(err, list.ErrNotFound) {
		httpx.Error(w, http.StatusNotFound, "list not found")
		return
	}
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	httpx.JSON(w, http.StatusOK, res)
}

func (h *Handler) listContacts(w http.ResponseWriter, r *http.Request) {
	ws, ok := wsID(w, r)
	if !ok {
		return
	}
	listID, err := uuid.Parse(r.URL.Query().Get("list"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "list query param required")
		return
	}
	limit := clamp(atoiDefault(r.URL.Query().Get("limit"), 50), 1, 200)
	offset := max0(atoiDefault(r.URL.Query().Get("offset"), 0))
	cs, err := h.svc.ListByList(r.Context(), ws, listID, int32(limit), int32(offset))
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "could not list contacts")
		return
	}
	type contactResponse struct {
		ID        string `json:"id"`
		Email     string `json:"email"`
		FirstName string `json:"first_name"`
	}
	out := make([]contactResponse, 0, len(cs))
	for _, c := range cs {
		out = append(out, contactResponse{ID: c.ID.String(), Email: c.Email, FirstName: c.FirstName})
	}
	httpx.JSON(w, http.StatusOK, out)
}

func wsID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	claims, ok := auth.UserFromContext(r.Context())
	if !ok {
		httpx.Error(w, http.StatusUnauthorized, "unauthorized")
		return uuid.Nil, false
	}
	id, err := uuid.Parse(claims.WorkspaceID)
	if err != nil {
		httpx.Error(w, http.StatusUnauthorized, "bad workspace")
		return uuid.Nil, false
	}
	return id, true
}

func atoiDefault(s string, d int) int {
	if s == "" {
		return d
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return d
	}
	return n
}
func clamp(n, lo, hi int) int {
	if n < lo {
		return lo
	}
	if n > hi {
		return hi
	}
	return n
}
func max0(n int) int {
	if n < 0 {
		return 0
	}
	return n
}
```

- [ ] **Step 7: Run tests + build**

Run: `go test ./internal/app/contact/ && go build ./internal/app/contact/` → PASS.

- [ ] **Step 8: Commit**

```bash
git add internal/app/contact
git commit -m "feat: add contact domain with CSV import"
```

---

## Task 5: Campaign domain (CRUD + ownership validation)

**Files:**
- Create: `internal/app/campaign/{status.go,store.go,service.go,handler.go,routes.go,service_test.go}`

**Interfaces:**
- Consumes: `gen`, `mailbox` (ownership: reuse `mailbox` store or a lookup), `list`, `auth`, `httpx`, `validate`.
- Produces: `campaign.CampaignStatus` typed enum; `campaign.NewService(Store, MailboxChecker, ListChecker)`; `Create(ctx, ws, CreateInput) (gen.Campaign, error)` with ownership + active-mailbox checks; `Get`, `List`, `Stats(ctx, id) (map[string]int64, error)`; sentinels `ErrNotFound`, `ErrMailboxNotActive`, `ErrValidation`. (Launch is Task 8.)

- [ ] **Step 1: Write `status.go`**

```go
package campaign

type CampaignStatus string

const (
	StatusDraft   CampaignStatus = "draft"
	StatusRunning CampaignStatus = "running"
	StatusPaused  CampaignStatus = "paused"
	StatusDone    CampaignStatus = "done"
)
```

- [ ] **Step 2: Write the failing service test** (fakes for store + checkers)

```go
package campaign

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/inroad/inroad/internal/platform/db/gen"
)

type fakeStore struct{}

func (fakeStore) Create(_ context.Context, _ uuid.UUID, in CreateInput) (gen.Campaign, error) {
	return gen.Campaign{ID: uuid.New(), Name: in.Name, Subject: in.Subject}, nil
}
func (fakeStore) Get(context.Context, uuid.UUID, uuid.UUID) (gen.Campaign, error) { return gen.Campaign{}, nil }
func (fakeStore) List(context.Context, uuid.UUID) ([]gen.Campaign, error)          { return nil, nil }
func (fakeStore) Stats(context.Context, uuid.UUID) (map[string]int64, error)       { return nil, nil }

type okChecker struct{ active bool }

func (o okChecker) MailboxActive(context.Context, uuid.UUID, uuid.UUID) (bool, error) { return o.active, nil }
func (o okChecker) ListExists(context.Context, uuid.UUID, uuid.UUID) (bool, error)    { return true, nil }

func TestCreateRejectsInactiveMailbox(t *testing.T) {
	svc := NewService(fakeStore{}, okChecker{active: false})
	_, err := svc.Create(context.Background(), uuid.New(), CreateInput{
		Name: "Q3", Subject: "Hi", BodyText: "hello", MailboxID: uuid.New(), ListID: uuid.New(),
	})
	if err != ErrMailboxNotActive {
		t.Fatalf("expected ErrMailboxNotActive, got %v", err)
	}
}

func TestCreateSucceeds(t *testing.T) {
	svc := NewService(fakeStore{}, okChecker{active: true})
	c, err := svc.Create(context.Background(), uuid.New(), CreateInput{
		Name: "Q3", Subject: "Hi", BodyText: "hello", MailboxID: uuid.New(), ListID: uuid.New(),
	})
	if err != nil || c.Name != "Q3" {
		t.Fatalf("Create: %v %+v", err, c)
	}
}
```

- [ ] **Step 3: Run to verify fail**

Run: `go test ./internal/app/campaign/` → FAIL.

- [ ] **Step 4: Implement `store.go`**

```go
package campaign

import (
	"context"

	"github.com/google/uuid"
	"github.com/inroad/inroad/internal/platform/db/gen"
)

type CreateInput struct {
	Name, Subject, BodyText, BodyHTML string
	MailboxID, ListID                 uuid.UUID
}

type Store interface {
	Create(ctx context.Context, ws uuid.UUID, in CreateInput) (gen.Campaign, error)
	Get(ctx context.Context, ws, id uuid.UUID) (gen.Campaign, error)
	List(ctx context.Context, ws uuid.UUID) ([]gen.Campaign, error)
	Stats(ctx context.Context, id uuid.UUID) (map[string]int64, error)
}

// Checker validates cross-domain references belong to the workspace.
type Checker interface {
	MailboxActive(ctx context.Context, ws, mailboxID uuid.UUID) (bool, error)
	ListExists(ctx context.Context, ws, listID uuid.UUID) (bool, error)
}

type PgStore struct{ q *gen.Queries }

func NewPgStore(q *gen.Queries) *PgStore { return &PgStore{q: q} }

func (s *PgStore) Create(ctx context.Context, ws uuid.UUID, in CreateInput) (gen.Campaign, error) {
	return s.q.CreateCampaign(ctx, gen.CreateCampaignParams{
		WorkspaceID: ws, Name: in.Name, MailboxID: in.MailboxID, ListID: in.ListID,
		Subject: in.Subject, BodyText: in.BodyText, BodyHtml: in.BodyHTML,
	})
}
func (s *PgStore) Get(ctx context.Context, ws, id uuid.UUID) (gen.Campaign, error) {
	return s.q.GetCampaign(ctx, gen.GetCampaignParams{ID: id, WorkspaceID: ws})
}
func (s *PgStore) List(ctx context.Context, ws uuid.UUID) ([]gen.Campaign, error) {
	return s.q.ListCampaigns(ctx, ws)
}
func (s *PgStore) Stats(ctx context.Context, id uuid.UUID) (map[string]int64, error) {
	rows, err := s.q.CountSendsByStatus(ctx, id)
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
> `Checker` is implemented by a small adapter over the `mailbox` and `list` stores wired in `cmd/inroad` (Task 9). Its `MailboxActive` also serves the ownership check (returns false if the mailbox isn't in the workspace).

- [ ] **Step 5: Implement `service.go`**

```go
package campaign

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/inroad/inroad/internal/platform/db/gen"
)

var (
	ErrNotFound         = errors.New("campaign not found")
	ErrMailboxNotActive = errors.New("mailbox not found or not active")
	ErrListMissing      = errors.New("list not found")
)

type Service struct {
	store   Store
	checker Checker
}

func NewService(store Store, checker Checker) *Service { return &Service{store: store, checker: checker} }

func (s *Service) Create(ctx context.Context, ws uuid.UUID, in CreateInput) (gen.Campaign, error) {
	active, err := s.checker.MailboxActive(ctx, ws, in.MailboxID)
	if err != nil {
		return gen.Campaign{}, err
	}
	if !active {
		return gen.Campaign{}, ErrMailboxNotActive
	}
	exists, err := s.checker.ListExists(ctx, ws, in.ListID)
	if err != nil {
		return gen.Campaign{}, err
	}
	if !exists {
		return gen.Campaign{}, ErrListMissing
	}
	return s.store.Create(ctx, ws, in)
}

func (s *Service) Get(ctx context.Context, ws, id uuid.UUID) (gen.Campaign, error) {
	return s.store.Get(ctx, ws, id)
}
func (s *Service) List(ctx context.Context, ws uuid.UUID) ([]gen.Campaign, error) {
	return s.store.List(ctx, ws)
}
func (s *Service) Stats(ctx context.Context, id uuid.UUID) (map[string]int64, error) {
	return s.store.Stats(ctx, id)
}
```

- [ ] **Step 6: Implement `handler.go` + `routes.go`**

`handler.go` — `POST /` (create, validated), `GET /`, `GET /{id}` (with stats). Launch route added in Task 8.
```go
package campaign

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/app/auth"
	"github.com/inroad/inroad/internal/platform/httpx"
	"github.com/inroad/inroad/internal/platform/validate"
)

type Handler struct {
	svc       *Service
	jwtSecret []byte
}

func NewHandler(svc *Service, jwtSecret []byte) *Handler { return &Handler{svc: svc, jwtSecret: jwtSecret} }

type createRequest struct {
	Name      string `json:"name" validate:"required,min=1,max=200"`
	MailboxID string `json:"mailbox_id" validate:"required,uuid"`
	ListID    string `json:"list_id" validate:"required,uuid"`
	Subject   string `json:"subject" validate:"required,min=1,max=500"`
	BodyText  string `json:"body_text"`
	BodyHTML  string `json:"body_html"`
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	ws, ok := wsID(w, r)
	if !ok {
		return
	}
	var req createRequest
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
	mid, _ := uuid.Parse(req.MailboxID)
	lid, _ := uuid.Parse(req.ListID)
	c, err := h.svc.Create(r.Context(), ws, CreateInput{
		Name: req.Name, Subject: req.Subject, BodyText: req.BodyText, BodyHTML: req.BodyHTML,
		MailboxID: mid, ListID: lid,
	})
	switch {
	case errors.Is(err, ErrMailboxNotActive):
		httpx.Error(w, http.StatusUnprocessableEntity, "mailbox not found or not active")
	case errors.Is(err, ErrListMissing):
		httpx.Error(w, http.StatusNotFound, "list not found")
	case err != nil:
		httpx.Error(w, http.StatusInternalServerError, "could not create campaign")
	default:
		httpx.JSON(w, http.StatusOK, toResponse(c, nil))
	}
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	ws, ok := wsID(w, r)
	if !ok {
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "bad id")
		return
	}
	c, err := h.svc.Get(r.Context(), ws, id)
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "not found")
		return
	}
	stats, _ := h.svc.Stats(r.Context(), id)
	httpx.JSON(w, http.StatusOK, toResponse(c, stats))
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	ws, ok := wsID(w, r)
	if !ok {
		return
	}
	cs, err := h.svc.List(r.Context(), ws)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "could not list")
		return
	}
	out := make([]campaignResponse, 0, len(cs))
	for _, c := range cs {
		out = append(out, toResponse(c, nil))
	}
	httpx.JSON(w, http.StatusOK, out)
}
```

`routes.go` (+ response mapper + `wsID` helper):
```go
package campaign

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/app/auth"
	"github.com/inroad/inroad/internal/platform/db/gen"
	"github.com/inroad/inroad/internal/platform/httpx"
)

func (h *Handler) Routes() http.Handler {
	r := chi.NewRouter()
	r.Use(auth.RequireAuth(h.jwtSecret))
	r.Post("/", h.create)
	r.Get("/", h.list)
	r.Get("/{id}", h.get)
	r.Post("/{id}/launch", h.launch) // implemented in Task 8
	return r
}

type campaignResponse struct {
	ID      string           `json:"id"`
	Name    string           `json:"name"`
	Subject string           `json:"subject"`
	Status  string           `json:"status"`
	Stats   map[string]int64 `json:"stats,omitempty"`
}

func toResponse(c gen.Campaign, stats map[string]int64) campaignResponse {
	return campaignResponse{ID: c.ID.String(), Name: c.Name, Subject: c.Subject, Status: c.Status, Stats: stats}
}

func wsID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	claims, ok := auth.UserFromContext(r.Context())
	if !ok {
		httpx.Error(w, http.StatusUnauthorized, "unauthorized")
		return uuid.Nil, false
	}
	id, err := uuid.Parse(claims.WorkspaceID)
	if err != nil {
		httpx.Error(w, http.StatusUnauthorized, "bad workspace")
		return uuid.Nil, false
	}
	return id, true
}
```
> `h.launch` is added in Task 8; until then it won't compile if referenced. To keep this task self-contained, add a temporary stub `func (h *Handler) launch(w http.ResponseWriter, r *http.Request) { httpx.Error(w, http.StatusNotImplemented, "not implemented") }` in `handler.go`, replaced in Task 8.

- [ ] **Step 7: Run tests + build**

Run: `go test ./internal/app/campaign/ && go build ./internal/app/campaign/` → PASS.

- [ ] **Step 8: Commit**

```bash
git add internal/app/campaign
git commit -m "feat: add campaign domain (create/get/list with ownership validation)"
```

---

## Task 6: Mail sender (go-mail)

**Files:**
- Create: `internal/platform/mail/sender.go`, `internal/platform/mail/sender_test.go`

**Interfaces:**
- Consumes: `vetAddr` (existing SSRF guard), `wneessen/go-mail`.
- Produces: `mail.Message` struct; `mail.NewNetSender(allowPrivate bool) *NetSender`; `(*NetSender) Send(cfg SMTPConfig, msg Message) (messageID string, err error)`.

- [ ] **Step 1: Write the failing test** (guard applies to Send too — no network for a blocked host)

```go
package mail

import (
	"testing"
	"time"
)

func TestSendRejectsLoopbackHost(t *testing.T) {
	s := &NetSender{Timeout: time.Second}
	_, err := s.Send(SMTPConfig{Host: "127.0.0.1", Port: 587, UseTLS: true, Username: "u", Password: "p"},
		Message{FromEmail: "a@x.com", To: "b@y.com", Subject: "hi", BodyText: "hello"})
	if err != ErrHostNotPermitted {
		t.Fatalf("expected ErrHostNotPermitted, got %v", err)
	}
}
```

- [ ] **Step 2: Run to verify fail**

Run: `go test ./internal/platform/mail/ -run TestSendRejects` → FAIL.

- [ ] **Step 3: Implement `sender.go`**

```go
package mail

import (
	"fmt"
	"net"
	"strconv"
	"time"

	gomail "github.com/wneessen/go-mail"
)

// Message is a single outbound email.
type Message struct {
	FromEmail, FromName string
	To                  string
	Subject             string
	BodyText, BodyHTML  string
	ListUnsubscribe     string // full URL for the List-Unsubscribe header + footer
}

// NetSender sends mail over SMTP, applying the same SSRF host vetting as the tester.
type NetSender struct {
	Timeout      time.Duration
	AllowPrivate bool
}

func NewNetSender(allowPrivate bool) *NetSender {
	return &NetSender{Timeout: 30 * time.Second, AllowPrivate: allowPrivate}
}

func (s *NetSender) Send(cfg SMTPConfig, msg Message) (string, error) {
	if _, err := vetAddr(cfg.Host, cfg.Port, allowedSMTPPorts, s.AllowPrivate); err != nil {
		return "", err
	}

	m := gomail.NewMsg()
	if err := m.FromFormat(msg.FromName, msg.FromEmail); err != nil {
		return "", fmt.Errorf("from: %w", err)
	}
	if err := m.To(msg.To); err != nil {
		return "", fmt.Errorf("to: %w", err)
	}
	m.Subject(msg.Subject)
	if msg.BodyText != "" {
		m.SetBodyString(gomail.TypeTextPlain, msg.BodyText)
	}
	if msg.BodyHTML != "" {
		if msg.BodyText != "" {
			m.AddAlternativeString(gomail.TypeTextHTML, msg.BodyHTML)
		} else {
			m.SetBodyString(gomail.TypeTextHTML, msg.BodyHTML)
		}
	}
	if msg.ListUnsubscribe != "" {
		m.SetGenHeader("List-Unsubscribe", "<"+msg.ListUnsubscribe+">")
		m.SetGenHeader("List-Unsubscribe-Post", "List-Unsubscribe=One-Click")
	}

	opts := []gomail.Option{
		gomail.WithPort(cfg.Port),
		gomail.WithUsername(cfg.Username),
		gomail.WithPassword(cfg.Password),
		gomail.WithSMTPAuth(gomail.SMTPAuthPlain),
		gomail.WithTimeout(s.Timeout),
	}
	if cfg.Port == 465 {
		opts = append(opts, gomail.WithSSLPort(false))
	} else if cfg.UseTLS {
		opts = append(opts, gomail.WithTLSPolicy(gomail.TLSMandatory))
	} else {
		opts = append(opts, gomail.WithTLSPolicy(gomail.NoTLS))
	}

	client, err := gomail.NewClient(cfg.Host, opts...)
	if err != nil {
		return "", fmt.Errorf("smtp client: %w", err)
	}
	if err := client.DialAndSend(m); err != nil {
		return "", fmt.Errorf("send: %w", err)
	}
	return m.GetMessageID(), nil
}

// ensure net import used (JoinHostPort available if needed by future callers)
var _ = net.JoinHostPort
var _ = strconv.Itoa
```
> If the installed go-mail API differs (option names move between versions), adapt to the installed version while preserving: SSRF vet first, PLAIN auth, TLS per port/UseTLS, and returning the generated Message-ID. Verify the exact option names with `go doc github.com/wneessen/go-mail` before implementing.

- [ ] **Step 4: Run test + build**

Run: `go test ./internal/platform/mail/ && go build ./internal/platform/mail/` → PASS (guard test passes without network).

- [ ] **Step 5: Commit**

```bash
git add internal/platform/mail go.mod go.sum
git commit -m "feat: add SMTP sender (go-mail) with SSRF vetting and List-Unsubscribe"
```

---

## Task 7: Suppression + unsubscribe (HMAC, public endpoint)

**Files:**
- Create: `internal/app/suppression/{token.go,store.go,handler.go,routes.go,token_test.go}`

**Interfaces:**
- Consumes: `gen` (AddSuppression/IsSuppressed), `httpx`.
- Produces: `suppression.MakeToken(secret []byte, workspaceID, email string) string`; `suppression.ParseToken(secret []byte, token string) (workspaceID, email string, ok bool)`; `suppression.NewStore(*gen.Queries)`; `suppression.NewHandler(secret []byte, store)` with `Routes()` mounting public `GET /{token}`.

- [ ] **Step 1: Write the failing token test**

```go
package suppression

import "testing"

func TestTokenRoundTrip(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	tok := MakeToken(secret, "ws-1", "Alice@Example.com")
	ws, email, ok := ParseToken(secret, tok)
	if !ok || ws != "ws-1" || email != "Alice@Example.com" {
		t.Fatalf("round-trip failed: %q %q %v", ws, email, ok)
	}
}

func TestTokenRejectsTamper(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	tok := MakeToken(secret, "ws-1", "a@b.com")
	if _, _, ok := ParseToken([]byte("different-secret-000000000000000"), tok); ok {
		t.Fatal("expected rejection under wrong secret")
	}
	if _, _, ok := ParseToken(secret, tok+"x"); ok {
		t.Fatal("expected rejection under tampered token")
	}
}
```

- [ ] **Step 2: Run to verify fail**

Run: `go test ./internal/app/suppression/` → FAIL.

- [ ] **Step 3: Implement `token.go`**

```go
// Package suppression handles the do-not-contact list and stateless unsubscribe.
package suppression

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"strings"
)

// MakeToken returns base64url(payload) + "." + base64url(HMAC(secret, payload)),
// where payload is "workspaceID:email". Stateless — no tokens table.
func MakeToken(secret []byte, workspaceID, email string) string {
	payload := workspaceID + ":" + email
	sig := sign(secret, payload)
	return b64(payload) + "." + b64(sig)
}

// ParseToken verifies the HMAC and returns the workspace id and email.
func ParseToken(secret []byte, token string) (string, string, bool) {
	dot := strings.IndexByte(token, '.')
	if dot < 0 {
		return "", "", false
	}
	payload, err := unb64(token[:dot])
	if err != nil {
		return "", "", false
	}
	gotSig, err := unb64(token[dot+1:])
	if err != nil {
		return "", "", false
	}
	if !hmac.Equal(gotSig, sign(secret, string(payload))) {
		return "", "", false
	}
	colon := strings.IndexByte(string(payload), ':')
	if colon < 0 {
		return "", "", false
	}
	return string(payload[:colon]), string(payload[colon+1:]), true
}

func sign(secret []byte, payload string) []byte {
	h := hmac.New(sha256.New, secret)
	h.Write([]byte(payload))
	return h.Sum(nil)
}
func b64(b any) string {
	switch v := b.(type) {
	case string:
		return base64.RawURLEncoding.EncodeToString([]byte(v))
	case []byte:
		return base64.RawURLEncoding.EncodeToString(v)
	}
	return ""
}
func unb64(s string) ([]byte, error) { return base64.RawURLEncoding.DecodeString(s) }
```

- [ ] **Step 4: Implement `store.go` + `handler.go` + `routes.go`**

`store.go`:
```go
package suppression

import (
	"context"

	"github.com/google/uuid"
	"github.com/inroad/inroad/internal/platform/db/gen"
)

type Store struct{ q *gen.Queries }

func NewStore(q *gen.Queries) *Store { return &Store{q: q} }

func (s *Store) Add(ctx context.Context, workspaceID uuid.UUID, email, reason string) error {
	return s.q.AddSuppression(ctx, gen.AddSuppressionParams{WorkspaceID: workspaceID, Email: email, Reason: reason})
}
```

`handler.go` + `routes.go`:
```go
package suppression

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/platform/httpx"
)

type Handler struct {
	secret []byte
	store  *Store
}

func NewHandler(secret []byte, store *Store) *Handler { return &Handler{secret: secret, store: store} }

// Routes mounts the PUBLIC unsubscribe endpoint (no auth).
func (h *Handler) Routes() http.Handler {
	r := chi.NewRouter()
	r.Get("/{token}", h.unsubscribe)
	return r
}

func (h *Handler) unsubscribe(w http.ResponseWriter, r *http.Request) {
	ws, email, ok := ParseToken(h.secret, chi.URLParam(r, "token"))
	if !ok {
		httpx.Error(w, http.StatusBadRequest, "invalid unsubscribe link")
		return
	}
	wsID, err := uuid.Parse(ws)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid unsubscribe link")
		return
	}
	_ = h.store.Add(r.Context(), wsID, email, "unsubscribe") // idempotent; ignore dup
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("<html><body><p>You have been unsubscribed. You will no longer receive emails.</p></body></html>"))
}
```

- [ ] **Step 5: Run tests + build**

Run: `go test ./internal/app/suppression/ && go build ./internal/app/suppression/` → PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/app/suppression
git commit -m "feat: add suppression + stateless HMAC unsubscribe endpoint"
```

---

## Task 8: coreapi send seam + inprocess (cap/ramp, decrypt) + campaign launch

**Files:**
- Modify: `internal/coreapi/coreapi.go`, `internal/coreapi/inprocess/inprocess.go`
- Create: `internal/coreapi/inprocess/sendjob.go`, `internal/coreapi/inprocess/ramp.go`, `internal/coreapi/inprocess/ramp_test.go`
- Modify: `internal/platform/queue/queue.go` (add `send:email`)
- Modify: `internal/app/campaign/{service.go,handler.go}` (add `Launch`)

**Interfaces:**
- Produces:
  - `coreapi.SendJob` + `coreapi.SendResult` structs (spec §6); `coreapi.Client` gains `GetSendJob(ctx, sendID string) (SendJob, error)` and `MarkSend(ctx, sendID string, res SendResult) error`.
  - `queue.TaskSendEmail = "send:email"`, `queue.SendEmailPayload{SendID string}`, `(*Client) EnqueueSend(sendID string) error`, `(*Client) EnqueueSendIn(sendID string, d time.Duration) error`.
  - `campaign.Service.Launch(ctx, ws, campaignID) (queued int, err error)`; the `campaign.Handler.launch` route enqueues.
  - `inprocess.effectiveCap(dailyCap, startCap, rampDays int, rampEnabled bool, ageDays int) int` (pure, unit-tested).

- [ ] **Step 1: Write the failing ramp test** (pure function)

`internal/coreapi/inprocess/ramp_test.go`:
```go
package inprocess

import "testing"

func TestEffectiveCap(t *testing.T) {
	// disabled ramp -> full cap
	if got := effectiveCap(50, 5, 30, false, 0); got != 50 {
		t.Errorf("disabled: got %d want 50", got)
	}
	// day 0 -> start cap
	if got := effectiveCap(50, 5, 30, true, 0); got != 5 {
		t.Errorf("day0: got %d want 5", got)
	}
	// day >= rampDays -> full cap
	if got := effectiveCap(50, 5, 30, true, 30); got != 50 {
		t.Errorf("dayN: got %d want 50", got)
	}
	// midpoint ~ halfway
	if got := effectiveCap(50, 10, 20, true, 10); got < 28 || got > 32 {
		t.Errorf("mid: got %d want ~30", got)
	}
}
```

- [ ] **Step 2: Run to verify fail**

Run: `go test ./internal/coreapi/inprocess/` → FAIL.

- [ ] **Step 3: Implement `ramp.go`**

```go
package inprocess

// effectiveCap returns today's allowed daily send count for a mailbox given its
// ramp schedule and age in days. Linear from startCap to dailyCap over rampDays.
func effectiveCap(dailyCap, startCap, rampDays int, rampEnabled bool, ageDays int) int {
	if !rampEnabled || ageDays >= rampDays || rampDays <= 0 {
		return dailyCap
	}
	if ageDays <= 0 {
		return startCap
	}
	return startCap + (dailyCap-startCap)*ageDays/rampDays
}
```

- [ ] **Step 4: Extend `coreapi.go`**

```go
package coreapi

import "context"

type Client interface {
	MailboxExists(ctx context.Context, id string) (bool, error)
	GetSendJob(ctx context.Context, sendID string) (SendJob, error)
	MarkSend(ctx context.Context, sendID string, res SendResult) error
}

// SendJob is everything the worker needs to send one email — including the
// decrypted SMTP password (in-memory only, never logged).
type SendJob struct {
	SendID            string
	Suppressed        bool
	EffectiveDailyCap int
	SentToday         int
	ToEmail           string
	FirstName         string
	Subject           string
	BodyText          string
	BodyHTML          string
	UnsubURL          string
	FromEmail         string
	FromName          string
	SMTPHost          string
	SMTPPort          int
	SMTPUsername      string
	SMTPPassword      string
	UseTLS            bool
}

type SendResult struct {
	Status    string // "sent" | "failed"
	MessageID string
	Err       string
}
```

- [ ] **Step 5: Implement `inprocess/sendjob.go`** (the join + decrypt + cap + unsub URL)

```go
package inprocess

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/app/suppression"
	"github.com/inroad/inroad/internal/coreapi"
)

func (c client) GetSendJob(ctx context.Context, sendID string) (coreapi.SendJob, error) {
	id, err := uuid.Parse(sendID)
	if err != nil {
		return coreapi.SendJob{}, err
	}
	b, err := c.q.GetSendBundle(ctx, id)
	if err != nil {
		return coreapi.SendJob{}, err
	}
	password, err := c.sealer.Open(b.SecretCiphertext)
	if err != nil {
		return coreapi.SendJob{}, err
	}
	suppressed, err := c.q.IsSuppressed(ctx, gen_IsSuppressedParams(b.WorkspaceID, b.ToEmail))
	if err != nil {
		return coreapi.SendJob{}, err
	}
	sentToday, err := c.q.CountSentToday(ctx, b.MailboxID)
	if err != nil {
		return coreapi.SendJob{}, err
	}
	ageDays := int(time.Since(b.MailboxCreatedAt.Time).Hours() / 24)
	cap := effectiveCap(int(b.DailyCap), int(b.RampStartCap), int(b.RampDays), b.RampEnabled, ageDays)
	token := suppression.MakeToken(c.jwtSecret, b.WorkspaceID.String(), b.ToEmail)

	return coreapi.SendJob{
		SendID:            sendID,
		Suppressed:        suppressed,
		EffectiveDailyCap: cap,
		SentToday:         int(sentToday),
		ToEmail:           b.ToEmail,
		FirstName:         b.FirstName,
		Subject:           b.Subject,
		BodyText:          b.BodyText,
		BodyHTML:          b.BodyHtml,
		UnsubURL:          c.publicURL + "/u/" + token,
		FromEmail:         b.FromEmail,
		FromName:          b.FromName,
		SMTPHost:          b.SmtpHost,
		SMTPPort:          int(b.SmtpPort),
		SMTPUsername:      b.SmtpUsername,
		SMTPPassword:      string(password),
		UseTLS:            b.UseTls,
	}, nil
}

func (c client) MarkSend(ctx context.Context, sendID string, res coreapi.SendResult) error {
	id, err := uuid.Parse(sendID)
	if err != nil {
		return err
	}
	return c.q.SetSendResult(ctx, gen_SetSendResultParams(id, res.Status, res.MessageID, res.Err))
}
```
> The `gen_IsSuppressedParams`/`gen_SetSendResultParams` shims stand in for the exact sqlc param-struct construction — replace with the real `gen.IsSuppressedParams{...}` / `gen.SetSendResultParams{...}` literals once `sqlc generate` has produced them (check field names in `gen/`).

- [ ] **Step 6: Extend `inprocess.New`** to carry sealer + jwtSecret + publicURL

Modify `internal/coreapi/inprocess/inprocess.go`:
```go
type client struct {
	q         *gen.Queries
	sealer    *crypto.Sealer
	jwtSecret []byte
	publicURL string
}

func New(q *gen.Queries, sealer *crypto.Sealer, jwtSecret []byte, publicURL string) coreapi.Client {
	return client{q: q, sealer: sealer, jwtSecret: jwtSecret, publicURL: publicURL}
}
```
(Keep the existing `MailboxExists`. Update `cmd/worker` and any caller to the new `New` signature in Task 9.)

- [ ] **Step 7: Add the queue task**

Append to `internal/platform/queue/queue.go`:
```go
const TaskSendEmail = "send:email"

type SendEmailPayload struct {
	SendID string `json:"send_id"`
}

func (c *Client) EnqueueSend(sendID string) error {
	b, err := json.Marshal(SendEmailPayload{SendID: sendID})
	if err != nil {
		return err
	}
	_, err = c.inner.Enqueue(asynq.NewTask(TaskSendEmail, b))
	return err
}

func (c *Client) EnqueueSendIn(sendID string, d time.Duration) error {
	b, err := json.Marshal(SendEmailPayload{SendID: sendID})
	if err != nil {
		return err
	}
	_, err = c.inner.Enqueue(asynq.NewTask(TaskSendEmail, b), asynq.ProcessIn(d))
	return err
}
```
(Add `"time"` to imports.)

- [ ] **Step 8: Implement campaign `Launch`**

Add to `campaign/store.go` interface + PgStore:
```go
// in Store interface:
EnqueueSends(ctx context.Context, ws, campaignID uuid.UUID) ([]uuid.UUID, error)
SetStatus(ctx context.Context, ws, id uuid.UUID, status CampaignStatus) error
```
```go
// in PgStore:
func (s *PgStore) EnqueueSends(ctx context.Context, ws, campaignID uuid.UUID) ([]uuid.UUID, error) {
	return s.q.EnqueueSends(ctx, gen.EnqueueSendsParams{Column1: campaignID, WorkspaceID: ws})
}
func (s *PgStore) SetStatus(ctx context.Context, ws, id uuid.UUID, status CampaignStatus) error {
	return s.q.SetCampaignStatus(ctx, gen.SetCampaignStatusParams{
		ID: id, WorkspaceID: ws, Status: string(status), LaunchedAt: nowTimestamptz(),
	})
}
```
> Verify the generated `EnqueueSendsParams` field names (the `$1`/`$2` positions from the query) and adjust; add a `nowTimestamptz()` helper returning `pgtype.Timestamptz{Time: <injected now>, Valid: true}` — but since scripts can't call time.Now at plan time, the implementer uses `time.Now()` here in real code.

Add to `campaign/service.go`:
```go
type Enqueuer interface{ EnqueueSend(sendID string) error }

// wire the enqueuer into Service
func (s *Service) Launch(ctx context.Context, ws, campaignID uuid.UUID, enq Enqueuer) (int, error) {
	c, err := s.store.Get(ctx, ws, campaignID)
	if err != nil {
		return 0, ErrNotFound
	}
	if c.Status != string(StatusDraft) {
		return 0, ErrAlreadyLaunched
	}
	ids, err := s.store.EnqueueSends(ctx, ws, campaignID)
	if err != nil {
		return 0, err
	}
	if len(ids) == 0 {
		return 0, ErrEmptyList
	}
	if err := s.store.SetStatus(ctx, ws, campaignID, StatusRunning); err != nil {
		return 0, err
	}
	for _, id := range ids {
		_ = enq.EnqueueSend(id.String())
	}
	return len(ids), nil
}
```
Add sentinels `ErrAlreadyLaunched`, `ErrEmptyList`.

Replace the stub `launch` handler (Task 5) in `campaign/handler.go`:
```go
func (h *Handler) launch(w http.ResponseWriter, r *http.Request) {
	ws, ok := wsID(w, r)
	if !ok {
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "bad id")
		return
	}
	n, err := h.svc.Launch(r.Context(), ws, id, h.enq)
	switch {
	case errors.Is(err, ErrNotFound):
		httpx.Error(w, http.StatusNotFound, "not found")
	case errors.Is(err, ErrAlreadyLaunched):
		httpx.Error(w, http.StatusConflict, "campaign already launched")
	case errors.Is(err, ErrEmptyList):
		httpx.Error(w, http.StatusUnprocessableEntity, "target list is empty")
	case err != nil:
		httpx.Error(w, http.StatusInternalServerError, "could not launch")
	default:
		httpx.JSON(w, http.StatusOK, map[string]int{"queued": n})
	}
}
```
Add `enq Enqueuer` field to `Handler` + `NewHandler(svc, jwtSecret, enq)`.

- [ ] **Step 9: sqlc regen + build + tests**

Run:
```bash
export PATH="$PATH:/c/Program Files/Go/bin:$HOME/go/bin"
sqlc generate
go build ./... 2>&1
go test ./internal/coreapi/... ./internal/app/campaign/ ./internal/platform/queue/
```
Expected: build OK; ramp + campaign tests PASS. (Fix any `gen.*Params` field-name mismatches flagged by the compiler.)

- [ ] **Step 10: Commit**

```bash
git add internal/coreapi internal/platform/queue internal/app/campaign internal/platform/db
git commit -m "feat: coreapi send seam (GetSendJob/MarkSend), ramp cap, and campaign launch"
```

---

## Task 9: Worker send handler + wiring + config

**Files:**
- Create: `internal/worker/sender/{sender.go,personalize.go,personalize_test.go}`
- Modify: `internal/worker/handlers.go`, `internal/platform/config/config.go`, `cmd/inroad/main.go`, `cmd/worker/main.go`

**Interfaces:**
- Consumes: `coreapi.Client`, `queue`, `mail.NetSender`.
- Produces: `sender.Handler(core coreapi.Client, sender *mail.NetSender, enq *queue.Client) func(ctx, *asynq.Task) error`; `worker.Register` gains sender+enq params; `config.Config.PublicURL`.

- [ ] **Step 1: Write the failing personalize test**

```go
package sender

import "testing"

func TestPersonalize(t *testing.T) {
	out := personalize("Hi {{first_name}} ({{email}})", "Alice", "alice@x.com")
	if out != "Hi Alice (alice@x.com)" {
		t.Fatalf("got %q", out)
	}
	// missing first name collapses cleanly
	if got := personalize("Hi {{first_name}}", "", "a@b.com"); got != "Hi there" {
		t.Fatalf("empty name: got %q", got)
	}
}
```

- [ ] **Step 2: Run to verify fail**

Run: `go test ./internal/worker/sender/` → FAIL.

- [ ] **Step 3: Implement `personalize.go`**

```go
package sender

import "strings"

// personalize substitutes {{first_name}} and {{email}}. An empty first name
// falls back to "there" so greetings read naturally.
func personalize(tmpl, firstName, email string) string {
	name := firstName
	if strings.TrimSpace(name) == "" {
		name = "there"
	}
	r := strings.NewReplacer("{{first_name}}", name, "{{email}}", email)
	return r.Replace(tmpl)
}
```

- [ ] **Step 4: Implement `sender.go`**

```go
// Package sender is the execution-plane email send handler.
package sender

import (
	"context"
	"encoding/json"
	"time"

	"github.com/hibiken/asynq"

	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/platform/mail"
	"github.com/inroad/inroad/internal/platform/queue"
)

// Handler returns an asynq handler for send:email tasks.
func Handler(core coreapi.Client, sender *mail.NetSender, enq *queue.Client) func(context.Context, *asynq.Task) error {
	return func(ctx context.Context, t *asynq.Task) error {
		var p queue.SendEmailPayload
		if err := json.Unmarshal(t.Payload(), &p); err != nil {
			return err
		}
		job, err := core.GetSendJob(ctx, p.SendID)
		if err != nil {
			return err
		}
		if job.Suppressed {
			return core.MarkSend(ctx, p.SendID, coreapi.SendResult{Status: "skipped"})
		}
		if job.SentToday >= job.EffectiveDailyCap {
			// Over today's cap: retry in the next daily window, leave status queued.
			return enq.EnqueueSendIn(p.SendID, 6*time.Hour)
		}

		subject := personalize(job.Subject, job.FirstName, job.ToEmail)
		bodyText := withUnsubText(personalize(job.BodyText, job.FirstName, job.ToEmail), job.UnsubURL)
		bodyHTML := ""
		if job.BodyHTML != "" {
			bodyHTML = withUnsubHTML(personalize(job.BodyHTML, job.FirstName, job.ToEmail), job.UnsubURL)
		}

		msgID, sendErr := sender.Send(
			mail.SMTPConfig{Host: job.SMTPHost, Port: job.SMTPPort, Username: job.SMTPUsername, Password: job.SMTPPassword, UseTLS: job.UseTLS},
			mail.Message{
				FromEmail: job.FromEmail, FromName: job.FromName, To: job.ToEmail,
				Subject: subject, BodyText: bodyText, BodyHTML: bodyHTML, ListUnsubscribe: job.UnsubURL,
			},
		)
		if sendErr != nil {
			return core.MarkSend(ctx, p.SendID, coreapi.SendResult{Status: "failed", Err: sendErr.Error()})
		}
		return core.MarkSend(ctx, p.SendID, coreapi.SendResult{Status: "sent", MessageID: msgID})
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
	return body + `<hr><p style="font-size:12px;color:#888">` +
		`<a href="` + url + `">Unsubscribe</a></p>`
}
```

- [ ] **Step 5: Register in `worker/handlers.go`**

```go
package worker

import (
	"github.com/hibiken/asynq"
	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/platform/mail"
	"github.com/inroad/inroad/internal/platform/queue"
	"github.com/inroad/inroad/internal/worker/sender"
	"github.com/inroad/inroad/internal/worker/warmup"
)

func Register(mux *asynq.ServeMux, core coreapi.Client, sndr *mail.NetSender, enq *queue.Client) {
	mux.HandleFunc(queue.TaskWarmupTick, warmup.Handler(core))
	mux.HandleFunc(queue.TaskSendEmail, sender.Handler(core, sndr, enq))
}
```

- [ ] **Step 6: Add `PublicURL` to config**

In `config.go`, add `PublicURL string` to `Config` and in `Load`:
```go
cfg.PublicURL = getenv("INROAD_PUBLIC_URL", "http://localhost:8080")
```
Add to `.env.example`: `INROAD_PUBLIC_URL=http://localhost:8080`.

- [ ] **Step 7: Wire `cmd/worker/main.go` and `cmd/inroad/main.go`**

`cmd/worker/main.go`: build the coreapi with the new deps and pass sender+enq:
```go
sealer, _ := crypto.NewSealer(cfg.MasterKey)
core := inprocess.New(gen.New(pool), sealer, cfg.JWTSecret, cfg.PublicURL)
sndr := mail.NewNetSender(cfg.MailAllowPrivateHosts)
enq := queue.NewClient(cfg.RedisAddr)
worker.Register(mux, core, sndr, enq)
```
(Add imports: crypto, mail, queue.)

`cmd/inroad/main.go`: construct the enqueue client + wire the new domain routes:
```go
enq := queue.NewClient(cfg.RedisAddr)
listSvc := list.NewService(list.NewPgStore(queries))
contactSvc := contact.NewService(contact.NewPgStore(queries), listSvc)
// checker adapts mailbox + list stores for campaign ownership checks
campaignSvc := campaign.NewService(campaign.NewPgStore(queries), ownershipChecker{mailboxes: mailbox.NewPgStore(queries), lists: listSvc})
suppStore := suppression.NewStore(queries)

router.Mount("/api/v1/lists", list.NewHandler(listSvc, cfg.JWTSecret).Routes())
router.Mount("/api/v1", contact.NewHandler(contactSvc, cfg.JWTSecret).Routes()) // /lists/{id}/import, /contacts
router.Mount("/api/v1/campaigns", campaign.NewHandler(campaignSvc, cfg.JWTSecret, enq).Routes())
router.Mount("/u", suppression.NewHandler(cfg.JWTSecret, suppStore).Routes())
```
Add an `ownershipChecker` type in `cmd/inroad` implementing `campaign.Checker` (`MailboxActive` queries the mailbox by id+workspace and checks `status='active'`; `ListExists` calls `listSvc.Get`). Add a `mailbox` store method `GetForWorkspace` if needed, or reuse `mailbox` service.

- [ ] **Step 8: Full build + tests**

Run:
```bash
export PATH="$PATH:/c/Program Files/Go/bin:$HOME/go/bin"
go build ./... && go test ./... 2>&1 | grep -vE '\[no test files\]'
go vet ./...
```
Expected: build OK, all unit tests PASS, vet clean.

- [ ] **Step 9: Commit**

```bash
git add internal/worker internal/platform/config cmd .env.example
git commit -m "feat: worker send handler, personalization, and full wiring"
```

---

## Task 10: OpenAPI contract + end-to-end verification

**Files:**
- Modify: `api/openapi.yaml`

**Interfaces:** documents the new endpoints for the next-increment frontend.

- [ ] **Step 1: Add paths + schemas to `api/openapi.yaml`**

Add (all bearer-secured except `/u/{token}`): `POST /lists`, `GET /lists`, `POST /lists/{id}/import` (multipart), `GET /contacts`, `POST /campaigns`, `GET /campaigns`, `GET /campaigns/{id}`, `POST /campaigns/{id}/launch`; schemas `List`, `ImportResult`, `Contact`, `Campaign`, `CreateCampaignRequest`. Validate with `npx --yes @redocly/cli lint api/openapi.yaml`.

- [ ] **Step 2: Integration test — import → launch → send via mailpit**

Add `mailpit` to `deploy/compose/docker-compose.dev.yml`:
```yaml
  mailpit:
    image: axllent/mailpit:latest
    ports: ["1025:1025", "8025:8025"]
```
Write `internal/app/campaign/launch_integration_test.go` (`//go:build integration`): seed a workspace + a mailbox pointed at `localhost:1025` (mailpit, no TLS), a list with 2 contacts, a campaign; call `Launch`; run the send handler inline; assert both `sends` rows reach `status='sent'` and mailpit's API (`http://localhost:8025/api/v1/messages`) shows 2 messages. (Mailpit accepts any auth, no TLS — set the mailbox `use_tls=false`, port 1025, which the SSRF guard allows only if `AllowPrivate` is true → set the tester/sender `AllowPrivate=true` in the test.)

- [ ] **Step 3: Run integration**

Run:
```bash
export PATH="$PATH:/c/Program Files/Go/bin:$HOME/go/bin"
docker compose -f deploy/compose/docker-compose.dev.yml up -d
go test -tags=integration ./internal/app/campaign/
```
Expected: PASS (2 messages delivered to mailpit).

- [ ] **Step 4: Live deliverability proof (manual, documented)**

Documented manual step (real provider): import a CSV with a real inbox → create a campaign on `ahmed@axomble.com` → launch → confirm the inbox receives the personalized email; click unsubscribe → verify `suppression` row → re-launch skips them.

- [ ] **Step 5: Commit**

```bash
git add api/openapi.yaml deploy/compose/docker-compose.dev.yml internal/app/campaign
git commit -m "feat: openapi for campaign send; mailpit integration test for import->launch->send"
```

---

## Self-Review Notes (author checklist — completed)

- **Spec coverage:** data model (Task 1), validation wrapper + per-route rules (Tasks 2–5), type-safe status enums (Tasks 1,5,8), CSV import (Task 4), campaign+ownership (Task 5), go-mail send (Task 6), unsubscribe HMAC + suppression (Task 7), coreapi seam + cap/ramp + launch (Task 8), worker send + personalization (Task 9), OpenAPI + e2e (Task 10). Scalability indexes/constraints in Task 1's DDL.
- **Type consistency:** `coreapi.SendJob`/`SendResult`, `queue.SendEmailPayload`/`TaskSendEmail`, `campaign.CampaignStatus`, `mail.Message`/`NetSender.Send`, `suppression.MakeToken`/`ParseToken` are used identically across producing/consuming tasks. `inprocess.New` signature change is propagated to `cmd/worker` (Task 9).
- **Known implementer caveats flagged inline:** exact `gen.*Params` field names must be checked against `sqlc generate` output (Tasks 8); go-mail option names verified against the installed version (Task 6); `time.Now()` used directly in real code (plan scripts can't call it).
- **Placeholder scan:** no TBD/TODO; the `gen_*Params` shims are explicitly marked to be replaced with real sqlc literals.

## Post-increment: Next

Following this, the **campaign-send UI** (import screen, campaign builder, launch + per-send status) on the design system, then reply/bounce detection and open/click tracking.
