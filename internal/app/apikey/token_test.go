package apikey

import (
	"crypto/sha256"
	"strings"
	"testing"
)

// TestNewTokenShapeAndHash proves a minted token has the inrd_<prefix>_<secret>
// shape, an 8-char prefix, and that the stored hash is the SHA-256 of the secret
// part (never the secret itself).
func TestNewTokenShapeAndHash(t *testing.T) {
	prefix, token, hash, err := newToken()
	if err != nil {
		t.Fatalf("newToken: %v", err)
	}
	if !strings.HasPrefix(token, tokenScheme) {
		t.Fatalf("token %q missing scheme %q", token, tokenScheme)
	}
	if len(prefix) != 8 {
		t.Fatalf("prefix %q len = %d, want 8", prefix, len(prefix))
	}
	// token = inrd_<prefix>_<secret>
	rest := strings.TrimPrefix(token, tokenScheme)
	gotPrefix, secret, ok := strings.Cut(rest, "_")
	if !ok {
		t.Fatalf("token %q not in <prefix>_<secret> form", token)
	}
	if gotPrefix != prefix {
		t.Fatalf("embedded prefix %q != returned %q", gotPrefix, prefix)
	}
	if secret == "" {
		t.Fatal("empty secret")
	}
	want := sha256.Sum256([]byte(secret))
	if string(hash) != string(want[:]) {
		t.Fatal("stored hash is not SHA-256 of the secret")
	}
	// The raw secret must not be recoverable from the hash: a hash is 32 bytes and
	// unequal to the secret text.
	if string(hash) == secret {
		t.Fatal("hash equals raw secret")
	}
}

// TestNewTokenUnique proves two mints differ in both prefix and secret.
func TestNewTokenUnique(t *testing.T) {
	p1, t1, _, _ := newToken()
	p2, t2, _, _ := newToken()
	if p1 == p2 {
		t.Fatal("two mints shared a prefix")
	}
	if t1 == t2 {
		t.Fatal("two mints shared a token")
	}
}

// TestParseTokenRoundTrip proves parseToken recovers the prefix and the SHA-256 of
// the secret from a freshly minted token.
func TestParseTokenRoundTrip(t *testing.T) {
	prefix, token, hash, _ := newToken()
	gotPrefix, gotHash, ok := parseToken(token)
	if !ok {
		t.Fatal("parseToken failed on a valid token")
	}
	if gotPrefix != prefix {
		t.Fatalf("prefix = %q, want %q", gotPrefix, prefix)
	}
	if string(gotHash) != string(hash) {
		t.Fatal("parsed secret hash mismatch")
	}
}

// TestParseTokenRejectsMalformed proves non-token and malformed strings do not
// parse (the verifier treats a false here as either DEFER or a hard reject
// depending on the scheme).
func TestParseTokenRejectsMalformed(t *testing.T) {
	cases := []string{
		"",
		"eyJhbGciOiJIUzI1NiJ9.jwt.sig", // a JWT
		"inrd_",                        // scheme only
		"inrd_onlyprefix",              // no secret separator
		"inrd__secret",                 // empty prefix
		"inrd_prefix_",                 // empty secret
		"nope_abc_def",                 // wrong scheme
	}
	for _, c := range cases {
		if _, _, ok := parseToken(c); ok {
			t.Fatalf("parseToken(%q) = ok, want not ok", c)
		}
	}
}

// TestHasScheme proves the engage-vs-defer discriminator: only an inrd_ token
// engages the api-key verifier; a JWT or bare value defers.
func TestHasScheme(t *testing.T) {
	if !hasScheme("inrd_abc_def") {
		t.Fatal("inrd_ token should have scheme")
	}
	for _, s := range []string{"", "eyJ.jwt.sig", "bearer-ish", "INRD_upper"} {
		if hasScheme(s) {
			t.Fatalf("hasScheme(%q) = true, want false", s)
		}
	}
}
