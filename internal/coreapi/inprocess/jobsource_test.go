package inprocess

import (
	"context"
	"errors"
	"testing"

	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/coreapi/remote"
	"github.com/inroad/inroad/internal/platform/mail"
)

// THE guard for the failure #216 came close to shipping: every one of these
// methods is reached by TYPE ASSERTION rather than through a declared
// dependency, so a signature that drifts does not break a build — it makes an
// assertion fail at runtime and a capability silently disappear.
//
// A compile-time assertion is the cheapest possible version of the check, and
// it lives here, beside the implementation, so the file that would break the
// contract is the file that fails.
//
// remote.JobReader is the control plane's side (cmd/inroad asserts it to serve
// the fleet listener); JobSource is the execution plane's (the field a fleet
// worker replaces). The two are the same eight methods from opposite ends, and
// the client has to satisfy BOTH: one because it serves them, one because it
// delegates to whatever holds them.
//
// The WORKER side of the same problem — testsend.Core and webhookworker.Core,
// asserted in internal/worker/handlers.go — is covered where it belongs, by
// internal/worker/register_integration_test.go, which builds the real client
// and dispatches a real task through the real mux. Duplicating it here would
// also point this package at internal/worker, which is the wrong direction.
var (
	_ remote.JobReader = client{}
	_ JobSource        = client{}
	_ coreapi.Client   = client{}
	// Slice 1's shape, declared in suppression_test.go and asserted here so
	// every capability this seam is reached through is checked in one place.
	_ suppressionCapability = client{}
)

// A client with no remote source resolves to the local builds — including a
// bare client{} literal, which several of this package's unit tests drive.
func TestAClientWithNoRemoteSourceReadsLocally(t *testing.T) {
	c := client{}
	if _, ok := c.jobSource().(localJobs); !ok {
		t.Fatalf("jobSource() = %T, want localJobs", c.jobSource())
	}
}

// And installing the option moves every one of the eight at once — the property
// that makes a single replaceable field worth having. Asserted by counting: a
// fake source records each call, and all eight must reach it.
func TestWithRemoteJobsMovesEveryReadAtOnce(t *testing.T) {
	f := &countingJobs{}
	c := New(nil, nil, nil, "", mail.GoogleOAuth{}, mail.MicrosoftOAuth{}, nil, nil, WithRemoteJobs(f))

	jobs, ok := c.(JobSource)
	if !ok {
		t.Fatal("the client does not satisfy JobSource")
	}
	ctx := context.Background()
	_, _ = jobs.GetStepSendJob(ctx, "a", "b")
	_, _ = jobs.GetInboxPollJob(ctx, "a", "b")
	_, _ = jobs.GetWarmupSendJob(ctx, "a", "b")
	_, _ = jobs.GetWarmupEngageJob(ctx, "a", "b")
	_, _ = jobs.FindSendByMessageID(ctx, "a", "b")

	extra, ok := c.(remote.JobReader)
	if !ok {
		t.Fatal("the client does not satisfy remote.JobReader")
	}
	_, _ = extra.GetWebhookDeliveryJob(ctx, "a", "b")
	_, _ = extra.GetTestSendContent(ctx, "a", "b", "c")
	_, _ = extra.ResolveSenderTransport(ctx, "a", "b")

	if f.calls != 8 {
		t.Errorf("the remote source answered %d of the 8 reads; the rest still went to the pool", f.calls)
	}
}

// Passing nil is a no-op, not a way to disable job reads: silently dropping a
// source that was meant to be wired would leave the process reading a pool it
// was supposed to give up, with nothing saying so.
func TestWithRemoteJobsIgnoresNil(t *testing.T) {
	c := New(nil, nil, nil, "", mail.GoogleOAuth{}, mail.MicrosoftOAuth{}, nil, nil, WithRemoteJobs(nil))
	inner, ok := c.(client)
	if !ok {
		t.Fatalf("New returned %T, want client", c)
	}
	if inner.jobs != nil {
		t.Error("WithRemoteJobs(nil) installed a source")
	}
	if _, ok := inner.jobSource().(localJobs); !ok {
		t.Error("WithRemoteJobs(nil) left the client without its local source")
	}
}

type countingJobs struct{ calls int }

var errCounting = errors.New("counting source")

func (f *countingJobs) GetStepSendJob(context.Context, string, string) (coreapi.StepSendJob, error) {
	f.calls++
	return coreapi.StepSendJob{}, errCounting
}

func (f *countingJobs) GetInboxPollJob(context.Context, string, string) (coreapi.InboxPollJob, error) {
	f.calls++
	return coreapi.InboxPollJob{}, errCounting
}

func (f *countingJobs) GetWarmupSendJob(context.Context, string, string) (coreapi.WarmupSendJob, error) {
	f.calls++
	return coreapi.WarmupSendJob{}, errCounting
}

func (f *countingJobs) GetWarmupEngageJob(context.Context, string, string) (coreapi.WarmupEngageJob, error) {
	f.calls++
	return coreapi.WarmupEngageJob{}, errCounting
}

func (f *countingJobs) GetWebhookDeliveryJob(context.Context, string, string) (coreapi.WebhookDeliveryJob, error) {
	f.calls++
	return coreapi.WebhookDeliveryJob{}, errCounting
}

func (f *countingJobs) GetTestSendContent(context.Context, string, string, string) (coreapi.TestSendContent, error) {
	f.calls++
	return coreapi.TestSendContent{}, errCounting
}

func (f *countingJobs) ResolveSenderTransport(context.Context, string, string) (coreapi.SenderTransport, error) {
	f.calls++
	return coreapi.SenderTransport{}, errCounting
}

func (f *countingJobs) FindSendByMessageID(context.Context, string, string) (coreapi.SendRef, error) {
	f.calls++
	return coreapi.SendRef{}, errCounting
}
