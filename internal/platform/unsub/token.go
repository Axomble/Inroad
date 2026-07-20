// Package unsub owns the stateless HMAC unsubscribe-token codec.
//
// Placement rationale: suppression is a business domain, but the token
// helpers are cross-cutting utility. Placing them here lets both the
// suppression HTTP handler AND the send job's URL builder (coreapi
// in-process) depend on the same primitive without either owning it — a
// domain package depending on another (`inprocess` -> `suppression`) would
// break the "app packages don't import each other" invariant. See
// docs/architecture.md.
package unsub

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"strings"
)

// MakeToken returns base64url(payload) + "." + base64url(HMAC(secret, payload)),
// where payload is "workspaceID:email". Stateless — no tokens table.
func MakeToken(secret []byte, workspaceID, email string) string {
	payload := workspaceID + ":" + email
	sig := sign(secret, payload)
	return b64([]byte(payload)) + "." + b64(sig)
}

// ParseToken verifies the HMAC and returns the workspace id and email.
// Ok is false for a malformed token, a bad signature, or missing fields —
// callers should treat all three as "invalid unsubscribe link" without
// distinguishing them (no oracle for the attacker).
func ParseToken(secret []byte, token string) (workspaceID, email string, ok bool) {
	dot := strings.IndexByte(token, '.')
	if dot < 0 {
		return "", "", false
	}
	payload, err := unb64(token[:dot])
	if err != nil {
		return "", "", false
	}
	gotSig, err := unb64(token[dot+1:])
	if err != nil {
		return "", "", false
	}
	if !hmac.Equal(gotSig, sign(secret, string(payload))) {
		return "", "", false
	}
	colon := strings.IndexByte(string(payload), ':')
	if colon < 0 {
		return "", "", false
	}
	return string(payload[:colon]), string(payload[colon+1:]), true
}

func sign(secret []byte, payload string) []byte {
	h := hmac.New(sha256.New, secret)
	h.Write([]byte(payload))
	return h.Sum(nil)
}

func b64(b []byte) string          { return base64.RawURLEncoding.EncodeToString(b) }
func unb64(s string) ([]byte, error) { return base64.RawURLEncoding.DecodeString(s) }
