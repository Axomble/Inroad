// Package track owns the stateless HMAC open/click tracking-token codec.
//
// Mirrors internal/platform/unsub's token design (same HMAC-SHA256 +
// constant-time compare + RawURLEncoding shape) but is kept self-contained
// rather than importing unsub's unexported helpers, so the two packages
// stay decoupled at this seam.
package track

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"strings"
)

// Domain prefixes are part of the signed payload so an open token can
// never validate as a click token, or vice-versa.
const (
	openPrefix  = "o:"
	clickPrefix = "c:"
)

// clickSep separates sendID from url in a click token's payload. sendID is
// a fixed-format UUID that never contains NUL, so splitting on the first
// occurrence is safe even if the URL itself contains one.
const clickSep = '\x00'

// MakeOpenToken returns base64url(payload) + "." + base64url(HMAC(secret, payload))
// where payload is "o:" + sendID.
func MakeOpenToken(secret []byte, sendID string) string {
	payload := openPrefix + sendID
	return encode(secret, payload)
}

// ParseOpenToken verifies the HMAC and returns the sendID. Ok is false for
// a malformed token, a bad signature, or a token from a different domain
// (e.g. a click token) — callers should treat all of these the same way.
func ParseOpenToken(secret []byte, token string) (sendID string, ok bool) {
	payload, ok := verify(secret, token)
	if !ok {
		return "", false
	}
	if !strings.HasPrefix(payload, openPrefix) {
		return "", false
	}
	return payload[len(openPrefix):], true
}

// MakeClickToken returns a token signing sendID AND url together, so the
// redirect target cannot be altered by an attacker without invalidating
// the signature (prevents open-redirect via a tampered click token).
func MakeClickToken(secret []byte, sendID, url string) string {
	payload := clickPrefix + sendID + string(clickSep) + url
	return encode(secret, payload)
}

// ParseClickToken verifies the HMAC over the whole payload before ever
// splitting it, so sendID and url are only trusted once the signature
// confirms neither was tampered with.
func ParseClickToken(secret []byte, token string) (sendID, url string, ok bool) {
	payload, ok := verify(secret, token)
	if !ok {
		return "", "", false
	}
	if !strings.HasPrefix(payload, clickPrefix) {
		return "", "", false
	}
	rest := payload[len(clickPrefix):]
	sep := strings.IndexByte(rest, clickSep)
	if sep < 0 {
		return "", "", false
	}
	return rest[:sep], rest[sep+1:], true
}

func encode(secret []byte, payload string) string {
	sig := sign(secret, payload)
	return b64([]byte(payload)) + "." + b64(sig)
}

func verify(secret []byte, token string) (payload string, ok bool) {
	dot := strings.IndexByte(token, '.')
	if dot < 0 {
		return "", false
	}
	p, err := unb64(token[:dot])
	if err != nil {
		return "", false
	}
	gotSig, err := unb64(token[dot+1:])
	if err != nil {
		return "", false
	}
	if !hmac.Equal(gotSig, sign(secret, string(p))) {
		return "", false
	}
	return string(p), true
}

func sign(secret []byte, payload string) []byte {
	h := hmac.New(sha256.New, secret)
	h.Write([]byte(payload))
	return h.Sum(nil)
}

func b64(b []byte) string            { return base64.RawURLEncoding.EncodeToString(b) }
func unb64(s string) ([]byte, error) { return base64.RawURLEncoding.DecodeString(s) }
