package inbox

import "testing"

// likeQuery is the seam ListFilter.Query crosses on its way into the sqlc
// narg: "" (no search requested) must stay nil so the query's IS NULL guard
// skips the filter, not match an empty pattern against every row. The
// escaping itself is db.EscapeLike's own responsibility (tested in
// internal/platform/db); this only pins likeQuery's nil-vs-escaped-pointer
// wiring around it.
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
