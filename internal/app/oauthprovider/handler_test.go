package oauthprovider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/google/uuid"
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
