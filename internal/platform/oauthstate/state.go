// Package oauthstate is the stateless HMAC codec for the OAuth `state`
// parameter. It binds a mailbox-OAuth callback to the workspace that started
// the flow, without a server-side session store: the HMAC proves the server
// minted it and the embedded expiry bounds replay. Same construction family as
// internal/platform/unsub. See docs/superpowers/specs/2026-07-23-mailbox-oauth-gmail-design.md §3.1.
package oauthstate

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strconv"
	"strings"
	"time"
)

// ErrInvalid is returned for any malformed, mis-signed, or expired state. It
// is deliberately opaque (no distinction) so a caller gives an attacker no
// oracle.
var ErrInvalid = errors.New("oauthstate: invalid state")

// Sign returns base64url(payload) + "." + base64url(HMAC(secret, payload))
// where payload is "workspaceID:expiryUnix:nonce". ttl is added to now to
// compute the expiry.
func Sign(secret []byte, workspaceID string, now time.Time, ttl time.Duration) string {
	nonce := make([]byte, 8)
	// crypto/rand.Read never returns an error on Go 1.24+, and the nonce is
	// covered by the HMAC, so a read error is intentionally ignored.
	_, _ = rand.Read(nonce)
	payload := workspaceID + ":" + strconv.FormatInt(now.Add(ttl).Unix(), 10) + ":" + b64(nonce)
	return b64([]byte(payload)) + "." + b64(sign(secret, payload))
}

// Verify checks the signature and expiry (against now) and returns the
// workspace id. Any failure yields ErrInvalid.
func Verify(secret []byte, token string, now time.Time) (string, error) {
	dot := strings.IndexByte(token, '.')
	if dot < 0 {
		return "", ErrInvalid
	}
	payload, err := unb64(token[:dot])
	if err != nil {
		return "", ErrInvalid
	}
	gotSig, err := unb64(token[dot+1:])
	if err != nil {
		return "", ErrInvalid
	}
	if !hmac.Equal(gotSig, sign(secret, string(payload))) {
		return "", ErrInvalid
	}
	parts := strings.SplitN(string(payload), ":", 3)
	if len(parts) != 3 {
		return "", ErrInvalid
	}
	exp, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || now.Unix() > exp {
		return "", ErrInvalid
	}
	return parts[0], nil
}

func sign(secret []byte, payload string) []byte {
	h := hmac.New(sha256.New, secret)
	h.Write([]byte(payload))
	return h.Sum(nil)
}

func b64(b []byte) string            { return base64.RawURLEncoding.EncodeToString(b) }
func unb64(s string) ([]byte, error) { return base64.RawURLEncoding.DecodeString(s) }
