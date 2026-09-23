//go:build integration

package sequence

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inroad/inroad/internal/platform/db/gen"
)

// branchFixture is a running three-step campaign (subjects "S1".."S3", all
// zero-delay) with a send window open around the clock — so no test outcome
// depends on the hour it happens to run — and one enrolled contact.
type branchFixture struct {
	itFixture
	pool  *pgxpool.Pool
	eid   string
	steps [3]uuid.UUID
	snd   *itSender
	enq   *itEnq
}

func seedBranchCampaign(t *testing.T) (branchFixture, func()) {
	t.Helper()
	ctx := context.Background()
	pool, q, closeFn := connect(t)
	fx := seedCampaign(t, ctx, pool, q, newSealer(t), [][3]string{
		{"S1", "one", "0"}, {"S2", "two", "0"}, {"S3", "three", "0"},
	})
	if _, err := pool.Exec(ctx, `
		INSERT INTO campaign_send_windows (workspace_id, campaign_id, weekday, start_minute, end_minute)
		SELECT $1, $2, d, 0, 1440 FROM generate_series(0, 6) AS d`, fx.ws, fx.campaignID); err != nil {
		t.Fatalf("send windows: %v", err)
	}
	steps, err := q.ListStepsByCampaign(ctx, gen.ListStepsByCampaignParams{CampaignID: fx.campaignID, WorkspaceID: fx.ws})
	if err != nil || len(steps) != 3 {
		t.Fatalf("steps: %v (%d)", err, len(steps))
	}
	ids, err := q.EnrollListMembers(ctx, gen.EnrollListMembersParams{ID: fx.campaignID, WorkspaceID: fx.ws})
	if err != nil || len(ids) != 1 {
		t.Fatalf("enroll: %v", err)
	}
	return branchFixture{
		itFixture: fx, pool: pool, eid: ids[0].ID.String(),
		steps: [3]uuid.UUID{steps[0].ID, steps[1].ID, steps[2].ID},
		snd:   &itSender{}, enq: newITEnq(),
	}, closeFn
}

// branch writes a router directly through the query (the save-time validation
// has its own tests in internal/app/sequencestep).
func (f branchFixture) branch(t *testing.T, step uuid.UUID, cond string, within int32, yes, no *uuid.UUID) {
	t.Helper()
	var w *int32
	if cond != "always" {
		w = &within
	}
	if _, err := f.q.UpsertBranch(context.Background(), gen.UpsertBranchParams{
		StepID: step, WorkspaceID: f.ws, CampaignID: f.campaignID, Condition: cond, WithinDays: w,
		YesStepID: optUUID(yes), NoStepID: optUUID(no),
	}); err != nil {
		t.Fatalf("branch: %v", err)
	}
}

func optUUID(id *uuid.UUID) pgtype.UUID {
	if id == nil {
		return pgtype.UUID{}
	}
	return pgtype.UUID{Bytes: *id, Valid: true}
}

func (f branchFixture) advance(t *testing.T) {
	t.Helper()
	advance(t, f.core, f.snd, f.enq, f.eid, f.ws.String())
}

// due models the advance task's wait: next_due_at back to now.
func (f branchFixture) due(t *testing.T) {
	t.Helper()
	arriveAtDueTime(t, context.Background(), f.pool, f.eid)
}

// ageLastSend moves the cursor step's send (and its window) d into the past.
func (f branchFixture) ageLastSend(t *testing.T, d time.Duration) {
	t.Helper()
	if _, err := f.pool.Exec(context.Background(),
		`UPDATE sequence_enrollments SET last_sent_at = last_sent_at - make_interval(secs => $2), next_due_at = now()
		 WHERE id = $1`, f.eid, d.Seconds()); err != nil {
		t.Fatalf("age last send: %v", err)
	}
}

// sendID is the deterministic sends row id of one step for this contact.
func (f branchFixture) sendID(t *testing.T, order int) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := f.pool.QueryRow(context.Background(),
		`SELECT id FROM sends WHERE campaign_id = $1 AND contact_id = $2 AND step_order = $3`,
		f.campaignID, f.contactID, order).Scan(&id); err != nil {
		t.Fatalf("send row for step %d: %v", order, err)
	}
	return id
}

func (f branchFixture) track(t *testing.T, order int, kind string, machine bool) {
	t.Helper()
	reason := ""
	if machine {
		reason = "proxy_user_agent"
	}
	if _, err := f.pool.Exec(context.Background(), `
		INSERT INTO tracking_events (workspace_id, campaign_id, send_id, kind, is_machine, machine_reason)
		VALUES ($1, $2, $3, $4, $5, $6)`, f.ws, f.campaignID, f.sendID(t, order), kind, machine, reason); err != nil {
		t.Fatalf("tracking event: %v", err)
	}
}

// reply stores an inbound reply the way the inbox poller does: a thread on the
// campaign + contact, and an inbound message classified replyClass.
func (f branchFixture) reply(t *testing.T, replyClass string) {
	t.Helper()
	ctx := context.Background()
	var mailbox uuid.UUID
	if err := f.pool.QueryRow(ctx, `SELECT mailbox_id FROM campaigns WHERE id = $1`, f.campaignID).Scan(&mailbox); err != nil {
		t.Fatalf("mailbox: %v", err)
	}
	var thread uuid.UUID
	if err := f.pool.QueryRow(ctx, `
		INSERT INTO inbox_threads (workspace_id, mailbox_id, campaign_id, contact_id, root_message_id)
		VALUES ($1, $2, $3, $4, $5) RETURNING id`,
		f.ws, mailbox, f.campaignID, f.contactID, "<root-"+uuid.NewString()+"@x>").Scan(&thread); err != nil {
		t.Fatalf("thread: %v", err)
	}
	if _, err := f.pool.Exec(ctx, `
		INSERT INTO inbox_messages (thread_id, workspace_id, mailbox_id, direction, message_id, reply_class, occurred_at)
		VALUES ($1, $2, $3, 'inbound', $4, $5, now())`,
		thread, f.ws, mailbox, "<reply-"+uuid.NewString()+"@x>", replyClass); err != nil {
		t.Fatalf("message: %v", err)
	}
}

func (f branchFixture) enrollment(t *testing.T) gen.SequenceEnrollment {
	t.Helper()
	e, err := f.q.GetEnrollment(context.Background(), gen.GetEnrollmentParams{ID: uuid.MustParse(f.eid), WorkspaceID: f.ws})
	if err != nil {
		t.Fatalf("enrollment: %v", err)
	}
	return e
}

func (f branchFixture) subjects() []string {
	out := make([]string, len(f.snd.sent))
	for i, m := range f.snd.sent {
		out[i] = m.Subject
	}
	return out
}

// requireSent asserts the exact sequence of steps the contact has received.
func (f branchFixture) requireSent(t *testing.T, want ...string) {
	t.Helper()
	got := f.subjects()
	if len(got) != len(want) {
		t.Fatalf("sent %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("sent %v, want %v", got, want)
		}
	}
}

// A human open inside the window routes YES — step 1 → step 3, never step 2 —
// and the enrollment completes on step 3.
func TestBranchOpenedRoutesYes(t *testing.T) {
	f, done := seedBranchCampaign(t)
	defer done()
	f.branch(t, f.steps[0], "opened", 2, &f.steps[2], &f.steps[1])

	f.advance(t)
	f.requireSent(t, "S1")
	f.track(t, 1, "open", false)

	f.due(t)
	f.advance(t)
	f.requireSent(t, "S1", "S3")
	if e := f.enrollment(t); e.Status != "completed" || e.CurrentStep != 3 {
		t.Fatalf("enrollment = %s at %d, want completed at 3", e.Status, e.CurrentStep)
	}
}

// A MACHINE open (a proxy prefetch) is not an open: the branch keeps waiting,
// and when the window closes it routes NO (docs/security.md invariant 82).
func TestBranchMachineOpenDoesNotCount(t *testing.T) {
	f, done := seedBranchCampaign(t)
	defer done()
	f.branch(t, f.steps[0], "opened", 1, &f.steps[2], &f.steps[1])

	f.advance(t)
	f.track(t, 1, "open", true)

	f.due(t)
	f.advance(t)
	f.requireSent(t, "S1")
	e := f.enrollment(t)
	if e.Status != "active" || !e.NextDueAt.Valid || !e.NextDueAt.Time.After(time.Now()) {
		t.Fatalf("a pending condition must park the enrollment in the future, got %s due=%v", e.Status, e.NextDueAt)
	}
	if e.AwaitingConditionStep == nil || *e.AwaitingConditionStep != 1 {
		t.Fatalf("awaiting_condition_step = %v, want 1", e.AwaitingConditionStep)
	}
	// Postgres keeps microseconds; the enqueued instant carries nanoseconds.
	if at, ok := f.enq.at[f.eid]; !ok || at.Sub(e.NextDueAt.Time).Abs() > time.Millisecond {
		t.Fatalf("recheck enqueued at %v, stamped %v — they must agree", at, e.NextDueAt.Time)
	}

	f.ageLastSend(t, 25*time.Hour)
	f.advance(t)
	f.requireSent(t, "S1", "S2")
}

// Replies span both legs: the step went out as a sends row, the answer is an
// inbox_messages row. An out-of-office is not a reply; a human one is, and it
// routes YES without waiting out the window.
func TestBranchRepliedCountsHumanRepliesOnly(t *testing.T) {
	f, done := seedBranchCampaign(t)
	defer done()
	f.branch(t, f.steps[0], "replied", 3, &f.steps[2], &f.steps[1])

	f.advance(t)
	f.reply(t, "out_of_office")
	f.due(t)
	f.advance(t)
	f.requireSent(t, "S1")

	f.reply(t, "neutral")
	f.due(t)
	f.advance(t)
	f.requireSent(t, "S1", "S3")
}

// A reply that does not stop the enrollment nudges one waiting on a reply
// condition to now, so the next sweep routes it; an automated reply must not
// (it is the kind that DEFERS an enrollment, and pulling it forward would undo
// the deferral).
func TestBranchReplyNudgesAwaitingEnrollment(t *testing.T) {
	f, done := seedBranchCampaign(t)
	defer done()
	ctx := context.Background()
	f.branch(t, f.steps[0], "replied", 3, &f.steps[2], nil)

	f.advance(t)
	f.due(t)
	f.advance(t) // parks until the next recheck
	parked := f.enrollment(t).NextDueAt.Time
	if !parked.After(time.Now()) {
		t.Fatalf("precondition: enrollment should be parked, due %v", parked)
	}

	if err := f.core.RecordReplyClass(ctx, f.eid, f.ws.String(), "out_of_office", "rules", 1); err != nil {
		t.Fatal(err)
	}
	if got := f.enrollment(t).NextDueAt.Time; !got.Equal(parked) {
		t.Fatalf("an automated reply moved the due time %v -> %v", parked, got)
	}

	if err := f.core.RecordReplyClass(ctx, f.eid, f.ws.String(), "neutral", "rules", 1); err != nil {
		t.Fatal(err)
	}
	// A minute of slack for the database container's clock against this one;
	// the parked due time was an hour out.
	if got := f.enrollment(t).NextDueAt.Time; got.After(time.Now().Add(time.Minute)) {
		t.Fatalf("a human reply must pull the due time to now, still %v", got)
	}
}

// A route to an unset exit ENDS the path: the enrollment completes without a
// send, and current_step/last_sent_at still describe the last real message.
func TestBranchRoutedEndCompletesWithoutSend(t *testing.T) {
	f, done := seedBranchCampaign(t)
	defer done()
	// not_opened: silence routes YES (unset = end); an open would route NO.
	f.branch(t, f.steps[0], "not_opened", 1, nil, &f.steps[1])

	f.advance(t)
	before := f.enrollment(t)
	f.ageLastSend(t, 25*time.Hour)
	f.advance(t)

	f.requireSent(t, "S1")
	e := f.enrollment(t)
	if e.Status != "completed" || e.CurrentStep != 1 || !e.CompletedAt.Valid || e.NextDueAt.Valid {
		t.Fatalf("routed end = status %s step %d completed %v due %v", e.Status, e.CurrentStep, e.CompletedAt, e.NextDueAt)
	}
	// Aged by the test, not re-stamped by the finish.
	if !e.LastSentAt.Time.Before(before.LastSentAt.Time) {
		t.Fatalf("a routed end must not stamp last_sent_at (%v -> %v)", before.LastSentAt.Time, e.LastSentAt.Time)
	}
}

// Mid-flight rule: a branch removed while the contact waits on it returns the
// step to linear fall-through, and the successor still waits out ITS OWN delay
// from the last send — even though the campaign now has no branches at all, so
// only awaiting_condition_step routes it through the graph.
func TestBranchRemovedMidWaitHonoursSuccessorDelay(t *testing.T) {
	f, done := seedBranchCampaign(t)
	defer done()
	ctx := context.Background()
	f.branch(t, f.steps[0], "opened", 3, &f.steps[2], &f.steps[1])

	f.advance(t)
	f.due(t)
	f.advance(t) // parked on the condition
	f.requireSent(t, "S1")

	if err := f.q.DeleteBranch(ctx, gen.DeleteBranchParams{StepID: f.steps[0], CampaignID: f.campaignID, WorkspaceID: f.ws}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE sequence_steps SET delay_seconds = 7200 WHERE id = $1`, f.steps[1]); err != nil {
		t.Fatal(err)
	}

	f.due(t) // the recheck the deleted condition had scheduled fires
	f.advance(t)
	f.requireSent(t, "S1")

	f.ageLastSend(t, 3*time.Hour)
	f.advance(t)
	f.requireSent(t, "S1", "S2")
}

// Mid-flight rule: a branch ADDED to a step after the contact received it
// governs the next advance, with its window measured from that step's send.
func TestBranchAddedAfterSendAppliesToNextAdvance(t *testing.T) {
	f, done := seedBranchCampaign(t)
	defer done()
	f.advance(t) // S1 sent while the campaign was linear
	f.branch(t, f.steps[0], "clicked", 1, &f.steps[2], &f.steps[1])

	f.due(t)
	f.advance(t)
	f.requireSent(t, "S1") // now waiting on the click

	f.track(t, 1, "click", false)
	f.due(t)
	f.advance(t)
	f.requireSent(t, "S1", "S3")
}

// The runtime loop backstop: a cycle written past the save-time validation (a
// direct write, here) ends the path instead of recovering-forward forever.
func TestBranchLoopBackstopEndsPath(t *testing.T) {
	f, done := seedBranchCampaign(t)
	defer done()
	f.branch(t, f.steps[0], "always", 0, &f.steps[1], nil)
	f.branch(t, f.steps[1], "always", 0, &f.steps[0], nil) // 1 -> 2 -> 1

	f.advance(t)
	f.due(t)
	f.advance(t)
	f.requireSent(t, "S1", "S2")

	f.due(t)
	f.advance(t)
	f.requireSent(t, "S1", "S2")
	if e := f.enrollment(t); e.Status != "completed" || e.CurrentStep != 2 {
		t.Fatalf("loop = %s at %d, want completed at 2", e.Status, e.CurrentStep)
	}
}

// Branch routing threads onto the LATEST message the contact received, not the
// highest-numbered step. On the path 1 -> 3 -> 2 -> 4 the third send (step 2)
// is the latest when step 4 goes out, while step 3 is the highest-numbered one
// sent — ordering by step_order would thread step 4 onto the wrong message.
func TestBranchThreadsOntoLatestSend(t *testing.T) {
	f, done := seedBranchCampaign(t)
	defer done()
	s4, err := f.q.CreateStep(context.Background(), gen.CreateStepParams{
		WorkspaceID: f.ws, CampaignID: f.campaignID, StepOrder: 4, Subject: "S4", BodyText: "four",
	})
	if err != nil {
		t.Fatal(err)
	}
	f.branch(t, f.steps[0], "always", 0, &f.steps[2], nil)
	f.branch(t, f.steps[2], "always", 0, &f.steps[1], nil)
	f.branch(t, f.steps[1], "always", 0, &s4.ID, nil)

	for range 4 {
		f.advance(t)
		f.due(t)
	}
	f.requireSent(t, "S1", "S3", "S2", "S4")

	var step2MessageID string
	if err := f.pool.QueryRow(context.Background(), `SELECT message_id FROM sends WHERE id = $1`, f.sendID(t, 2)).
		Scan(&step2MessageID); err != nil {
		t.Fatal(err)
	}
	if got := f.snd.sent[3].InReplyTo; got != step2MessageID {
		t.Fatalf("step 4 In-Reply-To = %q, want step 2's %q (the latest send)", got, step2MessageID)
	}
}

// A campaign with NO branches behaves exactly as before branching existed:
// every job the control plane builds is the linear one (next step by order, the
// following step's delay, last-step on the final step), nothing is ever parked,
// and awaiting_condition_step is never written.
func TestLinearCampaignUnchangedByBranching(t *testing.T) {
	f, done := seedBranchCampaign(t)
	defer done()
	ctx := context.Background()
	if _, err := f.pool.Exec(ctx, `UPDATE sequence_steps SET delay_seconds = 60 * step_order WHERE campaign_id = $1`, f.campaignID); err != nil {
		t.Fatal(err)
	}
	want := []struct {
		order, nextDelay int
		last             bool
	}{{1, 120, false}, {2, 180, false}, {3, 0, true}}
	for _, w := range want {
		job, err := f.core.GetStepSendJob(ctx, f.eid, f.ws.String())
		if err != nil {
			t.Fatal(err)
		}
		if job.Skip || job.ConditionPending || job.StepOrder != w.order || job.NextDelaySeconds != w.nextDelay || job.LastStep != w.last {
			t.Fatalf("linear job = order %d next %d last %v skip %v pending %v, want %+v",
				job.StepOrder, job.NextDelaySeconds, job.LastStep, job.Skip, job.ConditionPending, w)
		}
		f.advance(t)
		f.due(t)
	}
	f.requireSent(t, "S1", "S2", "S3")
	e := f.enrollment(t)
	if e.Status != "completed" || e.AwaitingConditionStep != nil {
		t.Fatalf("linear enrollment = %s awaiting=%v", e.Status, e.AwaitingConditionStep)
	}
	if job, err := f.core.GetStepSendJob(ctx, f.eid, f.ws.String()); err != nil || !job.Skip {
		t.Fatalf("completed enrollment job = %+v, %v", job, err)
	}
}

// The workspace pin holds on the graph path too: a foreign workspace id finds
// no enrollment, so nothing is routed, parked or finished.
func TestBranchRoutingIsWorkspacePinned(t *testing.T) {
	f, done := seedBranchCampaign(t)
	defer done()
	f.branch(t, f.steps[0], "always", 0, nil, nil)
	if _, err := f.core.GetStepSendJob(context.Background(), f.eid, uuid.NewString()); err == nil {
		t.Fatal("a foreign workspace must not resolve the enrollment")
	}
	if e := f.enrollment(t); e.Status != "active" || e.CurrentStep != 0 {
		t.Fatalf("foreign-workspace advance touched the enrollment: %s at %d", e.Status, e.CurrentStep)
	}
}
