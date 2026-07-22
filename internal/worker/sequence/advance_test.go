package sequence

import (
	"context"
	"encoding/json"
	"errors"
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
	job       coreapi.StepSendJob
	jobErr    error
	adv       coreapi.Advance
	sent      *coreapi.StepResult
	stopped   string
	deferrals int // value returned by IncrementEnrollmentCapDeferrals
	incrCalls int
}

func (s *stubCore) GetStepSendJob(context.Context, string, string) (coreapi.StepSendJob, error) {
	return s.job, s.jobErr
}
func (s *stubCore) MarkStepSent(_ context.Context, _ coreapi.StepSendJob, res coreapi.StepResult) (coreapi.Advance, error) {
	s.sent = &res
	return s.adv, nil
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
	called bool
	sent   mail.Message
	id     string
	err    error
}

func (f *fakeSender) Send(_ mail.SMTPConfig, m mail.Message) (string, error) {
	f.called, f.sent = true, m
	return f.id, f.err
}

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

func run(t *testing.T, core coreapi.Client, s Sender, enq Enqueuer) error {
	t.Helper()
	return AdvanceHandler(core, s, enq)(context.Background(), advanceTask(t))
}

func TestAdvanceSkipIsNoOp(t *testing.T) {
	core := &stubCore{job: coreapi.StepSendJob{Skip: true}}
	snd, enq := &fakeSender{}, &fakeEnq{}
	if err := run(t, core, snd, enq); err != nil {
		t.Fatal(err)
	}
	if snd.called || enq.atCalled || enq.inCalled || core.stopped != "" || core.sent != nil {
		t.Fatal("skip must be a pure no-op")
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
	if snd.called {
		t.Fatal("suppressed contact must not be emailed")
	}
}

func TestAdvanceOverCapReEnqueues(t *testing.T) {
	core := &stubCore{job: coreapi.StepSendJob{EffectiveDailyCap: 50, SentToday: 50}}
	snd, enq := &fakeSender{}, &fakeEnq{}
	if err := run(t, core, snd, enq); err != nil {
		t.Fatal(err)
	}
	if snd.called {
		t.Fatal("over-cap step must not send")
	}
	if !enq.inCalled || enq.in != capBackoff {
		t.Fatalf("expected re-enqueue in %v, got called=%v in=%v", capBackoff, enq.inCalled, enq.in)
	}
	if core.sent != nil {
		t.Fatal("over-cap must not advance the cursor")
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
	if enq.inCalled || enq.atCalled || snd.called || core.incrCalls != 0 {
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
		adv: coreapi.Advance{Completed: false, NextDueAt: next},
	}
	snd, enq := &fakeSender{id: "<mid@x>"}, &fakeEnq{}
	if err := run(t, core, snd, enq); err != nil {
		t.Fatal(err)
	}
	if !snd.called {
		t.Fatal("expected a send")
	}
	if snd.sent.InReplyTo != "<root@x>" || snd.sent.References != "<root@x>" {
		t.Fatalf("threading headers not passed through: %+v", snd.sent)
	}
	if core.sent == nil || core.sent.Status != "sent" || core.sent.MessageID != "<mid@x>" {
		t.Fatalf("MarkStepSent result wrong: %+v", core.sent)
	}
	if !enq.atCalled || !enq.at.Equal(next) {
		t.Fatalf("next advance not scheduled at NextDueAt: called=%v at=%v", enq.atCalled, enq.at)
	}
}

func TestAdvanceCompletedDoesNotReschedule(t *testing.T) {
	core := &stubCore{
		job: coreapi.StepSendJob{EffectiveDailyCap: 100, ToEmail: "a@b.io", Subject: "Bye", BodyText: "end"},
		adv: coreapi.Advance{Completed: true},
	}
	snd, enq := &fakeSender{id: "<m>"}, &fakeEnq{}
	if err := run(t, core, snd, enq); err != nil {
		t.Fatal(err)
	}
	if enq.atCalled {
		t.Fatal("completed enrollment must not schedule another advance")
	}
}

func TestAdvanceFailedSendRecordsFailureAndAdvances(t *testing.T) {
	next := time.Now().Add(time.Hour)
	core := &stubCore{
		job: coreapi.StepSendJob{EffectiveDailyCap: 100, ToEmail: "a@b.io", Subject: "Hi", BodyText: "yo"},
		adv: coreapi.Advance{Completed: false, NextDueAt: next},
	}
	snd, enq := &fakeSender{err: errors.New("smtp 550")}, &fakeEnq{}
	if err := run(t, core, snd, enq); err != nil {
		t.Fatal(err)
	}
	if core.sent == nil || core.sent.Status != "failed" {
		t.Fatalf("expected failed result recorded, got %+v", core.sent)
	}
	if !enq.atCalled {
		t.Fatal("fail-forward: should still schedule the next step")
	}
}
