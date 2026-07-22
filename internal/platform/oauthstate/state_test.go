package oauthstate

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

var secret = []byte("test-secret-at-least-16-bytes")

func TestSignVerifyRoundTrip(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	tok := Sign(secret, "ws-123", now, 10*time.Minute)
	ws, err := Verify(secret, tok, now.Add(time.Minute))
	if err != nil || ws != "ws-123" {
		t.Fatalf("round trip: ws=%q err=%v", ws, err)
	}
}

func TestVerifyRejectsExpired(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	tok := Sign(secret, "ws-123", now, 10*time.Minute)
	if _, err := Verify(secret, tok, now.Add(11*time.Minute)); err == nil {
		t.Fatal("expected expiry error")
	}
}

func TestVerifyRejectsTamperedSigAndWrongSecret(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	tok := Sign(secret, "ws-123", now, 10*time.Minute)
	if _, err := Verify([]byte("different-secret-16b"), tok, now); err == nil {
		t.Fatal("expected bad-signature error under wrong secret")
	}
	if _, err := Verify(secret, tok+"x", now); err == nil {
		t.Fatal("expected error on tampered token")
	}
}

// tamperPayload decodes the payload segment of a valid token, flips one byte,
// re-encodes it, and re-joins it with the ORIGINAL signature. Verify must
// reject it, proving the HMAC binds the payload (not just its own bytes).
func tamperPayload(t *testing.T, token string) string {
	t.Helper()
	dot := strings.IndexByte(token, '.')
	if dot < 0 {
		t.Fatalf("token has no dot: %q", token)
	}
	payload, err := base64.RawURLEncoding.DecodeString(token[:dot])
	if err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	payload[0] ^= 0xFF
	return base64.RawURLEncoding.EncodeToString(payload) + token[dot:]
}

func TestVerifyRejectsMalformedAndTamperedPayload(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	valid := Sign(secret, "ws-123", now, 10*time.Minute)

	tests := []struct {
		name  string
		token string
	}{
		{"no dot separator", "nodot"},
		{"invalid base64 both segments", "!!!.###"},
		{"payload tampered, original sig", tamperPayload(t, valid)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Verify(secret, tt.token, now); err != ErrInvalid {
				t.Fatalf("want ErrInvalid, got %v", err)
			}
		})
	}
}
