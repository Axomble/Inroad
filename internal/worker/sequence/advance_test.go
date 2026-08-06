package sequence

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/hibiken/asynq"

	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/platform/cadence"
	"github.com/inroad/inroad/internal/platform/mail"
	"github.com/inroad/inroad/internal/platform/metrics"
	"github.com/inroad/inroad/internal/platform/metrics/metricstest"
	"github.com/inroad/inroad/internal/platform/queue"
)

// stubCore embeds coreapi.Client so it satisfies the interface; only the
// methods the advance handler calls are implemented. Any other call panics —
// which is what we want if the handler unexpectedly reaches for one.
type stubCore struct {
	coreapi.Client
	job    coreapi.StepSendJob
	jobErr error
	adv    coreapi.Advance
	// claimOK makes the FIRST claim win (ClaimWon). sent models the DB row
	// reaching 'sent' after MarkStepDelivered commits: once set, every later claim
	// returns ClaimAlreadySent — the recover-forward path. Absent both, a claim
	// loses (ClaimSkip), modelling a row another worker owns.
	claimOK       bool
	claimDeferred bool
	claimErr      error
	claimCalls    int
	sent          bool
	delivered     *string // message id passed to MarkStepDelivered
	// advanceFail makes the first N AdvanceStepCursor calls return an error, to
	// exercise the cursor-advance-failure → recover-forward window.
	advanceFail  int
	advanceCalls int
	finalized    *coreapi.StepResult // set only by the (failed) FinalizeStepSend path
	released     bool
	stopped      string
	// stopFailN makes the first N MarkStepStopped calls fail (returning an
	// error, as if the DB write itself failed), to exercise the
	// fail-then-retry double-count regression: the metric for a stop must
	// only be recorded once the stop actually commits.
	stopFailN int
	stopCalls int
	deferrals int // value returned by IncrementEnrollmentCapDeferrals
	incrCalls int
}

func (s *stubCore) GetStepSendJob(context.Context, string, string) (coreapi.StepSendJob, error) {
	return s.job, s.jobErr
}
func (s *stubCore) ClaimStepSend(context.Context, coreapi.StepSendJob) (coreapi.ClaimOutcome, error) {
	s.claimCalls++
	if s.claimErr != nil {
		return coreapi.ClaimSkip, s.claimErr
	}
	if s.sent {
		return coreapi.ClaimAlreadySent, nil
	}
	if s.claimDeferred {
		return coreapi.ClaimDeferred, nil
	}
	if s.claimOK && s.claimCalls == 1 {
		return coreapi.ClaimWon, nil
	}
	return coreapi.ClaimSkip, nil
}
func (s *stubCore) MarkStepDelivered(_ context.Context, _ coreapi.StepSendJob, msgID string) error {
	s.delivered = &msgID
	s.sent = true
	return nil
}
func (s *stubCore) AdvanceStepCursor(context.Context, coreapi.StepSendJob) (coreapi.Advance, error) {
	s.advanceCalls++
	if s.advanceCalls <= s.advanceFail {
		return coreapi.Advance{}, errors.New("cursor advance failed")
	}
	return s.adv, nil
}
func (s *stubCore) FinalizeStepSend(_ context.Context, _ coreapi.StepSendJob, res coreapi.StepResult) (coreapi.Advance, error) {
	s.finalized = &res
	return s.adv, nil
}
func (s *stubCore) ReleaseStepSend(context.Context, coreapi.StepSendJob) error {
	s.released = true
	return nil
}
func (s *stubCore) MarkStepStopped(_ context.Context, _, _, reason string) error {
	s.stopCalls++
	if s.stopCalls <= s.stopFailN {
		return errors.New("mark step stopped failed")
	}
	s.stopped = reason
	return nil
}
func (s *stubCore) IncrementEnrollmentCapDeferrals(context.Context, string, string) (int, error) {
	s.incrCalls++
	return s.deferrals, nil
}

type fakeSender struct {
	calls int
	sent  mail.Message
	job   mail.OutboundJob
	id    string
	err   error
}

func (f *fakeSender) Send(_ context.Context, tj mail.OutboundJob, m mail.Message) (string, error) {
	f.calls++
	f.sent = m
	f.job = tj
	return f.id, f.err
}

func (f *fakeSender) called() bool { return f.calls > 0 }

type fakeEnq struct {
	atCalled bool
	at       time.Time
	inCalled bool
	in       time.Duration
	// inErr makes EnqueueAdvanceIn fail (still recording the call/delay), to
	// exercise the fail-then-retry double-count regression for the
	// "deferred" branches.
	inErr error
	// evaluated collects the campaign ids a breaker evaluation was enqueued for,
	// so a test can assert a finalised send triggers exactly one.
	evaluated   []string
	evaluateErr error
}

func (f *fakeEnq) EnqueueAdvanceAt(_, _ string, t time.Time) error {
	f.atCalled, f.at = true, t
	return nil
}
func (f *fakeEnq) EnqueueAdvanceIn(_, _ string, d time.Duration) error {
	f.inCalled, f.in = true, d
	return f.inErr
}
func (f *fakeEnq) EnqueueDeliverabilityEvaluate(campaignID, _ string) error {
	if f.evaluateErr != nil {
		return f.evaluateErr
	}
	f.evaluated = append(f.evaluated, campaignID)
	return nil
}

func advanceTask(t *testing.T) *asynq.Task {
	t.Helper()
	b, err := json.Marshal(queue.AdvancePayload{EnrollmentID: "e", WorkspaceID: "w"})
	if err != nil {
		t.Fatal(err)
	}
	return asynq.NewTask(queue.TaskSequenceAdvance, b)
}

const testBaseURL = "https://app.test"

var testTrackingSecret = []byte("0123456789abcdef0123456789abcdef")

// run drives one advance with no Metrics injected (nil) — every existing
// case in this file exercises AdvanceHandler's nil-safety for free: none of
// them need their own "metrics == nil" branch.
func run(t *testing.T, core coreapi.Client, s Sender, enq Enqueuer) error {
	t.Helper()
	return AdvanceHandler(core, s, enq, testBaseURL, testTrackingSecret, nil)(context.Background(), advanceTask(t))
}

// runWithMetrics is like run but injects a real *metrics.Metrics, for tests
// that assert on inroad_sends_total.
func runWithMetrics(t *testing.T, core coreapi.Client, s Sender, enq Enqueuer, mtx *metrics.Metrics) error {
	t.Helper()
	return AdvanceHandler(core, s, enq, testBaseURL, testTrackingSecret, mtx)(context.Background(), advanceTask(t))
}

func TestAdvanceSkipIsNoOp(t *testing.T) {
	core := &stubCore{job: coreapi.StepSendJob{Skip: true}}
	snd, enq := &fakeSender{}, &fakeEnq{}
	if err := run(t, core, snd, enq); err != nil {
		t.Fatal(err)
	}
	if snd.called() || enq.atCalled || enq.inCalled || core.stopped != "" || core.finalized != nil || core.claimCalls != 0 {
		t.Fatal("skip must be a pure no-op (no claim, no send)")
	}
}

func TestAdvanceSuppressedStops(t *testing.T) {
	core := &stubCore{job: coreapi.StepSendJob{Suppressed: true, EffectiveDailyCap: 100}}
	snd := &fakeSender{}
	if err := run(t, core, snd, &fakeEnq{}); err != nil {
		t.Fatal(err)
	}
	if core.stopped != "suppressed" {
		t.Fatalf("want stop reason suppressed, got %q", core.stopped)
	}
	if snd.called() || core.claimCalls != 0 {
		t.Fatal("suppressed contact must not be claimed or emailed")
	}
}

// A thread whose sending mailbox was deleted must stop, not send a follow-up from
// a different address referencing a Message-ID that address never sent. Note the
// job carries no cap/transport at all — the control plane returns the stop before
// resolving a sender — so the handler must act on MailboxRemoved BEFORE the
// degenerate-cap branch, or the enrollment would stop as 'failed' instead.
func TestAdvanceMailboxRemovedStops(t *testing.T) {
	core := &stubCore{job: coreapi.StepSendJob{MailboxRemoved: true}}
	snd, enq := &fakeSender{}, &fakeEnq{}
	if err := run(t, core, snd, enq); err != nil {
		t.Fatal(err)
	}
	if core.stopped != "mailbox_removed" {
		t.Fatalf("want stop reason mailbox_removed, got %q", core.stopped)
	}
	if snd.called() || core.claimCalls != 0 || enq.atCalled || enq.inCalled {
		t.Fatal("a thread with no mailbox must not be claimed, emailed, or re-enqueued")
	}
}

func TestAdvanceOverCapReEnqueues(t *testing.T) {
	core := &stubCore{job: coreapi.StepSendJob{EffectiveDailyCap: 50, SentToday: 50}}
	snd, enq := &fakeSender{}, &fakeEnq{}
	if err := run(t, core, snd, enq); err != nil {
		t.Fatal(err)
	}
	if snd.called() || core.claimCalls != 0 {
		t.Fatal("over-cap step must not claim or send")
	}
	// The cap clears when sent_today resets at the UTC boundary, so the retry targets
	// that rather than a fixed poll. The exact arithmetic is pinned by the
	// nextAttemptIn tests.
	if !enq.inCalled || enq.in <= 0 || enq.in > capBackoff+24*time.Hour {
		t.Fatalf("expected a bounded positive retry wait, got called=%v in=%v", enq.inCalled, enq.in)
	}
	if core.finalized != nil {
		t.Fatal("over-cap must not finalize/advance the cursor")
	}
}

// The campaign has hit campaigns.daily_limit. Every mailbox still has capacity, so
// nothing about the mailbox numbers says "wait" — the explicit flag has to, and it
// must defer rather than stop: the campaign gets a fresh allowance tomorrow.
func TestAdvanceCampaignLimitedDefers(t *testing.T) {
	core := &stubCore{job: coreapi.StepSendJob{CampaignLimited: true}}
	snd, enq := &fakeSender{}, &fakeEnq{}
	if err := run(t, core, snd, enq); err != nil {
		t.Fatal(err)
	}
	if snd.called() || core.claimCalls != 0 {
		t.Fatal("a campaign at its daily limit must not claim or send")
	}
	if core.stopped != "" {
		t.Fatalf("a campaign limit must not stop the enrollment, got stop reason %q", core.stopped)
	}
	// The wait targets the UTC day rollover, when the allowance resets — the exact
	// arithmetic is pinned by TestBlockedBackoffWaitsForTheMomentTheBlockCanClear.
	if !enq.inCalled || enq.in <= 0 || enq.in > 24*time.Hour {
		t.Fatalf("re-enqueue delay = %v (called=%v), want a positive wait within the day",
			enq.in, enq.inCalled)
	}
}

// The campaign has hit campaigns.max_new_leads_per_day. coreapi only ever sets
// this flag on a step-1 job (never a follow-up), so the handler doesn't
// re-check StepOrder here — it just has to defer exactly like CampaignLimited.
func TestAdvanceNewLeadLimitedDefers(t *testing.T) {
	core := &stubCore{job: coreapi.StepSendJob{NewLeadLimited: true}}
	snd, enq := &fakeSender{}, &fakeEnq{}
	if err := run(t, core, snd, enq); err != nil {
		t.Fatal(err)
	}
	if snd.called() || core.claimCalls != 0 {
		t.Fatal("a campaign at its new-lead limit must not claim or send")
	}
	if core.stopped != "" {
		t.Fatalf("a new-lead limit must not stop the enrollment, got stop reason %q", core.stopped)
	}
	// The wait targets the UTC day rollover, same as CampaignLimited.
	if !enq.inCalled || enq.in <= 0 || enq.in > 24*time.Hour {
		t.Fatalf("re-enqueue delay = %v (called=%v), want a positive wait within the day",
			enq.in, enq.inCalled)
	}
}

// Invariant 3: the warmup engine has paused the mailbox this thread must send from.
// The job carries no capacity of its own (the mailbox has plenty), so without the
// flag being checked BEFORE the degenerate-cap branch this enrollment would be
// STOPPED — and the mailbox may recover, while the thread cannot move to another
// mailbox mid-sequence.
func TestAdvanceHealthPausedDefersRatherThanStopping(t *testing.T) {
	core := &stubCore{job: coreapi.StepSendJob{HealthPaused: true, EffectiveDailyCap: 0}}
	snd, enq := &fakeSender{}, &fakeEnq{}
	if err := run(t, core, snd, enq); err != nil {
		t.Fatal(err)
	}
	if core.stopped != "" {
		t.Fatalf("a paused mailbox must not stop the enrollment, got stop reason %q", core.stopped)
	}
	if snd.called() || core.claimCalls != 0 {
		t.Fatal("a paused mailbox must not claim or send")
	}
	if !enq.inCalled || enq.in != capBackoff {
		t.Fatalf("expected re-enqueue in %v, got called=%v in=%v", capBackoff, enq.inCalled, enq.in)
	}
}

// A blocked thread waits INDEFINITELY: over many more attempts than the
// cap-deferral ceiling allows, it stays active and keeps being re-scheduled. The
// spelled-out form of the budget rule — a campaign that needs 100 days to finish
// must still be running on day 8.
func TestBlockedEnrollmentStaysActivePastTheDeferralCeiling(t *testing.T) {
	core := &stubCore{job: coreapi.StepSendJob{CampaignLimited: true}}
	snd, enq := &fakeSender{}, &fakeEnq{}
	for attempt := range maxCapDeferrals + 5 {
		enq.inCalled = false
		if err := run(t, core, snd, enq); err != nil {
			t.Fatalf("attempt %d: %v", attempt, err)
		}
		if core.stopped != "" {
			t.Fatalf("attempt %d: enrollment stopped %q; a campaign limit is a setting, not a failure",
				attempt, core.stopped)
		}
		if !enq.inCalled {
			t.Fatalf("attempt %d: no retry scheduled; the thread would never resume", attempt)
		}
	}
	if core.incrCalls != 0 {
		t.Errorf("cap-deferral budget consumed %d times across %d attempts, want never",
			core.incrCalls, maxCapDeferrals+5)
	}
	if snd.called() || core.claimCalls != 0 {
		t.Error("a blocked thread sent something")
	}
}

func TestAdvanceMailboxSpacingReEnqueuesWithoutSending(t *testing.T) {
	core := &stubCore{
		job: coreapi.StepSendJob{
			EffectiveDailyCap: 100, MinIntervalSeconds: 90,
		},
		claimDeferred: true,
	}
	snd, enq := &fakeSender{}, &fakeEnq{}
	if err := run(t, core, snd, enq); err != nil {
		t.Fatal(err)
	}
	if snd.called() || core.advanceCalls != 0 {
		t.Fatal("spacing-deferred step must not send or advance")
	}
	if !enq.inCalled || enq.in != 90*time.Second {
		t.Fatalf("expected retry after mailbox interval, got called=%v in=%v", enq.inCalled, enq.in)
	}
}

// TestAdvanceNotYetDueWaitsOutTheNewDueTime covers the out-of-office deferral's
// worker half: the task queued for the OLD due time cannot be cancelled, so it
// runs, the claim refuses it (ClaimDeferred), and this handler must reschedule
// for the NEW due time rather than the mailbox's min-interval — which would
// fail the same guard again seconds later, for the whole absence.
//
// Critically, nothing is sent and the cursor does not move: the recipient said
// they were away.
func TestAdvanceNotYetDueWaitsOutTheNewDueTime(t *testing.T) {
	until := time.Now().Add(72 * time.Hour)
	core := &stubCore{
		job: coreapi.StepSendJob{
			EnrollmentID: "e", EffectiveDailyCap: 100, MinIntervalSeconds: 90,
			NotDueUntil: until,
		},
		claimDeferred: true,
	}
	snd, enq := &fakeSender{}, &fakeEnq{}
	if err := run(t, core, snd, enq); err != nil {
		t.Fatal(err)
	}
	if snd.called() || core.advanceCalls != 0 {
		t.Fatal("a step deferred into a stated absence must not send or advance")
	}
	if !enq.inCalled {
		t.Fatal("expected a re-enqueue")
	}
	// The delay must clear the new due time. An exact value would pin the
	// window-snapping policy; what matters is that it is not the 90s spacing.
	if enq.in < 71*time.Hour {
		t.Fatalf("expected a retry after the new due time (~72h), got %v", enq.in)
	}
}

func TestAdvanceZeroCapStopsFailed(t *testing.T) {
	// Degenerate cap: cannot ever send. Must stop 'failed', not defer forever.
	core := &stubCore{job: coreapi.StepSendJob{EffectiveDailyCap: 0, SentToday: 0}}
	snd, enq := &fakeSender{}, &fakeEnq{}
	if err := run(t, core, snd, enq); err != nil {
		t.Fatal(err)
	}
	if core.stopped != "failed" {
		t.Fatalf("want stop reason failed, got %q", core.stopped)
	}
	if enq.inCalled || enq.atCalled || snd.called() || core.incrCalls != 0 {
		t.Fatalf("zero-cap must stop without deferring/sending: %+v", core)
	}
}

// A mailbox daily cap is self-clearing — sent_today is counted over the UTC day, and
// the only cap that can never clear is cap <= 0, which stops outright above. So
// being over cap must never fail the enrollment, however long it has waited: the old
// count-based ceiling killed the tail of every launch bigger than a day's capacity
// (1000 contacts at 50/day → everything past ~400 failed at ~7.5 days).
func TestAdvanceOverCapNeverFailsTheEnrollmentHoweverLongItWaits(t *testing.T) {
	core := &stubCore{
		job:       coreapi.StepSendJob{EffectiveDailyCap: 50, SentToday: 50},
		deferrals: maxCapDeferrals * 100,
	}
	snd, enq := &fakeSender{}, &fakeEnq{}
	if err := run(t, core, snd, enq); err != nil {
		t.Fatal(err)
	}
	if core.stopped != "" {
		t.Fatalf("a self-clearing cap must not stop the enrollment, got %q", core.stopped)
	}
	if snd.called() {
		t.Fatal("over cap must not send")
	}
	if !enq.inCalled || enq.in <= 0 {
		t.Fatalf("expected a positive retry wait, got called=%v in=%v", enq.inCalled, enq.in)
	}
	// Still counted, so a genuinely stuck loop remains observable.
	if core.incrCalls != 1 {
		t.Errorf("deferral counter incremented %d times, want 1", core.incrCalls)
	}
}

// The cap clears when sent_today resets, so the retry targets the UTC boundary
// rather than polling — and lands inside the campaign's window, because a deferred
// retry sends the moment it runs.
func TestAdvanceOverCapRetriesAtTheDayBoundaryInsideTheWindow(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatalf("LoadLocation: %v", err)
	}
	sched := cadence.Schedule{Timezone: "America/New_York"}
	for d := int(time.Monday); d <= int(time.Friday); d++ {
		sched.Windows = append(sched.Windows,
			cadence.SendWindow{Weekday: d, StartMinute: 9 * 60, EndMinute: 17 * 60})
	}
	win, err := sched.Compile()
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	core := &stubCore{job: coreapi.StepSendJob{EffectiveDailyCap: 50, SentToday: 50, Schedule: sched}}
	snd, enq := &fakeSender{}, &fakeEnq{}
	if err := run(t, core, snd, enq); err != nil {
		t.Fatal(err)
	}
	if !enq.inCalled {
		t.Fatal("no retry scheduled")
	}
	at := time.Now().Add(enq.in)
	if !win.Contains(at) {
		t.Errorf("retry at %s (%s local) is outside the campaign's window", at.UTC(), at.In(loc))
	}
}

func TestAdvanceSendsAndSchedulesNext(t *testing.T) {
	next := time.Now().Add(48 * time.Hour)
	core := &stubCore{
		job: coreapi.StepSendJob{
			EffectiveDailyCap: 100, SentToday: 0, ToEmail: "a@b.io",
			Subject: "Hi", BodyText: "yo", InReplyTo: "<root@x>", References: "<root@x>",
		},
		claimOK: true,
		adv:     coreapi.Advance{Completed: false, NextDueAt: next},
	}
	snd, enq := &fakeSender{id: "<mid@x>"}, &fakeEnq{}
	if err := run(t, core, snd, enq); err != nil {
		t.Fatal(err)
	}
	if !snd.called() {
		t.Fatal("expected a send")
	}
	if snd.sent.InReplyTo != "<root@x>" || snd.sent.References != "<root@x>" {
		t.Fatalf("threading headers not passed through: %+v", snd.sent)
	}
	if core.delivered == nil || *core.delivered != "<mid@x>" {
		t.Fatalf("MarkStepDelivered must record the message id, got %v", core.delivered)
	}
	if core.advanceCalls != 1 {
		t.Fatalf("success path must advance the cursor exactly once, got %d", core.advanceCalls)
	}
	if core.finalized != nil {
		t.Fatalf("success path must NOT use the failed-finalize, got %+v", core.finalized)
	}
	if !enq.atCalled || !enq.at.Equal(next) {
		t.Fatalf("next advance not scheduled at NextDueAt: called=%v at=%v", enq.atCalled, enq.at)
	}
}

// TestAdvanceSpintaxIsByteIdenticalOnRetry proves the retry-safety property
// spintax.Seed(job.SendID, ...) exists for: a "retried" job -- rebuilt fresh
// from the same immutable DB row, so it carries the identical SendID and raw
// spin content -- resolves the SAME variant both times, rather than rolling
// a different one on each attempt. Modeled here as two entirely independent
// handler invocations sharing one job value (standing in for two asynq
// attempts of the same task, or a crash-and-reclaim): if the build path drew
// fresh randomness instead of seeding on SendID, the two sends below would
// disagree.
func TestAdvanceSpintaxIsByteIdenticalOnRetry(t *testing.T) {
	job := coreapi.StepSendJob{
		EffectiveDailyCap: 100, ToEmail: "a@b.io",
		SendID:   "22222222-2222-4222-8222-222222222222",
		Subject:  "Hi {there|friend}",
		BodyText: "{Thanks|Cheers} for reading",
		BodyHTML: "<p>{Hello|Hi} world</p>",
	}

	first := &fakeSender{id: "<mid@x>"}
	if err := run(t, &stubCore{job: job, claimOK: true}, first, &fakeEnq{}); err != nil {
		t.Fatal(err)
	}
	second := &fakeSender{id: "<mid@x>"}
	if err := run(t, &stubCore{job: job, claimOK: true}, second, &fakeEnq{}); err != nil {
		t.Fatal(err)
	}

	if first.sent.Subject != second.sent.Subject {
		t.Fatalf("subject drifted across a retry: %q vs %q", first.sent.Subject, second.sent.Subject)
	}
	if first.sent.BodyText != second.sent.BodyText {
		t.Fatalf("body text drifted across a retry: %q vs %q", first.sent.BodyText, second.sent.BodyText)
	}
	if first.sent.BodyHTML != second.sent.BodyHTML {
		t.Fatalf("body html drifted across a retry: %q vs %q", first.sent.BodyHTML, second.sent.BodyHTML)
	}
	// Sanity: the spin groups actually resolved to a concrete option rather
	// than passing through as literal, unresolved syntax.
	if strings.Contains(first.sent.Subject, "|") || strings.Contains(first.sent.BodyText, "|") || strings.Contains(first.sent.BodyHTML, "|") {
		t.Fatalf("spintax was not resolved: subject=%q body_text=%q body_html=%q", first.sent.Subject, first.sent.BodyText, first.sent.BodyHTML)
	}
}

// A newly delivered send triggers a circuit-breaker evaluation, because the send
// changes the sample the breaker judges on - and crossing the minimum-delivered
// floor is itself the trigger for a campaign whose early sends all bounced.
//
// It is enqueued as a task, not called inline: the evaluation must read committed
// state from outside the send path, so a scoring bug cannot fail a delivery.
func TestAdvanceEnqueuesABreakerEvaluationAfterDelivery(t *testing.T) {
	core := &stubCore{
		job: coreapi.StepSendJob{
			CampaignID: "camp-1", EffectiveDailyCap: 100, ToEmail: "a@b.io", Subject: "Hi", BodyText: "yo",
		},
		claimOK: true,
		adv:     coreapi.Advance{Completed: true},
	}
	snd, enq := &fakeSender{id: "<mid@x>"}, &fakeEnq{}
	if err := run(t, core, snd, enq); err != nil {
		t.Fatal(err)
	}
	if len(enq.evaluated) != 1 || enq.evaluated[0] != "camp-1" {
		t.Fatalf("breaker evaluations enqueued = %v, want [camp-1]", enq.evaluated)
	}
}

// A failed enqueue must not fail the delivery: the send is already durable, and
// the next finalised send in the campaign enqueues another evaluation anyway.
func TestAdvanceSucceedsWhenTheBreakerEnqueueFails(t *testing.T) {
	core := &stubCore{
		job: coreapi.StepSendJob{
			CampaignID: "camp-1", EffectiveDailyCap: 100, ToEmail: "a@b.io", Subject: "Hi", BodyText: "yo",
		},
		claimOK: true,
		adv:     coreapi.Advance{Completed: true},
	}
	snd, enq := &fakeSender{id: "<mid@x>"}, &fakeEnq{evaluateErr: errors.New("redis down")}
	if err := run(t, core, snd, enq); err != nil {
		t.Fatalf("a delivered send failed because the breaker enqueue did: %v", err)
	}
	if core.delivered == nil {
		t.Fatal("the delivery was not recorded")
	}
}

// A send that FAILED changes neither the delivered nor the bounced count, so it
// triggers no evaluation - the breaker has nothing new to judge.
func TestAdvanceDoesNotEvaluateAfterAPermanentFailure(t *testing.T) {
	core := &stubCore{
		job: coreapi.StepSendJob{
			CampaignID: "camp-1", EffectiveDailyCap: 100, ToEmail: "a@b.io", Subject: "Hi", BodyText: "yo",
		},
		claimOK: true,
	}
	snd, enq := &fakeSender{err: errors.New("550 no such user")}, &fakeEnq{}
	if err := run(t, core, snd, enq); err != nil {
		t.Fatal(err)
	}
	if len(enq.evaluated) != 0 {
		t.Errorf("a failed send enqueued %v", enq.evaluated)
	}
}

func TestAdvanceCompletedDoesNotReschedule(t *testing.T) {
	core := &stubCore{
		job:     coreapi.StepSendJob{EffectiveDailyCap: 100, ToEmail: "a@b.io", Subject: "Bye", BodyText: "end"},
		claimOK: true,
		adv:     coreapi.Advance{Completed: true},
	}
	snd, enq := &fakeSender{id: "<m>"}, &fakeEnq{}
	if err := run(t, core, snd, enq); err != nil {
		t.Fatal(err)
	}
	if enq.atCalled {
		t.Fatal("completed enrollment must not schedule another advance")
	}
}

// TestAdvanceInjectsTrackingWhenEnabled proves the step-send path rewrites
// the HTML body's links and appends an open pixel when the job's campaign has
// tracking enabled, using the job's (pre-generated) SendID as the token
// subject, and leaves the unsubscribe link untouched.
func TestAdvanceInjectsTrackingWhenEnabled(t *testing.T) {
	core := &stubCore{claimOK: true, job: coreapi.StepSendJob{
		EffectiveDailyCap: 100, SentToday: 0, ToEmail: "a@b.io", SendID: "22222222-2222-4222-8222-222222222222",
		Subject: "Hi", BodyText: "yo",
		BodyHTML:        `<html><body><a href="https://example.com/x">click</a></body></html>`,
		UnsubURL:        testBaseURL + "/u/tok",
		TrackingEnabled: true,
	}}
	snd, enq := &fakeSender{id: "<mid@x>"}, &fakeEnq{}
	if err := run(t, core, snd, enq); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(snd.sent.BodyHTML, "/t/c/") || !strings.Contains(snd.sent.BodyHTML, "/t/o/") {
		t.Errorf("expected rewritten click link + open pixel, got %q", snd.sent.BodyHTML)
	}
	if !strings.Contains(snd.sent.BodyHTML, testBaseURL+"/u/tok") {
		t.Errorf("unsubscribe link must remain untouched, got %q", snd.sent.BodyHTML)
	}
}

// TestAdvanceSkipsTrackingWhenDisabled proves TrackingEnabled=false leaves the
// step's HTML body unrewritten.
func TestAdvanceSkipsTrackingWhenDisabled(t *testing.T) {
	html := `<html><body><a href="https://example.com/x">click</a></body></html>`
	core := &stubCore{claimOK: true, job: coreapi.StepSendJob{
		EffectiveDailyCap: 100, ToEmail: "a@b.io", SendID: "22222222-2222-4222-8222-222222222222",
		Subject: "Hi", BodyText: "yo", BodyHTML: html, TrackingEnabled: false,
	}}
	snd, enq := &fakeSender{id: "<mid@x>"}, &fakeEnq{}
	if err := run(t, core, snd, enq); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(snd.sent.BodyHTML, "/t/c/") || strings.Contains(snd.sent.BodyHTML, "/t/o/") {
		t.Errorf("tracking disabled must leave the HTML unrewritten, got %q", snd.sent.BodyHTML)
	}
}

func TestAdvancePermanentSendFailsForwardAndAdvances(t *testing.T) {
	// A 5xx-style permanent error (not classified Retryable): finalize 'failed'
	// and advance the cursor so one bad step doesn't wedge the enrollment.
	next := time.Now().Add(time.Hour)
	core := &stubCore{
		claimOK: true,
		job:     coreapi.StepSendJob{EffectiveDailyCap: 100, ToEmail: "a@b.io", Subject: "Hi", BodyText: "yo"},
		adv:     coreapi.Advance{Completed: false, NextDueAt: next},
	}
	snd, enq := &fakeSender{err: errors.New("smtp 550 no such user")}, &fakeEnq{}
	if err := run(t, core, snd, enq); err != nil {
		t.Fatal(err)
	}
	if core.finalized == nil || core.finalized.Status != "failed" {
		t.Fatalf("expected failed result finalized, got %+v", core.finalized)
	}
	if core.released {
		t.Fatal("a permanent failure must NOT release the claim")
	}
	if !enq.atCalled {
		t.Fatal("fail-forward: should still schedule the next step")
	}
}

func TestAdvanceTransientSendReleasesAndReturnsError(t *testing.T) {
	// A transient error (net timeout) must release the claim and return the error
	// so asynq retries — and must NOT finalize the send as failed or advance.
	core := &stubCore{
		claimOK: true,
		job:     coreapi.StepSendJob{EffectiveDailyCap: 100, ToEmail: "a@b.io", Subject: "Hi", BodyText: "yo"},
	}
	transient := &net.OpError{Op: "dial", Err: timeoutStub{}}
	snd, enq := &fakeSender{err: transient}, &fakeEnq{}
	err := run(t, core, snd, enq)
	if err == nil {
		t.Fatal("transient failure must return an error so asynq retries")
	}
	if !core.released {
		t.Fatal("transient failure must release the claim")
	}
	if core.finalized != nil {
		t.Fatalf("transient failure must NOT finalize the send, got %+v", core.finalized)
	}
	if enq.atCalled {
		t.Fatal("transient failure must NOT advance the cursor / schedule next")
	}
}

// timeoutStub is a net.Error reporting a timeout, used to force the transient
// classification in mail.Retryable.
type timeoutStub struct{}

func (timeoutStub) Error() string   { return "i/o timeout" }
func (timeoutStub) Timeout() bool   { return true }
func (timeoutStub) Temporary() bool { return true }

// TestAdvanceDoubleSendDeliversOnce is the headline regression: invoking the
// advance handler twice for the SAME enrollment/step delivers exactly once. The
// second claim loses (the row is already owned/delivered), so Send is never
// called a second time.
func TestAdvanceDoubleSendDeliversOnce(t *testing.T) {
	core := &stubCore{
		claimOK: true,
		job: coreapi.StepSendJob{
			EffectiveDailyCap: 100, ToEmail: "a@b.io", Subject: "Hi", BodyText: "yo",
		},
		adv: coreapi.Advance{Completed: true},
	}
	snd, enq := &fakeSender{id: "<mid@x>"}, &fakeEnq{}

	if err := run(t, core, snd, enq); err != nil {
		t.Fatalf("first advance: %v", err)
	}
	if err := run(t, core, snd, enq); err != nil {
		t.Fatalf("second advance: %v", err)
	}

	if core.claimCalls != 2 {
		t.Fatalf("expected two claim attempts, got %d", core.claimCalls)
	}
	if snd.calls != 1 {
		t.Fatalf("double-send bug: Send must be called EXACTLY once, got %d", snd.calls)
	}
}

// TestAdvanceThreadsAllowPlaintext proves the persisted per-mailbox
// allow_plaintext policy (carried on StepSendJob) reaches the transport via
// OutboundJob.AllowPlaintext, so a send applies the SAME TLS rule the
// connect-test validated (MAJOR 2). Default (false) keeps TLS enforced.
func TestAdvanceThreadsAllowPlaintext(t *testing.T) {
	for _, allow := range []bool{true, false} {
		core := &stubCore{
			claimOK: true,
			job: coreapi.StepSendJob{
				EffectiveDailyCap: 100, ToEmail: "a@b.io", Subject: "Hi", BodyText: "yo",
				AllowPlaintext: allow,
			},
			adv: coreapi.Advance{Completed: true},
		}
		snd, enq := &fakeSender{id: "<mid@x>"}, &fakeEnq{}
		if err := run(t, core, snd, enq); err != nil {
			t.Fatal(err)
		}
		if snd.job.AllowPlaintext != allow {
			t.Fatalf("OutboundJob.AllowPlaintext = %v, want %v", snd.job.AllowPlaintext, allow)
		}
	}
}

// TestAdvanceRecoverForwardAfterCursorAdvanceFailure is the MAJOR-1 regression:
// Send succeeds and the delivery is recorded (status='sent'), but the SEPARATE
// cursor advance fails on the first handler run, so the handler returns an error
// and asynq retries. On the retry the claim sees the 'sent' row (ClaimAlreadySent)
// and RECOVER-FORWARDS — it advances the cursor WITHOUT re-sending. Send is called
// exactly once across both runs.
func TestAdvanceRecoverForwardAfterCursorAdvanceFailure(t *testing.T) {
	core := &stubCore{
		claimOK:     true,
		advanceFail: 1, // first AdvanceStepCursor call errors; the retry succeeds
		job:         coreapi.StepSendJob{EffectiveDailyCap: 100, ToEmail: "a@b.io", Subject: "Hi", BodyText: "yo"},
		adv:         coreapi.Advance{Completed: true},
	}
	snd, enq := &fakeSender{id: "<mid@x>"}, &fakeEnq{}

	// Run 1: delivers, records 'sent', then the cursor advance fails → error.
	if err := run(t, core, snd, enq); err == nil {
		t.Fatal("first run: a cursor-advance failure must surface so asynq retries")
	}
	if core.delivered == nil || *core.delivered != "<mid@x>" {
		t.Fatalf("first run must durably record the delivery, got %v", core.delivered)
	}

	// Run 2 (retry): claim sees 'sent' → recover-forward, cursor advances, no re-send.
	if err := run(t, core, snd, enq); err != nil {
		t.Fatalf("second run: recover-forward must succeed, got %v", err)
	}
	if snd.calls != 1 {
		t.Fatalf("Send must be called EXACTLY once across both runs, got %d", snd.calls)
	}
	if core.advanceCalls != 2 {
		t.Fatalf("cursor advance should be attempted on both runs (fail, then succeed), got %d", core.advanceCalls)
	}
}

// A campaign at its daily_limit is correctly configured, not broken: daily_limit 10
// over 1000 contacts is a 100-day campaign. Charging its waiting enrollments against
// the cap-deferral budget (30 × 6h ≈ 7.5 days) would mark ~99% of them 'failed' for
// doing exactly what the operator asked. So these deferrals must not touch the
// budget, and must survive far past it.
func TestCampaignLimitedDeferralNeverConsumesTheFailBudget(t *testing.T) {
	for _, tc := range []struct {
		name string
		job  coreapi.StepSendJob
	}{
		{"campaign at its daily limit", coreapi.StepSendJob{CampaignLimited: true}},
		{"campaign at its new-lead-per-day limit", coreapi.StepSendJob{NewLeadLimited: true}},
		{"mailbox paused by warmup", coreapi.StepSendJob{HealthPaused: true, EffectiveDailyCap: 50}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// deferrals already far past the ceiling: if this path consulted the
			// budget at all, the enrollment would be stopped here.
			core := &stubCore{job: tc.job, deferrals: maxCapDeferrals * 10}
			enq := &fakeEnq{}

			if err := run(t, core, &fakeSender{}, enq); err != nil {
				t.Fatalf("handler: %v", err)
			}
			if core.incrCalls != 0 {
				t.Errorf("consumed the cap-deferral budget %d time(s); these blocks are not misconfigurations", core.incrCalls)
			}
			if core.stopped != "" {
				t.Errorf("enrollment stopped %q; a self-clearing block must leave it active", core.stopped)
			}
			if !enq.inCalled {
				t.Error("no retry scheduled; the thread would never resume")
			}
			if enq.in <= 0 {
				t.Errorf("retry delay = %s, want a positive wait", enq.in)
			}
		})
	}
}

func TestBlockedBackoffWaitsForTheMomentTheBlockCanClear(t *testing.T) {
	// 22:30 UTC → the campaign's allowance resets in 90 minutes.
	evening := time.Date(2026, 8, 1, 22, 30, 0, 0, time.UTC)

	cases := []struct {
		name string
		job  coreapi.StepSendJob
		now  time.Time
		want time.Duration
	}{
		{
			name: "campaign limit waits for the UTC day to roll over",
			job:  coreapi.StepSendJob{CampaignLimited: true},
			now:  evening,
			want: 90 * time.Minute,
		},
		{
			// The new-lead throttle resets on the same UTC boundary as the daily
			// limit — CountFirstStepSendsToday is counted over the identical UTC
			// calendar day.
			name: "new-lead limit waits for the same UTC day rollover",
			job:  coreapi.StepSendJob{NewLeadLimited: true},
			now:  evening,
			want: 90 * time.Minute,
		},
		{
			// A pause clears when the health sweep steps it down, on no schedule this
			// worker can predict, so it reuses the ordinary cap backoff.
			name: "health pause reuses the cap backoff",
			job:  coreapi.StepSendJob{HealthPaused: true},
			now:  evening,
			want: capBackoff,
		},
		{
			// Both blocked: re-check at the sooner of the two.
			name: "both blocked takes the shorter wait",
			job:  coreapi.StepSendJob{CampaignLimited: true, HealthPaused: true},
			now:  evening,
			want: 90 * time.Minute,
		},
		{
			name: "both blocked early in the day still takes the shorter wait",
			job:  coreapi.StepSendJob{CampaignLimited: true, HealthPaused: true},
			now:  time.Date(2026, 8, 1, 1, 0, 0, 0, time.UTC),
			want: capBackoff,
		},
		{
			// Landing in the last seconds of the day must not re-enqueue instantly
			// and spin.
			name: "a wait at the very end of the day is floored",
			job:  coreapi.StepSendJob{CampaignLimited: true},
			now:  time.Date(2026, 8, 1, 23, 59, 59, 0, time.UTC),
			want: minBlockedBackoff,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := blockedBackoff(tc.job, tc.now); got != tc.want {
				t.Errorf("blockedBackoff = %s, want %s", got, tc.want)
			}
		})
	}
}

// A non-UTC local clock must not shift the day boundary the limit resets on.
func TestBlockedBackoffUsesTheUTCDayBoundary(t *testing.T) {
	loc, err := time.LoadLocation("Asia/Karachi") // UTC+5, no DST
	if err != nil {
		t.Fatalf("LoadLocation: %v", err)
	}
	// 03:30 local on 2 Aug is 22:30 UTC on 1 Aug — 90 minutes from the UTC rollover,
	// not from the local one.
	now := time.Date(2026, 8, 2, 3, 30, 0, 0, loc)
	if got := blockedBackoff(coreapi.StepSendJob{CampaignLimited: true}, now); got != 90*time.Minute {
		t.Errorf("blockedBackoff = %s, want 1h30m from the UTC boundary", got)
	}
}

// A deferred retry re-enters the SEND flow rather than re-scheduling through the
// cadence engine, so the instant it wakes on is the instant it sends. Waking at the
// raw block-clears moment would send outside the campaign's window — and for a
// campaign-limit retry that moment is 00:00 UTC, i.e. 20:00 in New York, so the
// violation would be systematic rather than occasional.
func TestBlockedBackoffWakesInsideTheCampaignsSendWindow(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatalf("LoadLocation: %v", err)
	}
	businessHours := cadence.Schedule{Timezone: "America/New_York"}
	for d := int(time.Monday); d <= int(time.Friday); d++ {
		businessHours.Windows = append(businessHours.Windows,
			cadence.SendWindow{Weekday: d, StartMinute: 9 * 60, EndMinute: 17 * 60})
	}
	win, err := businessHours.Compile()
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	// Tuesday 18:00 EDT = Tuesday 22:00 UTC. The limit clears at 00:00 UTC, which is
	// 20:00 EDT — outside the window — so the retry must land Wednesday morning.
	now := time.Date(2026, 8, 4, 18, 0, 0, 0, loc)
	job := coreapi.StepSendJob{EnrollmentID: "e", CampaignLimited: true, Schedule: businessHours}

	got := now.Add(blockedBackoff(job, now))
	if !win.Contains(got) {
		t.Fatalf("retry scheduled for %s (%s local), outside the campaign's window", got.UTC(), got.In(loc))
	}
	if local := got.In(loc); local.Hour() < 9 || local.Hour() >= 17 {
		t.Errorf("retry local hour = %d, want business hours", local.Hour())
	}
	// It must still be after the block clears, not before it.
	if !got.After(now) {
		t.Errorf("retry %s is not after now %s", got, now)
	}
}

// A job carrying no usable schedule must still retry rather than refuse to.
func TestBlockedBackoffWithoutAScheduleKeepsTheRawInstant(t *testing.T) {
	now := time.Date(2026, 8, 1, 22, 30, 0, 0, time.UTC)
	job := coreapi.StepSendJob{EnrollmentID: "e", CampaignLimited: true} // no Schedule
	if got := blockedBackoff(job, now); got != 90*time.Minute {
		t.Errorf("blockedBackoff = %s, want 1h30m (the raw UTC rollover)", got)
	}
}

// sendsCount scrapes mtx's real Prometheus registry and returns the current
// inroad_sends_total value for kind="campaign" + result, or 0 if that series
// doesn't exist yet (a bucket AdvanceHandler never hit in this test).
func sendsCount(t *testing.T, mtx *metrics.Metrics, result string) float64 {
	t.Helper()
	families := metricstest.Scrape(t, mtx)
	return metricstest.CounterValue(families, "inroad_sends_total", map[string]string{"kind": "campaign", "result": result})
}

// TestAdvanceRecordsSendFinalizedMetrics proves each of the finalize points
// AdvanceHandler passes through increments inroad_sends_total{kind="campaign"}
// with the outcome bucket it actually belongs to.
func TestAdvanceRecordsSendFinalizedMetrics(t *testing.T) {
	cases := []struct {
		name string
		core *stubCore
		snd  *fakeSender
		enq  *fakeEnq
		want string // the one bucket that must have incremented by 1
	}{
		{
			name: "already-inactive enrollment is skipped",
			core: &stubCore{job: coreapi.StepSendJob{Skip: true}},
			snd:  &fakeSender{}, enq: &fakeEnq{}, want: "skipped",
		},
		{
			name: "suppressed contact is skipped",
			core: &stubCore{job: coreapi.StepSendJob{Suppressed: true, EffectiveDailyCap: 100}},
			snd:  &fakeSender{}, enq: &fakeEnq{}, want: "skipped",
		},
		{
			name: "another worker's live claim is skipped",
			core: &stubCore{job: coreapi.StepSendJob{EffectiveDailyCap: 100}}, // claimOK unset -> ClaimSkip
			snd:  &fakeSender{}, enq: &fakeEnq{}, want: "skipped",
		},
		{
			name: "mailbox removed is failed",
			core: &stubCore{job: coreapi.StepSendJob{MailboxRemoved: true}},
			snd:  &fakeSender{}, enq: &fakeEnq{}, want: "failed",
		},
		{
			name: "degenerate zero cap is failed",
			core: &stubCore{job: coreapi.StepSendJob{EffectiveDailyCap: 0}},
			snd:  &fakeSender{}, enq: &fakeEnq{}, want: "failed",
		},
		{
			name: "over mailbox cap is deferred",
			core: &stubCore{job: coreapi.StepSendJob{EffectiveDailyCap: 50, SentToday: 50}},
			snd:  &fakeSender{}, enq: &fakeEnq{}, want: "deferred",
		},
		{
			name: "campaign-limited is deferred",
			core: &stubCore{job: coreapi.StepSendJob{CampaignLimited: true}},
			snd:  &fakeSender{}, enq: &fakeEnq{}, want: "deferred",
		},
		{
			name: "mailbox spacing (ClaimDeferred) is deferred",
			core: &stubCore{job: coreapi.StepSendJob{EffectiveDailyCap: 100, MinIntervalSeconds: 90}, claimDeferred: true},
			snd:  &fakeSender{}, enq: &fakeEnq{}, want: "deferred",
		},
		{
			name: "successful send is sent",
			core: &stubCore{job: coreapi.StepSendJob{EffectiveDailyCap: 100, ToEmail: "a@b.io", Subject: "Hi", BodyText: "yo"}, claimOK: true, adv: coreapi.Advance{Completed: true}},
			snd:  &fakeSender{id: "<mid@x>"}, enq: &fakeEnq{}, want: "sent",
		},
		{
			// Transient (nothing delivered, asynq WILL retry the task): a
			// self-clearing wait, not a terminal outcome — "deferred", not
			// "failed".
			name: "transient send failure is deferred",
			core: &stubCore{job: coreapi.StepSendJob{EffectiveDailyCap: 100, ToEmail: "a@b.io", Subject: "Hi", BodyText: "yo"}, claimOK: true},
			snd:  &fakeSender{err: &net.OpError{Op: "dial", Err: timeoutStub{}}}, enq: &fakeEnq{}, want: "deferred",
		},
		{
			name: "permanent send failure is failed",
			core: &stubCore{job: coreapi.StepSendJob{EffectiveDailyCap: 100, ToEmail: "a@b.io", Subject: "Hi", BodyText: "yo"}, claimOK: true, adv: coreapi.Advance{Completed: true}},
			snd:  &fakeSender{err: errors.New("550 no such user")}, enq: &fakeEnq{}, want: "failed",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mtx := metrics.New()
			// Errors are expected/ignored here: the transient case returns one
			// so asynq retries, which is proven separately above.
			_ = runWithMetrics(t, tc.core, tc.snd, tc.enq, mtx)

			for _, result := range []string{"sent", "failed", "skipped", "deferred"} {
				got := sendsCount(t, mtx, result)
				want := 0.0
				if result == tc.want {
					want = 1
				}
				if got != want {
					t.Errorf("inroad_sends_total{kind=campaign,result=%s} = %v, want %v", result, got, want)
				}
			}
		})
	}
}

// TestAdvanceRecoverForwardDoesNotDoubleCountSent proves the ClaimAlreadySent
// recovery path (cursor advance retried after a crash between MarkStepDelivered
// and the cursor commit) does NOT emit a second "sent" — the delivery was
// already counted on the run that actually sent it.
func TestAdvanceRecoverForwardDoesNotDoubleCountSent(t *testing.T) {
	mtx := metrics.New()
	core := &stubCore{
		claimOK:     true,
		advanceFail: 1, // first AdvanceStepCursor call errors -> forces a retry
		job:         coreapi.StepSendJob{EffectiveDailyCap: 100, ToEmail: "a@b.io", Subject: "Hi", BodyText: "yo"},
		adv:         coreapi.Advance{Completed: true},
	}
	snd, enq := &fakeSender{id: "<mid@x>"}, &fakeEnq{}

	if err := runWithMetrics(t, core, snd, enq, mtx); err == nil {
		t.Fatal("first run: expected the cursor-advance failure to surface")
	}
	if err := runWithMetrics(t, core, snd, enq, mtx); err != nil {
		t.Fatalf("second run (recover-forward): %v", err)
	}
	if got := sendsCount(t, mtx, "sent"); got != 1 {
		t.Fatalf("inroad_sends_total{result=sent} = %v, want 1 (not double-counted on recover-forward)", got)
	}
}

// TestAdvanceSuppressedDoesNotDoubleCountOnStopFailureThenRetry is the
// regression for reordering the metric AFTER the side-effecting call it
// reports: if MarkStepStopped fails (e.g. a transient DB error), asynq
// redelivers the task, and the SAME Suppressed branch runs again on retry.
// Incrementing "skipped" BEFORE calling MarkStepStopped would count that
// failed, never-durable stop anyway — and count it AGAIN when the retry
// finally succeeds. The metric must fire only once, on the run that actually
// commits the stop.
func TestAdvanceSuppressedDoesNotDoubleCountOnStopFailureThenRetry(t *testing.T) {
	mtx := metrics.New()
	core := &stubCore{
		job:       coreapi.StepSendJob{Suppressed: true, EffectiveDailyCap: 100},
		stopFailN: 1, // first MarkStepStopped call fails; the retry succeeds
	}
	snd, enq := &fakeSender{}, &fakeEnq{}

	// First run: MarkStepStopped fails -> the error must surface (so asynq
	// retries) WITHOUT incrementing the metric — the stop never durably
	// happened.
	if err := runWithMetrics(t, core, snd, enq, mtx); err == nil {
		t.Fatal("first run: expected the MarkStepStopped failure to surface so asynq retries")
	}
	if got := sendsCount(t, mtx, "skipped"); got != 0 {
		t.Fatalf("inroad_sends_total{result=skipped} = %v, want 0 (must not count a stop that failed to commit)", got)
	}

	// Second run (retry): MarkStepStopped succeeds -> exactly one increment,
	// not two.
	if err := runWithMetrics(t, core, snd, enq, mtx); err != nil {
		t.Fatalf("second run: %v", err)
	}
	if core.stopped != stopReasonSuppressed {
		t.Fatalf("stop reason = %q, want %q", core.stopped, stopReasonSuppressed)
	}
	if got := sendsCount(t, mtx, "skipped"); got != 1 {
		t.Fatalf("inroad_sends_total{result=skipped} = %v, want 1 (recorded once, on the run that actually stopped it)", got)
	}
}

// TestAdvanceCampaignLimitedDoesNotDoubleCountOnEnqueueFailureThenRetry is the
// same reordering regression as Suppressed above, for the deferred-not-a-stop
// branches: EnqueueAdvanceIn failing must not count a wait that was never
// actually scheduled.
func TestAdvanceCampaignLimitedDoesNotDoubleCountOnEnqueueFailureThenRetry(t *testing.T) {
	mtx := metrics.New()
	core := &stubCore{job: coreapi.StepSendJob{CampaignLimited: true}}
	snd := &fakeSender{}
	enq := &fakeEnq{inErr: errors.New("redis down")}

	if err := runWithMetrics(t, core, snd, enq, mtx); err == nil {
		t.Fatal("first run: expected the EnqueueAdvanceIn failure to surface")
	}
	if got := sendsCount(t, mtx, "deferred"); got != 0 {
		t.Fatalf("inroad_sends_total{result=deferred} = %v, want 0 (must not count a wait that was never scheduled)", got)
	}

	enq.inErr = nil
	if err := runWithMetrics(t, core, snd, enq, mtx); err != nil {
		t.Fatalf("second run: %v", err)
	}
	if got := sendsCount(t, mtx, "deferred"); got != 1 {
		t.Fatalf("inroad_sends_total{result=deferred} = %v, want 1", got)
	}
}
