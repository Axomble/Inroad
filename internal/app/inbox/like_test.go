package inbox

import "testing"

// A user typing a LIKE metacharacter means the literal character, not "match
// anything" — the same behavior contact.escapeLike's own test pins for its
// identical (duplicated, not imported — see store.go's likeEscaper comment)
// helper.
func TestEscapeLikeNeutralisesMetacharacters(t *testing.T) {
	got := escapeLike(`100%_off\x`)
	if want := `100\%\_off\\x`; got != want {
		t.Fatalf("escapeLike(...) = %q, want %q", got, want)
	}
}

// likeQuery is the seam ListFilter.Query crosses on its way into the sqlc
// narg: "" (no search requested) must stay nil so the query's IS NULL guard
// skips the filter, not match an empty pattern against every row.
func TestLikeQueryIsNilForAnEmptyString(t *testing.T) {
	if got := likeQuery(""); got != nil {
		t.Fatalf("likeQuery(\"\") = %v, want nil", got)
	}
}

func TestLikeQueryEscapesAndWrapsANonEmptyString(t *testing.T) {
	got := likeQuery("100%off")
	if got == nil {
		t.Fatal("likeQuery(...) = nil, want a non-nil pointer")
	}
	if want := `100\%off`; *got != want {
		t.Fatalf("likeQuery(...) = %q, want %q", *got, want)
	}
}
