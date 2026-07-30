// Package twofa implements TOTP two-factor authentication with single-use
// recovery codes and a fail-closed login gate. TOTP secrets are USER-level
// secrets sealed under crypto.ServerKeyring (not a per-workspace DEK); recovery
// codes are argon2-hashed and single-use; the login gate issues a short-lived,
// try-capped challenge instead of a session for any user with a confirmed
// second factor.
package twofa

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	// RFC 6238 parameters. SHA-1, 6 digits, 30-second step is the near-universal
	// authenticator-app default.
	totpDigits    = 6
	totpPeriodSec = 30
	// totpSecretLen is 160 bits, the RFC 4226 recommended shared-secret length.
	totpSecretLen = 20
	// totpSkew accepts the adjacent steps on either side of the current one, so a
	// code entered near a period boundary (or with modest clock drift) still
	// verifies. ±1 step = a ±30s window.
	totpSkew = 1

	// issuerName labels the provisioning URI shown to the authenticator app.
	issuerName = "Inroad"
)

// base32NoPad is the RFC 4648 base32 alphabet without padding — the encoding
// authenticator apps expect for the otpauth `secret` parameter.
var base32NoPad = base32.StdEncoding.WithPadding(base32.NoPadding)

// newTOTPSecret returns a fresh random 160-bit TOTP secret. crypto/rand only.
func newTOTPSecret() ([]byte, error) {
	b := make([]byte, totpSecretLen)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	return b, nil
}

// encodeBase32Secret renders a raw secret as the unpadded base32 string a user
// types into (or scans onto) their authenticator app.
func encodeBase32Secret(secret []byte) string {
	return base32NoPad.EncodeToString(secret)
}

// hotp computes the RFC 4226 HOTP value of secret for the given counter, as a
// zero-padded decimal string of totpDigits digits.
func hotp(secret []byte, counter uint64) string {
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], counter)

	mac := hmac.New(sha1.New, secret)
	mac.Write(msg[:])
	sum := mac.Sum(nil)

	// Dynamic truncation (RFC 4226 §5.3).
	offset := sum[len(sum)-1] & 0x0f
	bin := (uint32(sum[offset]&0x7f) << 24) |
		(uint32(sum[offset+1]) << 16) |
		(uint32(sum[offset+2]) << 8) |
		uint32(sum[offset+3])

	mod := uint32(1)
	for i := 0; i < totpDigits; i++ {
		mod *= 10
	}
	return fmt.Sprintf("%0*d", totpDigits, bin%mod)
}

// counterAt maps a wall-clock time to the RFC 6238 time-step counter.
func counterAt(t time.Time) uint64 {
	return uint64(t.Unix() / totpPeriodSec)
}

// totpAt returns the TOTP code for secret at time t. Used by tests and to
// generate a code for a freshly-minted secret.
func totpAt(secret []byte, t time.Time) string {
	return hotp(secret, counterAt(t))
}

// verifyTOTP reports whether code is a valid TOTP for secret at time t, accepting
// the ±totpSkew step window. Every candidate is compared in constant time and the
// loop never short-circuits, so neither the match position nor timing leaks which
// step (if any) matched.
func verifyTOTP(secret []byte, code string, t time.Time) bool {
	code = strings.TrimSpace(code)
	// A non-numeric or wrong-length code can never match; reject cheaply. The
	// code value is not a secret, so this early return leaks nothing sensitive.
	if len(code) != totpDigits {
		return false
	}
	if _, err := strconv.Atoi(code); err != nil {
		return false
	}

	center := int64(counterAt(t))
	var matched int
	for d := -totpSkew; d <= totpSkew; d++ {
		c := center + int64(d)
		if c < 0 {
			continue
		}
		candidate := hotp(secret, uint64(c))
		matched |= subtle.ConstantTimeCompare([]byte(candidate), []byte(code))
	}
	return matched == 1
}

// provisioningURI builds the otpauth:// URI an authenticator app imports (via QR
// or manual entry). issuer/account label the entry; the secret is base32-encoded.
func provisioningURI(account string, secret []byte) string {
	label := url.PathEscape(issuerName + ":" + account)
	v := url.Values{}
	v.Set("secret", encodeBase32Secret(secret))
	v.Set("issuer", issuerName)
	v.Set("algorithm", "SHA1")
	v.Set("digits", strconv.Itoa(totpDigits))
	v.Set("period", strconv.Itoa(totpPeriodSec))
	return "otpauth://totp/" + label + "?" + v.Encode()
}

// zero wipes a sensitive byte slice (an opened TOTP secret) after use.
func zero(b []byte) {
	for i := range b {
		b[i] = 0
	}
}
