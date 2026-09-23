//go:build integration

package inbox

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/platform/db/gen"
	"github.com/inroad/inroad/internal/platform/mail"
	"github.com/inroad/inroad/internal/platform/replyclassify"
)

// Reply-routed branches, driven through the REAL inbox poll and dispatch: the
// classifier picks the key, the workspace's label flags decide what the reply
// does, and only then does the send path see whatever the dispatch left behind.
// A test that wrote inbox rows and called RecordReplyClass by hand would pass
// for a reply the dispatch would actually have used to STOP the sequence.

// humanReplyBody is a plain human answer to step 1; oooSubject marks an
// out-of-office one (the classifier's subject rule).
const (
	humanReplyBody = "\n\nSounds good, tell me more.\n"
	oooSubject     = "Out of Office: back Monday"
)

type branchInbox struct {
	itFixture
	pool     *pgxpool.Pool
	msgID    string
	uidNext  uint32
	classify *replyclassify.Classifier
}

// seedReplyBranch is the inbox fixture (step 1 sent as msgID) with a
// "replied within 3 days → step 2, otherwise end" branch on step 1, step 2
// due immediately, a send window open around the clock (so nothing depends on
// the hour the test runs), and the enrollment already parked on the condition.
func seedReplyBranch(t *testing.T) (branchInbox, func()) {
	t.Helper()
	ctx := context.Background()
	pool, q, closeFn := connect(t)
	msgID := "<step1-" + uuid.NewString() + "@test>"
	fx := seedActiveEnrollment(t, ctx, pool, q, newSealer(t), msgID)

	steps, err := q.ListStepsByCampaign(ctx, gen.ListStepsByCampaignParams{CampaignID: fx.campaignID, WorkspaceID: fx.ws})
	if err != nil || len(steps) != 2 {
		t.Fatalf("steps: %v (%d)", err, len(steps))
	}
	for _, stmt := range []string{
		`UPDATE sequence_steps SET delay_seconds = 0 WHERE campaign_id = $1`,
		`INSERT INTO campaign_send_windows (workspace_id, campaign_id, weekday, start_minute, end_minute)
		 SELECT c.workspace_id, c.id, d, 0, 1440 FROM campaigns c, generate_series(0, 6) AS d WHERE c.id = $1`,
	} {
		if _, err := pool.Exec(ctx, stmt, fx.campaignID); err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	three := int32(3)
	if _, err := q.UpsertBranch(ctx, gen.UpsertBranchParams{
		StepID: steps[0].ID, WorkspaceID: fx.ws, CampaignID: fx.campaignID, Condition: "replied",
		WithinDays: &three, YesStepID: pgtype.UUID{Bytes: steps[1].ID, Valid: true},
	}); err != nil {
		t.Fatalf("branch: %v", err)
	}
	if err := fx.core.SetInboxCursor(ctx, fx.mailboxID.String(), fx.ws.String(), 10, 5); err != nil {
		t.Fatalf("seed cursor: %v", err)
	}
	b := branchInbox{itFixture: fx, pool: pool, msgID: msgID, uidNext: 11, classify: replyclassify.New(nil)}

	// Nothing has arrived: the condition is open, so the enrollment parks.
	if job := b.job(t); !job.ConditionPending {
		t.Fatalf("before any reply the enrollment must wait on the condition, got %+v", job)
	}
	return b, closeFn
}

func (b *branchInbox) raw(subject, body string) string {
	return "From: " + b.email + "\nTo: from@acme.test\nSubject: " + subject +
		"\nMessage-ID: <reply-" + uuid.NewString() + "@x.test>\nIn-Reply-To: " + b.msgID +
		"\nReferences: " + b.msgID + "\n" + body
}

// classOf is what the production classifier makes of a raw message.
func (b *branchInbox) classOf(t *testing.T, raw string) string {
	t.Helper()
	// The same Input poll.go builds from a fetched message.
	msg := inboundMsg(t, 0, raw)
	return b.classify.Classify(context.Background(), replyclassify.Input{
		Headers: map[string][]string(msg.Header), Subject: msg.Header.Get("Subject"), BodyText: string(msg.Body),
	}).Class
}

// poll delivers one message through PollHandler, exactly as the scheduler does.
func (b *branchInbox) poll(t *testing.T, raw string) {
	t.Helper()
	uid := b.uidNext
	b.uidNext++
	reader := &fakeReader{uidValidity: 5, uidNext: b.uidNext, msgs: []mail.InboundMessage{inboundMsg(t, uid, raw)}}
	if err := PollHandler(b.core, reader, nil, nil, b.classify, nil, noopEngageEnqueuer{})(
		context.Background(), pollTaskFor(t, b.mailboxID.String(), b.ws.String())); err != nil {
		t.Fatalf("poll: %v", err)
	}
}

func (b *branchInbox) job(t *testing.T) coreapi.StepSendJob {
	t.Helper()
	job, err := b.core.GetStepSendJob(context.Background(), b.enrollmentID.String(), b.ws.String())
	if err != nil {
		t.Fatalf("GetStepSendJob: %v", err)
	}
	return job
}

func (b *branchInbox) enrollment(t *testing.T) gen.SequenceEnrollment {
	t.Helper()
	return getEnrollment(t, context.Background(), b.q, b.ws, b.enrollmentID)
}

// With the workspace's label for a human reply set NOT to stop the sequence,
// the reply is routed: an out-of-office first is neither a reply nor a nudge
// (it is the kind of reply that defers), then the human reply pulls the parked
// enrollment forward and the send path routes it down the YES exit.
func TestReplyBranchRoutesOnANonStoppingLabel(t *testing.T) {
	b, done := seedReplyBranch(t)
	defer done()
	ctx := context.Background()

	human := b.raw("Re: Hi", humanReplyBody)
	key := b.classOf(t, human)
	if replyclassify.IsAutomated(key) {
		t.Fatalf("precondition: a plain reply classified as automated %q", key)
	}
	if _, err := b.pool.Exec(ctx,
		`UPDATE reply_labels SET stops_enrollment = false WHERE workspace_id = $1 AND key = $2`, b.ws, key); err != nil {
		t.Fatalf("make %q non-stopping: %v", key, err)
	}

	ooo := b.raw(oooSubject, "\n\nI am away until Monday.\n")
	if got := b.classOf(t, ooo); got != replyclassify.ClassOutOfOffice {
		t.Fatalf("precondition: out-of-office message classified %q", got)
	}
	parked := b.enrollment(t).NextDueAt.Time
	b.poll(t, ooo)
	if e := b.enrollment(t); e.Status != "active" || !e.NextDueAt.Time.Equal(parked) {
		t.Fatalf("an out-of-office moved the enrollment: status %s due %v -> %v", e.Status, parked, e.NextDueAt.Time)
	}
	if job := b.job(t); !job.ConditionPending {
		t.Fatalf("an out-of-office is not a reply, the condition must stay open: %+v", job)
	}

	b.poll(t, human)
	e := b.enrollment(t)
	if e.Status != "active" {
		t.Fatalf("a non-stopping reply stopped the enrollment: %s", e.Status)
	}
	if e.NextDueAt.Time.After(time.Now().Add(time.Minute)) {
		t.Fatalf("the reply must nudge the parked enrollment to now, due %v", e.NextDueAt.Time)
	}
	job := b.job(t)
	if job.ConditionPending || job.Skip || job.StepOrder != 2 {
		t.Fatalf("the reply must route to step 2 (yes): pending=%v skip=%v order=%d", job.ConditionPending, job.Skip, job.StepOrder)
	}
}

// The product decision, pinned: with the workspace's DEFAULT labels a human
// reply STOPS the sequence exactly as it did before branching existed — the
// branch does not get a say, and the send path has nothing left to route.
func TestReplyBranchDefaultLabelStopsInsteadOfRouting(t *testing.T) {
	b, done := seedReplyBranch(t)
	defer done()

	human := b.raw("Re: Hi", humanReplyBody)
	var stops bool
	if err := b.pool.QueryRow(context.Background(),
		`SELECT stops_enrollment FROM reply_labels WHERE workspace_id = $1 AND key = $2`,
		b.ws, b.classOf(t, human)).Scan(&stops); err != nil || !stops {
		t.Fatalf("precondition: the seeded label for a human reply must stop the sequence (stops=%v err=%v)", stops, err)
	}

	b.poll(t, human)
	e := b.enrollment(t)
	if e.Status != "stopped" || e.StopReason == nil || *e.StopReason != "replied" {
		t.Fatalf("a default-label reply must stop the enrollment 'replied', got %s %v", e.Status, e.StopReason)
	}
	if job := b.job(t); !job.Skip || job.ConditionPending || job.StepOrder != 0 {
		t.Fatalf("a stopped enrollment routes nowhere: %+v", job)
	}
}

// A due time the enrollment carries for a reason OTHER than a condition wait —
// here an out-of-office deferral five days out (DeferEnrollment never stamps
// awaiting_condition_step) — is not the nudge's to pull forward, even for a
// non-stopping human reply the branch is watching for. The stated absence
// wins; the reply is still evidence, read when the enrollment next wakes.
func TestReplyNudgeLeavesAnOutOfOfficeDeferralAlone(t *testing.T) {
	b, done := seedReplyBranch(t)
	defer done()
	ctx := context.Background()

	human := b.raw("Re: Hi", humanReplyBody)
	if _, err := b.pool.Exec(ctx,
		`UPDATE reply_labels SET stops_enrollment = false WHERE workspace_id = $1 AND key = $2`, b.ws, b.classOf(t, human)); err != nil {
		t.Fatal(err)
	}
	if _, err := b.pool.Exec(ctx, `UPDATE sequence_enrollments SET awaiting_condition_step = NULL WHERE id = $1`, b.enrollmentID); err != nil {
		t.Fatal(err)
	}
	until := time.Now().Add(5 * 24 * time.Hour).UTC().Truncate(time.Second)
	if err := b.core.DeferEnrollment(ctx, b.enrollmentID.String(), b.ws.String(), until); err != nil {
		t.Fatal(err)
	}

	b.poll(t, human)
	if e := b.enrollment(t); e.Status != "active" || !e.NextDueAt.Time.Equal(until) {
		t.Fatalf("the out-of-office deferral was overwritten: status %s due %v, want %v", e.Status, e.NextDueAt.Time, until)
	}
}
