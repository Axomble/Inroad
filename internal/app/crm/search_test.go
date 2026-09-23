package crm

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestNormalizeCompanyFilter(t *testing.T) {
	cases := []struct {
		name, in, want string
		invalid        bool
	}{
		{name: "empty lists everything", in: "", want: ""},
		{name: "whitespace only is no filter", in: "   ", want: ""},
		{name: "trimmed and lower-cased to match the index", in: "  AcMe ", want: "acme"},
		{name: "two runes is the floor", in: "ac", want: "ac"},
		{name: "counts runes, not bytes", in: "Ép", want: "ép"},
		{name: "one character is refused", in: "a", invalid: true},
		{name: "one multibyte character is refused", in: "É", invalid: true},
		{name: "longer than any name or domain is refused", in: strings.Repeat("a", maxCompanyQueryLen+1), invalid: true},
		{name: "the ceiling itself is allowed", in: strings.Repeat("a", maxCompanyQueryLen), want: strings.Repeat("a", maxCompanyQueryLen)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := normalizeCompanyFilter(CompanyFilter{Query: tc.in})
			if tc.invalid {
				if !errors.Is(err, ErrValidation) {
					t.Fatalf("err = %v, want ErrValidation", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.Query != tc.want {
				t.Fatalf("query = %q, want %q", got.Query, tc.want)
			}
		})
	}
}

// A search cursor names a position in ONE result set. Replaying it under a
// different query, or against the unfiltered listing, must be refused rather
// than silently resuming inside the wrong list.
func TestCompanySearchCursorIsBoundToItsQuery(t *testing.T) {
	id := uuid.New()
	raw := encodeCursor(cursorCompanySearch, id.String(), "acme", "ac")

	gotID, gotName, err := decodeCompanyCursor(cursorCompanySearch, raw, "ac")
	if err != nil || gotID != id || gotName != "acme" {
		t.Fatalf("round trip = %v %q %v", gotID, gotName, err)
	}
	if _, _, err := decodeCompanyCursor(cursorCompanySearch, raw, "acm"); !errors.Is(err, ErrValidation) {
		t.Fatalf("changed query: err = %v, want ErrValidation", err)
	}
	if _, _, err := decodeCompanyCursor(cursorCompanies, raw); !errors.Is(err, ErrValidation) {
		t.Fatalf("search cursor on the plain listing: err = %v, want ErrValidation", err)
	}
	plain := encodeCursor(cursorCompanies, id.String(), "acme")
	if _, _, err := decodeCompanyCursor(cursorCompanySearch, plain, "ac"); !errors.Is(err, ErrValidation) {
		t.Fatalf("plain cursor on a search: err = %v, want ErrValidation", err)
	}
	bad := encodeCursor(cursorCompanySearch, "not-a-uuid", "acme", "ac")
	if _, _, err := decodeCompanyCursor(cursorCompanySearch, bad, "ac"); !errors.Is(err, ErrValidation) {
		t.Fatalf("malformed id: err = %v, want ErrValidation", err)
	}
}

func TestListCompaniesPassesTheNormalisedQueryToTheStore(t *testing.T) {
	store := newFakeStore()
	router, authz := sessionRouter(t, NewHandler(NewService(store)))

	w := do(t, router, http.MethodGet, "/crm/companies?q=%20AcMe%20", authz, "")
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
	if store.companyFilter.Query != "acme" {
		t.Fatalf("store saw query %q, want the trimmed lower-cased %q", store.companyFilter.Query, "acme")
	}

	w = do(t, router, http.MethodGet, "/crm/companies", authz, "")
	if w.Code != http.StatusOK || store.companyFilter.Query != "" {
		t.Fatalf("no q: code=%d filter=%+v", w.Code, store.companyFilter)
	}
}

func TestListCompaniesRejectsATooShortQueryBeforeTheStore(t *testing.T) {
	store := newFakeStore()
	store.companyFilter = CompanyFilter{Query: "untouched"}
	router, authz := sessionRouter(t, NewHandler(NewService(store)))

	w := do(t, router, http.MethodGet, "/crm/companies?q=a", authz, "")
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("want 422, got %d: %s", w.Code, w.Body.String())
	}
	if store.companyFilter.Query != "untouched" {
		t.Fatalf("a rejected query reached the store: %+v", store.companyFilter)
	}
}
