package campaign

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
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/inroad/inroad/internal/app/auth"
	"github.com/inroad/inroad/internal/platform/db/gen"
)

// newAuthedRequest builds a request carrying a valid JWT for the given
// workspace, routed through auth.RequireAuth exactly as the protected router
// group in cmd/inroad does. path holds the chi route param placeholders;
// urlParam sets the resolved {id} on the request context so the handler's
// chi.URLParam(r, "id") lookup works without a full router mount.
func newAuthedRequest(t *testing.T, secret []byte, ws, campaignID uuid.UUID, method, body string) *http.Request {
	t.Helper()
	tok, err := auth.IssueToken(secret, auth.Claims{
		UserID: uuid.New().String(), WorkspaceID: ws.String(), Role: "owner", SessionID: uuid.New().String(),
	}, time.Hour)
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}
	req := httptest.NewRequestWithContext(context.Background(), method, "/campaigns/"+campaignID.String()+"/tracking", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", campaignID.String())
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	return req
}

// newAuthedEnrollmentsRequest builds a GET /campaigns/{id}/enrollments request
// carrying a valid JWT for the workspace and the resolved {id} on the chi route
// context (as the protected router group + mount would), with an optional raw
// query string (e.g. "limit=5&offset=10").
func newAuthedEnrollmentsRequest(t *testing.T, secret []byte, ws, campaignID uuid.UUID, rawQuery string) *http.Request {
	t.Helper()
	tok, err := auth.IssueToken(secret, auth.Claims{
		UserID: uuid.New().String(), WorkspaceID: ws.String(), Role: "owner", SessionID: uuid.New().String(),
	}, time.Hour)
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}
	url := "/campaigns/" + campaignID.String() + "/enrollments"
	if rawQuery != "" {
		url += "?" + rawQuery
	}
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, url, http.NoBody)
	req.Header.Set("Authorization", "Bearer "+tok)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", campaignID.String())
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
}

// TestListEnrollmentsMapsRows proves GET /campaigns/{id}/enrollments returns the
// contract's snake_case fields, RFC3339 replied_at, and JSON null for an
// enrollment whose reply hasn't been classified (nil reply_class/source, invalid
// replied_at). Workspace comes from the JWT.
func TestListEnrollmentsMapsRows(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	ws, id := uuid.New(), uuid.New()
	pos := "positive"
	lex := "lexicon"
	repliedTime := time.Date(2026, 7, 20, 15, 4, 5, 0, time.UTC)
	store := &fakeStore{
		campaigns: map[[2]uuid.UUID]gen.Campaign{{ws, id}: {ID: id, WorkspaceID: ws, Status: "running"}},
		enrollmentRows: []gen.ListCampaignEnrollmentsRow{
			{
				Email: "replied@example.com", FirstName: "Ada", Status: "stopped",
				ReplyClass: &pos, ReplySource: &lex,
				RepliedAt: pgtype.Timestamptz{Time: repliedTime, Valid: true},
			},
			{
				Email: "pending@example.com", FirstName: "Grace", Status: "active",
				ReplyClass: nil, ReplySource: nil,
				RepliedAt: pgtype.Timestamptz{Valid: false},
			},
		},
	}
	svc := NewService(store, okChecker{active: true})
	h := NewHandler(svc, &fakeEnqueuer{})

	req := newAuthedEnrollmentsRequest(t, secret, ws, id, "limit=5&offset=10")
	w := httptest.NewRecorder()
	auth.RequireAuth(auth.NewJWTVerifier(secret))(http.HandlerFunc(h.listEnrollments)).ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
	if store.listEnrollmentsLimit != 5 || store.listEnrollmentsOffset != 10 {
		t.Fatalf("query pagination not forwarded: limit=%d offset=%d", store.listEnrollmentsLimit, store.listEnrollmentsOffset)
	}

	// Decode into json.RawMessage-friendly maps so an explicit JSON null (rather
	// than a missing key or empty string) is observable for the nullable fields.
	var resp []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp) != 2 {
		t.Fatalf("want 2 rows, got %d: %s", len(resp), w.Body.String())
	}
	first := resp[0]
	if first["email"] != "replied@example.com" || first["first_name"] != "Ada" || first["status"] != "stopped" {
		t.Fatalf("row0 base fields wrong: %+v", first)
	}
	if first["reply_class"] != "positive" || first["reply_source"] != "lexicon" {
		t.Fatalf("row0 reply fields wrong: %+v", first)
	}
	if first["replied_at"] != repliedTime.Format(time.RFC3339) {
		t.Fatalf("row0 replied_at not RFC3339: got %v want %v", first["replied_at"], repliedTime.Format(time.RFC3339))
	}
	second := resp[1]
	for _, k := range []string{"reply_class", "reply_source", "replied_at"} {
		v, ok := second[k]
		if !ok {
			t.Fatalf("row1 %q key missing (contract requires the key present as null)", k)
		}
		if v != nil {
			t.Fatalf("row1 %q should be JSON null, got %v", k, v)
		}
	}
}

// TestListEnrollmentsCrossTenantIsNotFound proves a campaign id from another
// workspace 404s and never reaches the enrollment read.
func TestListEnrollmentsHandlerCrossTenantIsNotFound(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	otherWS, ws, id := uuid.New(), uuid.New(), uuid.New()
	store := &fakeStore{campaigns: map[[2]uuid.UUID]gen.Campaign{
		{otherWS, id}: {ID: id, WorkspaceID: otherWS, Status: "running"},
	}}
	svc := NewService(store, okChecker{active: true})
	h := NewHandler(svc, &fakeEnqueuer{})

	req := newAuthedEnrollmentsRequest(t, secret, ws, id, "")
	w := httptest.NewRecorder()
	auth.RequireAuth(auth.NewJWTVerifier(secret))(http.HandlerFunc(h.listEnrollments)).ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d: %s", w.Code, w.Body.String())
	}
	if store.listEnrollmentsCalls != 0 {
		t.Fatalf("expected store.ListEnrollments not called on cross-tenant id, got %d calls", store.listEnrollmentsCalls)
	}
}

// TestToggleTrackingFlipsFlag proves PUT /campaigns/{id}/tracking decodes the
// body and forwards its enabled value to the service, workspace-scoped from
// the JWT (never a request body / path-supplied workspace id).
func TestToggleTrackingFlipsFlag(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	ws, id := uuid.New(), uuid.New()
	store := &fakeStore{campaigns: map[[2]uuid.UUID]gen.Campaign{
		{ws, id}: {ID: id, WorkspaceID: ws, Status: "running"},
	}}
	svc := NewService(store, okChecker{active: true})
	h := NewHandler(svc, &fakeEnqueuer{})

	req := newAuthedRequest(t, secret, ws, id, http.MethodPut, `{"enabled":false}`)
	w := httptest.NewRecorder()
	auth.RequireAuth(auth.NewJWTVerifier(secret))(http.HandlerFunc(h.toggleTracking)).ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
	if store.setTrackingCalls != 1 || store.setTrackingWS != ws || store.setTrackingID != id || store.setTrackingEnabled != false {
		t.Fatalf("SetTracking call wrong: calls=%d ws=%v id=%v enabled=%v",
			store.setTrackingCalls, store.setTrackingWS, store.setTrackingID, store.setTrackingEnabled)
	}
	var resp map[string]bool
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp["tracking_enabled"] != false {
		t.Fatalf("response body wrong: %+v", resp)
	}
}

// TestToggleTrackingCrossTenantIsNotFound proves a campaign id from another
// workspace 404s instead of flipping (or leaking the existence of) another
// tenant's campaign.
func TestToggleTrackingCrossTenantIsNotFound(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	otherWS, ws, id := uuid.New(), uuid.New(), uuid.New()
	store := &fakeStore{campaigns: map[[2]uuid.UUID]gen.Campaign{
		{otherWS, id}: {ID: id, WorkspaceID: otherWS, Status: "running"},
	}}
	svc := NewService(store, okChecker{active: true})
	h := NewHandler(svc, &fakeEnqueuer{})

	req := newAuthedRequest(t, secret, ws, id, http.MethodPut, `{"enabled":true}`)
	w := httptest.NewRecorder()
	auth.RequireAuth(auth.NewJWTVerifier(secret))(http.HandlerFunc(h.toggleTracking)).ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d: %s", w.Code, w.Body.String())
	}
	if store.setTrackingCalls != 0 {
		t.Fatalf("expected store.SetTracking not called on cross-tenant id, got %d calls", store.setTrackingCalls)
	}
}

// TestToggleTrackingInvalidJSON400 proves a malformed body 400s before the
// service is ever called.
func TestToggleTrackingInvalidJSON400(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	ws, id := uuid.New(), uuid.New()
	store := &fakeStore{campaigns: map[[2]uuid.UUID]gen.Campaign{
		{ws, id}: {ID: id, WorkspaceID: ws, Status: "running"},
	}}
	svc := NewService(store, okChecker{active: true})
	h := NewHandler(svc, &fakeEnqueuer{})

	req := newAuthedRequest(t, secret, ws, id, http.MethodPut, `not-json`)
	w := httptest.NewRecorder()
	auth.RequireAuth(auth.NewJWTVerifier(secret))(http.HandlerFunc(h.toggleTracking)).ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d: %s", w.Code, w.Body.String())
	}
	if store.setTrackingCalls != 0 {
		t.Fatalf("expected store.SetTracking not called on invalid json, got %d calls", store.setTrackingCalls)
	}
}
