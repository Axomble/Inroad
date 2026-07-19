// Package suppression handles the do-not-contact list and stateless unsubscribe.
package suppression

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
	return b64(payload) + "." + b64(sig)
}

// ParseToken verifies the HMAC and returns the workspace id and email.
func ParseToken(secret []byte, token string) (string, string, bool) {
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
func b64(b any) string {
	switch v := b.(type) {
	case string:
		return base64.RawURLEncoding.EncodeToString([]byte(v))
	case []byte:
		return base64.RawURLEncoding.EncodeToString(v)
	}
	return ""
}
func unb64(s string) ([]byte, error) { return base64.RawURLEncoding.DecodeString(s) }
