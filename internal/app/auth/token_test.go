package auth

import (
	"bytes"
	"testing"
)

func TestNewRefreshTokenIsHashStable(t *testing.T) {
	raw, hash, err := NewRefreshToken()
	if err != nil {
		t.Fatalf("gen: %v", err)
	}
	if raw == "" || len(hash) != 32 {
		t.Fatalf("bad token/hash: %q / %d bytes", raw, len(hash))
	}
	if !bytes.Equal(hash, HashRefreshToken(raw)) {
		t.Fatal("hash of raw token is not stable")
	}
}

func TestRefreshTokensAreUnique(t *testing.T) {
	a, _, _ := NewRefreshToken()
	b, _, _ := NewRefreshToken()
	if a == b {
		t.Fatal("tokens should be unique")
	}
}
