package webhookwire

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// SignatureHeader carries a timestamped HMAC over the delivery body. Its value
// is "t=<unix seconds>,v1=<hex hmac-sha256>", where the MAC is taken over the
// bytes "<unix>.<rawBody>". Binding the timestamp into the MAC is what lets a
// receiver reject a stale replay (it checks t is recent) without the signature
// itself changing shape.
const SignatureHeader = "Inroad-Signature"

// Sign returns the SignatureHeader value for body, signed with secret at time t.
//
// The signed message is exactly "<t.Unix()>.<body>" with a literal '.'
// separator. computeMAC writes the three pieces without allocating a joined
// buffer, and Verify below recomputes them the same way — the known-answer
// vectors in sign_test.go pin the byte sequence both ends must agree on.
func Sign(secret, body []byte, t time.Time) string {
	ts := strconv.FormatInt(t.Unix(), 10)
	return fmt.Sprintf("t=%s,v1=%s", ts, computeMAC(secret, ts, body))
}

func computeMAC(secret []byte, ts string, body []byte) string {
	h := hmac.New(sha256.New, secret)
	// hmac.Hash.Write never returns an error (documented), so the writes are
	// unchecked deliberately rather than sloppily.
	_, _ = h.Write([]byte(ts))
	_, _ = h.Write([]byte{'.'})
	_, _ = h.Write(body)
	return hex.EncodeToString(h.Sum(nil))
}

// Verify reports whether header is a well-formed SignatureHeader whose v1 MAC
// matches body under secret. The sender never calls this — it signs — but a
// receiver must, and keeping the verifier next to the signer is what keeps the
// known-answer test honest about both directions. The timestamp's freshness is
// the receiver's policy, not checked here.
func Verify(secret, body []byte, header string) bool {
	var ts, sig string
	for _, part := range strings.Split(header, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		switch k {
		case "t":
			ts = v
		case "v1":
			sig = v
		}
	}
	if ts == "" || sig == "" {
		return false
	}
	want := computeMAC(secret, ts, body)
	// Constant-time compare so a byte-by-byte timing side channel cannot help an
	// attacker forge the MAC.
	return hmac.Equal([]byte(want), []byte(sig))
}
