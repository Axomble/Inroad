package twofa

import (
	"crypto/rand"
	"strings"

	"github.com/inroad/inroad/internal/app/auth"
)

const (
	// recoveryCodeCount is how many single-use recovery codes are minted at
	// confirmation and shown to the user exactly once.
	recoveryCodeCount = 10
	// recoveryCodeBytes is the entropy per code (80 bits → 16 base32 chars).
	recoveryCodeBytes = 10
)

// newRecoveryCodes returns n fresh recovery codes in the display form
// "xxxx-xxxx-xxxx-xxxx" (lower-case base32, grouped for readability). The dashes
// and case are cosmetic — canonicalizeCode strips them before hashing/verifying.
func newRecoveryCodes(n int) ([]string, error) {
	codes := make([]string, n)
	for i := range codes {
		b := make([]byte, recoveryCodeBytes)
		if _, err := rand.Read(b); err != nil {
			return nil, err
		}
		s := strings.ToLower(base32NoPad.EncodeToString(b)) // 16 chars
		codes[i] = s[0:4] + "-" + s[4:8] + "-" + s[8:12] + "-" + s[12:16]
	}
	return codes, nil
}

// canonicalizeCode reduces a user-entered recovery code to its comparison form:
// lower-case, with dashes and whitespace stripped. Hashing and verifying both run
// on this form, so a user may type the code with or without the display dashes.
func canonicalizeCode(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if r == '-' || r == ' ' || r == '\t' {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// hashRecoveryCode argon2id-hashes a code's canonical form, reusing the password
// hasher's parameters (the raw code is single-use and never re-derivable).
func hashRecoveryCode(code string) (string, error) {
	return auth.HashPassword(canonicalizeCode(code))
}

// recoveryCodeMatches reports whether a presented code matches a stored hash,
// using the password hasher's constant-time argon2 comparison.
func recoveryCodeMatches(hash, presented string) bool {
	return auth.CheckPassword(hash, canonicalizeCode(presented))
}
