package webhook

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/app/auth"
)

// fakeVerifier authenticates every request as the given principal, so a
// route-level test can exercise the RequireRole gate with no session store.
type fakeVerifier struct{ p auth.Principal }

func (f fakeVerifier) Verify(context.Context, *http.Request) (auth.Principal, bool, error) {
	return f.p, true, nil
}

// serveAs drives one request through the same middleware chain cmd/inroad
// mounts this router behind: the session verifier, then Routes()' own role gate.
func serveAs(t *testing.T, role, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	store := &fakeStore{}
	h := NewHandler(newService(t, store, &fakeEnqueuer{}))
	principal := auth.Principal{
		UserID: uuid.NewString(), WorkspaceID: uuid.NewString(),
		Role: role, Kind: auth.KindSession,
	}

	var reader io.Reader = http.NoBody
	if body != "" {
		reader = strings.NewReader(body)
	}
	r := httptest.NewRequestWithContext(context.Background(), method, path, reader)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	auth.RequireAuth(fakeVerifier{p: principal})(h.Routes()).ServeHTTP(w, r)
	return w
}

// A webhook endpoint continuously streams workspace event payloads to an
// operator-chosen URL and mints an HMAC signing secret, which puts it in the
// same class as its three neighbours in the settings nav — API keys, connected
// apps and AI — all of which are RequireRole("admin"). This surface was the one
// that was not, and while it was API-only that was arguable; the UI added in
// this branch made it two clicks for any member.
//
// EVERY route is gated, reads included: the list carries no secret, but an
// endpoint's URL and event subscription are the sensitive part of it, and a
// member who can read them can also see where a workspace's data is being
// shipped.
func TestWebhookRoutesRequireAdmin(t *testing.T) {
	id := uuid.NewString()
	routes := []struct {
		method, path, body string
	}{
		{http.MethodGet, "/", ""},
		{http.MethodPost, "/", `{"url":"https://hook.example.test/x"}`},
		{http.MethodGet, "/" + id, ""},
		{http.MethodPatch, "/" + id, `{"active":false}`},
		{http.MethodDelete, "/" + id, ""},
		{http.MethodPost, "/" + id + "/rotate-secret", ""},
		{http.MethodPost, "/" + id + "/ping", ""},
		{http.MethodGet, "/" + id + "/deliveries", ""},
	}
	for _, rt := range routes {
		t.Run(rt.method+" "+rt.path, func(t *testing.T) {
			w := serveAs(t, "member", rt.method, rt.path, rt.body)
			if w.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403 for a member (body %s)", w.Code, w.Body.String())
			}
		})
	}
}

// The other half of the gate: an admin is not locked out. Asserting only the
// 403s would pass just as well with the whole router unreachable.
func TestWebhookRoutesAdmitAnAdmin(t *testing.T) {
	if w := serveAs(t, "admin", http.MethodGet, "/", ""); w.Code != http.StatusOK {
		t.Fatalf("admin list status = %d, want 200 (body %s)", w.Code, w.Body.String())
	}
	// An owner outranks an admin, so it must pass the same gate — the point of
	// ranking roles rather than comparing them.
	if w := serveAs(t, "owner", http.MethodGet, "/", ""); w.Code != http.StatusOK {
		t.Fatalf("owner list status = %d, want 200 (body %s)", w.Code, w.Body.String())
	}
}
