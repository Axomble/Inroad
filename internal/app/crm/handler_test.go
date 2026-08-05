package crm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/app/auth"
)

const testSecret = "0123456789abcdef0123456789abcdef"

// fakeStore is an in-memory Store. It exists so the handler tests exercise
// real routing, scope middleware and error mapping without a database; it
// deliberately does NOT implement integrationStore, so the board/events/move
// routes are out of scope here (they are covered by the integration tests).
type fakeStore struct {
	companies map[uuid.UUID]Company
	isDefault map[uuid.UUID]bool
	stages    map[uuid.UUID]bool
	stageDeal map[uuid.UUID]int64
	err       error
	primary   struct{ contact, email uuid.UUID }
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		companies: map[uuid.UUID]Company{},
		isDefault: map[uuid.UUID]bool{},
		stages:    map[uuid.UUID]bool{},
		stageDeal: map[uuid.UUID]int64{},
	}
}

func (f *fakeStore) ListCompanies(_ context.Context, _ uuid.UUID, page PageRequest) (Page[Company], error) {
	if page.Cursor != "" {
		if _, err := decodeCursor(cursorCompanies, page.Cursor, 2); err != nil {
			return Page[Company]{}, err
		}
	}
	out := Page[Company]{Items: []Company{}}
	for _, c := range f.companies {
		out.Items = append(out.Items, c)
	}
	if int32(len(out.Items)) >= page.Limit {
		out.NextCursor = encodeCursor(cursorCompanies, uuid.Nil.String(), "acme")
	}
	return out, f.err
}

func (f *fakeStore) GetCompany(_ context.Context, _, id uuid.UUID) (Company, error) {
	company, ok := f.companies[id]
	if !ok {
		return Company{}, ErrNotFound
	}
	return company, nil
}

func (f *fakeStore) CreateCompany(_ context.Context, ws uuid.UUID, in CompanyInput) (Company, error) {
	if f.err != nil {
		return Company{}, f.err
	}
	company := Company{ID: uuid.New(), WorkspaceID: ws, Name: in.Name, Domain: in.Domain, Currency: in.Currency}
	f.companies[company.ID] = company
	return company, nil
}

func (f *fakeStore) UpdateCompany(_ context.Context, _, id uuid.UUID, in CompanyInput) (Company, error) {
	company, ok := f.companies[id]
	if !ok {
		return Company{}, ErrNotFound
	}
	company.Name, company.Currency = in.Name, in.Currency
	f.companies[id] = company
	return company, f.err
}

func (f *fakeStore) DeleteCompany(_ context.Context, _, id uuid.UUID) error {
	if _, ok := f.companies[id]; !ok {
		return ErrNotFound
	}
	delete(f.companies, id)
	return f.err
}

func (f *fakeStore) ListPipelines(context.Context, uuid.UUID, int32) ([]Pipeline, error) {
	return []Pipeline{}, f.err
}
func (f *fakeStore) GetPipeline(context.Context, uuid.UUID, uuid.UUID) (Pipeline, error) {
	return Pipeline{}, ErrNotFound
}
func (f *fakeStore) CreatePipeline(_ context.Context, ws uuid.UUID, in PipelineInput) (Pipeline, error) {
	return Pipeline{ID: uuid.New(), WorkspaceID: ws, Name: in.Name}, f.err
}
func (f *fakeStore) UpdatePipeline(context.Context, uuid.UUID, uuid.UUID, PipelineInput) (Pipeline, error) {
	return Pipeline{}, ErrNotFound
}

func (f *fakeStore) PipelineIsDefault(_ context.Context, _, id uuid.UUID) (bool, error) {
	isDefault, ok := f.isDefault[id]
	if !ok {
		return false, ErrNotFound
	}
	return isDefault, nil
}
func (f *fakeStore) DeletePipeline(context.Context, uuid.UUID, uuid.UUID) error { return f.err }

func (f *fakeStore) CreateStage(context.Context, uuid.UUID, uuid.UUID, string, StageInput) (Stage, error) {
	return Stage{ID: uuid.New()}, f.err
}
func (f *fakeStore) UpdateStage(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, StageInput) (Stage, error) {
	return Stage{}, ErrNotFound
}
func (f *fakeStore) StageExists(_ context.Context, _, _, id uuid.UUID) (bool, error) {
	return f.stages[id], nil
}
func (f *fakeStore) CountStageDeals(_ context.Context, _, id uuid.UUID) (int64, error) {
	return f.stageDeal[id], nil
}
func (f *fakeStore) DeleteStage(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) error { return f.err }

func (f *fakeStore) ListDeals(context.Context, uuid.UUID, PageRequest) (Page[Deal], error) {
	return Page[Deal]{Items: []Deal{}}, f.err
}
func (f *fakeStore) GetDeal(context.Context, uuid.UUID, uuid.UUID) (Deal, error) {
	return Deal{}, ErrNotFound
}
func (f *fakeStore) CreateDeal(_ context.Context, ws uuid.UUID, in DealInput) (Deal, error) {
	return Deal{ID: uuid.New(), WorkspaceID: ws, Name: in.Name}, f.err
}
func (f *fakeStore) UpdateDeal(context.Context, uuid.UUID, uuid.UUID, DealInput) (Deal, error) {
	return Deal{}, ErrNotFound
}
func (f *fakeStore) DeleteDeal(context.Context, uuid.UUID, uuid.UUID) error { return f.err }

func (f *fakeStore) ListNotes(context.Context, uuid.UUID, Target, PageRequest) (Page[Note], error) {
	return Page[Note]{Items: []Note{}}, f.err
}
func (f *fakeStore) CreateNote(_ context.Context, ws uuid.UUID, in NoteInput) (Note, error) {
	return Note{ID: uuid.New(), WorkspaceID: ws, Title: in.Title, Body: in.Body}, f.err
}
func (f *fakeStore) UpdateNote(context.Context, uuid.UUID, uuid.UUID, string, string) (Note, error) {
	return Note{}, ErrNotFound
}
func (f *fakeStore) DeleteNote(context.Context, uuid.UUID, uuid.UUID) error { return f.err }

func (f *fakeStore) ListTasks(context.Context, uuid.UUID, Target, PageRequest) (Page[Task], error) {
	return Page[Task]{Items: []Task{}}, f.err
}
func (f *fakeStore) CreateTask(_ context.Context, ws uuid.UUID, in TaskInput) (Task, error) {
	return Task{ID: uuid.New(), WorkspaceID: ws, Title: in.Title, Status: in.Status}, f.err
}
func (f *fakeStore) UpdateTask(context.Context, uuid.UUID, uuid.UUID, TaskInput) (Task, error) {
	return Task{}, ErrNotFound
}
func (f *fakeStore) DeleteTask(context.Context, uuid.UUID, uuid.UUID) error { return f.err }

func (f *fakeStore) ListContactEmails(context.Context, uuid.UUID, uuid.UUID) ([]ContactEmail, error) {
	return []ContactEmail{}, f.err
}
func (f *fakeStore) AddContactEmail(_ context.Context, ws, contactID uuid.UUID, email string) (ContactEmail, error) {
	return ContactEmail{ID: uuid.New(), WorkspaceID: ws, ContactID: contactID, Email: email}, f.err
}
func (f *fakeStore) SetPrimaryContactEmail(_ context.Context, _, contactID, emailID uuid.UUID) error {
	f.primary.contact, f.primary.email = contactID, emailID
	return f.err
}

var _ Store = (*fakeStore)(nil)

// scopedVerifier authenticates as an API-key principal holding exactly the
// given scopes — the shape a machine credential has, and the only shape
// RequireScope actually attenuates (a session principal holds every scope).
type scopedVerifier struct {
	workspaceID uuid.UUID
	scopes      []string
}

func (v scopedVerifier) Verify(context.Context, *http.Request) (auth.Principal, bool, error) {
	return auth.Principal{
		UserID: uuid.NewString(), WorkspaceID: v.workspaceID.String(), Role: "member",
		Kind: auth.KindAPIKey, Scopes: v.scopes,
	}, true, nil
}

func newRouter(h *Handler, verifier auth.Verifier) http.Handler {
	r := chi.NewRouter()
	r.Use(auth.RequireAuth(verifier))
	r.Mount("/crm", h.Routes())
	return r
}

func sessionRouter(t *testing.T, h *Handler) (http.Handler, string) {
	t.Helper()
	token, err := auth.IssueToken([]byte(testSecret), auth.Claims{
		UserID: uuid.NewString(), WorkspaceID: uuid.NewString(), Role: "member", SessionID: uuid.NewString(),
	}, time.Hour)
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}
	return newRouter(h, auth.NewJWTVerifier([]byte(testSecret))), "Bearer " + token
}

func do(t *testing.T, h http.Handler, method, target, authz, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, target, http.NoBody)
	if body != "" {
		r = httptest.NewRequest(method, target, strings.NewReader(body))
	}
	if authz != "" {
		r.Header.Set("Authorization", authz)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// TestContactEmailWritesRequireContactsWriteScope is the regression for the
// privilege-escalation finding: contacts.email IS the send-path recipient, so
// a crm:write-only key must not be able to rewrite a contact's alias set and
// redirect a live campaign (or resume sending to a suppressed address).
func TestContactEmailWritesRequireContactsWriteScope(t *testing.T) {
	store := newFakeStore()
	handler := NewHandler(NewService(store))
	ws := uuid.New()
	contactID, emailID := uuid.NewString(), uuid.NewString()
	routes := []struct{ method, path, body string }{
		{http.MethodPost, "/crm/contacts/" + contactID + "/emails", `{"email":"attacker@evil.test"}`},
		{http.MethodPut, "/crm/contacts/" + contactID + "/emails/" + emailID + "/primary", ""},
	}

	crmOnly := newRouter(handler, scopedVerifier{workspaceID: ws, scopes: []string{auth.ScopeCRMRead, auth.ScopeCRMWrite}})
	for _, tc := range routes {
		if w := do(t, crmOnly, tc.method, tc.path, "x", tc.body); w.Code != http.StatusForbidden {
			t.Errorf("%s %s with crm:write only: want 403, got %d: %s", tc.method, tc.path, w.Code, w.Body.String())
		}
	}
	if store.primary.contact != uuid.Nil {
		t.Fatalf("a crm:write-only principal reached the primary-email write: %+v", store.primary)
	}

	both := newRouter(handler, scopedVerifier{workspaceID: ws,
		scopes: []string{auth.ScopeCRMRead, auth.ScopeCRMWrite, auth.ScopeContactsWrite}})
	for _, tc := range routes {
		if w := do(t, both, tc.method, tc.path, "x", tc.body); w.Code == http.StatusForbidden {
			t.Errorf("%s %s with both scopes: unexpected 403: %s", tc.method, tc.path, w.Body.String())
		}
	}
}

// TestCRMScopesGateReadsAndWrites proves the coarse gate still holds: a
// read-only key cannot mutate, and a key with neither scope reads nothing.
func TestCRMScopesGateReadsAndWrites(t *testing.T) {
	handler := NewHandler(NewService(newFakeStore()))
	readOnly := newRouter(handler, scopedVerifier{workspaceID: uuid.New(), scopes: []string{auth.ScopeCRMRead}})
	if w := do(t, readOnly, http.MethodGet, "/crm/companies", "x", ""); w.Code != http.StatusOK {
		t.Fatalf("GET companies with crm:read: want 200, got %d: %s", w.Code, w.Body.String())
	}
	if w := do(t, readOnly, http.MethodPost, "/crm/companies", "x", `{"name":"Acme","domain":"","owner_user_id":null,"annual_revenue_micros":null,"currency":"USD"}`); w.Code != http.StatusForbidden {
		t.Fatalf("POST companies with crm:read: want 403, got %d", w.Code)
	}
	none := newRouter(handler, scopedVerifier{workspaceID: uuid.New()})
	if w := do(t, none, http.MethodGet, "/crm/companies", "x", ""); w.Code != http.StatusForbidden {
		t.Fatalf("GET companies with no scope: want 403, got %d", w.Code)
	}
}

func TestUnauthenticatedIs401(t *testing.T) {
	handler := NewHandler(NewService(newFakeStore()))
	router, _ := sessionRouter(t, handler)
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/crm/companies"},
		{http.MethodPost, "/crm/companies"},
		{http.MethodGet, "/crm/deals"},
		{http.MethodDelete, "/crm/deals/" + uuid.NewString()},
	} {
		if w := do(t, router, tc.method, tc.path, "", ""); w.Code != http.StatusUnauthorized {
			t.Errorf("%s %s without a token: want 401, got %d", tc.method, tc.path, w.Code)
		}
	}
}

func TestMutationBodiesRejectUnknownFieldsAndTrailingJSON(t *testing.T) {
	handler := NewHandler(NewService(newFakeStore()))
	router, authz := sessionRouter(t, handler)
	for name, body := range map[string]string{
		"unknown field": `{"name":"Acme","currency":"USD","typo":true}`,
		"trailing json": `{"name":"Acme","currency":"USD"} {}`,
	} {
		t.Run(name, func(t *testing.T) {
			if w := do(t, router, http.MethodPost, "/crm/companies", authz, body); w.Code != http.StatusBadRequest {
				t.Fatalf("want 400, got %d: %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestErrorMapping(t *testing.T) {
	store := newFakeStore()
	defaultPipeline, otherPipeline := uuid.New(), uuid.New()
	store.isDefault[defaultPipeline], store.isDefault[otherPipeline] = true, false
	busyStage, emptyStage := uuid.New(), uuid.New()
	store.stages[busyStage], store.stages[emptyStage] = true, true
	store.stageDeal[busyStage] = 3
	router, authz := sessionRouter(t, NewHandler(NewService(store)))

	for name, tc := range map[string]struct {
		method, path, body string
		want               int
		wantBody           string
	}{
		"unknown company is 404": {
			http.MethodGet, "/crm/companies/" + uuid.NewString(), "", http.StatusNotFound, "",
		},
		"invalid uuid is 422": {
			http.MethodGet, "/crm/companies/not-a-uuid", "", http.StatusUnprocessableEntity, "",
		},
		"invalid company body is 422": {
			http.MethodPost, "/crm/companies", `{"name":"","currency":"USD"}`, http.StatusUnprocessableEntity, "",
		},
		"unknown pipeline delete is 404": {
			http.MethodDelete, "/crm/pipelines/" + uuid.NewString(), "", http.StatusNotFound, "",
		},
		"default pipeline delete is 409": {
			http.MethodDelete, "/crm/pipelines/" + defaultPipeline.String(), "", http.StatusConflict, "default pipeline cannot be deleted",
		},
		"stage with deals is 409": {
			http.MethodDelete, "/crm/pipelines/" + otherPipeline.String() + "/stages/" + busyStage.String(), "",
			http.StatusConflict, "3 deal(s)",
		},
		"unknown stage delete is 404": {
			http.MethodDelete, "/crm/pipelines/" + otherPipeline.String() + "/stages/" + uuid.NewString(), "",
			http.StatusNotFound, "",
		},
		"empty stage delete is 204": {
			http.MethodDelete, "/crm/pipelines/" + otherPipeline.String() + "/stages/" + emptyStage.String(), "",
			http.StatusNoContent, "",
		},
		"bad page limit is 422": {
			http.MethodGet, "/crm/companies?limit=0", "", http.StatusUnprocessableEntity, "limit must be",
		},
		"foreign cursor is 422": {
			http.MethodGet, "/crm/companies?cursor=" + encodeCursor(cursorDeals, "0", "1000", uuid.Nil.String()),
			"", http.StatusUnprocessableEntity, "cursor",
		},
	} {
		t.Run(name, func(t *testing.T) {
			w := do(t, router, tc.method, tc.path, authz, tc.body)
			if w.Code != tc.want {
				t.Fatalf("want %d, got %d: %s", tc.want, w.Code, w.Body.String())
			}
			if tc.wantBody != "" && !strings.Contains(w.Body.String(), tc.wantBody) {
				t.Fatalf("body %q does not mention %q", w.Body.String(), tc.wantBody)
			}
		})
	}
}

// TestListsCarryPaginationAndOmitWorkspaceID pins the wire contract: a page is
// {items, next_cursor} and no DTO leaks the tenant id back to the caller that
// already proved which tenant it is.
func TestListsCarryPaginationAndOmitWorkspaceID(t *testing.T) {
	store := newFakeStore()
	for i := 0; i < 2; i++ {
		if _, err := store.CreateCompany(context.Background(), uuid.New(), CompanyInput{Name: "Acme", Currency: "USD"}); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	router, authz := sessionRouter(t, NewHandler(NewService(store)))
	w := do(t, router, http.MethodGet, "/crm/companies?limit=2", authz, "")
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
	var page struct {
		Items      []map[string]any `json:"items"`
		NextCursor string           `json:"next_cursor"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if page.NextCursor == "" {
		t.Fatalf("a full page must carry next_cursor: %s", w.Body.String())
	}
	for _, item := range page.Items {
		if _, leaked := item["workspace_id"]; leaked {
			t.Fatalf("workspace_id leaked into a CRM DTO: %v", item)
		}
	}
}
