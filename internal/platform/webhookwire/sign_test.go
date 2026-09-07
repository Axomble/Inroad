package webhookwire

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
	"time"
)

// TestSignKnownAnswer pins Sign's output to a plain HMAC-SHA256 over the bytes
// "<unix>.<body>" — the exact construction a receiver reimplements from the
// docs. Computed here from the primitives (not from computeMAC) so a change to
// the framing is caught rather than mirrored.
func TestSignKnownAnswer(t *testing.T) {
	secret := []byte("whsec_test_0123456789abcdef")
	body := []byte(`{"id":"d1","event":"ping","occurred_at":"2026-09-07T00:00:00Z","data":{}}`)
	at := time.Unix(1757203200, 0)

	h := hmac.New(sha256.New, secret)
	h.Write([]byte("1757203200."))
	h.Write(body)
	want := "t=1757203200,v1=" + hex.EncodeToString(h.Sum(nil))

	if got := Sign(secret, body, at); got != want {
		t.Fatalf("Sign = %q, want %q", got, want)
	}
}

func TestSignAndVerifyRoundTrip(t *testing.T) {
	secret := []byte("whsec_abc")
	body := []byte(`{"hello":"world"}`)
	hdr := Sign(secret, body, time.Unix(1000, 0))
	if !Verify(secret, body, hdr) {
		t.Fatalf("Verify rejected a signature it should accept: %q", hdr)
	}
}

func TestVerifyDetectsTampering(t *testing.T) {
	secret := []byte("whsec_abc")
	body := []byte(`{"amount":10}`)
	hdr := Sign(secret, body, time.Unix(1000, 0))

	cases := map[string]func() bool{
		"tampered body":      func() bool { return Verify(secret, []byte(`{"amount":1000}`), hdr) },
		"wrong secret":       func() bool { return Verify([]byte("whsec_other"), body, hdr) },
		"flipped mac nibble": func() bool { return Verify(secret, body, flipLastHexNibble(hdr)) },
		"empty header":       func() bool { return Verify(secret, body, "") },
		"missing v1":         func() bool { return Verify(secret, body, "t=1000") },
		"missing t":          func() bool { return Verify(secret, body, "v1="+strings.Repeat("0", 64)) },
	}
	for name, attempt := range cases {
		if attempt() {
			t.Errorf("%s: Verify accepted an invalid signature", name)
		}
	}
}

func flipLastHexNibble(hdr string) string {
	if hdr == "" {
		return hdr
	}
	b := []byte(hdr)
	last := b[len(b)-1]
	v, err := hex.DecodeString(string([]byte{'0', last}))
	if err != nil {
		b[len(b)-1] = '0'
		return string(b)
	}
	b[len(b)-1] = "0123456789abcdef"[(v[0]+1)%16]
	return string(b)
}
