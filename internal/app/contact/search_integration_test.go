//go:build integration

package contact

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inroad/inroad/internal/platform/cursor"
	"github.com/inroad/inroad/internal/platform/db"
	"github.com/inroad/inroad/internal/platform/db/gen"
)

func dsn() string {
	if v := os.Getenv("INROAD_DATABASE_URL"); v != "" {
		return v
	}
	return "postgres://inroad:inroad@localhost:5433/inroad?sslmode=disable"
}

// dbListChecker is the real cross-domain ownership check, workspace-pinned, so
// the integration tests exercise the same 404 path the server wires up.
type dbListChecker struct{ pool *pgxpool.Pool }

func (c dbListChecker) ListExists(ctx context.Context, ws, listID uuid.UUID) (bool, error) {
	var n int
	err := c.pool.QueryRow(ctx,
		`SELECT count(*) FROM lists WHERE id = $1 AND workspace_id = $2`, listID, ws).Scan(&n)
	return n > 0, err
}

type fixture struct {
	pool  *pgxpool.Pool
	store *PgStore
	svc   *Service
	ws    uuid.UUID
	other uuid.UUID
	list  uuid.UUID
}

// connect migrates and opens the pool. Migration runs once per test binary
// invocation; it is idempotent.
func connect(t *testing.T, ctx context.Context) *pgxpool.Pool {
	t.Helper()
	if err := db.Migrate(dsn()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	pool, err := db.Connect(ctx, dsn())
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// newWorkspace creates a throwaway workspace and registers its deletion. Every
// fixture gets its own; the dev database holds real demo data that these tests
// must never touch, so nothing is ever truncated.
func newWorkspace(t *testing.T, ctx context.Context, pool *pgxpool.Pool, name string) uuid.UUID {
	t.Helper()
	w, err := gen.New(pool).CreateWorkspace(ctx, name+" "+uuid.NewString())
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	t.Cleanup(func() {
		// The FK cascade removes contacts, lists and list_members with it.
		if _, err := pool.Exec(context.Background(), `DELETE FROM workspaces WHERE id = $1`, w.ID); err != nil {
			t.Errorf("cleanup workspace %s: %v", w.ID, err)
		}
	})
	return w.ID
}

func setup(t *testing.T, ctx context.Context) fixture {
	t.Helper()
	pool := connect(t, ctx)
	f := fixture{
		pool:  pool,
		store: NewPgStore(pool),
		ws:    newWorkspace(t, ctx, pool, "Search"),
		other: newWorkspace(t, ctx, pool, "Search Other"),
	}
	f.svc = NewService(f.store, dbListChecker{pool: pool})
	lst, err := gen.New(pool).CreateList(ctx, gen.CreateListParams{WorkspaceID: f.ws, Name: "L"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	f.list = lst.ID
	return f
}

type seed struct {
	email, first, last, company string
	createdAt                   time.Time
	inList                      bool
}

// insert writes the seeds directly so created_at can be controlled (the sqlc
// upsert defaults it to now(), which cannot produce a deterministic ordering).
func (f fixture) insert(t *testing.T, ctx context.Context, ws uuid.UUID, seeds []seed) []uuid.UUID {
	t.Helper()
	ids := make([]uuid.UUID, 0, len(seeds))
	for _, s := range seeds {
		var id uuid.UUID
		err := f.pool.QueryRow(ctx,
			`INSERT INTO contacts (workspace_id, email, first_name, last_name, company, created_at)
			 VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`,
			ws, s.email, s.first, s.last, s.company, s.createdAt).Scan(&id)
		if err != nil {
			t.Fatalf("insert %s: %v", s.email, err)
		}
		if s.inList {
			if _, err := f.pool.Exec(ctx,
				`INSERT INTO list_members (list_id, contact_id) VALUES ($1, $2)`, f.list, id); err != nil {
				t.Fatalf("list member %s: %v", s.email, err)
			}
		}
		ids = append(ids, id)
	}
	return ids
}

func base() time.Time { return time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC) }

// emails collects the returned addresses so assertions read as sets of people
// rather than rows.
func emails(page Page) []string {
	out := make([]string, 0, len(page.Items))
	for _, it := range page.Items {
		out = append(out, it.Email)
	}
	return out
}

// TestSearchMatchesEveryIndexedColumn proves the generated search_text really
// spans all four columns, case-insensitively, and matches in the MIDDLE of a
// value — the substring case a prefix index could not serve and the reason the
// old client-side filter failed on a partial domain.
func TestSearchMatchesEveryIndexedColumn(t *testing.T) {
	ctx := context.Background()
	f := setup(t, ctx)
	f.insert(t, ctx, f.ws, []seed{
		{email: "jo@acme.com", first: "Josephine", last: "Trelawney", company: "Acme Rockets", createdAt: base()},
		{email: "kai@globex.io", first: "Kai", last: "Nakamura", company: "Globex", createdAt: base().Add(time.Minute)},
	})

	tests := []struct {
		name  string
		q     string
		want  []string
		count int
	}{
		{"email substring in the middle", "acme.co", []string{"jo@acme.com"}, 1},
		{"email local part", "jo@", []string{"jo@acme.com"}, 1},
		{"first name", "josephine", []string{"jo@acme.com"}, 1},
		{"last name", "trelawney", []string{"jo@acme.com"}, 1},
		{"company", "rockets", []string{"jo@acme.com"}, 1},
		{"uppercase query matches lowercase data", "TRELAWNEY", []string{"jo@acme.com"}, 1},
		{"lowercase query matches capitalised data", "nakamura", []string{"kai@globex.io"}, 1},
		{"substring inside a last name", "akamu", []string{"kai@globex.io"}, 1},
		{"no match", "zzzz", nil, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			page, err := f.svc.Search(ctx, f.ws, SearchRequest{Q: tc.q})
			if err != nil {
				t.Fatalf("Search: %v", err)
			}
			got := emails(page)
			if len(got) != len(tc.want) {
				t.Fatalf("q=%q got %v, want %v", tc.q, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("q=%q got %v, want %v", tc.q, got, tc.want)
				}
			}
			if int(page.Total) != tc.count {
				t.Fatalf("q=%q total = %d, want %d", tc.q, page.Total, tc.count)
			}
		})
	}
}

// A LIKE metacharacter must be matched literally, not treated as a wildcard.
// Unescaped, "%" would match every contact in the workspace.
func TestSearchTreatsLikeMetacharactersLiterally(t *testing.T) {
	ctx := context.Background()
	f := setup(t, ctx)
	f.insert(t, ctx, f.ws, []seed{
		{email: "a@x.test", first: "Alice", company: "100% Cotton", createdAt: base()},
		{email: "b@x.test", first: "Bob", company: "Wool", createdAt: base().Add(time.Minute)},
	})

	page, err := f.svc.Search(ctx, f.ws, SearchRequest{Q: "00% cot"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if got := emails(page); len(got) != 1 || got[0] != "a@x.test" {
		t.Fatalf("got %v, want just a@x.test", got)
	}
	// A bare wildcard is a literal too, so it matches nobody here.
	page, err = f.svc.Search(ctx, f.ws, SearchRequest{Q: "%%"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(page.Items) != 0 {
		t.Fatalf("'%%%%' matched %v; the wildcard was not escaped", emails(page))
	}
}

// TestKeysetPagingVisitsEveryRowExactlyOnce is the core pagination guarantee.
// Every contact shares one of three created_at values, so the id tiebreak is
// load-bearing: without it a page boundary landing inside a timestamp group
// would repeat or skip rows.
func TestKeysetPagingVisitsEveryRowExactlyOnce(t *testing.T) {
	ctx := context.Background()
	f := setup(t, ctx)

	const total = 1000
	seeds := make([]seed, total)
	for i := range seeds {
		seeds[i] = seed{
			email:     fmt.Sprintf("p%04d@paging.test", i),
			first:     fmt.Sprintf("First%04d", i),
			createdAt: base().Add(time.Duration(i%3) * time.Second),
		}
	}
	f.insert(t, ctx, f.ws, seeds)

	for _, sort := range []string{"newest", "oldest", "email"} {
		t.Run(sort, func(t *testing.T) {
			limit := 37 // deliberately not a divisor of 1000
			seen := make(map[uuid.UUID]int, total)
			order := make([]uuid.UUID, 0, total)
			cur := ""
			for pages := 0; ; pages++ {
				if pages > total/limit+2 {
					t.Fatal("paging did not terminate")
				}
				page, err := f.svc.Search(ctx, f.ws, SearchRequest{Sort: sort, Cursor: cur, Limit: &limit})
				if err != nil {
					t.Fatalf("page %d: %v", pages, err)
				}
				if (cur == "") != (page.PrevCursor == nil) {
					t.Fatalf("page %d: prev_cursor must be null on the first page only", pages)
				}
				for _, it := range page.Items {
					seen[it.ID]++
					order = append(order, it.ID)
				}
				if page.NextCursor == nil {
					if len(page.Items) == 0 && pages != 0 {
						t.Fatal("last page was empty; the lookahead over-reported a next page")
					}
					break
				}
				cur = *page.NextCursor
			}

			if len(seen) != total {
				t.Fatalf("visited %d distinct contacts, want %d", len(seen), total)
			}
			for id, n := range seen {
				if n != 1 {
					t.Fatalf("contact %s appeared %d times", id, n)
				}
			}

			// Walking back with prev_cursor must retrace the same sequence.
			assertBackwardRetracesForward(t, ctx, f, sort, limit, order)
		})
	}
}

// assertBackwardRetracesForward pages from the end to the start using
// prev_cursor and checks the concatenated result equals the forward order.
// Keyset "previous" is a reversed scan, so an off-by-one there would silently
// drop a row rather than error.
func assertBackwardRetracesForward(t *testing.T, ctx context.Context, f fixture, sort string, limit int, forward []uuid.UUID) {
	t.Helper()
	// Walk forward to the last page first, keeping its prev cursor.
	cur := ""
	var prev *string
	for {
		page, err := f.svc.Search(ctx, f.ws, SearchRequest{Sort: sort, Cursor: cur, Limit: &limit})
		if err != nil {
			t.Fatalf("forward: %v", err)
		}
		if page.NextCursor == nil {
			prev = page.PrevCursor
			break
		}
		cur = *page.NextCursor
	}

	// Now walk back, prepending each page.
	var back []uuid.UUID
	for prev != nil {
		page, err := f.svc.Search(ctx, f.ws, SearchRequest{Sort: sort, Cursor: *prev, Limit: &limit})
		if err != nil {
			t.Fatalf("backward: %v", err)
		}
		ids := make([]uuid.UUID, 0, len(page.Items))
		for _, it := range page.Items {
			ids = append(ids, it.ID)
		}
		back = append(ids, back...)
		prev = page.PrevCursor
	}

	// back covers everything except the final page reached going forward.
	if len(back) == 0 || len(back) >= len(forward) {
		t.Fatalf("backward walk covered %d of %d rows", len(back), len(forward))
	}
	for i, id := range back {
		if forward[i] != id {
			t.Fatalf("backward walk diverged at %d: %s != %s", i, id, forward[i])
		}
	}
}

func TestFirstAndLastPageCursorsAreNull(t *testing.T) {
	ctx := context.Background()
	f := setup(t, ctx)
	seeds := make([]seed, 5)
	for i := range seeds {
		seeds[i] = seed{email: fmt.Sprintf("e%d@x.test", i), createdAt: base().Add(time.Duration(i) * time.Minute)}
	}
	f.insert(t, ctx, f.ws, seeds)

	limit := 5
	page, err := f.svc.Search(ctx, f.ws, SearchRequest{Limit: &limit})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	// Exactly one full page of rows: there is no next page and no previous one.
	if page.NextCursor != nil || page.PrevCursor != nil {
		t.Fatalf("single full page offered cursors: next=%v prev=%v", page.NextCursor, page.PrevCursor)
	}
	if len(page.Items) != 5 {
		t.Fatalf("items = %d, want 5", len(page.Items))
	}
}

func TestBadCursorIsAnErrorNotAResetToPageOne(t *testing.T) {
	ctx := context.Background()
	f := setup(t, ctx)
	f.insert(t, ctx, f.ws, []seed{{email: "a@x.test", createdAt: base()}})

	first, err := f.svc.Search(ctx, f.ws, SearchRequest{})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	_ = first

	tests := []struct {
		name string
		req  SearchRequest
	}{
		{"garbage", SearchRequest{Cursor: "not-a-cursor!!"}},
		{"truncated", SearchRequest{Cursor: "MXxuZXdlc3Q"}},
		{"cursor minted for another sort", SearchRequest{
			Sort:   "email",
			Cursor: cursor.NewTime(cursor.SortNewest, cursor.After, base(), uuid.New()).Encode(),
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			page, err := f.svc.Search(ctx, f.ws, tc.req)
			if err == nil {
				t.Fatalf("bad cursor silently returned %d rows", len(page.Items))
			}
			if !IsCursorError(err) {
				t.Fatalf("err = %v, want a cursor error (400)", err)
			}
		})
	}
}

func TestTotalIsExactBelowTheCapAndCappedAbove(t *testing.T) {
	ctx := context.Background()
	f := setup(t, ctx)
	const n = 25
	seeds := make([]seed, n)
	for i := range seeds {
		seeds[i] = seed{email: fmt.Sprintf("t%02d@cap.test", i), createdAt: base()}
	}
	f.insert(t, ctx, f.ws, seeds)

	// CountMatches takes the cap as a parameter, so the capping behaviour is
	// provable against a small set instead of seeding 10,001 rows.
	tests := []struct {
		capAt      int
		wantN      int64
		wantCapped bool
	}{
		{capAt: 100, wantN: n, wantCapped: false},
		{capAt: n, wantN: n, wantCapped: false}, // exactly the cap is still exact
		{capAt: 10, wantN: 10, wantCapped: true},
	}
	for _, tc := range tests {
		got, err := f.store.CountMatches(ctx, f.ws, SearchFilter{}, tc.capAt)
		if err != nil {
			t.Fatalf("CountMatches: %v", err)
		}
		capped := got > int64(tc.capAt)
		if capped != tc.wantCapped {
			t.Fatalf("cap=%d capped = %v, want %v (raw %d)", tc.capAt, capped, tc.wantCapped, got)
		}
		if min(got, int64(tc.capAt)) != tc.wantN {
			t.Fatalf("cap=%d total = %d, want %d", tc.capAt, min(got, int64(tc.capAt)), tc.wantN)
		}
	}

	// The service reports the real total below its own (much larger) cap.
	page, err := f.svc.Search(ctx, f.ws, SearchRequest{})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if page.Total != n || page.TotalIsCapped {
		t.Fatalf("total = %d capped = %v, want %d/false", page.Total, page.TotalIsCapped, n)
	}
}

func TestListFilterAndAllContactsMode(t *testing.T) {
	ctx := context.Background()
	f := setup(t, ctx)
	f.insert(t, ctx, f.ws, []seed{
		{email: "in1@x.test", company: "Acme", createdAt: base(), inList: true},
		{email: "in2@x.test", company: "Acme", createdAt: base().Add(time.Minute), inList: true},
		{email: "out@x.test", company: "Acme", createdAt: base().Add(2 * time.Minute)},
	})

	all, err := f.svc.Search(ctx, f.ws, SearchRequest{})
	if err != nil {
		t.Fatalf("all: %v", err)
	}
	if len(all.Items) != 3 || all.Total != 3 {
		t.Fatalf("all-contacts mode returned %v (total %d), want 3", emails(all), all.Total)
	}

	inList, err := f.svc.Search(ctx, f.ws, SearchRequest{ListID: &f.list})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(inList.Items) != 2 || inList.Total != 2 {
		t.Fatalf("list mode returned %v (total %d), want the 2 members", emails(inList), inList.Total)
	}

	// The list filter and the text filter must compose.
	both, err := f.svc.Search(ctx, f.ws, SearchRequest{ListID: &f.list, Q: "in1"})
	if err != nil {
		t.Fatalf("list+q: %v", err)
	}
	if len(both.Items) != 1 || both.Items[0].Email != "in1@x.test" {
		t.Fatalf("list+q returned %v, want in1@x.test", emails(both))
	}
}

func TestUnknownOrCrossTenantListIsNotFound(t *testing.T) {
	ctx := context.Background()
	f := setup(t, ctx)

	// A list that really exists, but in the OTHER workspace.
	foreign, err := gen.New(f.pool).CreateList(ctx, gen.CreateListParams{WorkspaceID: f.other, Name: "Theirs"})
	if err != nil {
		t.Fatalf("foreign list: %v", err)
	}
	missing := uuid.New()
	for _, id := range []uuid.UUID{foreign.ID, missing} {
		if _, err := f.svc.Search(ctx, f.ws, SearchRequest{ListID: &id}); err == nil {
			t.Fatalf("list %s leaked: want ErrListNotFound", id)
		}
	}
}

// TestCrossWorkspaceContactsNeverAppear is the tenant-isolation matrix: the two
// workspaces hold contacts with IDENTICAL searchable text and overlapping
// timestamps, so any missing workspace_id filter shows up as a leaked row for
// some combination of q, sort and cursor.
func TestCrossWorkspaceContactsNeverAppear(t *testing.T) {
	ctx := context.Background()
	f := setup(t, ctx)

	mine := []seed{
		{email: "shared1@acme.com", first: "Sam", last: "Shared", company: "Acme", createdAt: base()},
		{email: "shared2@acme.com", first: "Sam", last: "Shared", company: "Acme", createdAt: base().Add(time.Minute)},
		{email: "shared3@acme.com", first: "Sam", last: "Shared", company: "Acme", createdAt: base().Add(2 * time.Minute)},
	}
	theirs := []seed{
		{email: "shared1@acme.com", first: "Sam", last: "Shared", company: "Acme", createdAt: base()},
		{email: "shared2@acme.com", first: "Sam", last: "Shared", company: "Acme", createdAt: base().Add(time.Minute)},
		{email: "theirs@acme.com", first: "Sam", last: "Shared", company: "Acme", createdAt: base().Add(90 * time.Second)},
	}
	mineIDs := f.insert(t, ctx, f.ws, mine)
	theirIDs := f.insert(t, ctx, f.other, theirs)

	ours := make(map[uuid.UUID]bool, len(mineIDs))
	for _, id := range mineIDs {
		ours[id] = true
	}
	foreign := make(map[uuid.UUID]bool, len(theirIDs))
	for _, id := range theirIDs {
		foreign[id] = true
	}

	limit := 2 // forces a cursor hop through the shared timestamps
	for _, sort := range []string{"newest", "oldest", "email"} {
		for _, q := range []string{"", "acme", "sam", "shared"} {
			name := sort + "/q=" + q
			t.Run(name, func(t *testing.T) {
				cur := ""
				n := 0
				for pages := 0; pages < 10; pages++ {
					page, err := f.svc.Search(ctx, f.ws, SearchRequest{Sort: sort, Q: q, Cursor: cur, Limit: &limit})
					if err != nil {
						t.Fatalf("Search: %v", err)
					}
					for _, it := range page.Items {
						if foreign[it.ID] {
							t.Fatalf("cross-tenant contact %s (%s) leaked", it.ID, it.Email)
						}
						if !ours[it.ID] {
							t.Fatalf("unknown contact %s (%s) appeared", it.ID, it.Email)
						}
						n++
					}
					if page.TotalIsCapped || page.Total != int64(len(mine)) {
						t.Fatalf("total = %d, want %d — the count must be workspace-scoped too", page.Total, len(mine))
					}
					if page.NextCursor == nil {
						break
					}
					cur = *page.NextCursor
				}
				if n != len(mine) {
					t.Fatalf("saw %d rows, want %d", n, len(mine))
				}
			})
		}
	}

	// A cursor minted in one workspace must not reveal the other's rows either:
	// the workspace comes from the caller, never the cursor.
	theirPage, err := f.svc.Search(ctx, f.other, SearchRequest{Limit: &limit})
	if err != nil {
		t.Fatalf("other workspace: %v", err)
	}
	if theirPage.NextCursor == nil {
		t.Fatal("expected the other workspace to have a next page")
	}
	replayed, err := f.svc.Search(ctx, f.ws, SearchRequest{Cursor: *theirPage.NextCursor, Limit: &limit})
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	for _, it := range replayed.Items {
		if foreign[it.ID] {
			t.Fatalf("replaying another workspace's cursor leaked %s", it.Email)
		}
	}
}

// The email sort must order by lower(email) — the same expression its index is
// built on — so a capitalised address does not sort into a separate ASCII block.
func TestEmailSortIsCaseInsensitiveAndTotal(t *testing.T) {
	ctx := context.Background()
	f := setup(t, ctx)
	f.insert(t, ctx, f.ws, []seed{
		{email: "Bravo@x.test", createdAt: base()},
		{email: "alpha@x.test", createdAt: base()},
		{email: "Charlie@x.test", createdAt: base()},
	})

	limit := 1
	var got []string
	cur := ""
	for i := 0; i < 4; i++ {
		page, err := f.svc.Search(ctx, f.ws, SearchRequest{Sort: "email", Cursor: cur, Limit: &limit})
		if err != nil {
			t.Fatalf("Search: %v", err)
		}
		got = append(got, emails(page)...)
		if page.NextCursor == nil {
			break
		}
		cur = *page.NextCursor
	}
	want := []string{"alpha@x.test", "Bravo@x.test", "Charlie@x.test"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}
