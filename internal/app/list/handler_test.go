package list

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
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/inroad/inroad/internal/app/auth"
)

// newAuthedRequest builds a request carrying a valid JWT for the workspace,
// with the resolved {id} on the chi route context — the same shape the
// protected router group in cmd/inroad produces.
func newAuthedRequest(t *testing.T, secret []byte, ws uuid.UUID, method, id, body string) *http.Request {
	t.Helper()
	tok, err := auth.IssueToken(secret, auth.Claims{
		UserID: uuid.New().String(), WorkspaceID: ws.String(), Role: "owner", SessionID: uuid.New().String(),
	}, time.Hour)
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}
	req := httptest.NewRequestWithContext(context.Background(), method, "/lists/"+id, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", id)
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
}

func serve(h http.HandlerFunc, secret []byte, req *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	auth.RequireAuth(auth.NewJWTVerifier(secret))(h).ServeHTTP(w, req)
	return w
}

func TestRenameHandlerStatusMapping(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	ws, id := uuid.New(), uuid.New()

	tests := map[string]struct {
		id       string
		body     string
		storeErr error
		want     int
	}{
		"renamed":      {id: id.String(), body: `{"name":"Renamed"}`, want: http.StatusOK},
		"bad id":       {id: "not-a-uuid", body: `{"name":"Renamed"}`, want: http.StatusBadRequest},
		"invalid json": {id: id.String(), body: `{`, want: http.StatusBadRequest},
		"empty name":   {id: id.String(), body: `{"name":""}`, want: http.StatusBadRequest},
		"not found":    {id: id.String(), body: `{"name":"Renamed"}`, storeErr: pgx.ErrNoRows, want: http.StatusNotFound},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			h := NewHandler(NewService(&fakeStore{renameErr: tc.storeErr}))
			req := newAuthedRequest(t, secret, ws, http.MethodPatch, tc.id, tc.body)
			w := serve(h.rename, secret, req)
			if w.Code != tc.want {
				t.Fatalf("status = %d, want %d (body %s)", w.Code, tc.want, w.Body.String())
			}
			if tc.want != http.StatusOK {
				return
			}
			var resp listResponse
			if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if resp.ID != id.String() || resp.Name != "Renamed" {
				t.Fatalf("response = %+v, want id=%s name=Renamed", resp, id)
			}
		})
	}
}

func TestDeleteHandlerStatusMapping(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	ws, id := uuid.New(), uuid.New()

	tests := map[string]struct {
		id       string
		storeErr error
		want     int
	}{
		"deleted":   {id: id.String(), want: http.StatusNoContent},
		"bad id":    {id: "not-a-uuid", want: http.StatusBadRequest},
		"not found": {id: id.String(), storeErr: pgx.ErrNoRows, want: http.StatusNotFound},
		"in use":    {id: id.String(), storeErr: &pgconn.PgError{Code: "23503"}, want: http.StatusConflict},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			h := NewHandler(NewService(&fakeStore{deleteErr: tc.storeErr}))
			req := newAuthedRequest(t, secret, ws, http.MethodDelete, tc.id, "")
			w := serve(h.delete, secret, req)
			if w.Code != tc.want {
				t.Fatalf("status = %d, want %d (body %s)", w.Code, tc.want, w.Body.String())
			}
		})
	}
}
