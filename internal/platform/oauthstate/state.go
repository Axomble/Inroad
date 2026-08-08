// Package oauthstate is the HMAC codec for the OAuth `state` parameter. It binds
// an OAuth callback to the flow that started it -- for a mailbox connect, to the
// workspace; for a sign-in, to nothing but its own purpose and nonce. The HMAC
// proves the server minted it and the embedded expiry bounds replay. Same
// construction family as internal/platform/unsub. See
// docs/superpowers/specs/2026-07-23-mailbox-oauth-gmail-design.md §3.1.
//
// This package is a pure codec: it holds no state and touches no database. The
// nonce it mints is returned to the caller so a flow that needs SINGLE-USE state
// (sign-in, where a replay is account access rather than a stray mailbox
// binding) can persist and consume it server-side. Callers that don't need that
// simply ignore the nonce, and get replay bounded by the TTL alone.
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

// ErrInvalid is returned for any malformed, mis-signed, expired, or
// wrong-purpose state. It is deliberately opaque (no distinction) so a caller
// gives an attacker no oracle.
var ErrInvalid = errors.New("oauthstate: invalid state")

// Purpose names the flow a state belongs to. It is part of the signed payload
// and is checked on Verify, so a state minted for one flow can never be
// presented to another: a mailbox-connect state replayed at the sign-in callback
// (or the reverse) fails as ErrInvalid rather than authenticating a different
// operation with the same secret.
type Purpose string

const (
	// PurposeMailboxConnect binds a Gmail/M365 mailbox-connect callback to the
	// workspace that started it (the subject is that workspace id).
	PurposeMailboxConnect Purpose = "mailbox_connect"
	// PurposeLogin marks a federated sign-in/sign-up callback. Its subject is
	// empty: at sign-in time there may be no workspace yet, and the workspace a
	// session lands in is always resolved server-side from the provider identity
	// -- never carried in (or derived from) the callback URL.
	PurposeLogin Purpose = "login"
)

// nonceBytes sizes the random nonce. 16 bytes is well past guessing range for a
// value whose whole job is to be unique-and-unpredictable within a 10-minute
// window.
const nonceBytes = 16

// Sign returns base64url(payload) + "." + base64url(HMAC(secret, payload)) where
// payload is "purpose:subject:expiryUnix:nonce", along with the raw nonce it
// minted. ttl is added to now to compute the expiry.
//
// The nonce is returned so a caller can persist it (hashed) and consume it once
// on the callback, making the state single-use. Ignoring it is safe but leaves
// replay bounded only by the TTL.
func Sign(secret []byte, purpose Purpose, subject string, now time.Time, ttl time.Duration) (token, nonce string) {
	raw := make([]byte, nonceBytes)
	// crypto/rand.Read never returns an error on Go 1.24+, and the nonce is
	// covered by the HMAC, so a read error is intentionally ignored.
	_, _ = rand.Read(raw)
	nonce = b64(raw)
	payload := string(purpose) + ":" + subject + ":" + strconv.FormatInt(now.Add(ttl).Unix(), 10) + ":" + nonce
	return b64([]byte(payload)) + "." + b64(sign(secret, payload)), nonce
}

// Verify checks the signature, the expiry (against now), and that the state was
// minted for want, returning the subject and the nonce. Any failure yields
// ErrInvalid.
//
// A caller that needs single-use semantics must still consume the returned nonce
// against its own server-side store; this function is stateless and cannot tell
// a first use from a second.
func Verify(secret []byte, want Purpose, token string, now time.Time) (subject, nonce string, err error) {
	dot := strings.IndexByte(token, '.')
	if dot < 0 {
		return "", "", ErrInvalid
	}
	payload, err := unb64(token[:dot])
	if err != nil {
		return "", "", ErrInvalid
	}
	gotSig, err := unb64(token[dot+1:])
	if err != nil {
		return "", "", ErrInvalid
	}
	if !hmac.Equal(gotSig, sign(secret, string(payload))) {
		return "", "", ErrInvalid
	}
	parts := strings.SplitN(string(payload), ":", 4)
	if len(parts) != 4 {
		return "", "", ErrInvalid
	}
	if Purpose(parts[0]) != want {
		return "", "", ErrInvalid
	}
	exp, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil || now.Unix() > exp {
		return "", "", ErrInvalid
	}
	return parts[1], parts[3], nil
}

func sign(secret []byte, payload string) []byte {
	h := hmac.New(sha256.New, secret)
	h.Write([]byte(payload))
	return h.Sum(nil)
}

func b64(b []byte) string            { return base64.RawURLEncoding.EncodeToString(b) }
func unb64(s string) ([]byte, error) { return base64.RawURLEncoding.DecodeString(s) }
