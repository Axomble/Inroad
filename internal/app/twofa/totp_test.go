package twofa

import (
	"encoding/base32"
	"net/url"
	"strings"
	"testing"
	"time"
)

// TestHOTPRFC4226Vectors checks hotp against the published RFC 4226 Appendix D
// test vectors (secret "12345678901234567890", counters 0..9).
func TestHOTPRFC4226Vectors(t *testing.T) {
	secret := []byte("12345678901234567890")
	want := []string{
		"755224", "287082", "359152", "969429", "338314",
		"254676", "287922", "162583", "399871", "520489",
	}
	for c, w := range want {
		if got := hotp(secret, uint64(c)); got != w {
			t.Errorf("hotp(counter=%d) = %s, want %s", c, got, w)
		}
	}
}

// TestTOTPRFC6238Vector checks the SHA-1 TOTP vector from RFC 6238 Appendix B at
// T=59s (counter 1) with the 20-byte ASCII seed, truncated to 6 digits.
func TestTOTPRFC6238Vector(t *testing.T) {
	secret := []byte("12345678901234567890")
	got := totpAt(secret, time.Unix(59, 0).UTC())
	if got != "287082" {
		t.Fatalf("totpAt(T=59) = %s, want 287082", got)
	}
}

func TestVerifyTOTPValid(t *testing.T) {
	secret, err := newTOTPSecret()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if !verifyTOTP(secret, totpAt(secret, now), now) {
		t.Fatal("current-step code should verify")
	}
}

func TestVerifyTOTPAcceptsOneStepSkew(t *testing.T) {
	secret, err := newTOTPSecret()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	prev := now.Add(-totpPeriodSec * time.Second)
	next := now.Add(totpPeriodSec * time.Second)
	if !verifyTOTP(secret, totpAt(secret, prev), now) {
		t.Error("previous-step code should verify within ±1 skew")
	}
	if !verifyTOTP(secret, totpAt(secret, next), now) {
		t.Error("next-step code should verify within ±1 skew")
	}
}

func TestVerifyTOTPRejectsOutsideWindow(t *testing.T) {
	secret, err := newTOTPSecret()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	twoAgo := now.Add(-2 * totpPeriodSec * time.Second)
	if verifyTOTP(secret, totpAt(secret, twoAgo), now) {
		t.Error("a code two steps old must be rejected (skew is ±1)")
	}
}

func TestVerifyTOTPRejectsGarbage(t *testing.T) {
	secret, err := newTOTPSecret()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	for _, bad := range []string{"", "12345", "1234567", "abcdef", "  12 34"} {
		if verifyTOTP(secret, bad, now) {
			t.Errorf("garbage code %q must not verify", bad)
		}
	}
}

func TestVerifyTOTPWrongSecret(t *testing.T) {
	a, _ := newTOTPSecret()
	b, _ := newTOTPSecret()
	now := time.Now()
	if verifyTOTP(b, totpAt(a, now), now) {
		t.Error("a code from a different secret must not verify")
	}
}

func TestProvisioningURI(t *testing.T) {
	secret := []byte("12345678901234567890")
	uri := provisioningURI("alice@example.test", secret)
	if !strings.HasPrefix(uri, "otpauth://totp/") {
		t.Fatalf("bad scheme/authority: %s", uri)
	}
	u, err := url.Parse(uri)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	q := u.Query()
	if q.Get("issuer") != issuerName {
		t.Errorf("issuer = %q, want %q", q.Get("issuer"), issuerName)
	}
	if q.Get("algorithm") != "SHA1" || q.Get("digits") != "6" || q.Get("period") != "30" {
		t.Errorf("unexpected params: %v", q)
	}
	wantSecret := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(secret)
	if q.Get("secret") != wantSecret {
		t.Errorf("secret = %q, want %q", q.Get("secret"), wantSecret)
	}
}
