package emailotp

import (
	"crypto/rand"
	"fmt"
	"math/big"

	"github.com/inroad/inroad/internal/app/auth"
)

// codeDigits is the length of the emailed numeric login code.
const codeDigits = 6

// codeSpace is 10^codeDigits — the exclusive upper bound for a uniform draw.
var codeSpace = big.NewInt(1_000_000)

// newNumericCode returns a fresh, uniformly-distributed codeDigits-long numeric
// code (leading zeros preserved, e.g. "008421"). It draws from crypto/rand via
// rand.Int, which rejection-samples internally, so there is NO modulo bias — every
// value in [0, 10^codeDigits) is equally likely. The raw code is returned once for
// hashing + emailing and is never persisted or logged.
func newNumericCode() (string, error) {
	n, err := rand.Int(rand.Reader, codeSpace)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%0*d", codeDigits, n), nil
}

// hashCode argon2id-hashes a code, reusing the password hasher's parameters (the
// code is single-use and never re-derivable from the hash).
func hashCode(code string) (string, error) {
	return auth.HashPassword(code)
}

// codeMatches reports whether presented matches a stored hash, using the password
// hasher's constant-time argon2 comparison.
func codeMatches(hash, presented string) bool {
	return auth.CheckPassword(hash, presented)
}
