//go:build integration

package deliverability

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
)

// The measurement seeds 200,000 sends and 20,000 bounced enrollments, which takes
// minutes. Opt-in even within the integration suite, so the everyday
// `go test -tags=integration ./...` run stays fast. Mirrors
// app/contact/perf_integration_test.go.
//
//	INROAD_PERF_TEST=1 go test -tags=integration -run TestDeliverabilityPerformance \
//	    -timeout 30m ./internal/app/deliverability/
const (
	perfEnv = "INROAD_PERF_TEST"
	// perfSends is the delivered volume. 200k matches the contact-search
	// measurement so the two are comparable.
	perfSends = 200_000
	// perfBounced is how many of those contacts hard-bounced: 10%, which is both a
	// realistically bad list AND the worst case for the bounce subquery, since
	// every one of those rows has to be read and de-duplicated.
	perfBounced = 20_000
	// perfOtherTenant holds a second workspace's rows, so a plan that failed to
	// pin the tenant would show up as extra rows read rather than passing quietly.
	perfOtherTenant = 20_000
	// perfHistoryDays is how far back the seeded history runs. It has to exceed the
	// 7-day scoring window, or every row falls inside it, a sequential scan becomes
	// the genuinely optimal plan, and the measurement stops saying anything about
	// the indexes. 56 days puts exactly an eighth of the rows in the window.
	perfHistoryDays = 56
	// perfWindowSends / perfWindowBounced are how many seeded rows fall INSIDE the
	// 7-day scoring window, derived from the spread above rather than restated: an
	// eighth of the history, at the same 10% bounce rate.
	perfWindowSends   = perfSends * deliverabilityWindowDays / perfHistoryDays
	perfWindowBounced = perfBounced * deliverabilityWindowDays / perfHistoryDays
	// perfBudget is the ceiling for one guardrail query. The breaker runs at most
	// once per campaign per minute (the asynq dedup bucket), so this is generous —
	// it exists to catch a plan that has collapsed into a scan, not to shave
	// milliseconds.
	perfBudget = 150 * time.Millisecond
)

// deliverabilityWindowDays mirrors platform/deliverability.WindowDays as an
// untyped constant, so the derived row counts above can be constant expressions.
const deliverabilityWindowDays = 7

var execTimeRe = regexp.MustCompile(`Execution Time: ([0-9.]+) ms`)

type perfFixture struct {
	*fixture
	other     uuid.UUID
	otherCamp uuid.UUID
	otherMbx  uuid.UUID
	otherList uuid.UUID
	// now is captured ONCE and both the seeded history and the queried window are
	// derived from it. Calling time.Now() separately in each would let the ~25s of
	// seeding shift the window boundary across rows, and the exact row-count
	// assertions below would flake by a row or two.
	now        time.Time
	windowFrom time.Time
}

func setupPerf(t *testing.T, ctx context.Context) perfFixture {
	t.Helper()
	if os.Getenv(perfEnv) == "" {
		t.Skipf("set %s=1 to run the %d-send performance measurement", perfEnv, perfSends)
	}
	f := newFixture(t, ctx)
	now := time.Now()
	p := perfFixture{fixture: f, now: now, windowFrom: now.AddDate(0, 0, -deliverabilityWindowDays)}
	p.other, p.otherMbx, p.otherList, p.otherCamp = seedTenant(t, ctx, f.q, f.pool)

	seedVolume(t, ctx, p, f.ws, f.campaign, f.mailbox, f.list, perfSends, perfBounced, "big")
	seedVolume(t, ctx, p, p.other, p.otherCamp, p.otherMbx, p.otherList, perfOtherTenant, perfOtherTenant/10, "other")

	// Without statistics the planner's choices say nothing about the index shape:
	// the first EXPLAIN would measure a cold-statistics guess.
	start := time.Now()
	for _, table := range []string{"sends", "sequence_enrollments", "deliverability_events", "contacts"} {
		if _, err := f.pool.Exec(ctx, "ANALYZE "+table); err != nil {
			t.Fatalf("analyze %s: %v", table, err)
		}
	}
	t.Logf("ANALYZE took %s", time.Since(start).Round(time.Millisecond))
	return p
}

// seedVolume bulk-loads contacts, 'sent' sends, and enrollments for one tenant.
//
// The shape matters more than the volume. Two properties make this a measurement
// of the indexes rather than of a scan:
//
//   - History is `perfHistoryDays` long while the scored window is 7 days, so the
//     window predicate selects an eighth of the rows. A fixture whose every row
//     falls inside the window makes a sequential scan genuinely optimal, and the
//     plan then says nothing about whether the index would have been used.
//   - Only `bouncedIn` of every 10 enrollments is stopped 'bounced'; the rest stay
//     active. A partial index covering 100% of its table is never worth a planner's
//     time either.
//
// Both were wrong in the first version of this fixture, which is why it reported
// sequential scans on tables that are in fact properly indexed.
func seedVolume(
	t *testing.T, ctx context.Context, p perfFixture,
	ws, campaign, mailbox, list uuid.UUID, sends, bounced int, tag string,
) {
	t.Helper()
	start := time.Now()
	// Spread the history backwards from now, so the last 7 days are the newest
	// 7/perfHistoryDays of it.
	base := p.now.AddDate(0, 0, -perfHistoryDays)
	step := (time.Duration(perfHistoryDays) * 24 * time.Hour) / time.Duration(sends)
	// bounceEvery yields `bounced` bounced rows out of `sends` enrollments.
	bounceEvery := sends / bounced

	contactIDs := make([]uuid.UUID, sends)
	for i := range contactIDs {
		contactIDs[i] = uuid.New()
	}

	i := 0
	n, err := p.pool.CopyFrom(ctx,
		pgx.Identifier{"contacts"},
		[]string{"id", "workspace_id", "email", "first_name"},
		pgx.CopyFromFunc(func() ([]any, error) {
			if i >= sends {
				return nil, nil
			}
			row := []any{contactIDs[i], ws, fmt.Sprintf("%s%07d@perf.test", tag, i), "C"}
			i++
			return row, nil
		}))
	if err != nil {
		t.Fatalf("seed %s contacts: %v", tag, err)
	}
	if n != int64(sends) {
		t.Fatalf("seeded %d of %d %s contacts", n, sends, tag)
	}

	i = 0
	n, err = p.pool.CopyFrom(ctx,
		pgx.Identifier{"sends"},
		[]string{"workspace_id", "campaign_id", "contact_id", "mailbox_id", "to_email", "status", "sent_at"},
		pgx.CopyFromFunc(func() ([]any, error) {
			if i >= sends {
				return nil, nil
			}
			row := []any{
				ws, campaign, contactIDs[i], mailbox,
				fmt.Sprintf("%s%07d@perf.test", tag, i), "sent",
				base.Add(time.Duration(i) * step),
			}
			i++
			return row, nil
		}))
	if err != nil {
		t.Fatalf("seed %s sends: %v", tag, err)
	}
	if n != int64(sends) {
		t.Fatalf("seeded %d of %d %s sends", n, sends, tag)
	}

	// One enrollment per contact, every `bounceEvery`-th one stopped 'bounced' and
	// the rest still active — so the partial index is selective, as it is in life.
	i = 0
	n, err = p.pool.CopyFrom(ctx,
		pgx.Identifier{"sequence_enrollments"},
		[]string{"workspace_id", "campaign_id", "contact_id", "status", "stop_reason", "stopped_at"},
		pgx.CopyFromFunc(func() ([]any, error) {
			if i >= sends {
				return nil, nil
			}
			at := base.Add(time.Duration(i) * step)
			row := []any{ws, campaign, contactIDs[i], "active", nil, nil}
			if i%bounceEvery == 0 {
				row = []any{ws, campaign, contactIDs[i], "stopped", "bounced", at}
			}
			i++
			return row, nil
		}))
	if err != nil {
		t.Fatalf("seed %s enrollments: %v", tag, err)
	}
	if n != int64(sends) {
		t.Fatalf("seeded %d of %d %s enrollments", n, sends, tag)
	}
	t.Logf("seeded %s: %d sends + %d enrollments (%d bounced) over %d days in %s",
		tag, sends, sends, bounced, perfHistoryDays, time.Since(start).Round(time.Millisecond))
}

// explain runs EXPLAIN (ANALYZE, BUFFERS) over the exact statement the store
// executes and returns the plan text plus its reported execution time.
func explain(t *testing.T, ctx context.Context, p perfFixture, label, sql string, args ...any) (string, time.Duration) {
	t.Helper()
	rows, err := p.pool.Query(ctx, "EXPLAIN (ANALYZE, BUFFERS) "+sql, args...)
	if err != nil {
		t.Fatalf("%s: explain: %v", label, err)
	}
	var b strings.Builder
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			rows.Close()
			t.Fatalf("%s: scan plan: %v", label, err)
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatalf("%s: plan: %v", label, err)
	}
	plan := b.String()
	m := execTimeRe.FindStringSubmatch(plan)
	if m == nil {
		t.Fatalf("%s: no Execution Time in plan:\n%s", label, plan)
	}
	ms, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		t.Fatalf("%s: parsing execution time %q: %v", label, m[1], err)
	}
	took := time.Duration(ms * float64(time.Millisecond))
	t.Logf("\n=== %s: %s ===\n%s", label, took.Round(100*time.Microsecond), plan)
	return plan, took
}

// The three guardrail reads at volume. Each is checked for BOTH a time budget and
// the absence of a sequential scan on a big table: a plan that has quietly
// collapsed into a scan still passes a generous budget on a warm cache, and would
// then fall over on a colder or larger database.
func TestDeliverabilityPerformance(t *testing.T) {
	ctx := context.Background()
	p := setupPerf(t, ctx)

	t.Run("campaign counts (the breaker hot path)", func(t *testing.T) {
		plan, took := explain(t, ctx, p, "GetCampaignDeliverabilityCounts",
			`SELECT
			    (SELECT COUNT(*) FROM sends s
			      WHERE s.workspace_id = $1 AND s.campaign_id = $2
			        AND s.status = 'sent' AND s.sent_at >= $3)::bigint AS delivered,
			    (SELECT COUNT(*) FROM (
			        SELECT e.contact_id FROM sequence_enrollments e
			         WHERE e.workspace_id = $1 AND e.campaign_id = $2
			           AND e.stop_reason = 'bounced' AND e.stopped_at >= $3
			        UNION
			        SELECT s.contact_id FROM deliverability_events d
			          JOIN sends s ON s.id = d.send_id AND s.workspace_id = d.workspace_id
			         WHERE d.workspace_id = $1 AND d.kind = 'bounce'
			           AND d.received_at >= $3 AND s.campaign_id = $2
			    ) b)::bigint AS bounced,
			    (SELECT COUNT(*) FROM deliverability_events d
			       JOIN sends s ON s.id = d.send_id AND s.workspace_id = d.workspace_id
			      WHERE d.workspace_id = $1 AND d.kind = 'complaint'
			        AND d.received_at >= $3 AND s.campaign_id = $2)::bigint AS complained,
			    EXISTS (SELECT 1 FROM deliverability_events d
			             WHERE d.workspace_id = $1 AND d.kind = 'complaint') AS complaint_feed`,
			p.ws, p.campaign, p.windowFrom)

		assertNoSeqScan(t, plan, "sends", "sequence_enrollments")
		assertUsesIndex(t, plan, "idx_enrollments_campaign_bounced")
		assertWithinBudget(t, "GetCampaignDeliverabilityCounts", took)
	})

	t.Run("workspace counts (the dashboard rollup)", func(t *testing.T) {
		plan, took := explain(t, ctx, p, "GetWorkspaceDeliverabilityCounts",
			`SELECT
			    (SELECT COUNT(*) FROM sends s
			      WHERE s.workspace_id = $1 AND s.status = 'sent' AND s.sent_at >= $2)::bigint AS delivered,
			    (SELECT COUNT(*) FROM (
			        SELECT e.contact_id FROM sequence_enrollments e
			         WHERE e.workspace_id = $1
			           AND e.stop_reason = 'bounced' AND e.stopped_at >= $2
			        UNION
			        SELECT s.contact_id FROM deliverability_events d
			          JOIN sends s ON s.id = d.send_id AND s.workspace_id = d.workspace_id
			         WHERE d.workspace_id = $1 AND d.kind = 'bounce' AND d.received_at >= $2
			    ) b)::bigint AS bounced,
			    (SELECT COUNT(*) FROM deliverability_events d
			      WHERE d.workspace_id = $1 AND d.kind = 'complaint'
			        AND d.received_at >= $2)::bigint AS complained,
			    EXISTS (SELECT 1 FROM deliverability_events d
			             WHERE d.workspace_id = $1 AND d.kind = 'complaint') AS complaint_feed`,
			p.ws, p.windowFrom)

		assertNoSeqScan(t, plan, "sends", "sequence_enrollments")
		// No index leads on (workspace_id, stop_reason): the planner reuses the
		// campaign-leading partial index and traverses it on stopped_at, which is
		// cheap because the partial index holds only the bounced rows. See the
		// scaling note in the report -- a (workspace_id, stopped_at) partial index
		// would range-seek instead, and is worth adding when bounce HISTORY (not the
		// window) gets large.
		assertUsesIndex(t, plan, "idx_enrollments_campaign_bounced")
		assertWithinBudget(t, "GetWorkspaceDeliverabilityCounts", took)
	})

	t.Run("per-day series", func(t *testing.T) {
		plan, took := explain(t, ctx, p, "ListDeliverabilitySeries",
			`WITH days AS (
			    SELECT generate_series($2::date, CURRENT_DATE, INTERVAL '1 day')::date AS day
			), sent AS (
			    SELECT (s.sent_at AT TIME ZONE 'UTC')::date AS day, COUNT(*) AS n
			    FROM sends s
			    WHERE s.workspace_id = $1 AND s.status = 'sent'
			      AND s.sent_at >= (($2::date)::timestamp AT TIME ZONE 'UTC')
			    GROUP BY 1
			), bounced AS (
			    SELECT day, COUNT(*) AS n FROM (
			        SELECT (e.stopped_at AT TIME ZONE 'UTC')::date AS day, e.contact_id
			        FROM sequence_enrollments e
			        WHERE e.workspace_id = $1 AND e.stop_reason = 'bounced'
			          AND e.stopped_at >= (($2::date)::timestamp AT TIME ZONE 'UTC')
			        UNION
			        SELECT (ev.received_at AT TIME ZONE 'UTC')::date AS day, s.contact_id
			        FROM deliverability_events ev
			          JOIN sends s ON s.id = ev.send_id AND s.workspace_id = ev.workspace_id
			        WHERE ev.workspace_id = $1 AND ev.kind = 'bounce'
			          AND ev.received_at >= (($2::date)::timestamp AT TIME ZONE 'UTC')
			    ) u GROUP BY day
			), complained AS (
			    SELECT (ev.received_at AT TIME ZONE 'UTC')::date AS day, COUNT(*) AS n
			    FROM deliverability_events ev
			    WHERE ev.workspace_id = $1 AND ev.kind = 'complaint'
			      AND ev.received_at >= (($2::date)::timestamp AT TIME ZONE 'UTC')
			    GROUP BY 1
			), placed AS (
			    SELECT w.day, SUM(w.spam) AS n
			    FROM warmup_daily_stats w
			    WHERE w.workspace_id = $1 AND w.day >= $2::date
			    GROUP BY 1
			)
			SELECT d.day,
			    COALESCE(sent.n, 0)::bigint,
			    COALESCE(bounced.n, 0)::bigint,
			    COALESCE(complained.n, 0)::bigint,
			    COALESCE(placed.n, 0)::bigint
			FROM days d
			LEFT JOIN sent       ON sent.day = d.day
			LEFT JOIN bounced    ON bounced.day = d.day
			LEFT JOIN complained ON complained.day = d.day
			LEFT JOIN placed     ON placed.day = d.day
			ORDER BY d.day`,
			p.ws, p.windowFrom)

		assertNoSeqScan(t, plan, "sends", "sequence_enrollments")
		// The regression this guards is not an index choice but a COST ESTIMATE.
		// generate_series always reports 1000 rows, so the previous per-day
		// correlated-subquery shape costed at 4.3M, crossed jit_above_cost, and
		// PostgreSQL spent 252ms JIT-compiling 47 functions to serve 26ms of work.
		// Aggregating each source once keeps the estimate small enough that no JIT
		// fires at all.
		if strings.Contains(plan, "JIT:") {
			t.Errorf("the series JIT-compiled, so its cost estimate is inflated again:\n%s", plan)
		}
		assertWithinBudget(t, "ListDeliverabilitySeries", took)

		// The hand-written SQL above mirrors queries/deliverability.sql (same
		// approach as app/contact's perf test). Timing the REAL store call next to
		// it is the guard against the two drifting: a large divergence means the
		// plan measured is not the plan shipped.
		start := time.Now()
		points, err := p.store.Series(ctx, p.ws, p.windowFrom)
		shipped := time.Since(start)
		if err != nil {
			t.Fatalf("Series: %v", err)
		}
		t.Logf("store.Series (the shipped query) took %s and returned %d points",
			shipped.Round(time.Millisecond), len(points))
		if len(points) != deliverabilityWindowDays+1 {
			t.Errorf("%d points, want %d", len(points), deliverabilityWindowDays+1)
		}
		assertWithinBudget(t, "store.Series", shipped)
	})

	// The tenant pin has to be inside the plan, not applied after the fact: the
	// other tenant's 20,000 sends must never be read.
	t.Run("cross-tenant rows are not read", func(t *testing.T) {
		counts, err := p.store.WorkspaceCounts(ctx, p.ws, p.windowFrom)
		if err != nil {
			t.Fatalf("WorkspaceCounts: %v", err)
		}
		if counts.Delivered != perfWindowSends {
			t.Errorf("delivered = %d, want exactly this tenant's in-window %d", counts.Delivered, perfWindowSends)
		}
		if counts.Bounced != perfWindowBounced {
			t.Errorf("bounced = %d, want exactly this tenant's in-window %d", counts.Bounced, perfWindowBounced)
		}
		other, err := p.store.WorkspaceCounts(ctx, p.other, p.windowFrom)
		if err != nil {
			t.Fatalf("WorkspaceCounts(other): %v", err)
		}
		if want := perfOtherTenant * deliverabilityWindowDays / perfHistoryDays; other.Delivered != want {
			t.Errorf("other tenant delivered = %d, want %d", other.Delivered, want)
		}
	})

	// The end-to-end read an operator's page actually performs, at volume.
	t.Run("full breaker evaluation", func(t *testing.T) {
		// The service's clock is pinned to the SAME instant the fixture seeded
		// against. Seeding 220,000 rows takes ~35s of wall clock, so a service using
		// time.Now() would evaluate a window that has drifted ~35s past the one the
		// data was laid out for — which moves the window boundary across rows and
		// makes both the sample and the rate depend on how fast the machine seeded.
		// This subtest was observed failing once and passing twice for exactly that
		// reason before the clock was pinned.
		svc := NewService(p.store)
		svc.now = func() time.Time { return p.now }

		start := time.Now()
		out, err := svc.EvaluateBreaker(ctx, p.ws, p.campaign)
		took := time.Since(start)
		if err != nil {
			t.Fatalf("EvaluateBreaker: %v", err)
		}
		t.Logf("EvaluateBreaker over %d in-window delivered / %d bounced took %s",
			out.Verdict.Delivered, perfWindowBounced, took.Round(time.Millisecond))
		// 10% bounce over 25k in-window delivered: the case the breaker exists for.
		if !out.Paused {
			t.Errorf("a 10%% bounce rate over %d delivered did not pause (verdict %+v)",
				out.Verdict.Delivered, out.Verdict)
		}
		if out.Verdict.Delivered != perfWindowSends {
			t.Errorf("judged on %d delivered, want exactly %d — the window has drifted",
				out.Verdict.Delivered, perfWindowSends)
		}
		assertWithinBudget(t, "EvaluateBreaker", took)
	})
}

func assertWithinBudget(t *testing.T, label string, took time.Duration) {
	t.Helper()
	if took > perfBudget {
		t.Errorf("%s took %s, over the %s budget", label, took.Round(time.Millisecond), perfBudget)
	}
}

// assertNoSeqScan fails if the plan sequentially scans one of the named tables.
// It is the assertion that survives a warm cache: a Seq Scan on 200,000 rows can
// still come in under a generous budget in memory and then fall over in production.
func assertNoSeqScan(t *testing.T, plan string, tables ...string) {
	t.Helper()
	for _, table := range tables {
		if strings.Contains(plan, "Seq Scan on "+table) {
			t.Errorf("plan sequentially scans %s:\n%s", table, plan)
		}
	}
}

func assertUsesIndex(t *testing.T, plan, index string) {
	t.Helper()
	if !strings.Contains(plan, index) {
		t.Errorf("plan does not use %s:\n%s", index, plan)
	}
}
