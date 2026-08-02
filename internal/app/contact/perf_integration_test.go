//go:build integration

package contact

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/inroad/inroad/internal/platform/cursor"
)

// The performance measurement seeds 200,000 contacts, which takes minutes and
// gigabytes. It is therefore opt-in even within the integration suite, so the
// everyday `go test -tags=integration ./...` run stays fast.
//
//	INROAD_PERF_TEST=1 go test -tags=integration -run TestSearchPerformance \
//	    -timeout 30m ./internal/app/contact/
const (
	perfEnv       = "INROAD_PERF_TEST"
	perfContacts  = 200_000
	perfOtherTenn = 20_000
	// rareTerm belongs to exactly one seeded contact, so a search for it is the
	// case an ordered index walk cannot serve cheaply.
	rareTerm = "quixotrope"
)

// perfFixture is a seeded pair of workspaces: a large one to measure against,
// and a second holding its own contacts so cross-tenant leakage would show up
// in both the row counts and the plans.
type perfFixture struct {
	fixture
	ws, other uuid.UUID
}

func setupPerf(t *testing.T, ctx context.Context) perfFixture {
	t.Helper()
	if os.Getenv(perfEnv) == "" {
		t.Skipf("set %s=1 to run the 200k-contact performance measurement", perfEnv)
	}
	f := setup(t, ctx)
	seedContacts(t, ctx, f, f.ws, perfContacts, "big")
	seedContacts(t, ctx, f, f.other, perfOtherTenn, "other")
	f.insert(t, ctx, f.ws, []seed{{
		email: "big-needle@haystack.example", first: "Needle", last: "Inhaystack",
		company: "Quixotrope Industries", createdAt: time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC),
	}})

	// The planner needs statistics or its choices say nothing about the index
	// shape; without this the first EXPLAIN measures a cold-statistics guess.
	start := time.Now()
	if _, err := f.pool.Exec(ctx, `ANALYZE contacts`); err != nil {
		t.Fatalf("analyze: %v", err)
	}
	t.Logf("ANALYZE contacts took %s", time.Since(start).Round(time.Millisecond))
	return perfFixture{fixture: f, ws: f.ws, other: f.other}
}

// seedContacts bulk-loads n contacts via COPY. created_at is spread over a year
// but only 1,000 distinct values are used, so thousands of rows share a
// timestamp exactly as a bulk import produces — the case the id tiebreak and
// the deep-page seek have to survive.
func seedContacts(t *testing.T, ctx context.Context, f fixture, ws uuid.UUID, n int, tag string) {
	t.Helper()
	start := time.Now()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	companies := []string{"Acme Rockets", "Globex", "Initech", "Umbrella", "Hooli"}

	i := 0
	src := pgx.CopyFromFunc(func() ([]any, error) {
		if i >= n {
			return nil, nil
		}
		row := []any{
			ws,
			fmt.Sprintf("%s%06d@mail%02d.example", tag, i, i%97),
			fmt.Sprintf("First%06d", i),
			fmt.Sprintf("Last%06d", i),
			companies[i%len(companies)],
			base.Add(time.Duration(i%1000) * time.Hour),
		}
		i++
		return row, nil
	})
	rows, err := f.pool.CopyFrom(ctx,
		pgx.Identifier{"contacts"},
		[]string{"workspace_id", "email", "first_name", "last_name", "company", "created_at"},
		src)
	if err != nil {
		t.Fatalf("seed %s: %v", tag, err)
	}
	if rows != int64(n) {
		t.Fatalf("seeded %d of %d %s contacts", rows, n, tag)
	}
	t.Logf("seeded %d %s contacts in %s", n, tag, time.Since(start).Round(time.Millisecond))
}

var execTimeRe = regexp.MustCompile(`Execution Time: ([0-9.]+) ms`)

// explain runs EXPLAIN (ANALYZE, BUFFERS) over the exact statement the store
// would execute, and returns the plan text.
func explain(t *testing.T, ctx context.Context, f perfFixture, label, sql string, args []any) string {
	t.Helper()
	rows, err := f.pool.Query(ctx, "EXPLAIN (ANALYZE, BUFFERS) "+sql, args...)
	if err != nil {
		t.Fatalf("%s: explain: %v", label, err)
	}
	defer rows.Close()
	var b strings.Builder
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatalf("%s: scan plan: %v", label, err)
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("%s: plan: %v", label, err)
	}
	plan := b.String()
	t.Logf("\n=== %s ===\n%s", label, plan)
	return plan
}

// assertIndexBacked is the measurement's actual assertion: the plan must use
// one of the new indexes and must not fall back to reading the whole table.
func assertIndexBacked(t *testing.T, label, plan string, wantIndexes ...string) {
	t.Helper()
	if strings.Contains(plan, "Seq Scan on contacts") {
		t.Errorf("%s: plan contains a Seq Scan on contacts — the index is not being used:\n%s", label, plan)
	}
	for _, idx := range wantIndexes {
		if strings.Contains(plan, idx) {
			return
		}
	}
	t.Errorf("%s: plan uses none of %v:\n%s", label, wantIndexes, plan)
}

var rowsRemovedRe = regexp.MustCompile(`Rows Removed by Filter: (\d+)`)

// assertBoundedWork is the assertion that fits the capped count. The count has
// a LIMIT, so the planner is free to choose a sequential scan that early-exits
// once the cap is filled — a legitimate plan, and demanding an index scan would
// be asserting a plan shape rather than the property that matters. What must
// hold is that the work stays bounded: the query may not read many multiples of
// the cap in rows it then throws away, because that is precisely the unbounded
// scan the cap exists to prevent.
func assertBoundedWork(t *testing.T, label, plan string) {
	t.Helper()
	discarded := 0
	for _, m := range rowsRemovedRe.FindAllStringSubmatch(plan, -1) {
		n, err := strconv.Atoi(m[1])
		if err != nil {
			t.Fatalf("%s: parse rows removed %q: %v", label, m[1], err)
		}
		discarded += n
	}
	// Ten times the cap is generous; the failure this guards against is reading
	// the entire table (here 220,000 rows, twenty-two times the cap).
	if limit := 10 * TotalCap; discarded > limit {
		t.Errorf("%s: discarded %d rows to reach a cap of %d (limit %d) — work is not bounded by the cap:\n%s",
			label, discarded, TotalCap, limit, plan)
	}
	if strings.Contains(plan, "Seq Scan on contacts") {
		t.Logf("%s: NOTE plan is a Seq Scan with an early exit, discarding %d rows", label, discarded)
	}
}

var indexNameRe = regexp.MustCompile(`(?:Index Scan using|Bitmap Index Scan on) (\S+)`)

// chosenIndex names the index a plan actually used, for the log line.
func chosenIndex(plan string) string {
	if m := indexNameRe.FindStringSubmatch(plan); m != nil {
		return m[1]
	}
	return "no index"
}

func execTime(t *testing.T, plan string) float64 {
	t.Helper()
	m := execTimeRe.FindStringSubmatch(plan)
	if m == nil {
		t.Fatalf("no execution time in plan:\n%s", plan)
	}
	ms, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		t.Fatalf("parse execution time %q: %v", m[1], err)
	}
	return ms
}

// TestSearchPerformance is the point of this change, so it is measured rather
// than asserted: a search, a DEEP page (the case OFFSET was bad at) and the
// capped count are all run against 200,000 contacts and their plans checked for
// index use.
func TestSearchPerformance(t *testing.T) {
	ctx := context.Background()
	f := setupPerf(t, ctx)

	// A search for a COMMON term has two legitimate plans and the planner picks
	// between them by estimated selectivity, run to run:
	//
	//   - a trigram bitmap over idx_contacts_search, then a top-N sort; or
	//   - an ordered walk of idx_contacts_ws_created, filtering by LIKE and
	//     stopping as soon as the page is full.
	//
	// The second is often the faster of the two here, because a GIN bitmap must
	// be built in full before it yields its first row while an ordered scan can
	// stop early. Asserting one specific index would therefore be asserting a
	// plan shape rather than a property. What must hold is what the migration
	// bought: the table is never read sequentially, and the work stays bounded.
	t.Run("search", func(t *testing.T) {
		sql, args := searchSQL(f.ws, SearchFilter{Query: "mail42.example"}, cursor.SortNewest, nil, DefaultLimit+1)
		plan := explain(t, ctx, f, "search on a common term, first page", sql, args)
		assertIndexBacked(t, "search", plan, "idx_contacts_search", "idx_contacts_ws_created")
		assertBoundedWork(t, "search", plan)
		t.Logf("search execution time: %.2f ms (plan: %s)", execTime(t, plan), chosenIndex(plan))
	})

	// A RARE term is the case only the trigram index can serve: an ordered walk
	// filtering by LIKE would have to cross all 200,000 of the workspace's rows
	// before it could report that one matched. If this plan ever stops using
	// idx_contacts_search, substring search has become an unbounded scan.
	t.Run("search on a rare term", func(t *testing.T) {
		sql, args := searchSQL(f.ws, SearchFilter{Query: rareTerm}, cursor.SortNewest, nil, DefaultLimit+1)
		plan := explain(t, ctx, f, "search on a rare term, first page", sql, args)
		assertIndexBacked(t, "rare search", plan, "idx_contacts_search")
		assertBoundedWork(t, "rare search", plan)
		t.Logf("rare-term search execution time: %.2f ms", execTime(t, plan))

		page, err := f.svc.Search(ctx, f.ws, SearchRequest{Q: rareTerm})
		if err != nil {
			t.Fatalf("rare search: %v", err)
		}
		if len(page.Items) != 1 || page.Total != 1 {
			t.Fatalf("rare search returned %d items (total %d), want exactly 1", len(page.Items), page.Total)
		}
	})

	t.Run("deep page", func(t *testing.T) {
		// Walk a cursor ~4,000 pages in. Under OFFSET this is the query that
		// makes Postgres discard 200,000 rows; under keyset it is a seek.
		deep := deepCursor(t, ctx, f)
		sql, args := searchSQL(f.ws, SearchFilter{}, cursor.SortNewest, &deep, DefaultLimit+1)
		plan := explain(t, ctx, f, "deep keyset page", sql, args)
		assertIndexBacked(t, "deep page", plan, "idx_contacts_ws_created")

		ms := execTime(t, plan)
		t.Logf("deep page execution time: %.2f ms", ms)
		// A keyset seek reads one page's worth of index entries wherever it
		// lands, so a deep page must not cost meaningfully more than a shallow
		// one. This is the property OFFSET could not hold.
		shallowSQL, shallowArgs := searchSQL(f.ws, SearchFilter{}, cursor.SortNewest, nil, DefaultLimit+1)
		shallow := explain(t, ctx, f, "first keyset page (for comparison)", shallowSQL, shallowArgs)
		t.Logf("first page execution time: %.2f ms", execTime(t, shallow))
	})

	// The capped count is the one query whose plan is chosen by cost rather than
	// forced by shape: with a LIMIT the planner may prefer a sequential scan
	// that early-exits once the cap is filled. That is a fine plan when the
	// caller's workspace is most of the table and a bad one when it is a small
	// tenant among many, so both are measured rather than assumed.
	t.Run("capped count, dominant tenant", func(t *testing.T) {
		sql, args := countSQL(f.ws, SearchFilter{Query: "acme"}, TotalCap)
		plan := explain(t, ctx, f, "capped count, broad search, tenant owns ~91% of the table", sql, args)
		assertBoundedWork(t, "capped count (dominant tenant)", plan)
		t.Logf("capped count execution time: %.2f ms", execTime(t, plan))

		// The cap has to actually bind, or the measurement proves nothing:
		// "acme" matches a fifth of 200,000 rows.
		n, err := f.store.CountMatches(ctx, f.ws, SearchFilter{Query: "acme"}, TotalCap)
		if err != nil {
			t.Fatalf("CountMatches: %v", err)
		}
		if n != int64(TotalCap+1) {
			t.Fatalf("count = %d, want the cap to bind at %d", n, TotalCap+1)
		}
	})

	// This is the shape a real multi-tenant deployment has: the caller owns a
	// small slice of the table. A sequential scan here would have to read past
	// every other tenant's rows, so the cap would stop bounding the work.
	t.Run("capped count, small tenant", func(t *testing.T) {
		sql, args := countSQL(f.other, SearchFilter{Query: "acme"}, TotalCap)
		plan := explain(t, ctx, f, "capped count, broad search, tenant owns ~9% of the table", sql, args)
		assertBoundedWork(t, "capped count (small tenant)", plan)
		t.Logf("small-tenant capped count execution time: %.2f ms", execTime(t, plan))
	})

	// A selective search never fills the cap, so there is no early exit to make
	// a sequential scan look cheap: this must be index-backed or it reads the
	// whole table for a query that matches almost nothing.
	t.Run("capped count, selective search", func(t *testing.T) {
		sql, args := countSQL(f.ws, SearchFilter{Query: "mail42.example"}, TotalCap)
		plan := explain(t, ctx, f, "capped count, selective search (cap never binds)", sql, args)
		assertIndexBacked(t, "capped count (selective)", plan, "idx_contacts_search")
		t.Logf("selective capped count execution time: %.2f ms", execTime(t, plan))
	})

	t.Run("unfiltered count", func(t *testing.T) {
		for _, tc := range []struct {
			label string
			ws    uuid.UUID
		}{
			{"dominant tenant", f.ws},
			{"small tenant", f.other},
		} {
			sql, args := countSQL(tc.ws, SearchFilter{}, TotalCap)
			plan := explain(t, ctx, f, "capped count, no search text, "+tc.label, sql, args)
			assertBoundedWork(t, "unfiltered count ("+tc.label+")", plan)
			t.Logf("unfiltered capped count (%s) execution time: %.2f ms", tc.label, execTime(t, plan))
		}
	})

	t.Run("email sort deep page", func(t *testing.T) {
		cur := cursor.NewEmail(cursor.After, fmt.Sprintf("big%06d@mail%02d.example", 150000, 150000%97), uuid.New())
		sql, args := searchSQL(f.ws, SearchFilter{}, cursor.SortEmail, &cur, DefaultLimit+1)
		plan := explain(t, ctx, f, "deep keyset page, email sort", sql, args)
		assertIndexBacked(t, "email deep page", plan, "idx_contacts_ws_email_id", "idx_contacts_ws_email")
		t.Logf("email deep page execution time: %.2f ms", execTime(t, plan))
	})

	// idx_contacts_ws_email_id duplicates the leading columns of the pre-existing
	// UNIQUE index idx_contacts_ws_email (workspace_id, lower(email)). Because
	// that uniqueness makes an email tie impossible, the id column may be dead
	// weight — a third index on 200,000 rows costs storage and write latency on
	// every insert. This measures the counterfactual rather than arguing it.
	t.Run("email sort without the dedicated index", func(t *testing.T) {
		tx, err := f.pool.Begin(ctx)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		defer func() {
			if err := tx.Rollback(ctx); err != nil {
				t.Errorf("rollback: %v", err)
			}
		}()
		if _, err := tx.Exec(ctx, `DROP INDEX idx_contacts_ws_email_id`); err != nil {
			t.Fatalf("drop index: %v", err)
		}
		cur := cursor.NewEmail(cursor.After, fmt.Sprintf("big%06d@mail%02d.example", 150000, 150000%97), uuid.New())
		sql, args := searchSQL(f.ws, SearchFilter{}, cursor.SortEmail, &cur, DefaultLimit+1)

		rows, err := tx.Query(ctx, "EXPLAIN (ANALYZE, BUFFERS) "+sql, args...)
		if err != nil {
			t.Fatalf("explain: %v", err)
		}
		var b strings.Builder
		for rows.Next() {
			var line string
			if err := rows.Scan(&line); err != nil {
				rows.Close()
				t.Fatalf("scan: %v", err)
			}
			b.WriteString(line)
			b.WriteByte('\n')
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			t.Fatalf("plan: %v", err)
		}
		plan := b.String()
		t.Logf("\n=== deep keyset page, email sort, WITHOUT idx_contacts_ws_email_id ===\n%s", plan)
		// Not an assertion about which index wins — the point is only whether
		// the query survives without the dedicated one. A regression here would
		// mean the third index is load-bearing after all.
		if strings.Contains(plan, "Seq Scan on contacts") {
			t.Logf("without idx_contacts_ws_email_id the email sort falls back to a Seq Scan: the index IS load-bearing")
		} else {
			t.Logf("without idx_contacts_ws_email_id the email sort is still index-backed at %.2f ms", execTime(t, plan))
		}
	})

	// Whatever the plans say, the deep page must still be CORRECT: it returns a
	// full page of this workspace's contacts and nobody else's.
	t.Run("deep page is correct and tenant-scoped", func(t *testing.T) {
		deep := deepCursor(t, ctx, f)
		limit := DefaultLimit
		page, err := f.svc.Search(ctx, f.ws, SearchRequest{Cursor: deep.Encode(), Limit: &limit})
		if err != nil {
			t.Fatalf("deep search: %v", err)
		}
		if len(page.Items) != limit {
			t.Fatalf("deep page returned %d rows, want %d", len(page.Items), limit)
		}
		for _, it := range page.Items {
			if !strings.HasPrefix(it.Email, "big") {
				t.Fatalf("deep page leaked a foreign contact: %s", it.Email)
			}
		}
		if page.Total != TotalCap || !page.TotalIsCapped {
			t.Fatalf("total = %d capped = %v, want the cap to bind", page.Total, page.TotalIsCapped)
		}
	})
}

// deepCursor builds a cursor pointing roughly 200,000 rows into the newest
// ordering by reading the row at that position directly. Paging there one page
// at a time would take thousands of round trips and measure the test, not the
// query.
func deepCursor(t *testing.T, ctx context.Context, f perfFixture) cursor.Cursor {
	t.Helper()
	var at time.Time
	var id uuid.UUID
	err := f.pool.QueryRow(ctx, `
		SELECT created_at, id FROM contacts
		WHERE workspace_id = $1
		ORDER BY created_at DESC, id DESC
		OFFSET $2 LIMIT 1`, f.ws, perfContacts-2*DefaultLimit).Scan(&at, &id)
	if err != nil {
		t.Fatalf("deep cursor: %v", err)
	}
	return cursor.NewTime(cursor.SortNewest, cursor.After, at, id)
}
