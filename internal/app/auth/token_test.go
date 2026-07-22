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

func TestNewOpaqueTokenIsHashStable(t *testing.T) {
	raw, hash, err := NewOpaqueToken()
	if err != nil {
		t.Fatalf("gen: %v", err)
	}
	if raw == "" || len(hash) != 32 {
		t.Fatalf("bad token/hash: %q / %d bytes", raw, len(hash))
	}
	if !bytes.Equal(hash, HashToken(raw)) {
		t.Fatal("hash of raw token is not stable")
	}
}

func TestOpaqueTokensAreUnique(t *testing.T) {
	a, _, _ := NewOpaqueToken()
	b, _, _ := NewOpaqueToken()
	if a == b {
		t.Fatal("tokens should be unique")
	}
}

// TestRefreshTokenHelpersAreOpaqueTokenAliases pins the Phase 1 refresh-token
// names to the generalized helpers: NewRefreshToken/HashRefreshToken must
// remain thin aliases so existing callers keep compiling and behaving
// identically after the generalization.
func TestRefreshTokenHelpersAreOpaqueTokenAliases(t *testing.T) {
	raw, hash, err := NewRefreshToken()
	if err != nil {
		t.Fatalf("gen: %v", err)
	}
	if !bytes.Equal(hash, HashToken(raw)) {
		t.Fatal("NewRefreshToken hash does not match HashToken(raw)")
	}
	if !bytes.Equal(HashRefreshToken(raw), HashToken(raw)) {
		t.Fatal("HashRefreshToken diverges from HashToken")
	}
}
