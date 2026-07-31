package oauthprovider

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

// s256 computes the S256 PKCE challenge for a verifier: BASE64URL-no-pad(SHA256(verifier)).
func s256(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// testVerifier / testChallengeS256 are the RFC 7636 Appendix B test vector.
const (
	testVerifier        = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	testChallengeS256   = "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"
	testCodeVerifierAlt = "0123456789012345678901234567890123456789abc" // 43 chars
)

// seedCode inserts an authorization code bound to the given params and returns the raw
// code the client would present.
func seedCode(t *testing.T, f *fakeStore, s *Service, clientID string, scopes []string, challenge string, uid, ws uuid.UUID) string {
	t.Helper()
	raw, hash, err := newAuthCode()
	if err != nil {
		t.Fatalf("newAuthCode: %v", err)
	}
	if err := f.CreateAuthCode(context.Background(), CreateAuthCodeParams{
		CodeHash: hash, ClientID: clientID, RedirectURI: testRedirectURI,
		CodeChallenge: challenge, CodeChallengeMethod: "S256", Scopes: scopes,
		UserID: uid, WorkspaceID: ws, ExpiresAt: s.now().Add(time.Minute),
	}); err != nil {
		t.Fatalf("CreateAuthCode: %v", err)
	}
	return raw
}

// publicClient registers a public PKCE client and returns its id.
func publicClient(t *testing.T, s *Service) string {
	t.Helper()
	uid, ws := uuid.New(), uuid.New()
	res := mustRegister(t, s, RegisterInput{
		ClientName: "Public", RedirectURIs: []string{testRedirectURI},
		Scope: "contacts:read lists:read", CreatedBy: &uid, WorkspaceID: &ws,
	})
	return res.Client.ClientID
}

// confidentialClient registers a confidential client and returns its id + raw secret.
func confidentialClient(t *testing.T, s *Service) (string, string) {
	t.Helper()
	uid, ws := uuid.New(), uuid.New()
	res := mustRegister(t, s, RegisterInput{
		ClientName: "Confidential", RedirectURIs: []string{testRedirectURI},
		Scope: "contacts:read lists:read", TokenEndpointAuthMethod: "client_secret_basic",
		CreatedBy: &uid, WorkspaceID: &ws,
	})
	return res.Client.ClientID, res.ClientSecret
}

func asTokenErr(t *testing.T, err error) *TokenError {
	t.Helper()
	var te *TokenError
	if !errors.As(err, &te) {
		t.Fatalf("want *TokenError, got %v", err)
	}
	return te
}

// --- authorization_code grant ------------------------------------------------

func TestTokenAuthCodeHappyPathPublic(t *testing.T) {
	s, f := newTestService()
	cid := publicClient(t, s)
	uid, ws := uuid.New(), uuid.New()
	scopes := []string{"contacts:read"}
	code := seedCode(t, f, s, cid, scopes, s256(testVerifier), uid, ws)

	res, err := s.Token(context.Background(), TokenRequest{
		GrantType: "authorization_code", Code: code, RedirectURI: testRedirectURI,
		CodeVerifier: testVerifier, Client: ClientCredentials{ID: cid},
	})
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if res.AccessToken == "" || res.RefreshToken == "" {
		t.Fatalf("empty tokens: %+v", res)
	}
	if res.TokenType != "Bearer" || res.ExpiresIn != 3600 || res.Scope != "contacts:read" {
		t.Fatalf("unexpected response: %+v", res)
	}
	// The access token is stored HASHED, and its raw carries the recognizable prefix.
	if res.AccessToken[:5] != "inoa_" {
		t.Fatalf("access token prefix: %s", res.AccessToken)
	}
	if _, ok := f.access[hexHash(res.AccessToken)]; !ok {
		t.Fatal("access token not persisted by hash")
	}
	if _, ok := f.access[res.AccessToken]; ok {
		t.Fatal("access token must not be stored in the clear")
	}
}

func TestTokenAuthCodeSingleUse(t *testing.T) {
	s, f := newTestService()
	cid := publicClient(t, s)
	uid, ws := uuid.New(), uuid.New()
	code := seedCode(t, f, s, cid, []string{"contacts:read"}, s256(testVerifier), uid, ws)

	req := TokenRequest{GrantType: "authorization_code", Code: code, RedirectURI: testRedirectURI, CodeVerifier: testVerifier, Client: ClientCredentials{ID: cid}}
	if _, err := s.Token(context.Background(), req); err != nil {
		t.Fatalf("first exchange: %v", err)
	}
	// A replay of the same code finds it already consumed -> invalid_grant.
	_, err := s.Token(context.Background(), req)
	if te := asTokenErr(t, err); te.Code != "invalid_grant" {
		t.Fatalf("replay: want invalid_grant, got %s", te.Code)
	}
}

func TestTokenAuthCodePKCEWrongVerifier(t *testing.T) {
	s, f := newTestService()
	cid := publicClient(t, s)
	uid, ws := uuid.New(), uuid.New()
	code := seedCode(t, f, s, cid, []string{"contacts:read"}, s256(testVerifier), uid, ws)

	_, err := s.Token(context.Background(), TokenRequest{
		GrantType: "authorization_code", Code: code, RedirectURI: testRedirectURI,
		CodeVerifier: testCodeVerifierAlt, Client: ClientCredentials{ID: cid},
	})
	if te := asTokenErr(t, err); te.Code != "invalid_grant" {
		t.Fatalf("wrong verifier: want invalid_grant, got %s", te.Code)
	}
}

func TestTokenAuthCodePKCEMissingVerifier(t *testing.T) {
	s, f := newTestService()
	cid := publicClient(t, s)
	uid, ws := uuid.New(), uuid.New()
	code := seedCode(t, f, s, cid, []string{"contacts:read"}, s256(testVerifier), uid, ws)

	_, err := s.Token(context.Background(), TokenRequest{
		GrantType: "authorization_code", Code: code, RedirectURI: testRedirectURI,
		CodeVerifier: "", Client: ClientCredentials{ID: cid},
	})
	if te := asTokenErr(t, err); te.Code != "invalid_grant" {
		t.Fatalf("missing verifier: want invalid_grant, got %s", te.Code)
	}
}

// TestVerifyPKCEVector asserts the S256 computation against the RFC 7636 vector.
func TestVerifyPKCEVector(t *testing.T) {
	if !verifyPKCE(testVerifier, testChallengeS256) {
		t.Fatal("RFC 7636 vector must verify")
	}
	if verifyPKCE("wrong", testChallengeS256) {
		t.Fatal("a wrong verifier must not verify")
	}
	if verifyPKCE("", testChallengeS256) || verifyPKCE(testVerifier, "") {
		t.Fatal("empty verifier/challenge must not verify")
	}
}

func TestTokenAuthCodeRedirectMismatch(t *testing.T) {
	s, f := newTestService()
	cid := publicClient(t, s)
	uid, ws := uuid.New(), uuid.New()
	code := seedCode(t, f, s, cid, []string{"contacts:read"}, s256(testVerifier), uid, ws)

	_, err := s.Token(context.Background(), TokenRequest{
		GrantType: "authorization_code", Code: code, RedirectURI: "https://evil.example.com/cb",
		CodeVerifier: testVerifier, Client: ClientCredentials{ID: cid},
	})
	if te := asTokenErr(t, err); te.Code != "invalid_grant" {
		t.Fatalf("redirect mismatch: want invalid_grant, got %s", te.Code)
	}
}

func TestTokenAuthCodeClientMismatch(t *testing.T) {
	s, f := newTestService()
	cidA := publicClient(t, s)
	cidB := publicClient(t, s)
	uid, ws := uuid.New(), uuid.New()
	// Code issued to A, but B (also authenticated) tries to redeem it.
	code := seedCode(t, f, s, cidA, []string{"contacts:read"}, s256(testVerifier), uid, ws)

	_, err := s.Token(context.Background(), TokenRequest{
		GrantType: "authorization_code", Code: code, RedirectURI: testRedirectURI,
		CodeVerifier: testVerifier, Client: ClientCredentials{ID: cidB},
	})
	if te := asTokenErr(t, err); te.Code != "invalid_grant" {
		t.Fatalf("client mismatch: want invalid_grant, got %s", te.Code)
	}
}

// --- client authentication ---------------------------------------------------

func TestTokenConfidentialGoodSecret(t *testing.T) {
	s, f := newTestService()
	cid, secret := confidentialClient(t, s)
	uid, ws := uuid.New(), uuid.New()
	code := seedCode(t, f, s, cid, []string{"contacts:read"}, s256(testVerifier), uid, ws)

	_, err := s.Token(context.Background(), TokenRequest{
		GrantType: "authorization_code", Code: code, RedirectURI: testRedirectURI,
		CodeVerifier: testVerifier, Client: ClientCredentials{ID: cid, Secret: secret, HasSecret: true},
	})
	if err != nil {
		t.Fatalf("good secret: %v", err)
	}
}

func TestTokenConfidentialBadSecret(t *testing.T) {
	s, f := newTestService()
	cid, _ := confidentialClient(t, s)
	uid, ws := uuid.New(), uuid.New()
	code := seedCode(t, f, s, cid, []string{"contacts:read"}, s256(testVerifier), uid, ws)

	_, err := s.Token(context.Background(), TokenRequest{
		GrantType: "authorization_code", Code: code, RedirectURI: testRedirectURI,
		CodeVerifier: testVerifier, Client: ClientCredentials{ID: cid, Secret: "wrong-secret", HasSecret: true},
	})
	te := asTokenErr(t, err)
	if te.Code != "invalid_client" || te.Status != 401 {
		t.Fatalf("bad secret: want invalid_client/401, got %s/%d", te.Code, te.Status)
	}
}

func TestTokenConfidentialNoSecret(t *testing.T) {
	s, f := newTestService()
	cid, _ := confidentialClient(t, s)
	uid, ws := uuid.New(), uuid.New()
	code := seedCode(t, f, s, cid, []string{"contacts:read"}, s256(testVerifier), uid, ws)

	_, err := s.Token(context.Background(), TokenRequest{
		GrantType: "authorization_code", Code: code, RedirectURI: testRedirectURI,
		CodeVerifier: testVerifier, Client: ClientCredentials{ID: cid}, // no secret
	})
	if te := asTokenErr(t, err); te.Code != "invalid_client" {
		t.Fatalf("confidential without secret: want invalid_client, got %s", te.Code)
	}
}

func TestTokenUnknownClient(t *testing.T) {
	s, _ := newTestService()
	_, err := s.Token(context.Background(), TokenRequest{
		GrantType: "authorization_code", Code: "x", RedirectURI: testRedirectURI,
		CodeVerifier: testVerifier, Client: ClientCredentials{ID: "inrc_nope"},
	})
	if te := asTokenErr(t, err); te.Code != "invalid_client" {
		t.Fatalf("unknown client: want invalid_client, got %s", te.Code)
	}
}

func TestTokenUnsupportedGrant(t *testing.T) {
	s, _ := newTestService()
	cid := publicClient(t, s)
	for _, g := range []string{"password", "client_credentials", "implicit", ""} {
		_, err := s.Token(context.Background(), TokenRequest{GrantType: g, Client: ClientCredentials{ID: cid}})
		if te := asTokenErr(t, err); te.Code != "unsupported_grant_type" {
			t.Fatalf("grant %q: want unsupported_grant_type, got %s", g, te.Code)
		}
	}
}

// --- refresh_token grant -----------------------------------------------------

// exchangeForTokens runs an authorization_code exchange and returns the response.
func exchangeForTokens(t *testing.T, s *Service, f *fakeStore, cid string, scopes []string) TokenResponse {
	t.Helper()
	uid, ws := uuid.New(), uuid.New()
	code := seedCode(t, f, s, cid, scopes, s256(testVerifier), uid, ws)
	res, err := s.Token(context.Background(), TokenRequest{
		GrantType: "authorization_code", Code: code, RedirectURI: testRedirectURI,
		CodeVerifier: testVerifier, Client: ClientCredentials{ID: cid},
	})
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}
	return res
}

func TestRefreshRotatesPair(t *testing.T) {
	s, f := newTestService()
	cid := publicClient(t, s)
	first := exchangeForTokens(t, s, f, cid, []string{"contacts:read", "lists:read"})

	second, err := s.Token(context.Background(), TokenRequest{
		GrantType: "refresh_token", RefreshToken: first.RefreshToken, Client: ClientCredentials{ID: cid},
	})
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if second.AccessToken == first.AccessToken || second.RefreshToken == first.RefreshToken {
		t.Fatal("refresh must issue a NEW access + refresh token")
	}
	if second.Scope != "contacts:read lists:read" {
		t.Fatalf("scope not carried forward: %q", second.Scope)
	}
}

func TestRefreshReuseRevokesFamily(t *testing.T) {
	s, f := newTestService()
	cid := publicClient(t, s)
	first := exchangeForTokens(t, s, f, cid, []string{"contacts:read"})

	// Rotate once: first.RefreshToken is now consumed, second issued in the same family.
	second, err := s.Token(context.Background(), TokenRequest{
		GrantType: "refresh_token", RefreshToken: first.RefreshToken, Client: ClientCredentials{ID: cid},
	})
	if err != nil {
		t.Fatalf("first refresh: %v", err)
	}
	// Replay the CONSUMED first token -> reuse detected -> family revoked + invalid_grant.
	_, err = s.Token(context.Background(), TokenRequest{
		GrantType: "refresh_token", RefreshToken: first.RefreshToken, Client: ClientCredentials{ID: cid},
	})
	if te := asTokenErr(t, err); te.Code != "invalid_grant" {
		t.Fatalf("reuse: want invalid_grant, got %s", te.Code)
	}
	// The still-valid second token is now revoked too (whole family killed).
	_, err = s.Token(context.Background(), TokenRequest{
		GrantType: "refresh_token", RefreshToken: second.RefreshToken, Client: ClientCredentials{ID: cid},
	})
	if te := asTokenErr(t, err); te.Code != "invalid_grant" {
		t.Fatalf("family sibling after reuse: want invalid_grant, got %s", te.Code)
	}
}

func TestRefreshScopeNarrowingAllowed(t *testing.T) {
	s, f := newTestService()
	cid := publicClient(t, s)
	first := exchangeForTokens(t, s, f, cid, []string{"contacts:read", "lists:read"})

	res, err := s.Token(context.Background(), TokenRequest{
		GrantType: "refresh_token", RefreshToken: first.RefreshToken,
		Scope: "contacts:read", Client: ClientCredentials{ID: cid},
	})
	if err != nil {
		t.Fatalf("narrow: %v", err)
	}
	if res.Scope != "contacts:read" {
		t.Fatalf("want narrowed scope, got %q", res.Scope)
	}
}

func TestRefreshScopeWideningRejected(t *testing.T) {
	s, f := newTestService()
	cid := publicClient(t, s)
	first := exchangeForTokens(t, s, f, cid, []string{"contacts:read"})

	_, err := s.Token(context.Background(), TokenRequest{
		GrantType: "refresh_token", RefreshToken: first.RefreshToken,
		Scope: "contacts:read lists:read", Client: ClientCredentials{ID: cid},
	})
	if te := asTokenErr(t, err); te.Code != "invalid_scope" {
		t.Fatalf("widen: want invalid_scope, got %s", te.Code)
	}
}

func TestRefreshWrongClientRejected(t *testing.T) {
	s, f := newTestService()
	cid := publicClient(t, s)
	other := publicClient(t, s)
	first := exchangeForTokens(t, s, f, cid, []string{"contacts:read"})

	_, err := s.Token(context.Background(), TokenRequest{
		GrantType: "refresh_token", RefreshToken: first.RefreshToken, Client: ClientCredentials{ID: other},
	})
	if te := asTokenErr(t, err); te.Code != "invalid_grant" {
		t.Fatalf("foreign client refresh: want invalid_grant, got %s", te.Code)
	}
}

func TestRefreshUnknownToken(t *testing.T) {
	s, _ := newTestService()
	cid := publicClient(t, s)
	_, err := s.Token(context.Background(), TokenRequest{
		GrantType: "refresh_token", RefreshToken: "inor_nope", Client: ClientCredentials{ID: cid},
	})
	if te := asTokenErr(t, err); te.Code != "invalid_grant" {
		t.Fatalf("unknown refresh: want invalid_grant, got %s", te.Code)
	}
}

// --- introspection -----------------------------------------------------------

func TestIntrospectActiveAccessToken(t *testing.T) {
	s, f := newTestService()
	cid := publicClient(t, s)
	tok := exchangeForTokens(t, s, f, cid, []string{"contacts:read"})

	res, err := s.Introspect(context.Background(), tok.AccessToken, ClientCredentials{ID: cid})
	if err != nil {
		t.Fatalf("introspect: %v", err)
	}
	if !res.Active || res.ClientID != cid || res.Scope != "contacts:read" || res.TokenType != "Bearer" {
		t.Fatalf("active access token metadata wrong: %+v", res)
	}
}

func TestIntrospectInactiveForUnknown(t *testing.T) {
	s, _ := newTestService()
	cid := publicClient(t, s)
	res, err := s.Introspect(context.Background(), "inoa_unknown", ClientCredentials{ID: cid})
	if err != nil {
		t.Fatalf("introspect unknown: %v", err)
	}
	if res.Active || res.ClientID != "" || res.Scope != "" {
		t.Fatalf("unknown token must be inactive with no detail: %+v", res)
	}
}

func TestIntrospectInactiveAfterRevoke(t *testing.T) {
	s, f := newTestService()
	cid := publicClient(t, s)
	tok := exchangeForTokens(t, s, f, cid, []string{"contacts:read"})
	if err := s.Revoke(context.Background(), tok.AccessToken, ClientCredentials{ID: cid}); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	res, _ := s.Introspect(context.Background(), tok.AccessToken, ClientCredentials{ID: cid})
	if res.Active {
		t.Fatal("revoked access token must introspect inactive")
	}
}

func TestIntrospectRequiresClientAuth(t *testing.T) {
	s, f := newTestService()
	cid, secret := confidentialClient(t, s)
	uid, ws := uuid.New(), uuid.New()
	code := seedCode(t, f, s, cid, []string{"contacts:read"}, s256(testVerifier), uid, ws)
	tok, err := s.Token(context.Background(), TokenRequest{
		GrantType: "authorization_code", Code: code, RedirectURI: testRedirectURI,
		CodeVerifier: testVerifier, Client: ClientCredentials{ID: cid, Secret: secret, HasSecret: true},
	})
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}
	// Confidential client, but no secret presented -> invalid_client (not an oracle answer).
	_, err = s.Introspect(context.Background(), tok.AccessToken, ClientCredentials{ID: cid})
	if te := asTokenErr(t, err); te.Code != "invalid_client" {
		t.Fatalf("introspect without client auth: want invalid_client, got %s", te.Code)
	}
}

// --- revocation --------------------------------------------------------------

func TestRevokeOwnAccessToken(t *testing.T) {
	s, f := newTestService()
	cid := publicClient(t, s)
	tok := exchangeForTokens(t, s, f, cid, []string{"contacts:read"})

	if err := s.Revoke(context.Background(), tok.AccessToken, ClientCredentials{ID: cid}); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if at, _ := f.GetAccessToken(context.Background(), hashSecret(tok.AccessToken)); !at.RevokedAt.Valid {
		t.Fatal("access token should be revoked")
	}
}

func TestRevokeRefreshRevokesFamily(t *testing.T) {
	s, f := newTestService()
	cid := publicClient(t, s)
	tok := exchangeForTokens(t, s, f, cid, []string{"contacts:read"})

	if err := s.Revoke(context.Background(), tok.RefreshToken, ClientCredentials{ID: cid}); err != nil {
		t.Fatalf("revoke refresh: %v", err)
	}
	// The refresh token can no longer rotate.
	_, err := s.Token(context.Background(), TokenRequest{
		GrantType: "refresh_token", RefreshToken: tok.RefreshToken, Client: ClientCredentials{ID: cid},
	})
	if te := asTokenErr(t, err); te.Code != "invalid_grant" {
		t.Fatalf("revoked refresh: want invalid_grant, got %s", te.Code)
	}
}

func TestRevokeForeignTokenIsNoOp(t *testing.T) {
	s, f := newTestService()
	cid := publicClient(t, s)
	other := publicClient(t, s)
	tok := exchangeForTokens(t, s, f, cid, []string{"contacts:read"})

	// `other` tries to revoke cid's access token: 200 no-op (no oracle), token stays live.
	if err := s.Revoke(context.Background(), tok.AccessToken, ClientCredentials{ID: other}); err != nil {
		t.Fatalf("foreign revoke should be a silent no-op, got %v", err)
	}
	if at, _ := f.GetAccessToken(context.Background(), hashSecret(tok.AccessToken)); at.RevokedAt.Valid {
		t.Fatal("another client must not be able to revoke this token")
	}
}

func TestRevokeUnknownTokenIsNoOp(t *testing.T) {
	s, _ := newTestService()
	cid := publicClient(t, s)
	if err := s.Revoke(context.Background(), "inoa_unknown", ClientCredentials{ID: cid}); err != nil {
		t.Fatalf("unknown revoke should be a no-op, got %v", err)
	}
}

// hexHash is a test helper mirroring the fake's key encoding.
func hexHash(raw string) string {
	return hex.EncodeToString(hashSecret(raw))
}
