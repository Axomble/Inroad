package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
)

// NewOpaqueToken returns a new random opaque token and its SHA-256 hash.
// Only the hash is persisted; the raw value lives solely with the caller
// (a client cookie for refresh tokens, an emailed link for verify/reset
// tokens, etc).
func NewOpaqueToken() (raw string, hash []byte, err error) {
	b := make([]byte, 32)
	if _, err = rand.Read(b); err != nil {
		return "", nil, err
	}
	raw = base64.RawURLEncoding.EncodeToString(b)
	return raw, HashToken(raw), nil
}

// HashToken hashes a raw opaque token for storage/lookup.
func HashToken(raw string) []byte {
	sum := sha256.Sum256([]byte(raw))
	return sum[:]
}

// NewRefreshToken returns a new opaque refresh token and its SHA-256 hash.
// Only the hash is persisted; the raw value lives solely in the client cookie.
//
// Kept as a thin alias of NewOpaqueToken so existing callers keep compiling
// and behaving identically after the helpers were generalized to cover
// other single-use token kinds (email verify, password reset).
func NewRefreshToken() (raw string, hash []byte, err error) {
	return NewOpaqueToken()
}

// HashRefreshToken hashes a raw refresh token for storage/lookup.
//
// Alias of HashToken; kept for Phase 1 callers.
func HashRefreshToken(raw string) []byte {
	return HashToken(raw)
}
