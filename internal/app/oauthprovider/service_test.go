package oauthprovider

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/inroad/inroad/internal/app/auth"
)

const (
	testRedirectURI = "https://app.example.com/cb"
	testChallenge   = "0123456789012345678901234567890123456789012" // 43 unreserved chars
	testPublicURL   = "https://inroad.example.com"
)

func newTestService() (*Service, *fakeStore) {
	f := newFakeStore()
	s := NewService(f, testPublicURL)
	return s, f
}

// registerConfidentialOrPublic registers a client and returns it, failing the test on
// error.
func mustRegister(t *testing.T, s *Service, in RegisterInput) RegisterResult {
	t.Helper()
	res, err := s.RegisterClient(context.Background(), in)
	if err != nil {
		t.Fatalf("RegisterClient: %v", err)
	}
	return res
}

func baseAuthorize(clientID string) AuthorizeInput {
	return AuthorizeInput{
		ResponseType:        "code",
		ClientID:            clientID,
		RedirectURI:         testRedirectURI,
		Scope:               "contacts:read",
		State:               "st-123",
		CodeChallenge:       testChallenge,
		CodeChallengeMethod: "S256",
		RawQuery:            "response_type=code&client_id=" + clientID,
	}
}

// --- Registration ------------------------------------------------------------

func TestRegisterPublicByDefault(t *testing.T) {
	s, _ := newTestService()
	res := mustRegister(t, s, RegisterInput{
		ClientName:   "Acme",
		RedirectURIs: []string{testRedirectURI},
		Scope:        "contacts:read lists:read",
	})
	if res.ClientSecret != "" {
		t.Fatal("public client must not receive a secret")
	}
	if res.Client.ClientType != clientTypePublic || res.Client.TokenEndpointAuthMethod != authMethodNone {
		t.Fatalf("want public/none, got %s/%s", res.Client.ClientType, res.Client.TokenEndpointAuthMethod)
	}
	if !strings.HasPrefix(res.Client.ClientID, "inrc_") {
		t.Fatalf("client_id not prefixed: %s", res.Client.ClientID)
	}
}

func TestRegisterConfidentialReturnsSecretOnceAndStoresHash(t *testing.T) {
	s, f := newTestService()
	res := mustRegister(t, s, RegisterInput{
		ClientName:              "Acme",
		RedirectURIs:            []string{testRedirectURI},
		Scope:                   "contacts:read",
		TokenEndpointAuthMethod: "client_secret_basic",
	})
	if res.ClientSecret == "" {
		t.Fatal("confidential client must receive a secret once")
	}
	if res.Client.ClientType != clientTypeConfidential {
		t.Fatalf("want confidential, got %s", res.Client.ClientType)
	}
	stored := f.clients[res.Client.ClientID]
	want := sha256.Sum256([]byte(res.ClientSecret))
	if !bytes.Equal(stored.ClientSecretHash, want[:]) {
		t.Fatal("stored hash must be sha256(secret), and the raw secret must not be stored")
	}
	if bytes.Contains(stored.ClientSecretHash, []byte(res.ClientSecret)) {
		t.Fatal("raw secret must never be persisted")
	}
}

func TestRegisterRejectsNonGrantableScopes(t *testing.T) {
	s, _ := newTestService()
	for _, sc := range []string{auth.ScopeCampaignsSend, auth.ScopeCampaignsWrite, auth.ScopeMailboxesWrite, "admin:everything"} {
		_, err := s.RegisterClient(context.Background(), RegisterInput{
			ClientName:   "Acme",
			RedirectURIs: []string{testRedirectURI},
			Scope:        "contacts:read " + sc,
		})
		if !errors.Is(err, ErrScopeNotGrantable) {
			t.Fatalf("scope %q: want ErrScopeNotGrantable, got %v", sc, err)
		}
	}
}

func TestRegisterRejectsBadRedirectURIs(t *testing.T) {
	s, _ := newTestService()
	for _, u := range []string{"javascript:alert(1)", "http://evil.example.com/cb", "https://x/cb#f"} {
		_, err := s.RegisterClient(context.Background(), RegisterInput{
			ClientName:   "Acme",
			RedirectURIs: []string{u},
			Scope:        "contacts:read",
		})
		if !errors.Is(err, ErrInvalidRedirectURI) {
			t.Fatalf("redirect %q: want ErrInvalidRedirectURI, got %v", u, err)
		}
	}
	if _, err := s.RegisterClient(context.Background(), RegisterInput{ClientName: "Acme", Scope: "contacts:read"}); !errors.Is(err, ErrInvalidRedirectURI) {
		t.Fatalf("no redirect_uri: want ErrInvalidRedirectURI, got %v", err)
	}
}

func TestRegisterRejectsImplicitGrantAndResponseType(t *testing.T) {
	s, _ := newTestService()
	if _, err := s.RegisterClient(context.Background(), RegisterInput{
		ClientName: "Acme", RedirectURIs: []string{testRedirectURI}, Scope: "contacts:read",
		ResponseTypes: []string{"token"},
	}); !errors.Is(err, ErrValidation) {
		t.Fatalf("response_type=token: want ErrValidation, got %v", err)
	}
	if _, err := s.RegisterClient(context.Background(), RegisterInput{
		ClientName: "Acme", RedirectURIs: []string{testRedirectURI}, Scope: "contacts:read",
		GrantTypes: []string{"implicit"},
	}); !errors.Is(err, ErrValidation) {
		t.Fatalf("grant=implicit: want ErrValidation, got %v", err)
	}
}

func TestRegisterRetriesOnClientIDCollision(t *testing.T) {
	s, f := newTestService()
	f.createClientErr = &pgconn.PgError{Code: "23505"} // first CreateClient collides, retry succeeds
	res := mustRegister(t, s, RegisterInput{
		ClientName: "Acme", RedirectURIs: []string{testRedirectURI}, Scope: "contacts:read",
	})
	if res.Client.ClientID == "" {
		t.Fatal("expected a client after the collision retry")
	}
}

// --- Authorize: no-redirect errors ------------------------------------------

func TestAuthorizeUnknownClientRenders(t *testing.T) {
	s, _ := newTestService()
	res, err := s.Authorize(context.Background(), baseAuthorize("inrc_nope"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != OutcomeError {
		t.Fatalf("unknown client must render (not redirect), got %v", res)
	}
}

func TestAuthorizeRevokedClientRenders(t *testing.T) {
	s, f := newTestService()
	c := mustRegister(t, s, RegisterInput{ClientName: "Acme", RedirectURIs: []string{testRedirectURI}, Scope: "contacts:read"})
	client := f.clients[c.Client.ClientID]
	client.RevokedAt = pgTime(time.Now())
	f.clients[c.Client.ClientID] = client

	res, _ := s.Authorize(context.Background(), baseAuthorize(c.Client.ClientID))
	if res.Outcome != OutcomeError {
		t.Fatalf("revoked client must render, got %v", res)
	}
}

func TestAuthorizeRedirectURIMismatchRendersNeverRedirects(t *testing.T) {
	s, _ := newTestService()
	c := mustRegister(t, s, RegisterInput{ClientName: "Acme", RedirectURIs: []string{testRedirectURI}, Scope: "contacts:read"})
	for _, bad := range []string{
		testRedirectURI + "/",
		testRedirectURI + "?x=1",
		"https://evil.example.com/cb",
		"http://app.example.com/cb",
		"",
	} {
		in := baseAuthorize(c.Client.ClientID)
		in.RedirectURI = bad
		res, err := s.Authorize(context.Background(), in)
		if err != nil {
			t.Fatal(err)
		}
		if res.Outcome != OutcomeError {
			t.Fatalf("redirect_uri %q must render an error, not redirect (got %q)", bad, res.RedirectTo)
		}
	}
}

// --- Authorize: redirect-based errors (state echoed) ------------------------

func assertErrRedirect(t *testing.T, res AuthorizeResult, wantErr, wantState string) {
	t.Helper()
	if res.Outcome != OutcomeRedirect {
		t.Fatalf("want redirect, got %+v", res)
	}
	u, err := url.Parse(res.RedirectTo)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(res.RedirectTo, testRedirectURI) {
		t.Fatalf("error must redirect to the client redirect_uri, got %s", res.RedirectTo)
	}
	if got := u.Query().Get("error"); got != wantErr {
		t.Fatalf("error=%q want %q", got, wantErr)
	}
	if got := u.Query().Get("state"); got != wantState {
		t.Fatalf("state=%q want %q (must be echoed)", got, wantState)
	}
}

func TestAuthorizeRejectsNonCodeResponseType(t *testing.T) {
	s, _ := newTestService()
	c := mustRegister(t, s, RegisterInput{ClientName: "Acme", RedirectURIs: []string{testRedirectURI}, Scope: "contacts:read"})
	in := baseAuthorize(c.Client.ClientID)
	in.ResponseType = "token"
	res, _ := s.Authorize(context.Background(), in)
	assertErrRedirect(t, res, "unsupported_response_type", "st-123")
}

func TestAuthorizeRequiresPKCES256(t *testing.T) {
	s, _ := newTestService()
	c := mustRegister(t, s, RegisterInput{ClientName: "Acme", RedirectURIs: []string{testRedirectURI}, Scope: "contacts:read"})
	cases := []struct {
		name              string
		challenge, method string
	}{
		{"missing challenge", "", "S256"},
		{"missing method", testChallenge, ""},
		{"plain method", testChallenge, "plain"},
		{"short challenge", "tooshort", "S256"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := baseAuthorize(c.Client.ClientID)
			in.CodeChallenge, in.CodeChallengeMethod = tc.challenge, tc.method
			res, _ := s.Authorize(context.Background(), in)
			assertErrRedirect(t, res, "invalid_request", "st-123")
		})
	}
}

func TestAuthorizeScopeMustSubsetClient(t *testing.T) {
	s, _ := newTestService()
	// client registered for contacts:read ONLY
	c := mustRegister(t, s, RegisterInput{ClientName: "Acme", RedirectURIs: []string{testRedirectURI}, Scope: "contacts:read"})
	in := baseAuthorize(c.Client.ClientID)
	in.Scope = "contacts:read lists:read" // lists:read grantable but NOT registered for this client
	res, _ := s.Authorize(context.Background(), in)
	assertErrRedirect(t, res, "invalid_scope", "st-123")
}

func TestAuthorizeEmptyScopeRejected(t *testing.T) {
	s, _ := newTestService()
	c := mustRegister(t, s, RegisterInput{ClientName: "Acme", RedirectURIs: []string{testRedirectURI}, Scope: "contacts:read"})
	in := baseAuthorize(c.Client.ClientID)
	in.Scope = ""
	res, _ := s.Authorize(context.Background(), in)
	assertErrRedirect(t, res, "invalid_scope", "st-123")
}

// --- Authorize: unauthenticated -> login redirect ---------------------------

func TestAuthorizeUnauthenticatedRedirectsToLogin(t *testing.T) {
	s, _ := newTestService()
	c := mustRegister(t, s, RegisterInput{ClientName: "Acme", RedirectURIs: []string{testRedirectURI}, Scope: "contacts:read"})
	in := baseAuthorize(c.Client.ClientID) // Owner nil
	res, err := s.Authorize(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != OutcomeRedirect || !strings.HasPrefix(res.RedirectTo, testPublicURL+loginPath) {
		t.Fatalf("want login redirect, got %s", res.RedirectTo)
	}
	u, _ := url.Parse(res.RedirectTo)
	returnTo := u.Query().Get("return_to")
	if !strings.Contains(returnTo, "/oauth2/authorize") || !strings.Contains(returnTo, in.RawQuery) {
		t.Fatalf("return_to must resume the authorize request, got %s", returnTo)
	}
}

// --- Authorize: authenticated -> consent handoff ----------------------------

func TestAuthorizeAuthenticatedNoPriorConsentPersistsRequest(t *testing.T) {
	s, f := newTestService()
	c := mustRegister(t, s, RegisterInput{ClientName: "Acme", RedirectURIs: []string{testRedirectURI}, Scope: "contacts:read"})
	owner := Owner{UserID: uuid.New(), WorkspaceID: uuid.New()}
	in := baseAuthorize(c.Client.ClientID)
	in.Owner = &owner

	res, err := s.Authorize(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != OutcomeRedirect || !strings.HasPrefix(res.RedirectTo, testPublicURL+consentPath) {
		t.Fatalf("want consent redirect, got %s", res.RedirectTo)
	}
	u, _ := url.Parse(res.RedirectTo)
	consentID := u.Query().Get("consent_id")
	if consentID == "" {
		t.Fatal("consent_id missing from consent redirect")
	}
	req, ok := f.requests[consentID]
	if !ok {
		t.Fatal("authorization request not persisted")
	}
	if req.UserID != owner.UserID || req.WorkspaceID != owner.WorkspaceID {
		t.Fatal("request not bound to the resolved owner")
	}
	if req.CodeChallenge != testChallenge || req.CodeChallengeMethod != "S256" {
		t.Fatal("PKCE not persisted on the request")
	}
	if len(f.codes) != 0 {
		t.Fatal("no code should be issued before consent")
	}
}

func TestAuthorizePriorConsentSkipsToCode(t *testing.T) {
	s, f := newTestService()
	c := mustRegister(t, s, RegisterInput{ClientName: "Acme", RedirectURIs: []string{testRedirectURI}, Scope: "contacts:read"})
	owner := Owner{UserID: uuid.New(), WorkspaceID: uuid.New()}
	f.seedConsent(owner.UserID, c.Client.ClientID, []string{"contacts:read"}, owner.WorkspaceID)

	in := baseAuthorize(c.Client.ClientID)
	in.Owner = &owner
	res, err := s.Authorize(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != OutcomeRedirect || !strings.HasPrefix(res.RedirectTo, testRedirectURI) {
		t.Fatalf("prior consent must issue a code to the client, got %s", res.RedirectTo)
	}
	u, _ := url.Parse(res.RedirectTo)
	code := u.Query().Get("code")
	if code == "" || u.Query().Get("state") != "st-123" {
		t.Fatalf("code/state missing: %s", res.RedirectTo)
	}
	stored, ok := f.codeByRaw(code)
	if !ok {
		t.Fatal("issued code not persisted (by hash)")
	}
	if stored.ClientID != c.Client.ClientID || stored.RedirectUri != testRedirectURI ||
		stored.CodeChallenge != testChallenge || stored.UserID != owner.UserID ||
		stored.WorkspaceID != owner.WorkspaceID {
		t.Fatal("code not bound to all request params")
	}
}

// --- Consent data + decision ------------------------------------------------

// startConsent runs an authenticated authorize that produces a consent handoff and
// returns the consent_id + owner.
func startConsent(t *testing.T, s *Service, f *fakeStore, clientID string) (string, Owner) {
	t.Helper()
	owner := Owner{UserID: uuid.New(), WorkspaceID: uuid.New()}
	in := baseAuthorize(clientID)
	in.Owner = &owner
	res, err := s.Authorize(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(res.RedirectTo)
	return u.Query().Get("consent_id"), owner
}

func TestConsentRequestReturnsDataForOwnerOnly(t *testing.T) {
	s, f := newTestService()
	c := mustRegister(t, s, RegisterInput{ClientName: "Acme Analytics", RedirectURIs: []string{testRedirectURI}, Scope: "contacts:read"})
	consentID, owner := startConsent(t, s, f, c.Client.ClientID)

	view, err := s.ConsentRequest(context.Background(), consentID, owner.UserID)
	if err != nil {
		t.Fatal(err)
	}
	if view.ClientName != "Acme Analytics" || view.RedirectURI != testRedirectURI || len(view.RequestedScopes) != 1 {
		t.Fatalf("unexpected consent view: %+v", view)
	}
	// A DIFFERENT user must not see it.
	if _, err := s.ConsentRequest(context.Background(), consentID, uuid.New()); !errors.Is(err, ErrConsentNotFound) {
		t.Fatalf("foreign user must get ErrConsentNotFound, got %v", err)
	}
}

func TestConsentRequestExpired(t *testing.T) {
	s, f := newTestService()
	c := mustRegister(t, s, RegisterInput{ClientName: "Acme", RedirectURIs: []string{testRedirectURI}, Scope: "contacts:read"})
	consentID, owner := startConsent(t, s, f, c.Client.ClientID)
	// Advance the clock past the request TTL.
	s.now = func() time.Time { return time.Now().Add(authRequestTTL + time.Minute) }
	f.now = s.now
	if _, err := s.ConsentRequest(context.Background(), consentID, owner.UserID); !errors.Is(err, ErrConsentNotFound) {
		t.Fatalf("expired request must be ErrConsentNotFound, got %v", err)
	}
}

func TestDecideApproveIssuesBoundCode(t *testing.T) {
	s, f := newTestService()
	c := mustRegister(t, s, RegisterInput{ClientName: "Acme", RedirectURIs: []string{testRedirectURI}, Scope: "contacts:read"})
	consentID, owner := startConsent(t, s, f, c.Client.ClientID)

	res, err := s.DecideConsent(context.Background(), DecideInput{ConsentID: consentID, UserID: owner.UserID, Approve: true})
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(res.RedirectTo)
	code := u.Query().Get("code")
	if code == "" || u.Query().Get("state") != "st-123" {
		t.Fatalf("approve must redirect with code+state: %s", res.RedirectTo)
	}
	stored, ok := f.codeByRaw(code)
	if !ok || stored.ClientID != c.Client.ClientID || stored.CodeChallenge != testChallenge ||
		stored.UserID != owner.UserID || stored.WorkspaceID != owner.WorkspaceID {
		t.Fatal("approved code not bound to the request params")
	}
	// The remembered consent was recorded.
	if _, ok := f.consents[consentKey(owner.UserID, c.Client.ClientID)]; !ok {
		t.Fatal("approve must upsert the remembered consent")
	}
	// Single-use: a second approve on the consumed request fails.
	if _, err := s.DecideConsent(context.Background(), DecideInput{ConsentID: consentID, UserID: owner.UserID, Approve: true}); !errors.Is(err, ErrConsentNotFound) {
		t.Fatalf("second approve must fail (single-use), got %v", err)
	}
}

func TestDecideApproveShortCodeTTL(t *testing.T) {
	s, f := newTestService()
	c := mustRegister(t, s, RegisterInput{ClientName: "Acme", RedirectURIs: []string{testRedirectURI}, Scope: "contacts:read"})
	consentID, owner := startConsent(t, s, f, c.Client.ClientID)
	before := time.Now()
	res, err := s.DecideConsent(context.Background(), DecideInput{ConsentID: consentID, UserID: owner.UserID, Approve: true})
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(res.RedirectTo)
	stored, _ := f.codeByRaw(u.Query().Get("code"))
	ttl := stored.ExpiresAt.Time.Sub(before)
	if ttl <= 0 || ttl > 5*time.Minute {
		t.Fatalf("auth code TTL must be short (<=5m), got %v", ttl)
	}
}

func TestDecideDenyRedirectsAccessDenied(t *testing.T) {
	s, f := newTestService()
	c := mustRegister(t, s, RegisterInput{ClientName: "Acme", RedirectURIs: []string{testRedirectURI}, Scope: "contacts:read"})
	consentID, owner := startConsent(t, s, f, c.Client.ClientID)

	res, err := s.DecideConsent(context.Background(), DecideInput{ConsentID: consentID, UserID: owner.UserID, Approve: false})
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(res.RedirectTo)
	if u.Query().Get("error") != "access_denied" || u.Query().Get("state") != "st-123" {
		t.Fatalf("deny must redirect error=access_denied&state=...: %s", res.RedirectTo)
	}
	if len(f.codes) != 0 {
		t.Fatal("deny must not issue a code")
	}
}

func TestDecideByDifferentUserRejected(t *testing.T) {
	s, f := newTestService()
	c := mustRegister(t, s, RegisterInput{ClientName: "Acme", RedirectURIs: []string{testRedirectURI}, Scope: "contacts:read"})
	consentID, _ := startConsent(t, s, f, c.Client.ClientID)
	if _, err := s.DecideConsent(context.Background(), DecideInput{ConsentID: consentID, UserID: uuid.New(), Approve: true}); !errors.Is(err, ErrConsentNotFound) {
		t.Fatalf("decision by a non-owner must be ErrConsentNotFound, got %v", err)
	}
}

// --- Revoke tenancy ----------------------------------------------------------

func TestRevokeClientTenantPinned(t *testing.T) {
	s, f := newTestService()
	ws := uuid.New()
	uid := uuid.New()
	res := mustRegister(t, s, RegisterInput{
		ClientName: "Acme", RedirectURIs: []string{testRedirectURI}, Scope: "contacts:read",
		CreatedBy: &uid, WorkspaceID: &ws,
	})
	// A foreign workspace cannot revoke it.
	if err := s.RevokeClient(context.Background(), uuid.New(), res.Client.ClientID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign revoke must be ErrNotFound, got %v", err)
	}
	// The owning workspace can.
	if err := s.RevokeClient(context.Background(), ws, res.Client.ClientID); err != nil {
		t.Fatalf("owner revoke: %v", err)
	}
	if !f.clients[res.Client.ClientID].RevokedAt.Valid {
		t.Fatal("client should be revoked")
	}
}
