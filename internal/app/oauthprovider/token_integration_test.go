//go:build integration

package oauthprovider

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/app/auth"
)

// pkce computes the S256 challenge for a verifier (integration-local copy so this file
// is self-contained under the integration build tag).
func pkce(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

const itVerifier = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"

// codeFor drives authorize -> approve for a registered client and returns the raw code.
func codeFor(t *testing.T, svc *Service, clientID string, owner Owner) string {
	t.Helper()
	ctx := context.Background()
	in := baseAuthorize(clientID)
	in.CodeChallenge = pkce(itVerifier)
	in.Owner = &owner
	authRes, err := svc.Authorize(ctx, in)
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	u, _ := url.Parse(authRes.RedirectTo)
	consentID := u.Query().Get("consent_id")
	if consentID == "" {
		t.Fatalf("no consent handoff: %s", authRes.RedirectTo)
	}
	dec, err := svc.DecideConsent(ctx, DecideInput{ConsentID: consentID, UserID: owner.UserID, Approve: true})
	if err != nil {
		t.Fatalf("DecideConsent: %v", err)
	}
	ru, _ := url.Parse(dec.RedirectTo)
	code := ru.Query().Get("code")
	if code == "" {
		t.Fatalf("no code: %s", dec.RedirectTo)
	}
	return code
}

// TestTokenRoundTripAndReuseDetection is the full P6b round-trip over real Postgres:
// register -> authorize -> consent -> code -> token -> use the access token via the
// oauthVerifier -> refresh -> reuse-detection revokes the family -> revoked access token
// is rejected.
func TestTokenRoundTripAndReuseDetection(t *testing.T) {
	ctx := context.Background()
	svc, store, mint := itSetup(t)
	ws, uid := mint()
	owner := Owner{UserID: uid, WorkspaceID: ws}

	reg, err := svc.RegisterClient(ctx, RegisterInput{
		ClientName:   "Token RT",
		RedirectURIs: []string{testRedirectURI},
		Scope:        "contacts:read lists:read",
		// Registered for BOTH grants: the round-trip exercises authorization_code then
		// refresh_token, and /token now enforces the client's registered grant_types.
		GrantTypes: []string{grantAuthorizationCode, grantRefreshToken},
		CreatedBy:  &uid, WorkspaceID: &ws,
	})
	if err != nil {
		t.Fatalf("RegisterClient: %v", err)
	}
	cid := reg.Client.ClientID

	// Exchange the authorization code for a token pair.
	code := codeFor(t, svc, cid, owner)
	tok, err := svc.Token(ctx, TokenRequest{
		GrantType: "authorization_code", Code: code, RedirectURI: testRedirectURI,
		CodeVerifier: itVerifier, Client: ClientCredentials{ID: cid},
	})
	if err != nil {
		t.Fatalf("Token(auth code): %v", err)
	}
	if tok.AccessToken == "" || tok.RefreshToken == "" {
		t.Fatalf("empty token pair: %+v", tok)
	}

	// The oauthVerifier authenticates the access token and mints a scoped principal.
	v := NewVerifier(store)
	r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1/contacts", http.NoBody)
	r.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	p, ok, err := v.Verify(ctx, r)
	if err != nil || !ok {
		t.Fatalf("verify access token: ok=%v err=%v", ok, err)
	}
	if p.Kind != auth.KindOAuth || p.WorkspaceID != ws.String() || p.UserID != uid.String() {
		t.Fatalf("principal wrong: %+v", p)
	}

	// A second redeem of the SAME code fails (single-use consume).
	if _, err := svc.Token(ctx, TokenRequest{
		GrantType: "authorization_code", Code: code, RedirectURI: testRedirectURI,
		CodeVerifier: itVerifier, Client: ClientCredentials{ID: cid},
	}); err == nil {
		t.Fatal("second redeem of a consumed code must fail")
	}

	// Rotate the refresh token: new pair, same family.
	rot, err := svc.Token(ctx, TokenRequest{
		GrantType: "refresh_token", RefreshToken: tok.RefreshToken, Client: ClientCredentials{ID: cid},
	})
	if err != nil {
		t.Fatalf("Token(refresh): %v", err)
	}
	if rot.RefreshToken == tok.RefreshToken || rot.AccessToken == tok.AccessToken {
		t.Fatal("rotation must issue a new pair")
	}

	// Reuse the CONSUMED first refresh token -> family revoked + invalid_grant.
	if _, err := svc.Token(ctx, TokenRequest{
		GrantType: "refresh_token", RefreshToken: tok.RefreshToken, Client: ClientCredentials{ID: cid},
	}); err == nil {
		t.Fatal("reuse of a consumed refresh token must fail")
	}
	// The rotated (previously valid) refresh token is now revoked with the family.
	if _, err := svc.Token(ctx, TokenRequest{
		GrantType: "refresh_token", RefreshToken: rot.RefreshToken, Client: ClientCredentials{ID: cid},
	}); err == nil {
		t.Fatal("family sibling must be revoked after reuse detection")
	}

	// Family-access-revoke (the P6b signature property): reuse detection ALSO killed the
	// ACCESS token minted in that family (rot.AccessToken), so the oauthVerifier rejects it
	// on the very next request — WITHOUT any explicit RFC 7009 revoke. Without this, a
	// compromised chain's access token would stay valid for its full ~1h TTL after reuse.
	rReuse := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1/contacts", http.NoBody)
	rReuse.Header.Set("Authorization", "Bearer "+rot.AccessToken)
	if _, ok, _ := v.Verify(ctx, rReuse); ok {
		t.Fatal("family-access-revoke: an access token in a reuse-revoked family must be rejected")
	}

	// And an explicit RFC 7009 access-token revoke is idempotent on the now already-revoked
	// token and the verifier still rejects it.
	if err := svc.Revoke(ctx, rot.AccessToken, ClientCredentials{ID: cid}); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	r2 := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1/contacts", http.NoBody)
	r2.Header.Set("Authorization", "Bearer "+rot.AccessToken)
	if _, ok, _ := v.Verify(ctx, r2); ok {
		t.Fatal("revoked access token must be rejected")
	}
}

// registerBothGrants registers a public client for authorization_code + refresh_token
// against real Postgres and returns its client_id.
func registerBothGrants(t *testing.T, svc *Service, name string, uid, ws uuid.UUID) string {
	t.Helper()
	reg, err := svc.RegisterClient(context.Background(), RegisterInput{
		ClientName: name, RedirectURIs: []string{testRedirectURI},
		Scope:      "contacts:read lists:read",
		GrantTypes: []string{grantAuthorizationCode, grantRefreshToken},
		CreatedBy:  &uid, WorkspaceID: &ws,
	})
	if err != nil {
		t.Fatalf("RegisterClient(%s): %v", name, err)
	}
	return reg.Client.ClientID
}

// TestRevokeForeignTokensNoOpDB proves the own-client revoke policy against real Postgres
// for BOTH token types: a client revoking ANOTHER client's ACCESS token — and, on the
// separate refresh lookup path, another client's REFRESH token — is a silent 200 no-op,
// leaving the victim token live. Mirrors the fake-store TestRevokeForeignTokenIsNoOp.
func TestRevokeForeignTokensNoOpDB(t *testing.T) {
	ctx := context.Background()
	svc, store, mint := itSetup(t)
	ws, uid := mint()
	owner := Owner{UserID: uid, WorkspaceID: ws}

	victim := registerBothGrants(t, svc, "Victim", uid, ws)
	attacker := registerBothGrants(t, svc, "Attacker", uid, ws)

	code := codeFor(t, svc, victim, owner)
	tok, err := svc.Token(ctx, TokenRequest{
		GrantType: "authorization_code", Code: code, RedirectURI: testRedirectURI,
		CodeVerifier: itVerifier, Client: ClientCredentials{ID: victim},
	})
	if err != nil {
		t.Fatalf("Token: %v", err)
	}

	// Attacker revokes the victim's ACCESS token -> silent no-op; token stays live.
	if err := svc.Revoke(ctx, tok.AccessToken, ClientCredentials{ID: attacker}); err != nil {
		t.Fatalf("foreign access revoke should be a silent no-op: %v", err)
	}
	v := NewVerifier(store)
	r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1/contacts", http.NoBody)
	r.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	if _, ok, err := v.Verify(ctx, r); !ok || err != nil {
		t.Fatalf("victim access token must stay live after a foreign revoke: ok=%v err=%v", ok, err)
	}

	// Attacker revokes the victim's REFRESH token (separate store lookup path) -> no-op;
	// the victim can still rotate it.
	if err := svc.Revoke(ctx, tok.RefreshToken, ClientCredentials{ID: attacker}); err != nil {
		t.Fatalf("foreign refresh revoke should be a silent no-op: %v", err)
	}
	if _, err := svc.Token(ctx, TokenRequest{
		GrantType: "refresh_token", RefreshToken: tok.RefreshToken, Client: ClientCredentials{ID: victim},
	}); err != nil {
		t.Fatalf("victim refresh token must still rotate after a foreign revoke: %v", err)
	}
}

// TestConcurrentCodeRedeemSingleWinner proves the atomic single-use consume: two
// concurrent redeems of the same code yield exactly one success.
func TestConcurrentCodeRedeemSingleWinner(t *testing.T) {
	ctx := context.Background()
	svc, _, mint := itSetup(t)
	ws, uid := mint()
	owner := Owner{UserID: uid, WorkspaceID: ws}

	reg, err := svc.RegisterClient(ctx, RegisterInput{
		ClientName: "Concurrent", RedirectURIs: []string{testRedirectURI},
		Scope: "contacts:read", CreatedBy: &uid, WorkspaceID: &ws,
	})
	if err != nil {
		t.Fatalf("RegisterClient: %v", err)
	}
	cid := reg.Client.ClientID
	code := codeFor(t, svc, cid, owner)

	type result struct {
		err error
	}
	const n = 8
	results := make(chan result, n)
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		go func() {
			<-start
			_, err := svc.Token(ctx, TokenRequest{
				GrantType: "authorization_code", Code: code, RedirectURI: testRedirectURI,
				CodeVerifier: itVerifier, Client: ClientCredentials{ID: cid},
			})
			results <- result{err: err}
		}()
	}
	close(start)
	var wins int
	for i := 0; i < n; i++ {
		if (<-results).err == nil {
			wins++
		}
	}
	if wins != 1 {
		t.Fatalf("exactly one concurrent redeem must win, got %d", wins)
	}
}

// TestIntrospectRoundTrip exercises introspection against real rows: an active access
// token, then inactive after revoke, and an unknown token is inactive.
func TestIntrospectRoundTrip(t *testing.T) {
	ctx := context.Background()
	svc, _, mint := itSetup(t)
	ws, uid := mint()
	owner := Owner{UserID: uid, WorkspaceID: ws}

	reg, err := svc.RegisterClient(ctx, RegisterInput{
		ClientName: "Introspect", RedirectURIs: []string{testRedirectURI},
		Scope: "contacts:read", CreatedBy: &uid, WorkspaceID: &ws,
	})
	if err != nil {
		t.Fatalf("RegisterClient: %v", err)
	}
	cid := reg.Client.ClientID
	code := codeFor(t, svc, cid, owner)
	tok, err := svc.Token(ctx, TokenRequest{
		GrantType: "authorization_code", Code: code, RedirectURI: testRedirectURI,
		CodeVerifier: itVerifier, Client: ClientCredentials{ID: cid},
	})
	if err != nil {
		t.Fatalf("Token: %v", err)
	}

	res, err := svc.Introspect(ctx, tok.AccessToken, ClientCredentials{ID: cid})
	if err != nil || !res.Active || res.ClientID != cid {
		t.Fatalf("active introspect wrong: %+v err=%v", res, err)
	}
	if err := svc.Revoke(ctx, tok.AccessToken, ClientCredentials{ID: cid}); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if res, _ := svc.Introspect(ctx, tok.AccessToken, ClientCredentials{ID: cid}); res.Active {
		t.Fatal("revoked token must introspect inactive")
	}
	if res, _ := svc.Introspect(ctx, "inoa_"+uuid.NewString(), ClientCredentials{ID: cid}); res.Active {
		t.Fatal("unknown token must introspect inactive")
	}
}
