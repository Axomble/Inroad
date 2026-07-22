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
	svc := NewService(newFakeStore(), nil, newTestSealer(t), mail.GoogleOAuth{}, fakeExchanger{})
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
	svc := NewService(store, nil, newTestSealer(t), oauth, exch)

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
	svc := NewService(store, nil, newTestSealer(t), oauth, fakeExchanger{tok: validToken(), email: "dup@example.com"})

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
	svc := NewService(store, nil, newTestSealer(t), oauth, fakeExchanger{tok: validToken(), email: ""})

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
	svc := NewService(store, nil, newTestSealer(t), oauth, fakeExchanger{err: errors.New("token endpoint 400")})

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
// driven with httptest. The exchanger always succeeds with the given email.
func newCallbackHarness(t *testing.T, email string) (*fakeStore, http.Handler) {
	t.Helper()
	store := newFakeStore()
	oauth := mail.GoogleOAuth{ClientID: "a", ClientSecret: "b", RedirectURL: "http://x/cb"}
	svc := NewService(store, nil, newTestSealer(t), oauth, fakeExchanger{tok: validToken(), email: email})
	h := NewHandler(svc, callbackTestSecret, callbackTestAppBase)
	return store, h.CallbackRoutes()
}

func getCallback(router http.Handler, rawQuery string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/google/callback?"+rawQuery, nil)
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
	wantLoc := callbackTestAppBase + "/mailboxes?connected=" + url.QueryEscape("rep@example.com")
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
