//go:build integration

package db_test

import (
	"context"
	_ "embed"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inroad/inroad/internal/platform/db"
	"github.com/inroad/inroad/internal/platform/db/dbtest"
)

//go:embed queries/retention.sql
var retentionQueries string

// sqlcArg matches sqlc's named-parameter syntax so the query text can be
// PREPAREd as Postgres sees it: each distinct name becomes $1, $2, … in order of
// first appearance, which is exactly what sqlc generates.
var sqlcArg = regexp.MustCompile(`sqlc\.arg\((\w+)\)`)

// retentionQuery returns one named query from queries/retention.sql with its
// sqlc parameters rewritten to positional ones, and the parameter names in order.
func retentionQuery(t *testing.T, name string) (string, []string) {
	t.Helper()
	start := strings.Index(retentionQueries, "-- name: "+name+" ")
	if start < 0 {
		t.Fatalf("query %s not found in retention.sql", name)
	}
	body := retentionQueries[start:]
	if next := strings.Index(body[1:], "-- name: "); next >= 0 {
		body = body[:next+1]
	}
	var params []string
	index := map[string]int{}
	body = sqlcArg.ReplaceAllStringFunc(body, func(m string) string {
		p := sqlcArg.FindStringSubmatch(m)[1]
		if _, ok := index[p]; !ok {
			params = append(params, p)
			index[p] = len(params)
		}
		return fmt.Sprintf("$%d", index[p])
	})
	return strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(body), ";")), params
}

// The sweep's guarded batches must stay bounded however the planner feels, and
// the way they could stop being bounded is a HASH anti-join: pull a guard's
// NOT EXISTS up into a join and the plan reads the whole guard table (every
// inbox thread, every enrollment, every deal) once per batch. The queries write
// each guard as NOT EXISTS ... OFFSET 0 to forbid that, and this pins it — on a
// GENERIC plan, the one a long-lived prepared statement ends up on, over enough
// data that the planner would otherwise choose the hash.
func TestRetentionBatchPlansProbeGuardsPerCandidate(t *testing.T) {
	ctx := context.Background()
	dsn := dbtest.DSN(t)
	if err := db.Migrate(dsn); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close) // registered first so it runs LAST, after the workspace delete
	ws := seedRetentionVolume(t, ctx, pool)
	t.Cleanup(func() {
		if _, err := pool.Exec(context.Background(), `DELETE FROM workspaces WHERE id = $1`, ws); err != nil {
			t.Errorf("cleanup: %v", err)
		}
	})

	for _, name := range []string{"PurgeSends", "PurgeInboxThreads", "PurgeDeliverabilityEvents"} {
		t.Run(name, func(t *testing.T) {
			plan := genericPlan(t, ctx, pool, name)
			t.Logf("generic plan for %s:\n%s", name, plan)
			if strings.Contains(plan, "Anti Join") {
				t.Errorf("%s's guards were pulled up into an anti-join; each must stay a per-candidate SubPlan (NOT EXISTS ... OFFSET 0)", name)
			}
			if !strings.Contains(plan, "SubPlan") {
				t.Errorf("%s has no SubPlan: the guards are not being evaluated per candidate", name)
			}
			if !strings.Contains(plan, "Limit") {
				t.Errorf("%s has no Limit on its candidate scan", name)
			}
		})
	}
}

// genericPlan PREPAREs the query and EXPLAINs it under force_generic_plan, on one
// connection so the session settings apply. EXPLAIN without ANALYZE does not run
// the DELETEs.
func genericPlan(t *testing.T, ctx context.Context, pool *pgxpool.Pool, name string) string {
	t.Helper()
	sql, params := retentionQuery(t, name)
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer conn.Release()
	stmt := "plan_" + strings.ToLower(name)
	for _, s := range []string{
		"SET plan_cache_mode = force_generic_plan",
		"DEALLOCATE ALL",
		"PREPARE " + stmt + " AS " + sql,
	} {
		if _, err := conn.Exec(ctx, s); err != nil {
			t.Fatalf("%s: %v", strings.SplitN(s, " AS ", 2)[0], err)
		}
	}
	args := make([]string, len(params))
	for i, p := range params {
		switch p {
		case "older_than_seconds":
			args[i] = "34560000" // 400 days
		case "after_at":
			args[i] = "'0001-01-01T00:00:00Z'"
		case "after_id":
			args[i] = "'" + uuid.Nil.String() + "'"
		case "batch_limit":
			args[i] = "20000"
		default:
			t.Fatalf("unexpected parameter %q in %s", p, name)
		}
	}
	rows, err := conn.Query(ctx, "EXPLAIN (COSTS OFF) EXECUTE "+stmt+"("+strings.Join(args, ", ")+")")
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	defer rows.Close()
	var lines []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatalf("scan plan: %v", err)
		}
		lines = append(lines, line)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("plan: %v", err)
	}
	return strings.Join(lines, "\n")
}

// seedRetentionVolume gives the planner a guard table worth hash-joining: tens of
// thousands of old sends, threads and enrollments in one workspace, then ANALYZE
// so the statistics reflect them.
func seedRetentionVolume(t *testing.T, ctx context.Context, pool *pgxpool.Pool) uuid.UUID {
	t.Helper()
	var ws uuid.UUID
	steps := []string{
		`INSERT INTO workspaces (name) VALUES ('retention plan ' || gen_random_uuid()) RETURNING id`,
	}
	if err := pool.QueryRow(ctx, steps[0]).Scan(&ws); err != nil {
		t.Fatalf("workspace: %v", err)
	}
	for _, s := range []string{
		`INSERT INTO mailboxes (workspace_id, email, secret_ciphertext) VALUES ($1, 'plan@x.test', 'x')`,
		`INSERT INTO lists (workspace_id, name) VALUES ($1, 'L')`,
		`INSERT INTO campaigns (workspace_id, name, mailbox_id, list_id, subject, status)
		 SELECT $1, 'C', m.id, l.id, 'S', 'done' FROM mailboxes m, lists l WHERE m.workspace_id = $1 AND l.workspace_id = $1`,
		`INSERT INTO contacts (workspace_id, email) SELECT $1, 'p' || g || '@x.test' FROM generate_series(1, 30000) g`,
		`INSERT INTO sends (workspace_id, campaign_id, contact_id, mailbox_id, to_email, status, created_at, sent_at, step_order)
		 SELECT $1, c.id, ct.id, c.mailbox_id, ct.email, 'sent', now() - interval '500 days', now() - interval '500 days', 1
		 FROM campaigns c JOIN contacts ct ON ct.workspace_id = c.workspace_id WHERE c.workspace_id = $1`,
		`INSERT INTO sequence_enrollments (workspace_id, campaign_id, contact_id, status)
		 SELECT $1, c.id, ct.id, 'completed' FROM campaigns c JOIN contacts ct ON ct.workspace_id = c.workspace_id WHERE c.workspace_id = $1`,
		`INSERT INTO inbox_threads (workspace_id, mailbox_id, campaign_id, contact_id, last_message_at)
		 SELECT $1, c.mailbox_id, c.id, ct.id, now() - interval '500 days'
		 FROM campaigns c JOIN contacts ct ON ct.workspace_id = c.workspace_id WHERE c.workspace_id = $1`,
		`INSERT INTO deliverability_events (workspace_id, kind, email, provider_event_id, received_at)
		 SELECT $1, 'bounce', 'p' || g || '@x.test', 'plan-' || g || '-' || gen_random_uuid(), now() - interval '500 days'
		 FROM generate_series(1, 30000) g`,
	} {
		if _, err := pool.Exec(ctx, s, ws); err != nil {
			t.Fatalf("seed %q: %v", s[:40], err)
		}
	}
	for _, table := range []string{"sends", "sequence_enrollments", "inbox_threads", "deliverability_events", "deals", "inbox_pending_replies", "inbox_thread_snoozes", "inbox_messages", "campaigns"} {
		if _, err := pool.Exec(ctx, "ANALYZE "+table); err != nil {
			t.Fatalf("analyze %s: %v", table, err)
		}
	}
	return ws
}
