package identity

import (
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"
)

const testGoogleClientID = "123456.apps.googleusercontent.com"

// makeIDToken builds a three-part token whose payload is claims. The signature
// segment is junk on purpose: parseGoogleIDToken does not verify it (the token
// arrives over a direct TLS call to Google's token endpoint), and this makes that
// explicit rather than accidental.
func makeIDToken(t *testing.T, claims googleClaims) string {
	t.Helper()
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatalf("marshal claims: %v", err)
	}
	b64 := base64.RawURLEncoding.EncodeToString
	return b64([]byte(`{"alg":"RS256"}`)) + "." + b64(payload) + "." + b64([]byte("not-a-signature"))
}

func validClaims(now time.Time) googleClaims {
	return googleClaims{
		Issuer: "https://accounts.google.com", Audience: testGoogleClientID,
		Subject: "google-sub-1", Expiry: now.Add(time.Hour).Unix(),
		Email: "dana@axomble.com", EmailVerified: true, GivenName: "Dana", HostedDomain: "axomble.com",
	}
}

func TestParseGoogleIDTokenReadsClaims(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	id, err := parseGoogleIDToken(makeIDToken(t, validClaims(now)), testGoogleClientID, now)
	if err != nil {
		t.Fatalf("parseGoogleIDToken: %v", err)
	}
	want := GoogleIdentity{
		Subject: "google-sub-1", Email: "dana@axomble.com", EmailVerified: true,
		GivenName: "Dana", HostedDomain: "axomble.com",
	}
	if id != want {
		t.Fatalf("want %+v, got %+v", want, id)
	}
}

// email_verified=false is carried through faithfully rather than defaulted to
// true — the caller refuses signup and linking on it, so silently coercing it
// would defeat that check.
func TestParseGoogleIDTokenPreservesUnverifiedEmail(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	c := validClaims(now)
	c.EmailVerified = false
	id, err := parseGoogleIDToken(makeIDToken(t, c), testGoogleClientID, now)
	if err != nil {
		t.Fatalf("parseGoogleIDToken: %v", err)
	}
	if id.EmailVerified {
		t.Fatal("want email_verified false to survive parsing")
	}
}

func TestParseGoogleIDTokenRejects(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)

	altAudience := validClaims(now)
	altAudience.Audience = "999999.apps.googleusercontent.com"

	badIssuer := validClaims(now)
	badIssuer.Issuer = "https://accounts.evil.example"

	expired := validClaims(now)
	expired.Expiry = now.Add(-time.Second).Unix()

	noExpiry := validClaims(now)
	noExpiry.Expiry = 0

	noSubject := validClaims(now)
	noSubject.Subject = ""

	tests := []struct {
		name  string
		token string
	}{
		// The audience check is the important one: a token minted for a DIFFERENT
		// Google client must never authenticate a user here.
		{"audience is another client", makeIDToken(t, altAudience)},
		{"unexpected issuer", makeIDToken(t, badIssuer)},
		{"expired", makeIDToken(t, expired)},
		{"no expiry claim", makeIDToken(t, noExpiry)},
		{"no subject claim", makeIDToken(t, noSubject)},
		{"not a three-part jwt", "header.payload"},
		{"payload not base64url", "aaa.!!!!.ccc"},
		{"payload not json", "aaa." + base64.RawURLEncoding.EncodeToString([]byte("nope")) + ".ccc"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := parseGoogleIDToken(tt.token, testGoogleClientID, now); err == nil {
				t.Fatal("want an error, got nil")
			}
		})
	}
}

// The login flow requests only openid/email/profile. Gmail scopes here would drag
// every sign-in through a restricted-scope consent screen for permissions login
// does not need, and would yield a mailbox token with no per-workspace DEK to be
// sealed under (the workspace does not exist yet at sign-up).
func TestLoginScopesExcludeGmail(t *testing.T) {
	want := map[string]bool{"openid": true, "email": true, "profile": true}
	for _, s := range loginScopes {
		if !want[s] {
			t.Fatalf("unexpected login scope %q", s)
		}
	}
	if len(loginScopes) != len(want) {
		t.Fatalf("want exactly %d login scopes, got %v", len(want), loginScopes)
	}
}

func TestGoogleAuthenticatorEnabled(t *testing.T) {
	tests := []struct {
		name string
		cfg  GoogleSignIn
		want bool
	}{
		{"fully configured", GoogleSignIn{ClientID: "id", ClientSecret: "secret"}, true},
		{"no secret", GoogleSignIn{ClientID: "id"}, false},
		{"no id", GoogleSignIn{ClientSecret: "secret"}, false},
		{"zero value", GoogleSignIn{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := NewGoogleAuthenticator(tt.cfg).Enabled(); got != tt.want {
				t.Fatalf("want %v, got %v", tt.want, got)
			}
		})
	}
}
