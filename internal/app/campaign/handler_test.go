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
	req := httptest.NewRequest(method, "/campaigns/"+campaignID.String()+"/tracking", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", campaignID.String())
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	return req
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
	auth.RequireAuth(secret)(http.HandlerFunc(h.toggleTracking)).ServeHTTP(w, req)

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
	auth.RequireAuth(secret)(http.HandlerFunc(h.toggleTracking)).ServeHTTP(w, req)

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
	auth.RequireAuth(secret)(http.HandlerFunc(h.toggleTracking)).ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d: %s", w.Code, w.Body.String())
	}
	if store.setTrackingCalls != 0 {
		t.Fatalf("expected store.SetTracking not called on invalid json, got %d calls", store.setTrackingCalls)
	}
}
