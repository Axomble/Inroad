//go:build integration

package inprocess

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hibiken/asynq"

	"github.com/inroad/inroad/internal/app/inbox"
	"github.com/inroad/inroad/internal/coreapi"
	workerinbox "github.com/inroad/inroad/internal/worker/inbox"
)

// The stranded-manual-send sweep, against real Postgres.
//
// What is being proved here is a PAIR, and only the pair means anything:
//
//   - a row under a LIVE lease is left alone, because another worker may be
//     mid-dial and the lease is the only thing standing between two workers and
//     one duplicate reply;
//   - a row past its lease IS rescued, and exactly one send results.
//
// Either half alone is trivially satisfiable by a broken implementation — a
// sweep that always does nothing passes the first, and a sweep with no lease
// check at all passes the second. Both are asserted against the same fixture.
//
// The scan is CROSS-TENANT and this suite shares one database under `go test
// -p 4`, so nothing here asserts on the total candidate count: every assertion
// asks whether THIS row is in the answer. A test that pinned the total would
// fail whenever another package happened to leave a pending row behind, which
// is a flake, not a finding.

// sweepWindow is deliberately tighter than DefaultPendingSweepWindow so a test
// does not have to age a row by ten real minutes. The LEASE is not shortened by
// it — the control plane adds inbox.PendingReplyLeaseSeconds itself, which is
// exactly the property the live-lease case below exercises.
var sweepWindow = coreapi.StrandedPendingWindow{
	OverdueAfter: time.Minute,
	LeaseGrace:   time.Minute,
}

// strandedIDs runs the real scan and returns the ids it nominated, so a test can
// ask "is my row in there" without caring what else the shared database holds.
func strandedIDs(t *testing.T, ctx context.Context, core coreapi.Client, w coreapi.StrandedPendingWindow) map[string]string {
	t.Helper()
	psc, ok := core.(workerinbox.PendingSweepCore)
	if !ok {
		t.Fatalf("the coreapi client (%T) does not satisfy worker/inbox.PendingSweepCore — the "+
			"sweep is registered by TYPE ASSERTION, so a signature drift silently unregisters it", core)
	}
	rows, err := psc.ListStrandedPendingInboxSends(ctx, w)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	out := make(map[string]string, len(rows))
	for _, r := range rows {
		out[r.Kind+":"+r.ID] = r.Status
	}
	return out
}

// sweepRescues drives the REAL sweep handler over the real scan and reports
// whether it enqueued a rescue for this row. The enqueuer is a fake — Redis is
// not what is under test — but the handler, the coreapi call and the SQL are
// all real.
func sweepRescues(t *testing.T, ctx context.Context, core coreapi.Client, kind, id string) bool {
	t.Helper()
	psc, ok := core.(workerinbox.PendingSweepCore)
	if !ok {
		t.Fatalf("the coreapi client (%T) does not satisfy worker/inbox.PendingSweepCore", core)
	}
	enq := &capturingRescueEnqueuer{}
	h := workerinbox.PendingSweepHandler(psc, enq, sweepWindow, nil)
	if err := h(ctx, asynq.NewTask("inbox:pending_send_sweep", nil)); err != nil {
		t.Fatalf("sweep handler: %v", err)
	}
	return enq.seen[kind+":"+id]
}

type capturingRescueEnqueuer struct{ seen map[string]bool }

func (c *capturingRescueEnqueuer) EnqueueStrandedPendingInboxReply(_ context.Context, pendingID, _ string) error {
	c.record(coreapi.StrandedPendingKindReply, pendingID)
	return nil
}

func (c *capturingRescueEnqueuer) EnqueueStrandedPendingInboxCompose(_ context.Context, pendingID, _ string) error {
	c.record(coreapi.StrandedPendingKindCompose, pendingID)
	return nil
}

func (c *capturingRescueEnqueuer) record(kind, id string) {
	if c.seen == nil {
		c.seen = map[string]bool{}
	}
	c.seen[kind+":"+id] = true
}

// ageClaim backdates a claimed row's lease by d, so the reclaim window can be
// reached without waiting d of real time. Distinct from expireLease (which jumps
// a whole hour) because these tests need to land either side of the lease
// boundary deliberately.
func ageClaim(t *testing.T, ctx context.Context, f poolFixture, table string, id uuid.UUID, d time.Duration) {
	t.Helper()
	// The table name is one of two literals chosen by the caller in this file,
	// never a value, so the interpolation cannot carry input.
	if _, err := f.pool.Exec(ctx,
		`UPDATE `+table+` SET claimed_at = now() - make_interval(secs => $3) WHERE id = $1 AND workspace_id = $2`,
		id, f.ws, d.Seconds()); err != nil {
		t.Fatalf("age the claim on %s: %v", table, err)
	}
}

// ageSendAfter backdates a scheduled row so it is overdue by d.
func ageSendAfter(t *testing.T, ctx context.Context, f poolFixture, id uuid.UUID, d time.Duration) {
	t.Helper()
	if _, err := f.pool.Exec(ctx,
		`UPDATE inbox_pending_replies SET send_after = now() - make_interval(secs => $3)
		 WHERE id = $1 AND workspace_id = $2`, id, f.ws, d.Seconds()); err != nil {
		t.Fatalf("age send_after: %v", err)
	}
}

// ---------------------------------------------------------------------------
// The pair.
// ---------------------------------------------------------------------------

// HALF ONE: a live lease is not stranded.
//
// A worker holds the claim — it may have the body in hand and an SMTP
// conversation open. If the sweep re-drives this row, the second task claims it
// too and the customer receives the operator's words twice. The lease is the
// mutual exclusion; the sweep must respect it even though the row has, by every
// other measure, "stopped making progress".
//
// The claim is aged just PAST the caller's LeaseGrace but well short of the
// lease itself, so the test fails for a sweep that forgot to add the lease
// rather than only for one with no age check at all.
func TestASweepLeavesARowUnderALiveLeaseAlone(t *testing.T) {
	ctx, f := setupPool(t)
	pendingID := seedPendingReply(t, ctx, f, "One copy of this, please.")

	claimPending(t, ctx, f, pendingID)

	// Older than LeaseGrace (1m) on its own, younger than the 300s lease the
	// control plane adds to it. A sweep that used the grace alone would nominate
	// this row; the real one must not.
	ageClaim(t, ctx, f, "inbox_pending_replies", pendingID, 2*time.Minute)

	if status, _ := pendingReplyState(t, ctx, f, pendingID); status != inbox.PendingStatusSending {
		t.Fatalf("fixture is wrong: row status = %q, want sending", status)
	}
	if got := strandedIDs(t, ctx, f.core, sweepWindow); got[coreapi.StrandedPendingKindReply+":"+pendingID.String()] != "" {
		t.Fatalf("the scan nominated a row whose lease is still live (claimed %s ago, lease %ds) — "+
			"a worker may be mid-dial, and re-driving it is THE double send this subsystem is built "+
			"to avoid", 2*time.Minute, inbox.PendingReplyLeaseSeconds)
	}
	if sweepRescues(t, ctx, f.core, coreapi.StrandedPendingKindReply, pendingID.String()) {
		t.Fatal("the sweep re-enqueued a send for a row under a LIVE lease")
	}
	if status, _ := pendingReplyState(t, ctx, f, pendingID); status != inbox.PendingStatusSending {
		t.Errorf("the sweep changed the row to %q — it must mutate nothing: the claim guard reads "+
			"claimed_at, and clearing it is how a sweep destroys the evidence that protects a "+
			"live worker", status)
	}
}

// HALF TWO: a lease past its expiry IS stranded, and rescuing it sends exactly
// once.
//
// This is the case slice 3b made reachable: the claim committed, its response
// (carrying the lease AND the body) was lost, and until now only a task attempt
// that happened to land after the lease could rescue the row — so asynq's retry
// schedule decided whether a human's reply was ever sent.
func TestASweepRescuesAnAbandonedLeaseAndExactlyOneSendFollows(t *testing.T) {
	ctx, f := setupPool(t)
	pendingID := seedPendingReply(t, ctx, f, "Sorry for the delay — here are the numbers.")
	claimPending(t, ctx, f, pendingID)

	// Past the lease AND the grace: abandoned by any measure.
	ageClaim(t, ctx, f, "inbox_pending_replies", pendingID,
		time.Duration(inbox.PendingReplyLeaseSeconds)*time.Second+2*time.Minute)

	got := strandedIDs(t, ctx, f.core, sweepWindow)
	if got[coreapi.StrandedPendingKindReply+":"+pendingID.String()] != inbox.PendingStatusSending {
		t.Fatalf("the scan did not nominate an abandoned 'sending' row (saw %q) — this row holds a "+
			"reply a person wrote, and without the sweep nothing will ever send it",
			got[coreapi.StrandedPendingKindReply+":"+pendingID.String()])
	}
	if !sweepRescues(t, ctx, f.core, coreapi.StrandedPendingKindReply, pendingID.String()) {
		t.Fatal("the sweep did not re-enqueue a send for an abandoned row")
	}

	// The rescue enqueues the ORDINARY task, so this is what actually happens
	// next. Driving it twice is the retry: the second attempt must find the row
	// 'sent' and stop.
	s := &countingSender{}
	if err := pendingReplyOnce(t, ctx, f.core, s, pendingID.String(), f.ws.String()); err != nil {
		t.Fatalf("the rescued send: %v", err)
	}
	if err := pendingReplyOnce(t, ctx, f.core, s, pendingID.String(), f.ws.String()); err != nil {
		t.Fatalf("the redelivery after the rescue: %v", err)
	}
	if s.sends != 1 {
		t.Fatalf("THE DOUBLE SEND: the rescued reply went out %d times, want exactly 1", s.sends)
	}
	if status, messageID := pendingReplyState(t, ctx, f, pendingID); status != inbox.PendingStatusSent || messageID != s.lastMsg {
		t.Errorf("row = %q/%q, want sent/%s", status, messageID, s.lastMsg)
	}
	if got := outboundMessages(t, ctx, f, pendingID); got != 1 {
		t.Errorf("the thread has %d outbound messages, want exactly 1", got)
	}
}

// ---------------------------------------------------------------------------
// The 'scheduled' half — the gap internal/app/inbox/pending.go named and could
// not close on its own.
// ---------------------------------------------------------------------------

// A reply whose task was lost sits 'scheduled' past its send_after with nothing
// coming. Rescuing it delivers it — once.
func TestASweepRescuesAScheduledReplyWhoseTaskWasLost(t *testing.T) {
	ctx, f := setupPool(t)
	pendingID := seedPendingReply(t, ctx, f, "Following up as promised.")
	ageSendAfter(t, ctx, f, pendingID, 5*time.Minute)

	got := strandedIDs(t, ctx, f.core, sweepWindow)
	if got[coreapi.StrandedPendingKindReply+":"+pendingID.String()] != inbox.PendingStatusScheduled {
		t.Fatalf("the scan did not nominate a 'scheduled' row five minutes overdue (saw %q)",
			got[coreapi.StrandedPendingKindReply+":"+pendingID.String()])
	}

	s := &countingSender{}
	if err := pendingReplyOnce(t, ctx, f.core, s, pendingID.String(), f.ws.String()); err != nil {
		t.Fatalf("the rescued send: %v", err)
	}
	if s.sends != 1 {
		t.Fatalf("the rescued reply went out %d times, want 1", s.sends)
	}
}

// A reply that is merely a little overdue is NOT swept. Its own task is most
// likely sitting in asynq's retry backoff, and re-enqueuing on top of that is
// noise during exactly the incident that produced the delay.
func TestASweepLeavesABarelyOverdueScheduledReplyAlone(t *testing.T) {
	ctx, f := setupPool(t)
	pendingID := seedPendingReply(t, ctx, f, "Just sent, give it a second.")
	ageSendAfter(t, ctx, f, pendingID, 10*time.Second)

	if got := strandedIDs(t, ctx, f.core, sweepWindow); got[coreapi.StrandedPendingKindReply+":"+pendingID.String()] != "" {
		t.Fatalf("the scan nominated a row overdue by 10s against a %s window", sweepWindow.OverdueAfter)
	}
}

// A reply the operator UNDID must never be resurrected. The status guard on the
// scan is what holds it, and this is the assertion that would catch a scan
// widened to "everything not sent".
func TestASweepNeverResurrectsATerminalRow(t *testing.T) {
	ctx, f := setupPool(t)
	for _, tc := range []struct {
		name   string
		status string
	}{
		{"cancelled", inbox.PendingStatusCancelled},
		{"failed", inbox.PendingStatusFailed},
		{"sent", inbox.PendingStatusSent},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pendingID := seedPendingReply(t, ctx, f, "Terminal: "+tc.name)
			ageSendAfter(t, ctx, f, pendingID, time.Hour)
			if _, err := f.pool.Exec(ctx,
				`UPDATE inbox_pending_replies SET status = $3 WHERE id = $1 AND workspace_id = $2`,
				pendingID, f.ws, tc.status); err != nil {
				t.Fatalf("set status %s: %v", tc.status, err)
			}
			if got := strandedIDs(t, ctx, f.core, sweepWindow); got[coreapi.StrandedPendingKindReply+":"+pendingID.String()] != "" {
				t.Fatalf("the scan nominated a %q row — re-driving a cancelled reply sends mail the "+
					"operator explicitly took back", tc.status)
			}
		})
	}
}

// The compose table is swept by the same tick and rescued through its OWN task
// type. Left out, a scheduled composed email would have exactly the gap this
// sweep closes for replies, and the next person would have to add a ninth sweep.
func TestASweepRescuesAStrandedComposeThroughTheComposeTask(t *testing.T) {
	ctx, f := setupPool(t)
	pendingID := seedPendingCompose(t, ctx, f, "Intro, take two")
	if _, err := f.pool.Exec(ctx,
		`UPDATE inbox_pending_composes SET send_after = now() - interval '5 minutes'
		 WHERE id = $1 AND workspace_id = $2`, pendingID, f.ws); err != nil {
		t.Fatalf("age the compose: %v", err)
	}

	if got := strandedIDs(t, ctx, f.core, sweepWindow); got[coreapi.StrandedPendingKindCompose+":"+pendingID.String()] != inbox.PendingStatusScheduled {
		t.Fatal("the scan did not nominate a stranded composed email")
	}
	if sweepRescues(t, ctx, f.core, coreapi.StrandedPendingKindReply, pendingID.String()) {
		t.Fatal("a compose was rescued through the REPLY task — that handler would look up a " +
			"thread the row does not have")
	}
	if !sweepRescues(t, ctx, f.core, coreapi.StrandedPendingKindCompose, pendingID.String()) {
		t.Fatal("the sweep did not re-enqueue the compose task for a stranded composed email")
	}

	s := &countingSender{}
	if err := pendingComposeOnce(t, ctx, f.core, s, pendingID.String(), f.ws.String()); err != nil {
		t.Fatalf("the rescued compose: %v", err)
	}
	if s.sends != 1 {
		t.Fatalf("the rescued compose went out %d times, want 1", s.sends)
	}
}

// The scan is cross-tenant and must SAY SO correctly: the workspace it reports
// is the row's own, because that id is what pins every query the rescue task
// then runs. A scan that returned the wrong workspace would produce a rescue
// whose claim matches zero rows — a silent drop of a human's reply.
func TestTheScanReportsEachRowsOwnWorkspace(t *testing.T) {
	ctx, f := setupPool(t)
	pendingID := seedPendingReply(t, ctx, f, "Whose workspace is this?")
	ageSendAfter(t, ctx, f, pendingID, 5*time.Minute)

	psc, ok := f.core.(workerinbox.PendingSweepCore)
	if !ok {
		t.Fatalf("client %T is not a PendingSweepCore", f.core)
	}
	rows, err := psc.ListStrandedPendingInboxSends(ctx, sweepWindow)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	var found bool
	for _, r := range rows {
		if r.ID != pendingID.String() {
			continue
		}
		found = true
		if r.WorkspaceID != f.ws.String() {
			t.Errorf("row reported workspace %q, want %q", r.WorkspaceID, f.ws.String())
		}
	}
	if !found {
		t.Fatal("the seeded row was not in the scan at all")
	}
}

// claimPending takes the claim the way a worker does — through the coreapi seam,
// not with raw SQL — so the fixture's 'sending' row is the shape the production
// path produces, lease and all.
func claimPending(t *testing.T, ctx context.Context, f poolFixture, pendingID uuid.UUID) {
	t.Helper()
	pc, ok := f.core.(workerinbox.PendingReplyCore)
	if !ok {
		t.Fatalf("client %T is not a PendingReplyCore", f.core)
	}
	if _, err := pc.ClaimPendingInboxReply(ctx, f.ws.String(), pendingID.String()); err != nil {
		t.Fatalf("claim: %v", err)
	}
}
