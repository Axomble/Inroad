package emailotp

import (
	"regexp"
	"testing"
)

var sixDigits = regexp.MustCompile(`^\d{6}$`)

// TestNewNumericCodeShape asserts every generated code is exactly six decimal
// digits (leading zeros preserved) — never shorter because a small draw dropped
// its zeros.
func TestNewNumericCodeShape(t *testing.T) {
	for i := 0; i < 2000; i++ {
		code, err := newNumericCode()
		if err != nil {
			t.Fatalf("newNumericCode: %v", err)
		}
		if !sixDigits.MatchString(code) {
			t.Fatalf("code %q is not six digits", code)
		}
	}
}

// TestNewNumericCodeSpread is a coarse uniformity smoke test: over many draws the
// leading digit should span the whole 0–9 range (a modulo-biased or truncated
// generator would starve some buckets). It asserts coverage, not a strict
// distribution.
func TestNewNumericCodeSpread(t *testing.T) {
	var seenFirst [10]bool
	var seenLast [10]bool
	for i := 0; i < 5000; i++ {
		code, err := newNumericCode()
		if err != nil {
			t.Fatalf("newNumericCode: %v", err)
		}
		seenFirst[code[0]-'0'] = true
		seenLast[code[5]-'0'] = true
	}
	for d := 0; d < 10; d++ {
		if !seenFirst[d] {
			t.Errorf("leading digit %d never appeared in 5000 draws", d)
		}
		if !seenLast[d] {
			t.Errorf("trailing digit %d never appeared in 5000 draws", d)
		}
	}
}

// TestHashCodeRoundTrip proves the argon2 hash verifies the matching code with a
// constant-time compare and rejects a different one.
func TestHashCodeRoundTrip(t *testing.T) {
	hash, err := hashCode("012345")
	if err != nil {
		t.Fatalf("hashCode: %v", err)
	}
	if !codeMatches(hash, "012345") {
		t.Fatal("codeMatches rejected the correct code")
	}
	if codeMatches(hash, "543210") {
		t.Fatal("codeMatches accepted a wrong code")
	}
	if codeMatches(hash, "") {
		t.Fatal("codeMatches accepted an empty code")
	}
}
