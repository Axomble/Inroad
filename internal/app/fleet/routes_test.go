package fleet

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/app/auth"
)

// The gate on this router is the whole reason it may return a worker id and an
// egress IP at all (see Routes), so it is asserted here rather than left to the
// composition root. Two principals are driven through the real mounted router:
// a member session, which must be refused everywhere, and a machine principal
// holding every scope in the vocabulary, which must ALSO be refused everywhere —
// scopes are the wrong currency for this surface and must not buy it.

// everyRoute is the full surface, so a route added later without a gate shows up
// as a failing case rather than as an untested one.
func everyRoute() []string {
	return []string{
		"/workers",
		"/mailboxes/" + uuid.NewString() + "/decisions",
		"/jobs",
	}
}

func handlerForRoutes() *Handler {
	return NewHandler(NewService(&fakeStore{}, WithClock(func() time.Time { return frozen })))
}

func requestAs(t *testing.T, v auth.Verifier, path string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/fleet"+path, http.NoBody)
	w := httptest.NewRecorder()
	mountedRouter(handlerForRoutes(), v).ServeHTTP(w, r)
	return w
}

// A member is a legitimate, authenticated user of this workspace and still must
// not read the fleet: worker ids and egress IPs are deployment infrastructure,
// and the settings screens that neighbour this one (API keys, connected apps,
// AI, webhooks) all draw the line in the same place.
func TestAMemberSessionIsRefusedEveryRoute(t *testing.T) {
	member := sessionVerifier{ws: uuid.New(), role: "member"}
	for _, path := range everyRoute() {
		if w := requestAs(t, member, path); w.Code != http.StatusForbidden {
			t.Errorf("GET %s as a member: status = %d, want 403: %s", path, w.Code, w.Body)
		}
	}
}

// An admin passes, and an owner (which ranks above admin) passes too. Without
// this the test above would also pass against a router that refused everyone.
func TestAdminAndOwnerSessionsAreAdmitted(t *testing.T) {
	for _, role := range []string{"admin", "owner"} {
		for _, path := range everyRoute() {
			if w := requestAs(t, sessionVerifier{ws: uuid.New(), role: role}, path); w.Code != http.StatusOK {
				t.Errorf("GET %s as %s: status = %d, want 200: %s", path, role, w.Code, w.Body)
			}
		}
	}
}

// machinePrincipal is an API-key/OAuth-shaped caller: every scope the server
// knows, and the EMPTY role both machine verifiers assign by construction
// (apikey/verifier.go and oauthprovider/verifier.go each say so, for exactly
// this reason).
type machinePrincipal struct{ ws uuid.UUID }

func (m machinePrincipal) Verify(_ context.Context, _ *http.Request) (auth.Principal, bool, error) {
	return auth.Principal{
		Kind:        auth.KindAPIKey,
		UserID:      uuid.NewString(),
		WorkspaceID: m.ws.String(),
		Scopes:      auth.AllScopes,
	}, true, nil
}

// THE LOAD-BEARING ONE. cmd/inroad mounts this router in the session-only group,
// so an api-key or OAuth token is rejected at authentication and never reaches
// here. This asserts the SECOND, independent barrier: even a machine principal
// that somehow authenticated, holding the entire scope vocabulary, still ranks 0
// against RequireRole and is refused. A scope cannot buy this surface.
func TestNoScopeGrantReachesTheFleetSurface(t *testing.T) {
	machine := machinePrincipal{ws: uuid.New()}
	for _, path := range everyRoute() {
		w := requestAs(t, machine, path)
		if w.Code != http.StatusForbidden {
			t.Errorf("GET %s as a machine principal holding every scope: status = %d, want 403. "+
				"This surface returns worker ids and egress IPs; a delegated credential must "+
				"never reach it: %s", path, w.Code, w.Body)
		}
	}
}

// Fails closed without any credential at all.
func TestUnauthenticatedIsRejected(t *testing.T) {
	for _, path := range everyRoute() {
		r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/fleet"+path, http.NoBody)
		w := httptest.NewRecorder()
		// A verifier that defers, i.e. "no credential presented".
		mountedRouter(handlerForRoutes(), deferringVerifier{}).ServeHTTP(w, r)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("GET %s with no credential: status = %d, want 401", path, w.Code)
		}
	}
}

type deferringVerifier struct{}

func (deferringVerifier) Verify(_ context.Context, _ *http.Request) (auth.Principal, bool, error) {
	return auth.Principal{}, false, nil
}
