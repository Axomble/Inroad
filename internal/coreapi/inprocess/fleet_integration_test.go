//go:build integration

package inprocess

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/platform/db/gen"
	"github.com/inroad/inroad/internal/platform/fleetdecision"
)

// These exercise the fleet signal and decision-log writes against Postgres.
// Everything else covering them uses a fake recorder, which can only prove the
// worker CALLS the seam — never that the SQL behind it does what the seam
// promises. Docker must be up.

// signalRow is one persisted verdict, read back with raw SQL because the write
// path deliberately has no read query: nothing in the application reads these
// back yet (the placement scorer that will is not built), and adding an
// unused query to satisfy a test would be the test dictating the schema.
type signalRow struct {
	provider  string
	operation string
	reason    string
	events    int64
}

func readSignals(t *testing.T, ctx context.Context, c client, workerID string) []signalRow {
	t.Helper()
	rows, err := c.pool.Query(ctx,
		`SELECT provider, operation, reason, events
		   FROM worker_provider_signals
		  WHERE worker_id = $1
		  ORDER BY provider, operation, reason`, workerID)
	if err != nil {
		t.Fatalf("read signals: %v", err)
	}
	defer rows.Close()

	var out []signalRow
	for rows.Next() {
		var r signalRow
		if err := rows.Scan(&r.provider, &r.operation, &r.reason, &r.events); err != nil {
			t.Fatalf("scan signal: %v", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate signals: %v", err)
	}
	return out
}

// TestRecordWorkerProviderSignalsKeepsEachDimensionInItsOwnColumn is the test
// this write most needs, and the one a fake recorder structurally cannot
// provide.
//
// The query passes the three varying dimensions as PARALLEL ARRAYS that
// Postgres unnests back into rows in lockstep. Transposing two of them — reasons
// into the operation column, say — produces a write that succeeds, returns no
// error, and is wrong. Every value below is therefore unique ACROSS dimensions,
// not merely within one: if any pair were swapped, at least one row would carry
// a value no column of that name could legitimately hold.
func TestRecordWorkerProviderSignalsKeepsEachDimensionInItsOwnColumn(t *testing.T) {
	ctx := context.Background()
	pool, q := claimConnect(t)
	c := routingClient(pool, q)

	workerID := "sig-columns-" + uuid.NewString()
	start := time.Now().UTC().Add(-time.Minute)
	end := start.Add(time.Minute)

	if err := c.RecordWorkerProviderSignals(ctx, coreapi.WorkerProviderSignals{
		WorkerID:    workerID,
		WindowStart: start,
		WindowEnd:   end,
		Counts: []coreapi.WorkerProviderSignalCount{
			{Provider: "smtp", Operation: "send", Reason: "rate_limited", Events: 3},
			{Provider: "gmail", Operation: "poll", Reason: "auth_failed", Events: 7},
		},
	}); err != nil {
		t.Fatalf("record signals: %v", err)
	}

	got := readSignals(t, ctx, c, workerID)
	if len(got) != 2 {
		t.Fatalf("persisted %d rows, want 2: %+v", len(got), got)
	}

	// Ordered by (provider, operation, reason): "gmail" sorts before "smtp".
	want := []signalRow{
		{provider: "gmail", operation: "poll", reason: "auth_failed", events: 7},
		{provider: "smtp", operation: "send", reason: "rate_limited", events: 3},
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("row %d = %+v, want %+v — a mismatch here means the parallel "+
				"arrays were unnested out of step, so a verdict is filed under the "+
				"wrong dimension", i, got[i], w)
		}
	}
}

// TestRecordWorkerProviderSignalsSumsRepeatedWindowsRatherThanCollapsingThem
// pins the deliberate absence of conflict handling. Two flushes carrying the
// same (worker, provider, operation, reason) are two windows' worth of events
// and must BOTH survive: an upsert here would silently discard whichever
// arrived second, and the counter it discarded is exactly the evidence that a
// worker is degrading fast.
func TestRecordWorkerProviderSignalsSumsRepeatedWindowsRatherThanCollapsingThem(t *testing.T) {
	ctx := context.Background()
	pool, q := claimConnect(t)
	c := routingClient(pool, q)

	workerID := "sig-sum-" + uuid.NewString()
	window := coreapi.WorkerProviderSignals{
		WorkerID:    workerID,
		WindowStart: time.Now().UTC().Add(-time.Minute),
		WindowEnd:   time.Now().UTC(),
		Counts: []coreapi.WorkerProviderSignalCount{
			{Provider: "smtp", Operation: "send", Reason: "ok", Events: 5},
		},
	}
	for i := 0; i < 2; i++ {
		if err := c.RecordWorkerProviderSignals(ctx, window); err != nil {
			t.Fatalf("record window %d: %v", i, err)
		}
	}

	got := readSignals(t, ctx, c, workerID)
	if len(got) != 2 {
		t.Fatalf("persisted %d rows, want 2 — the second window must not collapse "+
			"into the first: %+v", len(got), got)
	}
	var total int64
	for _, r := range got {
		total += r.events
	}
	if total != 10 {
		t.Errorf("summed events = %d, want 10", total)
	}
}

// TestRecordWorkerProviderSignalsIgnoresEmptyAndNonPositiveCounts covers the two
// branches that must never reach Postgres. An empty window is the normal state
// of a healthy idle worker and must not be an error; a non-positive delta
// violates the table's CHECK and would fail the WHOLE window's insert, taking
// every legitimate counter beside it down over a row carrying no information.
func TestRecordWorkerProviderSignalsIgnoresEmptyAndNonPositiveCounts(t *testing.T) {
	ctx := context.Background()
	pool, q := claimConnect(t)
	c := routingClient(pool, q)

	workerID := "sig-empty-" + uuid.NewString()
	start := time.Now().UTC().Add(-time.Minute)
	end := time.Now().UTC()

	if err := c.RecordWorkerProviderSignals(ctx, coreapi.WorkerProviderSignals{
		WorkerID: workerID, WindowStart: start, WindowEnd: end,
	}); err != nil {
		t.Fatalf("empty window must be a no-op, got: %v", err)
	}

	// A zero and a negative alongside one good count: the good one survives and
	// the write does not fail.
	if err := c.RecordWorkerProviderSignals(ctx, coreapi.WorkerProviderSignals{
		WorkerID: workerID, WindowStart: start, WindowEnd: end,
		Counts: []coreapi.WorkerProviderSignalCount{
			{Provider: "smtp", Operation: "send", Reason: "ok", Events: 0},
			{Provider: "gmail", Operation: "send", Reason: "ok", Events: -4},
			{Provider: "m365", Operation: "send", Reason: "ok", Events: 2},
		},
	}); err != nil {
		t.Fatalf("record with dropped counts: %v", err)
	}

	got := readSignals(t, ctx, c, workerID)
	if len(got) != 1 {
		t.Fatalf("persisted %d rows, want only the positive one: %+v", len(got), got)
	}
	if got[0].provider != "m365" || got[0].events != 2 {
		t.Errorf("surviving row = %+v, want m365 with 2 events", got[0])
	}
}

// TestRecordWorkerProviderSignalsRejectsABackwardsWindow: window_end before
// window_start is a caller bug, and a window that ends before it begins makes
// every SUM over a time range silently wrong rather than loudly broken.
func TestRecordWorkerProviderSignalsRejectsABackwardsWindow(t *testing.T) {
	ctx := context.Background()
	pool, q := claimConnect(t)
	c := routingClient(pool, q)

	now := time.Now().UTC()
	err := c.RecordWorkerProviderSignals(ctx, coreapi.WorkerProviderSignals{
		WorkerID:    "sig-backwards-" + uuid.NewString(),
		WindowStart: now,
		WindowEnd:   now.Add(-time.Minute),
		Counts: []coreapi.WorkerProviderSignalCount{
			{Provider: "smtp", Operation: "send", Reason: "ok", Events: 1},
		},
	})
	if err == nil {
		t.Fatal("a window ending before it starts must be rejected")
	}
}

// TestRecordFleetDecisionRoundTripsAndIsWorkspacePinned covers the decision log
// end to end: a written decision is readable by the question the table exists to
// answer ("why is this mailbox on this worker?"), and is invisible to another
// tenant asking the same question about the same mailbox id.
func TestRecordFleetDecisionRoundTripsAndIsWorkspacePinned(t *testing.T) {
	ctx := context.Background()
	pool, q := claimConnect(t)
	c := routingClient(pool, q)

	ws, err := q.CreateWorkspace(ctx, "Fleet decisions "+uuid.NewString())
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	foreign, err := q.CreateWorkspace(ctx, "Fleet decisions foreign "+uuid.NewString())
	if err != nil {
		t.Fatalf("foreign workspace: %v", err)
	}
	mb := createRoutingMailbox(t, ctx, q, ws.ID)

	reason := fleetdecision.Forced("worker stopped heartbeating")
	if err := c.RecordFleetDecision(ctx, fleetdecision.Entry{
		Kind:        fleetdecision.KindRotate,
		WorkerID:    "decision-worker",
		MailboxID:   mb.String(),
		WorkspaceID: ws.ID.String(),
		Reason:      reason,
		TriggeredBy: fleetdecision.Auto(fleetdecision.KindRotate),
	}); err != nil {
		t.Fatalf("record decision: %v", err)
	}

	got, err := q.ListFleetDecisionsForMailbox(ctx, gen.ListFleetDecisionsForMailboxParams{
		MailboxID: mb, WorkspaceID: ws.ID, RowLimit: 10,
	})
	if err != nil {
		t.Fatalf("list decisions: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("owner read %d decisions, want 1", len(got))
	}
	if got[0].Reason != reason.String() {
		t.Errorf("reason = %q, want %q", got[0].Reason, reason.String())
	}
	if got[0].TriggeredBy != "auto:rotate" {
		t.Errorf("triggered_by = %q, want auto:rotate", got[0].TriggeredBy)
	}

	// The tenant pin: same mailbox id, different workspace, zero rows.
	foreignRead, err := q.ListFleetDecisionsForMailbox(ctx, gen.ListFleetDecisionsForMailboxParams{
		MailboxID: mb, WorkspaceID: foreign.ID, RowLimit: 10,
	})
	if err != nil {
		t.Fatalf("foreign list: %v", err)
	}
	if len(foreignRead) != 0 {
		t.Errorf("a foreign workspace read %d decisions about another tenant's "+
			"mailbox, want 0", len(foreignRead))
	}
}

// TestRecordFleetDecisionRejectsACrossTenantPair: the composite FK makes a
// (mailbox, workspace) pair from different tenants unrepresentable rather than
// merely unread. Writing one must fail loudly — a decision row naming another
// tenant's mailbox is worse than a lost decision.
func TestRecordFleetDecisionRejectsACrossTenantPair(t *testing.T) {
	ctx := context.Background()
	pool, q := claimConnect(t)
	c := routingClient(pool, q)

	ws, err := q.CreateWorkspace(ctx, "Fleet pair owner "+uuid.NewString())
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	foreign, err := q.CreateWorkspace(ctx, "Fleet pair foreign "+uuid.NewString())
	if err != nil {
		t.Fatalf("foreign workspace: %v", err)
	}
	mb := createRoutingMailbox(t, ctx, q, ws.ID)

	err = c.RecordFleetDecision(ctx, fleetdecision.Entry{
		Kind:        fleetdecision.KindAssign,
		WorkerID:    "decision-worker",
		MailboxID:   mb.String(),
		WorkspaceID: foreign.ID.String(), // owner's mailbox, foreign workspace
		Reason:      fleetdecision.Forced("placement"),
		TriggeredBy: fleetdecision.Auto(fleetdecision.KindAssign),
	})
	if err == nil {
		t.Fatal("a decision pairing one tenant's mailbox with another's workspace must be refused")
	}
}
