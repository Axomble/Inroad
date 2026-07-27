// Package warmup holds the pure, I/O-free building blocks of the mailbox
// warmup engine: the content library (behind an injected generator seam), the
// HMAC receipt-token codec, and the deterministic schedule/health math. Nothing
// in this package touches Postgres, the network, the clock, or math/rand — every
// function is a pure function of its inputs, so the whole package is unit-testable
// without a database or fakes. State and policy live in the warmup domain/worker;
// this is only the reusable primitive layer.
package warmup

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"strings"
)

// HeaderWarmup is the MIME header that carries the signed receipt token on every
// warmup send. The inbox poller reads it to recognise warmup mail; an unsigned or
// mismatched value is ignored (never trusted) — see Verify.
const HeaderWarmup = "X-Inroad-Warmup"

// Payload is the signed body of a warmup receipt token. It is the minimal set of
// identifiers the inbox poller needs to attribute a received warmup message back
// to its send and its workspace. It carries no secret material.
type Payload struct {
	WorkspaceID  string `json:"workspace_id"`
	WarmupSendID string `json:"warmup_send_id"`
	FromMailbox  string `json:"from_mailbox"`
}

// Sign returns base64url(json(payload)) + "." + base64url(HMAC-SHA256(json,
// secret)), mirroring the unsub/track token discipline. The secret is injected by
// the caller — there is no package-global key. A marshal failure (impossible for
// this all-string struct, but handled rather than swallowed) yields an empty
// string, which can never Verify.
func Sign(payload Payload, secret []byte) string {
	raw, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	return b64(raw) + "." + b64(sign(raw, secret))
}

// Verify parses and authenticates a token. It returns ok=false for any malformed
// input (no dot, bad base64, bad JSON) or any HMAC mismatch, using a constant-time
// compare so a tampered signature reveals nothing by timing. Callers must treat
// ok=false as "not warmup" and fall through to normal handling — the header alone
// is never trusted.
func Verify(token string, secret []byte) (Payload, bool) {
	dot := strings.IndexByte(token, '.')
	if dot < 0 {
		return Payload{}, false
	}
	raw, err := unb64(token[:dot])
	if err != nil {
		return Payload{}, false
	}
	gotSig, err := unb64(token[dot+1:])
	if err != nil {
		return Payload{}, false
	}
	if !hmac.Equal(gotSig, sign(raw, secret)) {
		return Payload{}, false
	}
	var payload Payload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return Payload{}, false
	}
	return payload, true
}

func sign(payload, secret []byte) []byte {
	h := hmac.New(sha256.New, secret)
	h.Write(payload)
	return h.Sum(nil)
}

func b64(b []byte) string            { return base64.RawURLEncoding.EncodeToString(b) }
func unb64(s string) ([]byte, error) { return base64.RawURLEncoding.DecodeString(s) }
