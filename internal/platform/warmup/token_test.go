package warmup

import (
	"strings"
	"testing"
)

var testSecret = []byte("0123456789abcdef0123456789abcdef")

func TestTokenRoundTrip(t *testing.T) {
	want := Payload{
		WorkspaceID:  "ws-1",
		WarmupSendID: "send-9",
		FromMailbox:  "mbx-3",
	}
	tok := Sign(want, testSecret)
	got, ok := Verify(tok, testSecret)
	if !ok {
		t.Fatalf("Verify rejected a freshly signed token")
	}
	if got != want {
		t.Fatalf("round-trip mismatch: got %+v want %+v", got, want)
	}
}

func TestTokenRejectsWrongSecret(t *testing.T) {
	tok := Sign(Payload{WorkspaceID: "ws-1", WarmupSendID: "s", FromMailbox: "m"}, testSecret)
	if _, ok := Verify(tok, []byte("different-secret-000000000000000")); ok {
		t.Fatal("expected rejection under wrong secret")
	}
}

// TestTokenRejectsTamper flips a SIGNIFICANT byte in each segment (not the last
// base64 char, which can be a no-op padding bit) so the check is deterministic:
// mutating the payload changes the decoded JSON the HMAC is recomputed over, and
// mutating the signature breaks the constant-time compare.
func TestTokenRejectsTamper(t *testing.T) {
	tok := Sign(Payload{WorkspaceID: "ws-1", WarmupSendID: "s", FromMailbox: "m"}, testSecret)
	dot := strings.IndexByte(tok, '.')
	if dot <= 0 || dot >= len(tok)-1 {
		t.Fatalf("unexpected token shape: %q", tok)
	}

	payloadTampered := flipMid(tok, 1, dot) // a byte well inside the payload
	if _, ok := Verify(payloadTampered, testSecret); ok {
		t.Fatal("expected rejection when payload byte flipped")
	}

	sigTampered := flipMid(tok, dot+1, len(tok)) // a byte well inside the signature
	if _, ok := Verify(sigTampered, testSecret); ok {
		t.Fatal("expected rejection when signature byte flipped")
	}
}

func TestTokenRejectsMalformed(t *testing.T) {
	for _, tok := range []string{
		"",            // empty
		".",           // empty payload + empty sig
		"no-dot-here", // no separator
		"!!!.???",     // invalid base64 both sides
		"Zm9v",        // valid base64, but no dot
		"Zm9v.!!!",    // valid payload, invalid sig base64
	} {
		if _, ok := Verify(tok, testSecret); ok {
			t.Fatalf("expected malformed token %q to be rejected", tok)
		}
	}
}

// TestTokenRejectsSignedNonJSON pins that a validly-signed but corrupt payload is
// still rejected: the HMAC passes (we sign the raw bytes with the real secret), so
// Verify reaches the JSON decode, which must fail closed. A good signature over
// garbage is not a valid token.
func TestTokenRejectsSignedNonJSON(t *testing.T) {
	raw := []byte("this is validly signed but is not JSON at all")
	tok := b64(raw) + "." + b64(sign(raw, testSecret))
	if _, ok := Verify(tok, testSecret); ok {
		t.Fatal("expected a validly-signed but non-JSON payload to be rejected")
	}
}

// TestTokenVerifiedPayloadNotTrusted documents that a valid signature over a
// mismatched workspace is still returned to the caller — Verify only proves the
// token is authentic; the poller must additionally check the workspace matches.
func TestTokenVerifiedPayloadNotTrusted(t *testing.T) {
	tok := Sign(Payload{WorkspaceID: "other-ws", WarmupSendID: "s", FromMailbox: "m"}, testSecret)
	got, ok := Verify(tok, testSecret)
	if !ok || got.WorkspaceID != "other-ws" {
		t.Fatalf("expected authentic token to decode, got %+v ok=%v", got, ok)
	}
}

// flipMid changes a byte at the midpoint of [lo,hi) to a different valid base64url
// character, so the mutated token stays decodable (exercising the HMAC-mismatch
// path, not the bad-base64 path).
func flipMid(s string, lo, hi int) string {
	i := lo + (hi-lo)/2
	b := []byte(s)
	if b[i] == 'A' {
		b[i] = 'B'
	} else {
		b[i] = 'A'
	}
	return string(b)
}
