package mail

import (
	"testing"
	"time"

	"golang.org/x/oauth2"
)

func TestMarshalUnmarshalTokenRoundTrip(t *testing.T) {
	exp := time.Unix(1_700_000_000, 0)
	in := &oauth2.Token{AccessToken: "at", RefreshToken: "rt", TokenType: "Bearer", Expiry: exp}
	b, err := MarshalToken(in)
	if err != nil {
		t.Fatal(err)
	}
	out, err := UnmarshalToken(b)
	if err != nil {
		t.Fatal(err)
	}
	if out.AccessToken != "at" || out.RefreshToken != "rt" || !out.Expiry.Equal(exp) {
		t.Fatalf("round trip mismatch: %+v", out)
	}
}

func TestGoogleOAuthEnabled(t *testing.T) {
	if (GoogleOAuth{}).Enabled() {
		t.Fatal("empty config must be disabled")
	}
	if !(GoogleOAuth{ClientID: "a", ClientSecret: "b"}).Enabled() {
		t.Fatal("configured must be enabled")
	}
}
