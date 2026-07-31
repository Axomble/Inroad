package oauthprovider

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/platform/db/gen"
)

// postForm builds a form-urlencoded POST request to path with the given values.
func postForm(path string, form url.Values) *http.Request {
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return r
}

// decodeTokenErr reads an RFC 6749 §5.2 error body.
func decodeTokenErr(t *testing.T, body io.Reader) tokenErrorBody {
	t.Helper()
	var b tokenErrorBody
	if err := json.NewDecoder(body).Decode(&b); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	return b
}

// TestTokenHandlerMalformedFormIsInvalidRequest: a body that fails ParseForm surfaces as
// invalid_request (400), still no-store.
func TestTokenHandlerMalformedFormIsInvalidRequest(t *testing.T) {
	h, _ := newTestHandler(fakeOwner{})
	// An invalid percent-escape makes r.ParseForm fail.
	r := httptest.NewRequest(http.MethodPost, "/oauth2/token", strings.NewReader("%zz"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.token(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", w.Code)
	}
	if got := decodeTokenErr(t, w.Body).Error; got != "invalid_request" {
		t.Fatalf("want invalid_request, got %s", got)
	}
	if cc := w.Header().Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("want Cache-Control: no-store on error, got %q", cc)
	}
}

// TestTokenHandlerBasicBeatsPostCreds: client_secret_basic must take precedence over
// client_secret_post in clientCredsFromRequest. The Basic header carries the CORRECT
// secret and the body a WRONG one; if Basic wins, client auth passes and the request
// fails later on the missing code (invalid_grant) rather than at auth (invalid_client).
func TestTokenHandlerBasicBeatsPostCreds(t *testing.T) {
	h, _ := newTestHandler(fakeOwner{})
	reg := mustRegister(t, h.svc, RegisterInput{
		ClientName: "Conf", RedirectURIs: []string{testRedirectURI},
		Scope: "contacts:read", TokenEndpointAuthMethod: "client_secret_basic",
	})
	cid, secret := reg.Client.ClientID, reg.ClientSecret

	form := url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {cid},
		"client_secret": {"WRONG-post-secret"}, // must be IGNORED in favor of Basic
		// no `code`: reaching grant handling proves client auth passed.
	}
	r := postForm("/oauth2/token", form)
	r.SetBasicAuth(cid, secret) // correct secret via client_secret_basic
	w := httptest.NewRecorder()
	h.token(w, r)

	if got := decodeTokenErr(t, w.Body).Error; got != "invalid_grant" {
		t.Fatalf("client_secret_basic must take precedence over client_secret_post; got %s (want invalid_grant)", got)
	}
}

// TestTokenHandlerInvalidClientChallenge: an unknown client is 401 invalid_client with
// the RFC 6749 §5.2 Basic WWW-Authenticate challenge and no-store.
func TestTokenHandlerInvalidClientChallenge(t *testing.T) {
	h, _ := newTestHandler(fakeOwner{})
	form := url.Values{"grant_type": {"authorization_code"}, "client_id": {"inrc_unknown"}}
	w := httptest.NewRecorder()
	h.token(w, postForm("/oauth2/token", form))

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", w.Code)
	}
	body := decodeTokenErr(t, w.Body)
	if body.Error != "invalid_client" {
		t.Fatalf("want invalid_client, got %s", body.Error)
	}
	if wa := w.Header().Get("WWW-Authenticate"); wa != `Basic realm="oauth2"` {
		t.Fatalf(`want WWW-Authenticate: Basic realm="oauth2", got %q`, wa)
	}
	if cc := w.Header().Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("want no-store on error, got %q", cc)
	}
}

// TestTokenHandlerSuccessShapeAndNoStore: a full authorization_code exchange over the
// HTTP layer returns 200, no-store, and the exact RFC 6749 §5.1 snake_case JSON body.
func TestTokenHandlerSuccessShapeAndNoStore(t *testing.T) {
	h, f := newTestHandler(fakeOwner{})
	h.svc.now = f.now // share the clock so seedCode/consume expiry line up
	cid := publicClient(t, h.svc)
	uid, ws := uuid.New(), uuid.New()
	code := seedCode(t, f, h.svc, cid, []string{"contacts:read"}, s256(testVerifier), uid, ws)

	form := url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {cid},
		"code":          {code},
		"redirect_uri":  {testRedirectURI},
		"code_verifier": {testVerifier},
	}
	w := httptest.NewRecorder()
	h.token(w, postForm("/oauth2/token", form))

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
	if cc := w.Header().Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("want no-store on success, got %q", cc)
	}
	var body tokenResponseBody
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode token response: %v", err)
	}
	if body.TokenType != "Bearer" || body.ExpiresIn != 3600 || body.AccessToken == "" || body.RefreshToken == "" {
		t.Fatalf("unexpected token body: %+v", body)
	}
	// Exact snake_case field names (RFC 6749 §5.1).
	raw := w.Body.String()
	for _, k := range []string{`"access_token"`, `"token_type"`, `"expires_in"`, `"refresh_token"`, `"scope"`} {
		if !strings.Contains(raw, k) {
			t.Fatalf("token response missing field %s: %s", k, raw)
		}
	}
}

// TestRevokeHandlerAlways200: /oauth2/revoke returns 200 (with no-store) even for an
// unknown token — no token-existence oracle.
func TestRevokeHandlerAlways200(t *testing.T) {
	h, _ := newTestHandler(fakeOwner{})
	cid := publicClient(t, h.svc)
	form := url.Values{"token": {"inoa_unknown"}, "client_id": {cid}}
	w := httptest.NewRecorder()
	h.revoke(w, postForm("/oauth2/revoke", form))

	if w.Code != http.StatusOK {
		t.Fatalf("revoke must always be 200, got %d", w.Code)
	}
	if cc := w.Header().Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("want no-store, got %q", cc)
	}
}

// TestIntrospectHandlerInactiveNoStore: /oauth2/introspect for an unknown token is a
// 200 `{"active":false}` with no-store and no leaked metadata.
func TestIntrospectHandlerInactiveNoStore(t *testing.T) {
	h, _ := newTestHandler(fakeOwner{})
	cid := publicClient(t, h.svc)
	form := url.Values{"token": {"inoa_unknown"}, "client_id": {cid}}
	w := httptest.NewRecorder()
	h.introspect(w, postForm("/oauth2/introspect", form))

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}
	if cc := w.Header().Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("want no-store, got %q", cc)
	}
	var body introspectBody
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode introspect body: %v", err)
	}
	if body.Active || body.ClientID != "" || body.Scope != "" {
		t.Fatalf("unknown token must be inactive with no detail: %+v", body)
	}
}

// stubErrStore embeds a fakeStore but fails GetClient with a non-TokenError infra error,
// so the handler's writeTokenError falls through to a generic 500 (the errors.As
// fallback path, otherwise unexercised).
type stubErrStore struct {
	*fakeStore
	err error
}

func (s stubErrStore) GetClient(context.Context, string) (gen.OauthClient, error) {
	return gen.OauthClient{}, s.err
}

// TestTokenHandlerInfraFaultIsGeneric500: an infra (non-TokenError) fault maps to a
// generic 500 server_error with NO internal detail leaked, still no-store.
func TestTokenHandlerInfraFaultIsGeneric500(t *testing.T) {
	boom := errors.New("db exploded: connection refused at 10.0.0.5")
	store := stubErrStore{fakeStore: newFakeStore(), err: boom}
	h := NewHandler(NewService(store, testPublicURL), fakeOwner{})

	form := url.Values{"grant_type": {"authorization_code"}, "client_id": {"inrc_x"}}
	w := httptest.NewRecorder()
	h.token(w, postForm("/oauth2/token", form))

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("want 500, got %d", w.Code)
	}
	body := decodeTokenErr(t, w.Body)
	if body.Error != "server_error" {
		t.Fatalf("want server_error, got %s", body.Error)
	}
	// The raw internal error must not leak anywhere in the response.
	if strings.Contains(w.Body.String(), "db exploded") || strings.Contains(w.Body.String(), "10.0.0.5") {
		t.Fatalf("internal error detail leaked: %s", w.Body.String())
	}
	if cc := w.Header().Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("want no-store even on 500, got %q", cc)
	}
}
