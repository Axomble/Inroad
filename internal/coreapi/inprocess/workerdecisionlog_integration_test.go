//go:build integration

package inprocess

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/platform/db/gen"
	"github.com/inroad/inroad/internal/platform/warmup"
)

// These integration tests cover the fleet decision log as placement WRITES it
// (#203 built the table and the typed writer; fleet F4 wired them in). The rule
// they exist to hold is the one fleetdecision's package doc calls out: AN ENTRY
// MUST NEVER PRINT A SCORE COMPARISON IT DID NOT ACTUALLY MAKE. A log that
// overstates its own reasoning is worse than no log, because an operator then
// tunes a threshold against a number nothing computed.
//
// They also pin the other half of that rule, which is about volume rather than
// prose: an entry is written when something was DECIDED, so a retained incumbent
// and a fleet-wide outage write nothing at all. Docker must be up; the fixtures
// live in workerrouting_integration_test.go and
// workerplacement_integration_test.go.

// decisionsFor reads the decision log for one mailbox, newest first.
func decisionsFor(t *testing.T, ctx context.Context, q *gen.Queries, mb, ws uuid.UUID) []gen.FleetDecision {
	t.Helper()
	got, err := q.ListFleetDecisionsForMailbox(ctx, gen.ListFleetDecisionsForMailboxParams{
		MailboxID: mb, WorkspaceID: ws, RowLimit: 20,
	})
	if err != nil {
		t.Fatalf("list decisions: %v", err)
	}
	return got
}

// TestPlacementRecordsAContestedChoiceAndNothingForARetainedIncumbent covers the
// decision log's two most important shapes at once, because they are two sides
// of one rule: an entry is written when something was DECIDED.
//
// A contested placement reports the comparison it actually made. Re-resolving
// the same mailbox writes nothing at all — the incumbent was retained without
// scoring, the entry explaining how it got there is already in the log, and
// re-stating it on every warmup tick would bury every decision that changed
// something under thousands of identical rows.
func TestPlacementRecordsAContestedChoiceAndNothingForARetainedIncumbent(t *testing.T) {
	ctx := context.Background()
	pool, q := claimConnect(t)
	c := routingClient(pool, q)
	resetRouting(t, ctx, pool)

	ws, err := q.CreateWorkspace(ctx, "Routing decision-contested "+uuid.NewString())
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	for _, w := range []string{"dc-a", "dc-b"} {
		if err := c.UpsertWorkerHeartbeat(ctx, w, "203.0.113.91", "hostname"); err != nil {
			t.Fatalf("heartbeat %s: %v", w, err)
		}
	}
	seedAssignments(t, ctx, q, ws.ID, "dc-a", warmup.RiskBandHealthy, "smtp", 5)

	mb := createRoutingMailbox(t, ctx, q, ws.ID)
	placed, err := c.AssignMailboxWorker(ctx, mb.String(), ws.ID.String())
	if err != nil {
		t.Fatalf("assign: %v", err)
	}

	entries := decisionsFor(t, ctx, q, mb, ws.ID)
	if len(entries) != 1 {
		t.Fatalf("recorded %d decisions for one placement, want 1: %+v", len(entries), entries)
	}
	entry := entries[0]
	if entry.Kind != "assign" {
		t.Errorf("kind = %q, want assign", entry.Kind)
	}
	if entry.WorkerID == nil || "w:"+*entry.WorkerID != placed {
		t.Errorf("entry names worker %v but the mailbox was placed on %q", entry.WorkerID, placed)
	}
	if entry.TriggeredBy != "auto:assign" {
		t.Errorf("triggered_by = %q, want auto:assign", entry.TriggeredBy)
	}
	// Two eligible workers were genuinely compared, so the comparison is real.
	if !strings.Contains(entry.Reason, " over ") || !strings.Contains(entry.Reason, "2 candidates") {
		t.Errorf("reason = %q, want a comparison of both candidates", entry.Reason)
	}

	// Re-resolving retains the incumbent and decides nothing, so nothing is
	// appended.
	for i := range 3 {
		if again, err := c.AssignMailboxWorker(ctx, mb.String(), ws.ID.String()); err != nil || again != placed {
			t.Fatalf("re-resolve %d = %q err=%v, want stable %q", i, again, err, placed)
		}
	}
	if after := decisionsFor(t, ctx, q, mb, ws.ID); len(after) != 1 {
		t.Fatalf("retaining an incumbent appended %d more decisions, want none: %+v", len(after)-1, after)
	}
}

// TestPlacementRecordsAForcedReasonWithNoScoreOnTheSelfHostPath: the self-host
// bypass compares nothing, so its entry must say so and print no number. A log
// that overstates its own reasoning is worse than no log — an operator tunes a
// threshold against a score nothing computed.
func TestPlacementRecordsAForcedReasonWithNoScoreOnTheSelfHostPath(t *testing.T) {
	ctx := context.Background()
	pool, q := claimConnect(t)
	c := routingClient(pool, q)
	resetRouting(t, ctx, pool)

	ws, err := q.CreateWorkspace(ctx, "Routing decision-selfhost "+uuid.NewString())
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	if err := c.UpsertWorkerHeartbeat(ctx, "ds-solo", "203.0.113.92", "hostname"); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}

	mb := createRoutingMailbox(t, ctx, q, ws.ID)
	if _, err := c.AssignMailboxWorker(ctx, mb.String(), ws.ID.String()); err != nil {
		t.Fatalf("assign: %v", err)
	}

	entries := decisionsFor(t, ctx, q, mb, ws.ID)
	if len(entries) != 1 {
		t.Fatalf("recorded %d decisions, want 1: %+v", len(entries), entries)
	}
	entry := entries[0]
	if entry.WorkerID == nil || *entry.WorkerID != "ds-solo" {
		t.Errorf("entry names worker %v, want ds-solo", entry.WorkerID)
	}
	if !strings.HasPrefix(entry.Reason, "forced:") {
		t.Errorf("reason = %q, want a forced reason — nothing was scored", entry.Reason)
	}
	for _, forbidden := range []string{" over ", "score", "candidate"} {
		if strings.Contains(entry.Reason, forbidden) {
			t.Errorf("reason = %q contains %q, which claims a comparison the self-host path never made",
				entry.Reason, forbidden)
		}
	}
	if strings.ContainsAny(entry.Reason, "0123456789") {
		t.Errorf("reason = %q prints a number, and nothing numeric was computed", entry.Reason)
	}
}

// TestPlacementNeverRecordsForAMailboxItDidNotPlace: with the fleet down the
// caller still sends (via the shared default queue) and nothing is persisted, so
// there is no decision to explain. This branch runs for EVERY mailbox on EVERY
// tick for as long as the outage lasts, which is exactly the shape that turns a
// decision log into noise.
func TestPlacementNeverRecordsForAMailboxItDidNotPlace(t *testing.T) {
	ctx := context.Background()
	pool, q := claimConnect(t)
	c := routingClient(pool, q)
	resetRouting(t, ctx, pool)

	ws, err := q.CreateWorkspace(ctx, "Routing decision-fleetdown "+uuid.NewString())
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	mb := createRoutingMailbox(t, ctx, q, ws.ID)

	if got, err := c.AssignMailboxWorker(ctx, mb.String(), ws.ID.String()); err != nil || got != "" {
		t.Fatalf("assign with no live worker = %q err=%v, want the default queue", got, err)
	}
	if entries := decisionsFor(t, ctx, q, mb, ws.ID); len(entries) != 0 {
		t.Fatalf("recorded %d decisions for a placement that did not happen: %+v", len(entries), entries)
	}
}
