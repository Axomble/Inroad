package oauthprovider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/app/auth"
)

// fakeOwner is an injectable ResourceOwner: it returns a fixed resolution regardless
// of the request, so handler tests need no real session.
type fakeOwner struct {
	owner Owner
	ok    bool
	err   error
}

func (f fakeOwner) Resolve(context.Context, *http.Request) (Owner, bool, error) {
	return f.owner, f.ok, f.err
}

// fakeVerifier is an injectable auth.Verifier that always authenticates a request as
// the given principal, so a route-level test can exercise the RequireAuth + RequireRole
// gate without a real session store.
type fakeVerifier struct{ p auth.Principal }

func (f fakeVerifier) Verify(context.Context, *http.Request) (auth.Principal, bool, error) {
	return f.p, true, nil
}

// cappingThrottle is a deterministic fake rate-limit middleware: it passes the first
// `limit` requests through and answers every request beyond that with a 429, so a test
// can trip the register throttle without Redis or a clock.
func cappingThrottle(limit int) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		var seen int
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			seen++
			if seen > limit {
				w.WriteHeader(http.StatusTooManyRequests)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// TestRegisterRateLimitedReturns429 drives POST /oauth2/register past the injected
// per-IP cap as an authenticated admin and asserts the over-cap request is a
// deterministic 429 (the throttle sits OUTERMOST, ahead of the session/admin gate).
func TestRegisterRateLimitedReturns429(t *testing.T) {
	h, _ := newTestHandler(fakeOwner{})
	admin := auth.Principal{
		UserID:      uuid.NewString(),
		WorkspaceID: uuid.NewString(),
		Role:        "admin",
		Kind:        auth.KindSession,
	}
	const limit = 2
	srv := h.Routes(fakeVerifier{p: admin}, cappingThrottle(limit))

	body := `{"client_name":"Acme","redirect_uris":["` + testRedirectURI + `"],"scope":"contacts:read"}`
	send := func() int {
		r := httptest.NewRequest(http.MethodPost, "/register", strings.NewReader(body))
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, r)
		return w.Code
	}
	for i := 0; i < limit; i++ {
		if code := send(); code != http.StatusCreated {
			t.Fatalf("request %d under the cap must register (201), got %d", i+1, code)
		}
	}
	if code := send(); code != http.StatusTooManyRequests {
		t.Fatalf("request over the cap must be 429, got %d", code)
	}
}

// TestRegisterRequiresAdmin proves an authenticated NON-admin (member) cannot register a
// client: the RequireRole("admin") gate answers 403 before the handler runs.
func TestRegisterRequiresAdmin(t *testing.T) {
	h, _ := newTestHandler(fakeOwner{})
	member := auth.Principal{
		UserID:      uuid.NewString(),
		WorkspaceID: uuid.NewString(),
		Role:        "member",
		Kind:        auth.KindSession,
	}
	srv := h.Routes(fakeVerifier{p: member}, nil)
	body := `{"client_name":"Acme","redirect_uris":["` + testRedirectURI + `"],"scope":"contacts:read"}`
	r := httptest.NewRequest(http.MethodPost, "/register", strings.NewReader(body))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("a non-admin must be 403, got %d", w.Code)
	}
}

func newTestHandler(owner ResourceOwner) (*Handler, *fakeStore) {
	f := newFakeStore()
	return NewHandler(NewService(f, testPublicURL), owner), f
}

func TestAuthorizeHandlerUnknownClientNoOpenRedirect(t *testing.T) {
	h, _ := newTestHandler(fakeOwner{})
	// An unknown client with an attacker-controlled redirect_uri MUST NOT redirect.
	q := url.Values{
		"response_type":         {"code"},
		"client_id":             {"inrc_unknown"},
		"redirect_uri":          {"https://evil.example.com/steal"},
		"scope":                 {"contacts:read"},
		"code_challenge":        {testChallenge},
		"code_challenge_method": {"S256"},
	}
	r := httptest.NewRequest(http.MethodGet, "/oauth2/authorize?"+q.Encode(), http.NoBody)
	w := httptest.NewRecorder()
	h.authorize(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", w.Code)
	}
	if loc := w.Header().Get("Location"); loc != "" {
		t.Fatalf("must NOT redirect on an unknown client, got Location=%s", loc)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("want an html error page, got %s", ct)
	}
}

func TestAuthorizeHandlerUnauthenticatedRedirectsToLogin(t *testing.T) {
	h, f := newTestHandler(fakeOwner{ok: false}) // no session
	c := mustRegister(t, h.svc, RegisterInput{ClientName: "Acme", RedirectURIs: []string{testRedirectURI}, Scope: "contacts:read"})
	_ = f
	q := url.Values{
		"response_type":         {"code"},
		"client_id":             {c.Client.ClientID},
		"redirect_uri":          {testRedirectURI},
		"scope":                 {"contacts:read"},
		"state":                 {"st-9"},
		"code_challenge":        {testChallenge},
		"code_challenge_method": {"S256"},
	}
	r := httptest.NewRequest(http.MethodGet, "/oauth2/authorize?"+q.Encode(), http.NoBody)
	w := httptest.NewRecorder()
	h.authorize(w, r)

	if w.Code != http.StatusFound {
		t.Fatalf("want 302, got %d", w.Code)
	}
	loc := w.Header().Get("Location")
	if !strings.HasPrefix(loc, testPublicURL+loginPath) {
		t.Fatalf("unauthenticated authorize must redirect to login, got %s", loc)
	}
}

func TestAuthorizeHandlerAuthenticatedRedirectsToConsent(t *testing.T) {
	owner := Owner{UserID: uuid.New(), WorkspaceID: uuid.New()}
	h, _ := newTestHandler(fakeOwner{owner: owner, ok: true})
	c := mustRegister(t, h.svc, RegisterInput{ClientName: "Acme", RedirectURIs: []string{testRedirectURI}, Scope: "contacts:read"})
	q := url.Values{
		"response_type":         {"code"},
		"client_id":             {c.Client.ClientID},
		"redirect_uri":          {testRedirectURI},
		"scope":                 {"contacts:read"},
		"code_challenge":        {testChallenge},
		"code_challenge_method": {"S256"},
	}
	r := httptest.NewRequest(http.MethodGet, "/oauth2/authorize?"+q.Encode(), http.NoBody)
	w := httptest.NewRecorder()
	h.authorize(w, r)

	if w.Code != http.StatusFound {
		t.Fatalf("want 302, got %d", w.Code)
	}
	if loc := w.Header().Get("Location"); !strings.HasPrefix(loc, testPublicURL+consentPath) {
		t.Fatalf("authenticated authorize must hand off to consent, got %s", loc)
	}
}
