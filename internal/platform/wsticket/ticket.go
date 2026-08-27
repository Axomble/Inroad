// Package wsticket owns the stateless HMAC connect-ticket codec for the
// realtime WebSocket handshake.
//
// It exists because a browser cannot set an Authorization header on
// `new WebSocket()`, and this app's access token lives in memory rather than a
// cookie (web/src/store/index.ts, deliberately). The alternatives were rejected
// in docs/superpowers/specs/realtime-websocket.md §3: a token in the query
// string is a credential in URL logs, Referer headers and browser history;
// Sec-WebSocket-Protocol smuggling abuses a negotiation header; making the
// access token a cookie reverses a deliberate decision and widens CSRF surface
// across every endpoint to serve one.
//
// So the client mints a short-lived, single-use ticket over an authenticated
// POST and spends it on the Upgrade request. A leaked ticket URL is worth
// nothing 30 seconds later, and nothing at all once spent.
//
// Mirrors internal/platform/track's token design (HMAC-SHA256 + constant-time
// compare + RawURLEncoding) and is deliberately self-contained rather than
// importing that package's unexported helpers, so the two stay decoupled — the
// same choice track made with respect to unsub.
//
// This package is pure: no Redis, no session lookup, no clock beyond the expiry
// it is handed. Single-use enforcement and the session re-check are the
// handshake's job (spec §3), because both need I/O and both must be able to
// refuse a ticket this codec considers perfectly valid.
package wsticket

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strconv"
	"strings"
	"time"
)

// ticketPrefix is part of the signed payload so a connect ticket can never
// validate as a tracking, unsub or warmup token — and neither can they validate
// here. Domain separation is the same reasoning track uses between its own open
// and click tokens, extended across packages because all four codecs fall back
// to the same JWTSecret when a dedicated secret is unset.
const ticketPrefix = "ws:"

// fieldSep separates the payload's fields. Every field is either a UUID or a
// decimal integer, none of which may contain a colon, so a fixed separator
// cannot be smuggled through by a caller.
const fieldSep = ':'

// DefaultTTL is how long a minted ticket stays valid: long enough for a
// handshake on a slow connection, short enough that a ticket captured from a
// proxy log or browser history is worthless by the time it is read.
const DefaultTTL = 30 * time.Second

// NonceBytes is the size of the random nonce a caller must generate. It makes
// each ticket unique so the handshake can burn it exactly once (spec §3); 16
// bytes is far beyond collision range for a 30-second window.
const NonceBytes = 16

// Errors are distinct so the handshake can log *why* a ticket was refused
// without telling the client, which learns only that the upgrade failed. A
// client that could distinguish "expired" from "bad signature" gets a probing
// oracle.
var (
	ErrMalformed = errors.New("wsticket: malformed ticket")
	ErrSignature = errors.New("wsticket: signature mismatch")
	ErrExpired   = errors.New("wsticket: ticket expired")
)

// Ticket is the verified content of a connect ticket.
//
// WorkspaceID is the whole point of the type: the handshake takes the channel
// key from HERE and never from anything the client sends, which is security
// invariant 1 in the spec (§7). A client cannot request a workspace.
type Ticket struct {
	WorkspaceID string
	UserID      string
	// SessionID is re-checked against the session cache at handshake time, so a
	// logout between mint and connect is refused rather than honoured for the
	// remainder of the TTL.
	SessionID string
	ExpiresAt time.Time
	// Nonce is burned by the handshake (SETNX in Redis) to make the ticket
	// single-use. Without that, a ticket in a proxy log is a reusable credential
	// for its whole lifetime.
	Nonce string
}

// Make returns base64url(payload) + "." + base64url(HMAC(secret, payload)),
// where payload is "ws:<workspaceID>:<userID>:<sessionID>:<expiryUnix>:<nonce>".
//
// The caller owns the expiry and the nonce: expiry so tests are not hostage to
// a real clock, and nonce so randomness comes from the caller's crypto/rand
// rather than hidden global state in this package.
func Make(secret []byte, t Ticket) string {
	return encode(secret, payloadOf(t))
}

// Parse verifies the HMAC, then the expiry, and returns the ticket.
//
// Order matters: the signature is checked over the whole payload BEFORE any
// field is split out or interpreted, so nothing in the returned Ticket was ever
// attacker-controlled. Checking expiry first would leak whether a forged
// payload happened to parse.
func Parse(secret []byte, token string, now time.Time) (Ticket, error) {
	payload, err := verify(secret, token)
	if err != nil {
		return Ticket{}, err
	}
	if !strings.HasPrefix(payload, ticketPrefix) {
		return Ticket{}, ErrMalformed
	}

	// SplitN with the exact field count rather than Split: a nonce containing a
	// separator would otherwise silently shift every field left. It cannot, since
	// callers pass base64 of random bytes — but this parser does not get to
	// assume that about a payload an attacker may have chosen the shape of.
	parts := strings.SplitN(payload[len(ticketPrefix):], string(fieldSep), 5)
	if len(parts) != 5 {
		return Ticket{}, ErrMalformed
	}
	workspaceID, userID, sessionID, rawExpiry, nonce := parts[0], parts[1], parts[2], parts[3], parts[4]

	// Every field is load-bearing: an empty workspace would pin the connection to
	// no tenant, an empty session would skip the logout re-check, and an empty
	// nonce would make every ticket identical and therefore infinitely replayable
	// once the first was burned.
	if workspaceID == "" || userID == "" || sessionID == "" || nonce == "" {
		return Ticket{}, ErrMalformed
	}

	unix, err := strconv.ParseInt(rawExpiry, 10, 64)
	if err != nil {
		return Ticket{}, ErrMalformed
	}
	expiresAt := time.Unix(unix, 0)
	// Exclusive: a ticket is dead ON its expiry second, not after it.
	if !now.Before(expiresAt) {
		return Ticket{}, ErrExpired
	}

	return Ticket{
		WorkspaceID: workspaceID,
		UserID:      userID,
		SessionID:   sessionID,
		ExpiresAt:   expiresAt,
		Nonce:       nonce,
	}, nil
}

func payloadOf(t Ticket) string {
	sep := string(fieldSep)
	return ticketPrefix + t.WorkspaceID + sep + t.UserID + sep + t.SessionID + sep +
		strconv.FormatInt(t.ExpiresAt.Unix(), 10) + sep + t.Nonce
}

func encode(secret []byte, payload string) string {
	return b64([]byte(payload)) + "." + b64(sign(secret, payload))
}

func verify(secret []byte, token string) (payload string, err error) {
	rawPayload, rawSig, found := strings.Cut(token, ".")
	if !found {
		return "", ErrMalformed
	}
	p, err := unb64(rawPayload)
	if err != nil {
		return "", ErrMalformed
	}
	gotSig, err := unb64(rawSig)
	if err != nil {
		return "", ErrMalformed
	}
	// hmac.Equal, not bytes.Equal: constant-time so a caller cannot time its way
	// to a valid signature byte by byte.
	if !hmac.Equal(gotSig, sign(secret, string(p))) {
		return "", ErrSignature
	}
	return string(p), nil
}

func sign(secret []byte, payload string) []byte {
	h := hmac.New(sha256.New, secret)
	h.Write([]byte(payload))
	return h.Sum(nil)
}

func b64(b []byte) string            { return base64.RawURLEncoding.EncodeToString(b) }
func unb64(s string) ([]byte, error) { return base64.RawURLEncoding.DecodeString(s) }
