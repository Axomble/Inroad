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
	"github.com/inroad/inroad/internal/platform/mail"
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
	claimOK    bool
	claimErr   error
	claimCalls int
	sent       bool
	delivered  *string // message id passed to MarkStepDelivered
	// advanceFail makes the first N AdvanceStepCursor calls return an error, to
	// exercise the cursor-advance-failure → recover-forward window.
	advanceFail  int
	advanceCalls int
	finalized    *coreapi.StepResult // set only by the (failed) FinalizeStepSend path
	released     bool
	stopped      string
	deferrals    int // value returned by IncrementEnrollmentCapDeferrals
	incrCalls    int
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
}

func (f *fakeEnq) EnqueueAdvanceAt(_, _ string, t time.Time) error {
	f.atCalled, f.at = true, t
	return nil
}
func (f *fakeEnq) EnqueueAdvanceIn(_, _ string, d time.Duration) error {
	f.inCalled, f.in = true, d
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

func run(t *testing.T, core coreapi.Client, s Sender, enq Enqueuer) error {
	t.Helper()
	return AdvanceHandler(core, s, enq, testBaseURL, testTrackingSecret)(context.Background(), advanceTask(t))
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

func TestAdvanceOverCapReEnqueues(t *testing.T) {
	core := &stubCore{job: coreapi.StepSendJob{EffectiveDailyCap: 50, SentToday: 50}}
	snd, enq := &fakeSender{}, &fakeEnq{}
	if err := run(t, core, snd, enq); err != nil {
		t.Fatal(err)
	}
	if snd.called() || core.claimCalls != 0 {
		t.Fatal("over-cap step must not claim or send")
	}
	if !enq.inCalled || enq.in != capBackoff {
		t.Fatalf("expected re-enqueue in %v, got called=%v in=%v", capBackoff, enq.inCalled, enq.in)
	}
	if core.finalized != nil {
		t.Fatal("over-cap must not finalize/advance the cursor")
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

func TestAdvanceCapDeferralCeilingStopsFailed(t *testing.T) {
	// Over cap AND past the deferral ceiling: stop 'failed' instead of re-enqueue.
	core := &stubCore{job: coreapi.StepSendJob{EffectiveDailyCap: 50, SentToday: 50}, deferrals: maxCapDeferrals + 1}
	snd, enq := &fakeSender{}, &fakeEnq{}
	if err := run(t, core, snd, enq); err != nil {
		t.Fatal(err)
	}
	if core.stopped != "failed" {
		t.Fatalf("want stop reason failed at deferral ceiling, got %q", core.stopped)
	}
	if enq.inCalled {
		t.Fatal("past the ceiling must not re-enqueue")
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
