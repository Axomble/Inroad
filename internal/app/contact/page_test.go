package contact

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/platform/cursor"
)

// fakeChecker answers list-ownership questions without a database. exists=false
// stands in for both "no such list" and "another tenant's list" — the store
// query cannot tell them apart either, which is the point.
type fakeChecker struct {
	exists bool
	err    error
	asked  []uuid.UUID
}

func (f *fakeChecker) ListExists(_ context.Context, _, listID uuid.UUID) (bool, error) {
	f.asked = append(f.asked, listID)
	return f.exists, f.err
}

func rows(n int) []SearchRow {
	base := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	out := make([]SearchRow, n)
	for i := range out {
		out[i] = SearchRow{
			ID:        uuid.New(),
			Email:     "c@x.test",
			CreatedAt: base.Add(-time.Duration(i) * time.Minute),
			SortEmail: "c@x.test",
		}
	}
	return out
}

func newSvc(store *fakeStore) *Service {
	return NewService(store, &fakeChecker{exists: true})
}

func TestSearchRejectsShortQuery(t *testing.T) {
	svc := newSvc(&fakeStore{})
	for _, q := range []string{"a", " a ", "\tx\t"} {
		if _, err := svc.Search(context.Background(), testWS, SearchRequest{Q: q}); !errors.Is(err, ErrQueryTooShort) {
			t.Fatalf("q=%q err = %v, want ErrQueryTooShort", q, err)
		}
	}
}

// An all-whitespace query is not a query at all; it must fall through to the
// unfiltered list rather than being rejected as too short.
func TestSearchTreatsBlankQueryAsNoQuery(t *testing.T) {
	store := &fakeStore{}
	if _, err := newSvc(store).Search(context.Background(), testWS, SearchRequest{Q: "   "}); err != nil {
		t.Fatalf("Search: %v", err)
	}
	if store.lastSearch.Filter.Query != "" {
		t.Fatalf("query = %q, want empty", store.lastSearch.Filter.Query)
	}
}

func TestSearchLowercasesAndTrimsQuery(t *testing.T) {
	store := &fakeStore{}
	if _, err := newSvc(store).Search(context.Background(), testWS, SearchRequest{Q: "  ACME.com  "}); err != nil {
		t.Fatalf("Search: %v", err)
	}
	if got := store.lastSearch.Filter.Query; got != "acme.com" {
		t.Fatalf("query = %q, want %q", got, "acme.com")
	}
}

func TestSearchRejectsOutOfRangeLimit(t *testing.T) {
	svc := newSvc(&fakeStore{})
	for _, n := range []int{0, -1, MaxLimit + 1} {
		if _, err := svc.Search(context.Background(), testWS, SearchRequest{Limit: &n}); !errors.Is(err, ErrInvalidLimit) {
			t.Fatalf("limit=%d err = %v, want ErrInvalidLimit", n, err)
		}
	}
}

func TestSearchDefaultsLimitAndAsksForOneExtraRow(t *testing.T) {
	store := &fakeStore{}
	if _, err := newSvc(store).Search(context.Background(), testWS, SearchRequest{}); err != nil {
		t.Fatalf("Search: %v", err)
	}
	if store.lastSearch.Limit != DefaultLimit+1 {
		t.Fatalf("store limit = %d, want %d (page size plus the lookahead row)", store.lastSearch.Limit, DefaultLimit+1)
	}
}

func TestSearchRejectsUnknownSort(t *testing.T) {
	_, err := newSvc(&fakeStore{}).Search(context.Background(), testWS, SearchRequest{Sort: "sideways"})
	if !errors.Is(err, cursor.ErrUnknownSort) {
		t.Fatalf("err = %v, want ErrUnknownSort", err)
	}
	if !IsValidationError(err) {
		t.Fatal("an unknown sort must classify as a validation error (422)")
	}
}

func TestSearchRejectsBadCursor(t *testing.T) {
	svc := newSvc(&fakeStore{})
	wrongSort := cursor.NewEmail(cursor.After, "a@x.test", uuid.New()).Encode()

	tests := []struct {
		name string
		req  SearchRequest
	}{
		{"garbage", SearchRequest{Cursor: "!!!"}},
		{"cursor from another sort", SearchRequest{Cursor: wrongSort}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.Search(context.Background(), testWS, tc.req)
			if !IsCursorError(err) {
				t.Fatalf("err = %v, want a cursor error (400)", err)
			}
			// The critical property: a bad cursor must NOT silently become
			// "page 1" — the operator would never learn their place was lost.
			if err == nil {
				t.Fatal("bad cursor was silently reset to the first page")
			}
		})
	}
}

func TestSearchUnknownListIsNotFound(t *testing.T) {
	svc := NewService(&fakeStore{}, &fakeChecker{exists: false})
	_, err := svc.Search(context.Background(), testWS, SearchRequest{ListID: &testList})
	if !errors.Is(err, ErrListNotFound) {
		t.Fatalf("err = %v, want ErrListNotFound", err)
	}
}

// The list is checked before any read, so a cross-tenant list id can never
// reach the store and produce a (correctly empty, but misleading) page.
func TestSearchChecksListBeforeReading(t *testing.T) {
	store := &fakeStore{}
	svc := NewService(store, &fakeChecker{exists: false})
	if _, err := svc.Search(context.Background(), testWS, SearchRequest{ListID: &testList}); err == nil {
		t.Fatal("expected ErrListNotFound")
	}
	if store.lastSearch.Limit != 0 {
		t.Fatal("store was queried despite the list check failing")
	}
}

func TestSearchNoListMeansWholeWorkspace(t *testing.T) {
	store := &fakeStore{}
	checker := &fakeChecker{exists: true}
	if _, err := NewService(store, checker).Search(context.Background(), testWS, SearchRequest{}); err != nil {
		t.Fatalf("Search: %v", err)
	}
	if store.lastSearch.Filter.ListID != nil {
		t.Fatal("filter carried a list id when none was requested")
	}
	if len(checker.asked) != 0 {
		t.Fatal("list ownership was checked when no list was requested")
	}
}

func TestSearchFirstPageHasNoPrevCursor(t *testing.T) {
	store := &fakeStore{searchRows: rows(3), countN: 3}
	limit := 2
	page, err := newSvc(store).Search(context.Background(), testWS, SearchRequest{Limit: &limit})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if page.PrevCursor != nil {
		t.Fatalf("first page has a prev cursor: %v", *page.PrevCursor)
	}
	if page.NextCursor == nil {
		t.Fatal("a full page with a lookahead row must offer a next cursor")
	}
	if len(page.Items) != 2 {
		t.Fatalf("items = %d, want the lookahead row trimmed off", len(page.Items))
	}
}

func TestSearchLastPageHasNoNextCursor(t *testing.T) {
	store := &fakeStore{searchRows: rows(2), countN: 2}
	limit := 2
	page, err := newSvc(store).Search(context.Background(), testWS, SearchRequest{Limit: &limit})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if page.NextCursor != nil {
		t.Fatalf("exactly-full page offered a next cursor: %v", *page.NextCursor)
	}
}

func TestSearchEmptyPageHasNoCursors(t *testing.T) {
	store := &fakeStore{searchRows: nil, countN: 0}
	page, err := newSvc(store).Search(context.Background(), testWS, SearchRequest{Q: "nomatch"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if page.NextCursor != nil || page.PrevCursor != nil {
		t.Fatal("an empty page must offer no cursors")
	}
	if page.Total != 0 || page.TotalIsCapped {
		t.Fatalf("total = %d capped = %v, want 0/false", page.Total, page.TotalIsCapped)
	}
}

// A page reached with a forward cursor is by definition not the first, so it
// must be able to go back even though the keyset has no "page N-1".
func TestSearchMiddlePageOffersBothCursors(t *testing.T) {
	rs := rows(3)
	store := &fakeStore{searchRows: rs, countN: 10}
	limit := 2
	from := cursor.NewTime(cursor.SortNewest, cursor.After, rs[0].CreatedAt, rs[0].ID).Encode()
	page, err := newSvc(store).Search(context.Background(), testWS, SearchRequest{Cursor: from, Limit: &limit})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if page.PrevCursor == nil || page.NextCursor == nil {
		t.Fatalf("middle page cursors: prev=%v next=%v, want both", page.PrevCursor, page.NextCursor)
	}
}

// A backward page arrives in reverse scan order; the service must flip it back
// into display order or the UI would show a page upside down.
func TestSearchBackwardPageIsReturnedInDisplayOrder(t *testing.T) {
	// A "newest" backward scan walks the index ASCENDING, so this is the order
	// the store really hands back.
	rs := rows(3)
	slices.Reverse(rs)
	store := &fakeStore{searchRows: rs, countN: 10}
	limit := 3
	back := cursor.NewTime(cursor.SortNewest, cursor.Before, rs[0].CreatedAt, rs[0].ID).Encode()
	page, err := newSvc(store).Search(context.Background(), testWS, SearchRequest{Cursor: back, Limit: &limit})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	for i := 1; i < len(page.Items); i++ {
		if !page.Items[i].CreatedAt.Before(page.Items[i-1].CreatedAt) {
			t.Fatalf("items are not in newest-first order at %d: %v", i, page.Items)
		}
	}
	// Travelling back means the page we came from still exists ahead of us.
	if page.NextCursor == nil {
		t.Fatal("a backward page must offer a next cursor")
	}
}

func TestSearchBackwardPageWithoutLookaheadHasNoPrev(t *testing.T) {
	rs := rows(2)
	store := &fakeStore{searchRows: rs, countN: 10}
	limit := 2
	back := cursor.NewTime(cursor.SortNewest, cursor.Before, rs[0].CreatedAt, rs[0].ID).Encode()
	page, err := newSvc(store).Search(context.Background(), testWS, SearchRequest{Cursor: back, Limit: &limit})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if page.PrevCursor != nil {
		t.Fatal("a backward page that reached the start must not offer a prev cursor")
	}
}

func TestSearchTotalCapping(t *testing.T) {
	tests := []struct {
		name       string
		count      int64
		wantTotal  int64
		wantCapped bool
	}{
		{"below the cap is exact", 2340, 2340, false},
		// The count query fetches cap+1 rows, so exactly TotalCap really is
		// exactly TotalCap and must not be flagged capped.
		{"exactly the cap is exact", TotalCap, TotalCap, false},
		{"above the cap is capped", TotalCap + 1, TotalCap, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := &fakeStore{searchRows: rows(1), countN: tc.count}
			page, err := newSvc(store).Search(context.Background(), testWS, SearchRequest{})
			if err != nil {
				t.Fatalf("Search: %v", err)
			}
			if page.Total != tc.wantTotal || page.TotalIsCapped != tc.wantCapped {
				t.Fatalf("total = %d capped = %v, want %d/%v", page.Total, page.TotalIsCapped, tc.wantTotal, tc.wantCapped)
			}
		})
	}
}

// The count must see the same predicate as the page, or the UI would report a
// total for a different search than the rows it is showing.
func TestSearchCountUsesTheSameFilter(t *testing.T) {
	store := &fakeStore{}
	svc := NewService(store, &fakeChecker{exists: true})
	if _, err := svc.Search(context.Background(), testWS, SearchRequest{Q: "Acme", ListID: &testList}); err != nil {
		t.Fatalf("Search: %v", err)
	}
	if store.lastFilter != store.lastSearch.Filter {
		t.Fatalf("count filter %+v != page filter %+v", store.lastFilter, store.lastSearch.Filter)
	}
}

func TestSearchPropagatesStoreErrors(t *testing.T) {
	boom := errors.New("boom")
	if _, err := newSvc(&fakeStore{searchErr: boom}).Search(context.Background(), testWS, SearchRequest{}); !errors.Is(err, boom) {
		t.Fatalf("search err = %v, want boom", err)
	}
	if _, err := newSvc(&fakeStore{countErr: boom}).Search(context.Background(), testWS, SearchRequest{}); !errors.Is(err, boom) {
		t.Fatalf("count err = %v, want boom", err)
	}
}

func TestSearchEmailSortCursorsUseTheEmailKey(t *testing.T) {
	rs := rows(3)
	rs[2].SortEmail = "zed@x.test"
	store := &fakeStore{searchRows: rs, countN: 9}
	limit := 2
	page, err := newSvc(store).Search(context.Background(), testWS, SearchRequest{Sort: "email", Limit: &limit})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if page.NextCursor == nil {
		t.Fatal("want a next cursor")
	}
	got, err := cursor.Decode(*page.NextCursor, cursor.SortEmail)
	if err != nil {
		t.Fatalf("the emitted cursor does not decode under its own sort: %v", err)
	}
	if got.Email != rs[1].SortEmail || got.ID != rs[1].ID {
		t.Fatalf("cursor = %+v, want the last displayed row %+v", got, rs[1])
	}
}
