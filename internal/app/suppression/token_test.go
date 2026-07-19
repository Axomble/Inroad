package suppression

import "testing"

func TestTokenRoundTrip(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	tok := MakeToken(secret, "ws-1", "Alice@Example.com")
	ws, email, ok := ParseToken(secret, tok)
	if !ok || ws != "ws-1" || email != "Alice@Example.com" {
		t.Fatalf("round-trip failed: %q %q %v", ws, email, ok)
	}
}

func TestTokenRejectsTamper(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	tok := MakeToken(secret, "ws-1", "a@b.com")
	if _, _, ok := ParseToken([]byte("different-secret-000000000000000"), tok); ok {
		t.Fatal("expected rejection under wrong secret")
	}
	if _, _, ok := ParseToken(secret, tok+"x"); ok {
		t.Fatal("expected rejection under tampered token")
	}
}
