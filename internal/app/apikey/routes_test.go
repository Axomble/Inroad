package apikey

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/app/auth"
)

// fakeSessionVerifier stashes a fixed session principal so the routes tests can
// exercise the admin gate without minting a real JWT.
type fakeSessionVerifier struct{ role string }

func (f fakeSessionVerifier) Verify(_ context.Context, _ *http.Request) (auth.Principal, bool, error) {
	return auth.Principal{
		Kind:        auth.KindSession,
		Role:        f.role,
		UserID:      uuid.NewString(),
		WorkspaceID: uuid.NewString(),
	}, true, nil
}

// TestRoutesAdminGate proves api-key management is admin-gated: a non-admin
// session is 403 on every verb, while admin/owner pass the gate (never 403).
func TestRoutesAdminGate(t *testing.T) {
	body, _ := json.Marshal(createRequest{Name: "k", Scopes: []string{auth.ScopeContactsRead}})

	verbs := []struct {
		name   string
		method string
		target string
		body   []byte
	}{
		{"create", http.MethodPost, "/", body},
		{"list", http.MethodGet, "/", nil},
		{"revoke", http.MethodDelete, "/" + uuid.NewString(), nil},
	}
	roles := []struct {
		role    string
		allowed bool
	}{
		{"member", false},
		{"admin", true},
		{"owner", true},
	}

	for _, role := range roles {
		for _, v := range verbs {
			t.Run(role.role+"_"+v.name, func(t *testing.T) {
				h := NewHandler(NewService(newFakeStore()))
				srv := h.Routes(fakeSessionVerifier{role: role.role})

				var reader *bytes.Reader
				if v.body != nil {
					reader = bytes.NewReader(v.body)
				} else {
					reader = bytes.NewReader(nil)
				}
				req := httptest.NewRequest(v.method, v.target, reader)
				w := httptest.NewRecorder()
				srv.ServeHTTP(w, req)

				if role.allowed && w.Code == http.StatusForbidden {
					t.Fatalf("%s %s as %s: got 403, want the gate to pass", v.method, v.target, role.role)
				}
				if !role.allowed && w.Code != http.StatusForbidden {
					t.Fatalf("%s %s as %s: got %d, want 403", v.method, v.target, role.role, w.Code)
				}
			})
		}
	}
}
