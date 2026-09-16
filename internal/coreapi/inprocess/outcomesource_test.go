package inprocess

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/coreapi/remote"
	"github.com/inroad/inroad/internal/platform/mail"
)

// The same guard jobsource_test.go describes, for the write half: every one of
// these methods is reached by TYPE ASSERTION at cmd/inroad rather than through a
// declared dependency, so a signature that drifts does not break a build — it
// makes an assertion fail at runtime and a capability silently disappear.
//
// remote.OutcomeWriter is the control plane's side (cmd/inroad asserts it to
// serve the fleet listener); OutcomeSource is the execution plane's (the field a
// fleet worker replaces). The two are the same twenty methods from opposite
// ends, and the client has to satisfy BOTH.
var (
	_ remote.OutcomeWriter = client{}
	_ OutcomeSource        = client{}
)

// A client with no remote source writes locally — including a bare client{}
// literal, which several of this package's unit tests drive.
func TestAClientWithNoRemoteSourceWritesLocally(t *testing.T) {
	c := client{}
	if _, ok := c.outcomeSource().(localOutcomes); !ok {
		t.Fatalf("outcomeSource() = %T, want localOutcomes", c.outcomeSource())
	}
}

// Installing the option moves every one of the twenty at once — the property
// that makes a single replaceable field worth having, and the one that matters
// most here: a worker fetching its work remotely while CLAIMING it locally would
// be two processes disagreeing about who owns a send.
//
// Asserted by counting rather than by reading the source, because the failure
// this catches is a method that was added to the interface and never routed
// through the field.
func TestWithRemoteOutcomesMovesEveryWriteAtOnce(t *testing.T) {
	f := &countingOutcomes{}
	c := New(nil, nil, nil, "", mail.GoogleOAuth{}, mail.MicrosoftOAuth{}, nil, nil, WithRemoteOutcomes(f))

	out, ok := c.(OutcomeSource)
	if !ok {
		t.Fatal("the client does not satisfy OutcomeSource")
	}
	ctx := context.Background()
	step := coreapi.StepSendJob{}
	warm := coreapi.WarmupSendJob{}
	status := 500

	_, _ = out.ClaimStepSend(ctx, step)
	_ = out.MarkStepDelivered(ctx, step, "<m@x>")
	_, _ = out.AdvanceStepCursor(ctx, step)
	_ = out.ReleaseStepSend(ctx, step)
	_, _ = out.FinalizeStepSend(ctx, step, coreapi.StepResult{})
	_ = out.MarkStepStopped(ctx, "a", "b", "suppressed")
	_ = out.DeferEnrollment(ctx, "a", "b", time.Now())
	_, _ = out.IncrementEnrollmentCapDeferrals(ctx, "a", "b")
	_, _ = out.ClaimWarmupSend(ctx, warm)
	_ = out.MarkWarmupSent(ctx, warm, "<w@x>")
	_ = out.ReleaseWarmupSend(ctx, warm)
	_ = out.FailWarmupSend(ctx, warm, "boom")
	_ = out.MarkWarmupEngaged(ctx, "a", "b", true)
	_ = out.MarkReplied(ctx, "a", "b", "positive", "lexicon", 1)
	_ = out.RecordReplyClass(ctx, "a", "b", "out_of_office", "header", 1)
	_ = out.MarkUnsubscribed(ctx, "a", "b", "ada@example.test")
	_ = out.MarkBounced(ctx, "a", "b", "ada@example.test", true)
	_ = out.MarkWebhookDelivered(ctx, "a", "b", 1, 200)
	_ = out.MarkWebhookRetrying(ctx, "a", "b", 1, "502", &status, time.Now())
	_ = out.MarkWebhookFailed(ctx, "a", "b", 5, "gave up", &status)

	if f.calls != 20 {
		t.Errorf("the remote source answered %d of the 20 writes; the rest still went to the pool", f.calls)
	}
}

// Passing nil is a no-op, not a way to disable outcome writes: silently dropping
// a source that was meant to be wired would leave the process writing to a pool
// it was supposed to give up, with nothing saying so.
func TestWithRemoteOutcomesIgnoresNil(t *testing.T) {
	c := New(nil, nil, nil, "", mail.GoogleOAuth{}, mail.MicrosoftOAuth{}, nil, nil, WithRemoteOutcomes(nil))
	inner, ok := c.(client)
	if !ok {
		t.Fatalf("New returned %T, want client", c)
	}
	if inner.outcomes != nil {
		t.Error("WithRemoteOutcomes(nil) installed a source")
	}
	if _, ok := inner.outcomeSource().(localOutcomes); !ok {
		t.Error("WithRemoteOutcomes(nil) left the client without its local source")
	}
}

type countingOutcomes struct{ calls int }

var errCountingOutcome = errors.New("counting outcome source")

func (f *countingOutcomes) ClaimStepSend(context.Context, coreapi.StepSendJob) (coreapi.ClaimOutcome, error) {
	f.calls++
	return coreapi.ClaimSkip, errCountingOutcome
}

func (f *countingOutcomes) MarkStepDelivered(context.Context, coreapi.StepSendJob, string) error {
	f.calls++
	return errCountingOutcome
}

func (f *countingOutcomes) AdvanceStepCursor(context.Context, coreapi.StepSendJob) (coreapi.Advance, error) {
	f.calls++
	return coreapi.Advance{}, errCountingOutcome
}

func (f *countingOutcomes) ReleaseStepSend(context.Context, coreapi.StepSendJob) error {
	f.calls++
	return errCountingOutcome
}

func (f *countingOutcomes) FinalizeStepSend(context.Context, coreapi.StepSendJob, coreapi.StepResult) (coreapi.Advance, error) {
	f.calls++
	return coreapi.Advance{}, errCountingOutcome
}

func (f *countingOutcomes) MarkStepStopped(context.Context, string, string, string) error {
	f.calls++
	return errCountingOutcome
}

func (f *countingOutcomes) DeferEnrollment(context.Context, string, string, time.Time) error {
	f.calls++
	return errCountingOutcome
}

func (f *countingOutcomes) IncrementEnrollmentCapDeferrals(context.Context, string, string) (int, error) {
	f.calls++
	return 0, errCountingOutcome
}

func (f *countingOutcomes) ClaimWarmupSend(context.Context, coreapi.WarmupSendJob) (coreapi.ClaimOutcome, error) {
	f.calls++
	return coreapi.ClaimSkip, errCountingOutcome
}

func (f *countingOutcomes) MarkWarmupSent(context.Context, coreapi.WarmupSendJob, string) error {
	f.calls++
	return errCountingOutcome
}

func (f *countingOutcomes) ReleaseWarmupSend(context.Context, coreapi.WarmupSendJob) error {
	f.calls++
	return errCountingOutcome
}

func (f *countingOutcomes) FailWarmupSend(context.Context, coreapi.WarmupSendJob, string) error {
	f.calls++
	return errCountingOutcome
}

func (f *countingOutcomes) MarkWarmupEngaged(context.Context, string, string, bool) error {
	f.calls++
	return errCountingOutcome
}

func (f *countingOutcomes) MarkReplied(context.Context, string, string, string, string, float64) error {
	f.calls++
	return errCountingOutcome
}

func (f *countingOutcomes) RecordReplyClass(context.Context, string, string, string, string, float64) error {
	f.calls++
	return errCountingOutcome
}

func (f *countingOutcomes) MarkUnsubscribed(context.Context, string, string, string) error {
	f.calls++
	return errCountingOutcome
}

func (f *countingOutcomes) MarkBounced(context.Context, string, string, string, bool) error {
	f.calls++
	return errCountingOutcome
}

func (f *countingOutcomes) MarkWebhookDelivered(context.Context, string, string, int, int) error {
	f.calls++
	return errCountingOutcome
}

func (f *countingOutcomes) MarkWebhookRetrying(context.Context, string, string, int, string, *int, time.Time) error {
	f.calls++
	return errCountingOutcome
}

func (f *countingOutcomes) MarkWebhookFailed(context.Context, string, string, int, string, *int) error {
	f.calls++
	return errCountingOutcome
}
