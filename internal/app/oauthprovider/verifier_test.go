package oauthprovider

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/app/auth"
)

// verify runs the verifier against a request carrying the given Authorization header.
func verify(t *testing.T, v *Verifier, authHeader string) (auth.Principal, bool, error) {
	t.Helper()
	r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1/contacts", http.NoBody)
	if authHeader != "" {
		r.Header.Set("Authorization", authHeader)
	}
	return v.Verify(context.Background(), r)
}

// issueAccessToken exchanges a code and returns the raw access token + the fake it lives in.
func issueAccessToken(t *testing.T) (*Service, *fakeStore, string, string) {
	t.Helper()
	s, f := newTestService()
	cid := publicClient(t, s)
	tok := exchangeForTokens(t, s, f, cid, []string{"contacts:read", "lists:read"})
	return s, f, cid, tok.AccessToken
}

func TestVerifierEngagesValidToken(t *testing.T) {
	_, f, _, raw := issueAccessToken(t)
	v := NewVerifier(f)

	p, ok, err := verify(t, v, "Bearer "+raw)
	if err != nil || !ok {
		t.Fatalf("valid token: ok=%v err=%v", ok, err)
	}
	if p.Kind != auth.KindOAuth {
		t.Fatalf("want KindOAuth, got %v", p.Kind)
	}
	if p.Role != "" {
		t.Fatalf("OAuth principal must have empty role, got %q", p.Role)
	}
	if !p.HasScope("contacts:read") || p.HasScope("campaigns:send") {
		t.Fatalf("principal scopes wrong: %v", p.Scopes)
	}
}

func TestVerifierDefersOnNonOAuthTokens(t *testing.T) {
	_, f, _, _ := issueAccessToken(t)
	v := NewVerifier(f)

	for _, h := range []string{
		"",                    // no header
		"Bearer eyJhbGc...",   // a session JWT
		"Bearer inrd_abc_def", // an api-key token
		"Basic Zm9vOmJhcg==",  // basic auth
	} {
		_, ok, err := verify(t, v, h)
		if ok || err != nil {
			t.Fatalf("header %q must DEFER, got ok=%v err=%v", h, ok, err)
		}
	}
}

func TestVerifierRejectsUnknownToken(t *testing.T) {
	_, f, _, _ := issueAccessToken(t)
	v := NewVerifier(f)

	_, ok, err := verify(t, v, "Bearer inoa_unknowntoken")
	if ok || !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatalf("unknown oauth token: want ErrUnauthorized, got ok=%v err=%v", ok, err)
	}
}

func TestVerifierRejectsRevokedToken(t *testing.T) {
	s, f, cid, raw := issueAccessToken(t)
	v := NewVerifier(f)
	if err := s.Revoke(context.Background(), raw, ClientCredentials{ID: cid}); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	_, ok, err := verify(t, v, "Bearer "+raw)
	if ok || !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatalf("revoked token: want ErrUnauthorized, got ok=%v err=%v", ok, err)
	}
}

func TestVerifierRejectsExpiredToken(t *testing.T) {
	_, f, _, raw := issueAccessToken(t)
	v := NewVerifier(f)
	// Advance the verifier's clock past the access-token TTL.
	v.now = func() time.Time { return time.Now().Add(2 * accessTokenTTL) }

	_, ok, err := verify(t, v, "Bearer "+raw)
	if ok || !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatalf("expired token: want ErrUnauthorized, got ok=%v err=%v", ok, err)
	}
}

// TestVerifierPinsWorkspaceFromToken proves the principal's workspace comes from the
// stored token binding, never a request parameter.
func TestVerifierPinsWorkspaceFromToken(t *testing.T) {
	s, f := newTestService()
	cid := publicClient(t, s)
	uid, ws := uuid.New(), uuid.New()
	code := seedCode(t, f, s, cid, []string{"contacts:read"}, s256(testVerifier), uid, ws)
	res, err := s.Token(context.Background(), TokenRequest{
		GrantType: "authorization_code", Code: code, RedirectURI: testRedirectURI,
		CodeVerifier: testVerifier, Client: ClientCredentials{ID: cid},
	})
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}
	v := NewVerifier(f)
	p, ok, _ := verify(t, v, "Bearer "+res.AccessToken)
	if !ok || p.WorkspaceID != ws.String() || p.UserID != uid.String() {
		t.Fatalf("principal not pinned to token bindings: %+v", p)
	}
}
