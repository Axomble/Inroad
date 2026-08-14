//go:build integration

package inprocess

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/inroad/inroad/internal/platform/db/gen"
)

// countingDBTX wraps a gen.DBTX and tallies how many times a named statement runs.
//
// gen.New takes the DBTX interface, so a decorator is enough to observe query VOLUME
// without touching production code or reaching for pg_stat_statements.
type countingDBTX struct {
	inner gen.DBTX
	match string
	calls atomic.Int64
}

func (c *countingDBTX) count(sql string) {
	if strings.Contains(sql, c.match) {
		c.calls.Add(1)
	}
}

func (c *countingDBTX) Exec(ctx context.Context, sql string, args ...interface{}) (pgconn.CommandTag, error) {
	c.count(sql)
	return c.inner.Exec(ctx, sql, args...)
}

func (c *countingDBTX) Query(ctx context.Context, sql string, args ...interface{}) (pgx.Rows, error) {
	c.count(sql)
	return c.inner.Query(ctx, sql, args...)
}

func (c *countingDBTX) QueryRow(ctx context.Context, sql string, args ...interface{}) pgx.Row {
	c.count(sql)
	return c.inner.QueryRow(ctx, sql, args...)
}

func (c *countingDBTX) SendBatch(ctx context.Context, b *pgx.Batch) pgx.BatchResults {
	return c.inner.SendBatch(ctx, b)
}

// The domain half of the campaign gate used to be three correlated SQL LATERALs —
// one per outer row. Moving the grouping into Go replaced that with ONE read plus an
// in-memory fold, and the map is passed into the loop rather than rebuilt inside it.
//
// Nothing guarded that. Every existing test hands eligibleCandidates a precomputed
// DomainLanes, so a regression that moved the read back inside the per-row loop would
// pass the entire suite while reintroducing a query per candidate on the hottest read
// in the send path. This test observes the query VOLUME, which is the property that
// actually matters and the one no assertion on the result can see.
func TestDomainLanesAreReadOncePerAssignmentNotPerPoolRow(t *testing.T) {
	ctx, f := setupPool(t)
	f.addSender(t, ctx, f.mailboxA, 1, true)
	f.addSender(t, ctx, f.mailboxB, 1, true)
	enrollmentID := f.enroll(t, ctx)

	impl, ok := f.core.(client)
	if !ok {
		t.Fatalf("New returned %T, want inprocess.client — cannot observe query volume", f.core)
	}
	counter := &countingDBTX{inner: f.pool, // Unique to ListWorkspaceWarmupLanes. "FROM warmup_participants wp" is NOT — a
		// health subquery in the same file shares that alias, and matching it counted two
		// unrelated statements and looked exactly like an N+1.
		match: "SELECT m.email, wp.lane"}
	impl.q = gen.New(counter)

	if _, err := impl.GetStepSendJob(ctx, enrollmentID.String(), f.ws.String()); err != nil {
		t.Fatalf("GetStepSendJob: %v", err)
	}

	if got := counter.calls.Load(); got != 1 {
		t.Fatalf("the domain-lane read ran %d times for a 2-mailbox pool, want exactly 1 — "+
			"it must be read once per assignment and folded in memory, not once per candidate", got)
	}
}
