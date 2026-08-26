package deadletter

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/app/auth"
)

// fakeAPIKeyVerifier stands in an API-KEY principal carrying exactly the given
// scopes. A session principal implicitly holds every scope, so it cannot show a
// gate is present; only an attenuated machine principal can.
type fakeAPIKeyVerifier struct {
	ws     uuid.UUID
	scopes []string
}

func (f fakeAPIKeyVerifier) Verify(_ context.Context, _ *http.Request) (auth.Principal, bool, error) {
	return auth.Principal{
		Kind:        auth.KindAPIKey,
		UserID:      uuid.NewString(),
		WorkspaceID: f.ws.String(),
		Role:        "member",
		Scopes:      f.scopes,
	}, true, nil
}

// scopedRouter mounts the domain behind an API-key principal holding scopes.
func scopedRouter(h *Handler, ws uuid.UUID, scopes []string) http.Handler {
	r := chi.NewRouter()
	r.Use(auth.RequireAuth(fakeAPIKeyVerifier{ws: ws, scopes: scopes}))
	r.Mount("/dead-letters", h.Routes())
	return r
}

// The scope gates are a security boundary — replaying a dead letter delivers
// mail — so they are asserted directly rather than left to the composition
// root. These tests drive an API-KEY principal (which holds exactly the scopes
// it was granted, unlike a session principal which implicitly holds all of
// them), so a missing gate shows up as a 200 where a 403 belongs.

// apiKeyRequest drives one call with a principal carrying exactly the given
// scopes, through the real mounted router.
func apiKeyRequest(t *testing.T, h *Handler, method, path string, ws uuid.UUID, scopes []string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, "/dead-letters"+path, http.NoBody)
	w := httptest.NewRecorder()
	scopedRouter(h, ws, scopes).ServeHTTP(w, r)
	return w
}

// Every route fails closed without authentication.
func TestUnauthenticatedIsRejected(t *testing.T) {
	_, _, h, _, row := handlerFixture(t)
	id := row.ID.String()

	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/"},
		{http.MethodGet, "/" + id},
		{http.MethodPost, "/" + id + "/replay"},
		{http.MethodPost, "/" + id + "/discard"},
	} {
		r := httptest.NewRequest(tc.method, "/dead-letters"+tc.path, http.NoBody)
		w := httptest.NewRecorder()
		authedRouter(h).ServeHTTP(w, r)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("%s %s without a token: status = %d, want 401", tc.method, tc.path, w.Code)
		}
	}
}

// The read routes need campaigns:read; the write routes need campaigns:SEND.
// A credential holding only campaigns:read (or even campaigns:write) must not be
// able to replay — replaying delivers mail — or discard, which permanently
// prevents a send from ever going out.
func TestScopeGates(t *testing.T) {
	_, enq, h, ws, row := handlerFixture(t)
	id := row.ID.String()

	readOnly := []string{"campaigns:read"}
	writeButNotSend := []string{"campaigns:read", "campaigns:write"}
	sender := []string{"campaigns:read", "campaigns:send"}

	t.Run("campaigns:read can list and get", func(t *testing.T) {
		if w := apiKeyRequest(t, h, http.MethodGet, "/", ws, readOnly); w.Code != http.StatusOK {
			t.Errorf("list: status = %d, want 200: %s", w.Code, w.Body)
		}
		if w := apiKeyRequest(t, h, http.MethodGet, "/"+id, ws, readOnly); w.Code != http.StatusOK {
			t.Errorf("get: status = %d, want 200: %s", w.Code, w.Body)
		}
	})

	t.Run("reading requires campaigns:read", func(t *testing.T) {
		if w := apiKeyRequest(t, h, http.MethodGet, "/", ws, []string{"contacts:read"}); w.Code != http.StatusForbidden {
			t.Errorf("status = %d, want 403", w.Code)
		}
	})

	t.Run("campaigns:write alone cannot replay or discard", func(t *testing.T) {
		for _, path := range []string{"/" + id + "/replay", "/" + id + "/discard"} {
			w := apiKeyRequest(t, h, http.MethodPost, path, ws, writeButNotSend)
			if w.Code != http.StatusForbidden {
				t.Errorf("POST %s: status = %d, want 403", path, w.Code)
			}
		}
		if enq.count() != 0 {
			t.Errorf("enqueued %d times from an unauthorized caller, want 0", enq.count())
		}
	})

	t.Run("campaigns:send can replay", func(t *testing.T) {
		if w := apiKeyRequest(t, h, http.MethodPost, "/"+id+"/replay", ws, sender); w.Code != http.StatusOK {
			t.Errorf("status = %d, want 200: %s", w.Code, w.Body)
		}
		if enq.count() != 1 {
			t.Errorf("enqueued %d times, want 1", enq.count())
		}
	})
}
