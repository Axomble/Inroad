package oauthstate

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"
)

var secret = []byte("test-secret-at-least-16-bytes")

func TestSignVerifyRoundTrip(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	tok, nonce := Sign(secret, PurposeMailboxConnect, "ws-123", now, 10*time.Minute)
	if nonce == "" {
		t.Fatal("Sign returned an empty nonce")
	}
	ws, gotNonce, err := Verify(secret, PurposeMailboxConnect, tok, now.Add(time.Minute))
	if err != nil || ws != "ws-123" {
		t.Fatalf("round trip: ws=%q err=%v", ws, err)
	}
	if gotNonce != nonce {
		t.Fatalf("nonce round trip: signed %q, verified %q", nonce, gotNonce)
	}
}

// A login state carries no subject: the workspace a federated session lands in
// is resolved server-side from the provider identity, never from the callback URL.
func TestSignVerifyLoginHasEmptySubject(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	tok, _ := Sign(secret, PurposeLogin, "", now, 10*time.Minute)
	subject, nonce, err := Verify(secret, PurposeLogin, tok, now)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if subject != "" {
		t.Fatalf("want empty subject, got %q", subject)
	}
	if nonce == "" {
		t.Fatal("want a nonce on a login state")
	}
}

// Each Sign mints a fresh nonce, so two states for the same flow are distinct
// values — which is what makes single-use consumption meaningful.
func TestSignMintsAFreshNonceEachTime(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	_, a := Sign(secret, PurposeLogin, "", now, 10*time.Minute)
	_, b := Sign(secret, PurposeLogin, "", now, 10*time.Minute)
	if a == b {
		t.Fatalf("two Sign calls reused nonce %q", a)
	}
}

// The purpose is inside the HMAC-signed payload, so a mailbox-connect state can
// never be replayed at the sign-in callback (or the reverse). This is the whole
// reason Purpose exists: a login-state replay is account access, not a stray
// mailbox binding.
func TestVerifyRejectsWrongPurpose(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)

	mailbox, _ := Sign(secret, PurposeMailboxConnect, "ws-123", now, 10*time.Minute)
	if _, _, err := Verify(secret, PurposeLogin, mailbox, now); !errors.Is(err, ErrInvalid) {
		t.Fatalf("mailbox state accepted as login: %v", err)
	}

	login, _ := Sign(secret, PurposeLogin, "", now, 10*time.Minute)
	if _, _, err := Verify(secret, PurposeMailboxConnect, login, now); !errors.Is(err, ErrInvalid) {
		t.Fatalf("login state accepted as mailbox connect: %v", err)
	}
}

func TestVerifyRejectsExpired(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	tok, _ := Sign(secret, PurposeMailboxConnect, "ws-123", now, 10*time.Minute)
	if _, _, err := Verify(secret, PurposeMailboxConnect, tok, now.Add(11*time.Minute)); err == nil {
		t.Fatal("expected expiry error")
	}
}

func TestVerifyRejectsTamperedSigAndWrongSecret(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	tok, _ := Sign(secret, PurposeMailboxConnect, "ws-123", now, 10*time.Minute)
	if _, _, err := Verify([]byte("different-secret-16b"), PurposeMailboxConnect, tok, now); err == nil {
		t.Fatal("expected bad-signature error under wrong secret")
	}
	if _, _, err := Verify(secret, PurposeMailboxConnect, tok+"x", now); err == nil {
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
	valid, _ := Sign(secret, PurposeMailboxConnect, "ws-123", now, 10*time.Minute)

	// A correctly-signed payload with too few fields: proves Verify checks the
	// shape after the HMAC, rather than indexing into whatever it was handed.
	shortPayload := "a:b:c"
	truncated := base64.RawURLEncoding.EncodeToString([]byte(shortPayload)) + "." +
		base64.RawURLEncoding.EncodeToString(sign(secret, shortPayload))

	tests := []struct {
		name  string
		token string
	}{
		{"no dot separator", "nodot"},
		{"invalid base64 both segments", "!!!.###"},
		{"payload tampered, original sig", tamperPayload(t, valid)},
		{"correctly signed but too few fields", truncated},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, _, err := Verify(secret, PurposeMailboxConnect, tt.token, now); !errors.Is(err, ErrInvalid) {
				t.Fatalf("want ErrInvalid, got %v", err)
			}
		})
	}
}
