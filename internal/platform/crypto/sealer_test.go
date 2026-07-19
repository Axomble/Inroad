package crypto

import (
	"bytes"
	"testing"
)

func key32() []byte { return bytes.Repeat([]byte{0x11}, 32) }

func TestSealOpenRoundTrip(t *testing.T) {
	s, err := NewSealer(key32())
	if err != nil {
		t.Fatalf("NewSealer: %v", err)
	}
	secret := []byte("smtp-app-password")
	token, err := s.Seal(secret)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if bytes.Contains([]byte(token), secret) {
		t.Fatal("ciphertext leaked plaintext")
	}
	got, err := s.Open(token)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if !bytes.Equal(got, secret) {
		t.Fatalf("round-trip mismatch: got %q", got)
	}
}

func TestNewSealerRejectsBadKey(t *testing.T) {
	if _, err := NewSealer([]byte("short")); err == nil {
		t.Fatal("expected error for short key")
	}
}
