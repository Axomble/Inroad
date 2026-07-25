package mailbox

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"golang.org/x/oauth2"

	"github.com/inroad/inroad/internal/platform/db/gen"
	"github.com/inroad/inroad/internal/platform/mail"
	"github.com/inroad/inroad/internal/platform/oauthstate"
)

// fakeExchanger stands in for the live Google token exchange so the connect
// flow can be unit-tested without hitting Google.
type fakeExchanger struct {
	tok   *oauth2.Token
	email string
	err   error
}

func (f fakeExchanger) Exchange(ctx context.Context, code string) (*oauth2.Token, string, error) {
	return f.tok, f.email, f.err
}

// validToken is a plausible refreshed token for the happy path.
func validToken() *oauth2.Token {
	return &oauth2.Token{AccessToken: "at", RefreshToken: "rt", Expiry: time.Now().Add(time.Hour)}
}

// TestStartGoogleOAuthDisabled asserts a zero-value GoogleOAuth config fails
// closed: GoogleAuthCodeURL (the start endpoint's only OAuth call) returns
// ErrOAuthDisabled -- the handler turns that into a 501 -- and
// CompleteGoogleOAuth refuses with ErrOAuthDisabled too.
func TestStartGoogleOAuthDisabled(t *testing.T) {
	svc := NewService(newFakeStore(), nil, newTestKeyring(t), mail.GoogleOAuth{}, fakeExchanger{}, mail.MicrosoftOAuth{}, fakeExchanger{})
	if _, err := svc.GoogleAuthCodeURL("state"); !errors.Is(err, ErrOAuthDisabled) {
		t.Fatalf("GoogleAuthCodeURL: want ErrOAuthDisabled, got %v", err)
	}
	if _, err := svc.CompleteGoogleOAuth(context.Background(), "code", uuid.New()); !errors.Is(err, ErrOAuthDisabled) {
		t.Fatalf("CompleteGoogleOAuth: want ErrOAuthDisabled, got %v", err)
	}
}

func TestCompleteGoogleOAuthCreatesGmailMailbox(t *testing.T) {
	store := newFakeStore()
	oauth := mail.GoogleOAuth{ClientID: "a", ClientSecret: "b", RedirectURL: "http://x/cb"}
	exch := fakeExchanger{tok: validToken(), email: "rep@example.com"}
	svc := NewService(store, nil, newTestKeyring(t), oauth, exch, mail.MicrosoftOAuth{}, nil)

	workspaceID := uuid.New()
	m, err := svc.CompleteGoogleOAuth(context.Background(), "code", workspaceID)
	if err != nil {
		t.Fatal(err)
	}
	if m.Provider != "gmail" || m.Email != "rep@example.com" {
		t.Fatalf("unexpected mailbox: provider=%q email=%q", m.Provider, m.Email)
	}
	// The workspace_id must come from the (verified) caller-supplied argument,
	// which the callback derives from the signed state, never a request body.
	if store.lastCreate.WorkspaceID != workspaceID {
		t.Fatalf("workspace_id = %v, want %v", store.lastCreate.WorkspaceID, workspaceID)
	}
	// The OAuth token must be sealed into secret_ciphertext, never stored raw.
	if store.lastCreate.SecretCiphertext == "" {
		t.Fatal("token was not sealed into secret_ciphertext")
	}
	if store.lastCreate.SecretCiphertext == "at" {
		t.Fatal("secret_ciphertext holds the raw access token, expected sealed bytes")
	}
}

func TestCompleteGoogleOAuthDuplicateEmailRejected(t *testing.T) {
	store := newFakeStore()
	oauth := mail.GoogleOAuth{ClientID: "a", ClientSecret: "b"}
	svc := NewService(store, nil, newTestKeyring(t), oauth, fakeExchanger{tok: validToken(), email: "dup@example.com"}, mail.MicrosoftOAuth{}, nil)

	workspaceID := uuid.New()
	// Seed an existing mailbox with the same email in the same workspace.
	if _, err := store.Create(context.Background(), gen.CreateMailboxParams{
		WorkspaceID: workspaceID, Provider: "gmail", Email: "dup@example.com",
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := svc.CompleteGoogleOAuth(context.Background(), "code", workspaceID); !errors.Is(err, ErrDuplicateMailbox) {
		t.Fatalf("want ErrDuplicateMailbox, got %v", err)
	}
}

func TestCompleteGoogleOAuthEmptyEmailRejected(t *testing.T) {
	store := newFakeStore()
	oauth := mail.GoogleOAuth{ClientID: "a", ClientSecret: "b"}
	svc := NewService(store, nil, newTestKeyring(t), oauth, fakeExchanger{tok: validToken(), email: ""}, mail.MicrosoftOAuth{}, nil)

	if _, err := svc.CompleteGoogleOAuth(context.Background(), "code", uuid.New()); !errors.Is(err, ErrValidation) {
		t.Fatalf("want ErrValidation, got %v", err)
	}
	if store.lastCreate.WorkspaceID != (uuid.UUID{}) {
		t.Fatal("no mailbox should be created when userinfo has no email")
	}
}

func TestCompleteGoogleOAuthExchangeFailure(t *testing.T) {
	store := newFakeStore()
	oauth := mail.GoogleOAuth{ClientID: "a", ClientSecret: "b"}
	svc := NewService(store, nil, newTestKeyring(t), oauth, fakeExchanger{err: errors.New("token endpoint 400")}, mail.MicrosoftOAuth{}, nil)

	if _, err := svc.CompleteGoogleOAuth(context.Background(), "code", uuid.New()); err == nil {
		t.Fatal("want error on exchange failure, got nil")
	}
	if store.lastCreate.WorkspaceID != (uuid.UUID{}) {
		t.Fatal("no mailbox should be created when the exchange fails")
	}
}

// --- Public callback surface (httptest end-to-end) ---

const callbackTestAppBase = "http://localhost:5173"

var callbackTestSecret = []byte("test-secret-at-least-16-bytes")

// newCallbackHarness builds a Handler whose CallbackRoutes() router can be
// driven with httptest. Both the Gmail and M365 exchangers always succeed with
// the given email, so the same harness drives /google/callback and
// /microsoft/callback.
func newCallbackHarness(t *testing.T, email string) (*fakeStore, http.Handler) {
	t.Helper()
	store := newFakeStore()
	oauth := mail.GoogleOAuth{ClientID: "a", ClientSecret: "b", RedirectURL: "http://x/cb"}
	msOAuth := mail.MicrosoftOAuth{ClientID: "a", ClientSecret: "b", RedirectURL: "http://x/cb"}
	exch := fakeExchanger{tok: validToken(), email: email}
	svc := NewService(store, nil, newTestKeyring(t), oauth, exch, msOAuth, exch)
	h := NewHandler(svc, callbackTestSecret, callbackTestAppBase)
	return store, h.CallbackRoutes()
}

func getCallback(router http.Handler, rawQuery string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/google/callback?"+rawQuery, nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func getMicrosoftCallback(router http.Handler, rawQuery string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/microsoft/callback?"+rawQuery, nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func TestGoogleCallbackValidStateCreatesMailbox(t *testing.T) {
	store, router := newCallbackHarness(t, "rep@example.com")
	wsA := uuid.New()
	state := oauthstate.Sign(callbackTestSecret, wsA.String(), time.Now(), 10*time.Minute)

	rec := getCallback(router, "code=abc&state="+url.QueryEscape(state))

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", rec.Code)
	}
	// The redirect carries connected=<email> plus &provider=gmail so the SPA
	// banner can render provider-correct copy.
	wantLoc := callbackTestAppBase + "/mailboxes?connected=" + url.QueryEscape("rep@example.com") + "&provider=gmail"
	if got := rec.Header().Get("Location"); got != wantLoc {
		t.Fatalf("Location = %q, want %q", got, wantLoc)
	}
	// The mailbox's workspace comes from the SIGNED STATE, never a request
	// param -- a state for workspace A must land only in workspace A.
	if store.lastCreate.WorkspaceID != wsA {
		t.Fatalf("created WorkspaceID = %v, want %v (from state)", store.lastCreate.WorkspaceID, wsA)
	}
	if store.lastCreate.Provider != "gmail" {
		t.Fatalf("provider = %q, want gmail", store.lastCreate.Provider)
	}
}

// TestGoogleCallbackProviderErrorRedirectsDenied covers the branch where Google
// bounces the user back with ?error (e.g. the user declined consent): no code is
// usable, so the callback must redirect with oauth_error=denied and create
// nothing.
func TestGoogleCallbackProviderErrorRedirectsDenied(t *testing.T) {
	store, router := newCallbackHarness(t, "rep@example.com")

	rec := getCallback(router, "error=access_denied")

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", rec.Code)
	}
	loc := rec.Header().Get("Location")
	if !strings.Contains(loc, "oauth_error=denied") {
		t.Fatalf("Location = %q, want oauth_error=denied", loc)
	}
	// Error redirects are tagged with the provider too, so the SPA banner can
	// render provider-correct disabled/failure copy.
	if !strings.Contains(loc, "provider=gmail") {
		t.Fatalf("Location = %q, want provider=gmail", loc)
	}
	if store.lastCreate.WorkspaceID != (uuid.UUID{}) {
		t.Fatal("a denied consent must create no mailbox")
	}
}

// TestGoogleCallbackDuplicateEmailRedirectsAlreadyConnected covers the branch
// where the exchanged email is already connected in the (state-derived)
// workspace: the callback maps ErrDuplicateMailbox to oauth_error=already_connected.
func TestGoogleCallbackDuplicateEmailRedirectsAlreadyConnected(t *testing.T) {
	store, router := newCallbackHarness(t, "dup@example.com")
	wsA := uuid.New()
	// Pre-seed the same email in the workspace the signed state points at.
	if _, err := store.Create(context.Background(), gen.CreateMailboxParams{
		WorkspaceID: wsA, Provider: "gmail", Email: "dup@example.com",
	}); err != nil {
		t.Fatal(err)
	}
	state := oauthstate.Sign(callbackTestSecret, wsA.String(), time.Now(), 10*time.Minute)

	rec := getCallback(router, "code=abc&state="+url.QueryEscape(state))

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", rec.Code)
	}
	if loc := rec.Header().Get("Location"); !strings.Contains(loc, "oauth_error=already_connected") {
		t.Fatalf("Location = %q, want oauth_error=already_connected", loc)
	}
}

// TestGoogleCallbackEmptyEmailRedirectsNoEmail covers the branch where userinfo
// yields no email (ErrValidation): the callback maps it to oauth_error=no_email
// and creates nothing.
func TestGoogleCallbackEmptyEmailRedirectsNoEmail(t *testing.T) {
	store, router := newCallbackHarness(t, "")
	wsA := uuid.New()
	state := oauthstate.Sign(callbackTestSecret, wsA.String(), time.Now(), 10*time.Minute)

	rec := getCallback(router, "code=abc&state="+url.QueryEscape(state))

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", rec.Code)
	}
	if loc := rec.Header().Get("Location"); !strings.Contains(loc, "oauth_error=no_email") {
		t.Fatalf("Location = %q, want oauth_error=no_email", loc)
	}
	if store.lastCreate.WorkspaceID != (uuid.UUID{}) {
		t.Fatal("an empty userinfo email must create no mailbox")
	}
}

func TestGoogleCallbackGarbageStateNoMailbox(t *testing.T) {
	store, router := newCallbackHarness(t, "rep@example.com")

	rec := getCallback(router, "code=abc&state=not-a-valid-state")

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", rec.Code)
	}
	if loc := rec.Header().Get("Location"); !strings.Contains(loc, "oauth_error=bad_state") {
		t.Fatalf("Location = %q, want oauth_error=bad_state", loc)
	}
	if store.lastCreate.WorkspaceID != (uuid.UUID{}) {
		t.Fatal("no mailbox should be created for a garbage state")
	}
}

func TestGoogleCallbackAbsentStateNoMailbox(t *testing.T) {
	store, router := newCallbackHarness(t, "rep@example.com")

	rec := getCallback(router, "code=abc")

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", rec.Code)
	}
	if loc := rec.Header().Get("Location"); !strings.Contains(loc, "oauth_error=bad_state") {
		t.Fatalf("Location = %q, want oauth_error=bad_state", loc)
	}
	if store.lastCreate.WorkspaceID != (uuid.UUID{}) {
		t.Fatal("no mailbox should be created without a state")
	}
}

// --- Microsoft 365 (m365) service + callback surface ---

// TestStartMicrosoftOAuthDisabled asserts a zero-value MicrosoftOAuth config
// fails closed, mirroring the Gmail disabled case: both MicrosoftAuthCodeURL
// and CompleteMicrosoftOAuth return ErrOAuthDisabled.
func TestStartMicrosoftOAuthDisabled(t *testing.T) {
	svc := NewService(newFakeStore(), nil, newTestKeyring(t), mail.GoogleOAuth{}, nil, mail.MicrosoftOAuth{}, fakeExchanger{})
	if _, err := svc.MicrosoftAuthCodeURL("state"); !errors.Is(err, ErrOAuthDisabled) {
		t.Fatalf("MicrosoftAuthCodeURL: want ErrOAuthDisabled, got %v", err)
	}
	if _, err := svc.CompleteMicrosoftOAuth(context.Background(), "code", uuid.New()); !errors.Is(err, ErrOAuthDisabled) {
		t.Fatalf("CompleteMicrosoftOAuth: want ErrOAuthDisabled, got %v", err)
	}
}

func TestCompleteMicrosoftOAuthCreatesM365Mailbox(t *testing.T) {
	store := newFakeStore()
	msOAuth := mail.MicrosoftOAuth{ClientID: "a", ClientSecret: "b", RedirectURL: "http://x/cb"}
	exch := fakeExchanger{tok: validToken(), email: "rep@example.com"}
	svc := NewService(store, nil, newTestKeyring(t), mail.GoogleOAuth{}, nil, msOAuth, exch)

	workspaceID := uuid.New()
	m, err := svc.CompleteMicrosoftOAuth(context.Background(), "code", workspaceID)
	if err != nil {
		t.Fatal(err)
	}
	if m.Provider != "m365" || m.Email != "rep@example.com" {
		t.Fatalf("unexpected mailbox: provider=%q email=%q", m.Provider, m.Email)
	}
	// The workspace_id must come from the (verified) caller-supplied argument,
	// which the callback derives from the signed state, never a request body.
	if store.lastCreate.WorkspaceID != workspaceID {
		t.Fatalf("workspace_id = %v, want %v", store.lastCreate.WorkspaceID, workspaceID)
	}
	// The OAuth token must be sealed into secret_ciphertext, never stored raw.
	if store.lastCreate.SecretCiphertext == "" {
		t.Fatal("token was not sealed into secret_ciphertext")
	}
	if store.lastCreate.SecretCiphertext == "at" {
		t.Fatal("secret_ciphertext holds the raw access token, expected sealed bytes")
	}
}

func TestCompleteMicrosoftOAuthDuplicateEmailRejected(t *testing.T) {
	store := newFakeStore()
	msOAuth := mail.MicrosoftOAuth{ClientID: "a", ClientSecret: "b"}
	svc := NewService(store, nil, newTestKeyring(t), mail.GoogleOAuth{}, nil, msOAuth, fakeExchanger{tok: validToken(), email: "dup@example.com"})

	workspaceID := uuid.New()
	// Seed an existing mailbox with the same email in the same workspace.
	if _, err := store.Create(context.Background(), gen.CreateMailboxParams{
		WorkspaceID: workspaceID, Provider: "m365", Email: "dup@example.com",
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := svc.CompleteMicrosoftOAuth(context.Background(), "code", workspaceID); !errors.Is(err, ErrDuplicateMailbox) {
		t.Fatalf("want ErrDuplicateMailbox, got %v", err)
	}
}

func TestCompleteMicrosoftOAuthEmptyEmailRejected(t *testing.T) {
	store := newFakeStore()
	msOAuth := mail.MicrosoftOAuth{ClientID: "a", ClientSecret: "b"}
	svc := NewService(store, nil, newTestKeyring(t), mail.GoogleOAuth{}, nil, msOAuth, fakeExchanger{tok: validToken(), email: ""})

	if _, err := svc.CompleteMicrosoftOAuth(context.Background(), "code", uuid.New()); !errors.Is(err, ErrValidation) {
		t.Fatalf("want ErrValidation, got %v", err)
	}
	if store.lastCreate.WorkspaceID != (uuid.UUID{}) {
		t.Fatal("no mailbox should be created when userinfo has no email")
	}
}

func TestMicrosoftCallbackValidStateCreatesMailbox(t *testing.T) {
	store, router := newCallbackHarness(t, "rep@example.com")
	wsA := uuid.New()
	state := oauthstate.Sign(callbackTestSecret, wsA.String(), time.Now(), 10*time.Minute)

	rec := getMicrosoftCallback(router, "code=abc&state="+url.QueryEscape(state))

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", rec.Code)
	}
	// The redirect carries connected=<email> plus &provider=m365 (the persisted
	// mailbox provider value, not the "microsoft" route segment) so the SPA
	// banner renders Microsoft 365 copy.
	wantLoc := callbackTestAppBase + "/mailboxes?connected=" + url.QueryEscape("rep@example.com") + "&provider=m365"
	if got := rec.Header().Get("Location"); got != wantLoc {
		t.Fatalf("Location = %q, want %q", got, wantLoc)
	}
	// The mailbox's workspace comes from the SIGNED STATE, never a request
	// param -- a state for workspace A must land only in workspace A.
	if store.lastCreate.WorkspaceID != wsA {
		t.Fatalf("created WorkspaceID = %v, want %v (from state)", store.lastCreate.WorkspaceID, wsA)
	}
	if store.lastCreate.Provider != "m365" {
		t.Fatalf("provider = %q, want m365", store.lastCreate.Provider)
	}
}

func TestMicrosoftCallbackGarbageStateNoMailbox(t *testing.T) {
	store, router := newCallbackHarness(t, "rep@example.com")

	rec := getMicrosoftCallback(router, "code=abc&state=not-a-valid-state")

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", rec.Code)
	}
	loc := rec.Header().Get("Location")
	if !strings.Contains(loc, "oauth_error=bad_state") {
		t.Fatalf("Location = %q, want oauth_error=bad_state", loc)
	}
	// Error redirects are tagged with the m365 provider too, so the SPA banner
	// renders Microsoft 365 copy rather than the Gmail default.
	if !strings.Contains(loc, "provider=m365") {
		t.Fatalf("Location = %q, want provider=m365", loc)
	}
	if store.lastCreate.WorkspaceID != (uuid.UUID{}) {
		t.Fatal("no mailbox should be created for a garbage state")
	}
}

func TestMicrosoftCallbackAbsentStateNoMailbox(t *testing.T) {
	store, router := newCallbackHarness(t, "rep@example.com")

	rec := getMicrosoftCallback(router, "code=abc")

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", rec.Code)
	}
	if loc := rec.Header().Get("Location"); !strings.Contains(loc, "oauth_error=bad_state") {
		t.Fatalf("Location = %q, want oauth_error=bad_state", loc)
	}
	if store.lastCreate.WorkspaceID != (uuid.UUID{}) {
		t.Fatal("no mailbox should be created without a state")
	}
}
