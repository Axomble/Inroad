package db

import "testing"

// A user typing a LIKE metacharacter means the literal character, not "match
// anything" — left unescaped, "%" would become "match anything" and "_"
// would become "match any one character", either of which turns a keystroke
// into a wildcard the caller never asked for.
func TestEscapeLikeNeutralisesMetacharacters(t *testing.T) {
	got := EscapeLike(`100%_off\x`)
	if want := `100\%\_off\\x`; got != want {
		t.Fatalf("EscapeLike(...) = %q, want %q", got, want)
	}
}

func TestEscapeLikeLeavesAnOrdinaryStringUnchanged(t *testing.T) {
	if got := EscapeLike("acme corp"); got != "acme corp" {
		t.Fatalf("EscapeLike(...) = %q, want it unchanged", got)
	}
}
