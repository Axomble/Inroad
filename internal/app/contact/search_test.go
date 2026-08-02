package contact

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/platform/cursor"
)

var (
	testWS   = uuid.MustParse("11111111-1111-4111-8111-111111111111")
	testList = uuid.MustParse("22222222-2222-4222-8222-222222222222")
	testID   = uuid.MustParse("33333333-3333-4333-8333-333333333333")
	testAt   = time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC)
)

// The workspace pin is the tenant boundary, so it is asserted structurally on
// every statement shape rather than trusted to review: it must be present, and
// it must be the FIRST bound argument, so no filter can be built without it.
func TestEveryStatementPinsWorkspaceFirst(t *testing.T) {
	cur := cursor.NewTime(cursor.SortNewest, cursor.After, testAt, testID)
	filters := []SearchFilter{
		{},
		{Query: "acme"},
		{ListID: &testList},
		{Query: "acme", ListID: &testList},
	}
	for _, f := range filters {
		for _, sort := range []cursor.Sort{cursor.SortNewest, cursor.SortOldest, cursor.SortEmail} {
			for _, c := range []*cursor.Cursor{nil, &cur} {
				if c != nil && sort != cursor.SortNewest {
					continue // a cursor only ever pairs with its own sort
				}
				sql, args := searchSQL(testWS, f, sort, c, 50)
				assertWorkspacePinned(t, sql, args)
			}
			sql, args := countSQL(testWS, f, TotalCap)
			assertWorkspacePinned(t, sql, args)
		}
	}
}

func assertWorkspacePinned(t *testing.T, sql string, args []any) {
	t.Helper()
	if !strings.Contains(sql, "c.workspace_id = $1") {
		t.Fatalf("statement is not workspace-pinned:\n%s", sql)
	}
	if len(args) == 0 || args[0] != any(testWS) {
		t.Fatalf("first bound arg = %v, want the workspace id", args)
	}
}

func TestSearchSQLFirstPageOmitsTheKeysetComparison(t *testing.T) {
	sql, args := searchSQL(testWS, SearchFilter{}, cursor.SortNewest, nil, 50)
	// A first page must not carry a neutralised comparison: a "$n IS NULL OR"
	// guard would survive into the plan and stop the index condition forming.
	if strings.Contains(sql, "IS NULL") || strings.Contains(sql, "c.id) <") || strings.Contains(sql, "c.id) >") {
		t.Fatalf("first page emitted a keyset comparison:\n%s", sql)
	}
	if !strings.Contains(sql, "ORDER BY c.created_at DESC, c.id DESC") {
		t.Fatalf("wrong order:\n%s", sql)
	}
	if len(args) != 2 { // workspace + limit
		t.Fatalf("args = %v, want workspace and limit only", args)
	}
}

// The unused filters must be ABSENT, not neutralised — this is the property the
// whole design rests on.
func TestSearchSQLOmitsUnusedFilters(t *testing.T) {
	sql, _ := searchSQL(testWS, SearchFilter{}, cursor.SortNewest, nil, 50)
	if strings.Contains(sql, "search_text") {
		t.Fatalf("no-query search still references search_text:\n%s", sql)
	}
	if strings.Contains(sql, "list_members") {
		t.Fatalf("no-list search still references list_members:\n%s", sql)
	}
}

func TestSearchSQLDirections(t *testing.T) {
	tests := []struct {
		name      string
		sort      cursor.Sort
		dir       cursor.Direction
		wantCmp   string
		wantOrder string
	}{
		{"newest next", cursor.SortNewest, cursor.After, "(c.created_at, c.id) <", "ORDER BY c.created_at DESC, c.id DESC"},
		{"newest prev", cursor.SortNewest, cursor.Before, "(c.created_at, c.id) >", "ORDER BY c.created_at ASC, c.id ASC"},
		{"oldest next", cursor.SortOldest, cursor.After, "(c.created_at, c.id) >", "ORDER BY c.created_at ASC, c.id ASC"},
		{"oldest prev", cursor.SortOldest, cursor.Before, "(c.created_at, c.id) <", "ORDER BY c.created_at DESC, c.id DESC"},
		{"email next", cursor.SortEmail, cursor.After, "(lower(c.email), c.id) >", "ORDER BY lower(c.email) ASC, c.id ASC"},
		{"email prev", cursor.SortEmail, cursor.Before, "(lower(c.email), c.id) <", "ORDER BY lower(c.email) DESC, c.id DESC"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := cursor.NewTime(tc.sort, tc.dir, testAt, testID)
			if tc.sort == cursor.SortEmail {
				c = cursor.NewEmail(tc.dir, "jo@acme.com", testID)
			}
			sql, _ := searchSQL(testWS, SearchFilter{}, tc.sort, &c, 50)
			if !strings.Contains(sql, tc.wantCmp) {
				t.Fatalf("missing %q in:\n%s", tc.wantCmp, sql)
			}
			if !strings.Contains(sql, tc.wantOrder) {
				t.Fatalf("missing %q in:\n%s", tc.wantOrder, sql)
			}
		})
	}
}

// The tiebreak must always be present and always share the key's direction; a
// bulk import gives thousands of rows the same created_at, and a partial order
// would let a page repeat or skip them.
func TestSearchSQLAlwaysBreaksTiesOnID(t *testing.T) {
	for _, sort := range []cursor.Sort{cursor.SortNewest, cursor.SortOldest, cursor.SortEmail} {
		sql, _ := searchSQL(testWS, SearchFilter{}, sort, nil, 50)
		_, order, found := strings.Cut(sql, "ORDER BY")
		if !found {
			t.Fatalf("sort %q: statement has no ORDER BY:\n%s", sort, sql)
		}
		asc, desc := strings.Count(order, "ASC"), strings.Count(order, "DESC")
		if (asc != 2 && desc != 2) || (asc > 0 && desc > 0) {
			t.Fatalf("sort %q: key and id disagree on direction: %q", sort, order)
		}
		if !strings.Contains(order, "c.id") {
			t.Fatalf("sort %q: no id tiebreak in %q", sort, order)
		}
	}
}

// A user typing a LIKE metacharacter means the literal character. Left raw, "%"
// would become "match anything" — wrong results and a pattern the trigram index
// cannot serve.
func TestSearchSQLEscapesLikeMetacharacters(t *testing.T) {
	_, args := searchSQL(testWS, SearchFilter{Query: `100%_off\x`}, cursor.SortNewest, nil, 50)
	got, ok := args[1].(string)
	if !ok {
		t.Fatalf("query arg = %#v, want a string", args[1])
	}
	if want := `100\%\_off\\x`; got != want {
		t.Fatalf("escaped query = %q, want %q", got, want)
	}
}

func TestCountSQLIsBoundedAtCapPlusOne(t *testing.T) {
	sql, args := countSQL(testWS, SearchFilter{Query: "acme"}, TotalCap)
	if !strings.Contains(sql, "LIMIT $3") {
		t.Fatalf("count is not limited:\n%s", sql)
	}
	// cap+1 is what makes "exactly the cap" distinguishable from "at least the
	// cap"; a bare cap would report a set of exactly 10000 as capped.
	if args[2] != any(int32(TotalCap+1)) {
		t.Fatalf("count limit = %v, want %d", args[2], TotalCap+1)
	}
	if strings.Contains(sql, "ORDER BY") {
		t.Fatalf("count must not sort:\n%s", sql)
	}
}

func TestCountSQLCarriesTheSameFilters(t *testing.T) {
	sql, args := countSQL(testWS, SearchFilter{Query: "acme", ListID: &testList}, TotalCap)
	if !strings.Contains(sql, "search_text LIKE") || !strings.Contains(sql, "list_members") {
		t.Fatalf("count dropped a filter, so it would not match the page:\n%s", sql)
	}
	if args[2] != any(testList) {
		t.Fatalf("list arg = %v, want %v", args[2], testList)
	}
}
