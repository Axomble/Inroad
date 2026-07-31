package apikey

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"strings"
)

// Token format: inrd_<prefix>_<secret>
//
//   - The literal "inrd_" scheme makes a leaked key recognizable and greppable in
//     logs/secret scanners.
//   - <prefix> is a PUBLIC, non-secret 8-char id used for an O(1) DB lookup.
//   - <secret> is a 256-bit random value; only its SHA-256 is ever stored, and it
//     is shown to the operator exactly once at creation.
const (
	tokenScheme = "inrd_"
	// publicIDBytes*8/5 = 8 base32 chars (5 bytes -> 8 chars, no padding).
	publicIDBytes = 5
	// secretBytes is the raw secret entropy: 256 bits, per the spec.
	secretBytes = 32
)

// base32NoPad is lower-cased RFC 4648 base32 without padding — url-safe, no
// ambiguous separators, and matches the encoding used elsewhere (twofa).
var base32NoPad = base32.StdEncoding.WithPadding(base32.NoPadding)

// newToken mints a fresh key: the public prefix, the full one-time token, and the
// SHA-256 of the secret (what gets persisted). The raw secret never leaves this
// function except embedded in the returned token.
func newToken() (prefix, token string, secretHash []byte, err error) {
	idb := make([]byte, publicIDBytes)
	if _, err = rand.Read(idb); err != nil {
		return "", "", nil, err
	}
	sb := make([]byte, secretBytes)
	if _, err = rand.Read(sb); err != nil {
		return "", "", nil, err
	}
	prefix = strings.ToLower(base32NoPad.EncodeToString(idb))
	secret := strings.ToLower(base32NoPad.EncodeToString(sb))
	token = tokenScheme + prefix + "_" + secret
	sum := sha256.Sum256([]byte(secret))
	return prefix, token, sum[:], nil
}

// parseToken splits a presented token into its public prefix and the SHA-256 of
// its secret part. ok=false means the string is not shaped like an api-key token
// (wrong scheme or missing secret) — the verifier treats that as DEFER, not a
// rejection, so another credential scheme can still claim the request.
func parseToken(s string) (prefix string, secretHash []byte, ok bool) {
	rest, found := strings.CutPrefix(s, tokenScheme)
	if !found {
		return "", nil, false
	}
	prefix, secret, found := strings.Cut(rest, "_")
	if !found || prefix == "" || secret == "" {
		return "", nil, false
	}
	sum := sha256.Sum256([]byte(secret))
	return prefix, sum[:], true
}

// hasScheme reports whether s carries the api-key token scheme. The verifier uses
// it to decide ENGAGE vs DEFER before touching the store: only a clearly-shaped
// api-key token engages this verifier.
func hasScheme(s string) bool { return strings.HasPrefix(s, tokenScheme) }
