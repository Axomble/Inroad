package inprocess

import (
	"context"
	"errors"
	"testing"

	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/coreapi/remote"
	"github.com/inroad/inroad/internal/platform/mail"
)

// The same guard jobsource_test.go and outcomesource_test.go describe, for the
// manual reply/compose protocol: every one of these twelve methods is reached by
// TYPE ASSERTION (worker/inbox.ReplyCore, PendingReplyCore and ComposeCore at
// registration; remote.InboxSendWriter at cmd/inroad) rather than through a
// declared dependency, so a signature that drifts does not break a build — it
// makes an assertion fail at runtime and a send path silently disappear.
//
// That is not hypothetical here: internal/coreapi/inprocess/inboxwiring_test.go
// exists because exactly this happened once already, and no composed email or
// deferred reply could be sent at all.
// remote.InboxSendWriter is the control plane's side (cmd/inroad asserts it to
// serve the fleet listener); InboxSendSource is the execution plane's (the field
// a fleet worker replaces). The two are the same twelve methods from opposite
// ends, and the client has to satisfy BOTH.
var (
	_ remote.InboxSendWriter = client{}
	_ InboxSendSource        = client{}
)

// A client with no remote source sends locally — including a bare client{}
// literal, which several of this package's unit tests drive.
func TestAClientWithNoRemoteSourceSendsInboxMailLocally(t *testing.T) {
	c := client{}
	if _, ok := c.inboxSendSource().(localInboxSends); !ok {
		t.Fatalf("inboxSendSource() = %T, want localInboxSends", c.inboxSendSource())
	}
}

// Installing the option moves every one of the twelve at once. Asserted by
// COUNTING rather than by reading the source, because the failure this catches is
// a method added to the interface and never routed through the field — which on
// this protocol would mean a fleet worker claiming a reply over the wire and
// recording it into a pool it does not have.
func TestWithRemoteInboxSendsMovesEveryCallAtOnce(t *testing.T) {
	f := &countingInboxSends{}
	c := New(nil, nil, nil, "", mail.GoogleOAuth{}, mail.MicrosoftOAuth{}, nil, nil, WithRemoteInboxSends(f))

	sends, ok := c.(InboxSendSource)
	if !ok {
		t.Fatal("the client does not satisfy InboxSendSource")
	}
	ctx := context.Background()
	_, _ = sends.GetInboxReplyJob(ctx, "a", "b")
	_ = sends.RecordInboxReply(ctx, coreapi.RecordInboxReplyInput{})
	_, _ = sends.ClaimInboxReply(ctx, "a", "b")
	_ = sends.ReleaseInboxReply(ctx, "a", "b")
	_, _ = sends.ClaimPendingInboxReply(ctx, "a", "b")
	_ = sends.MarkPendingInboxReplySent(ctx, "a", "b", "<m@x>")
	_ = sends.ReleasePendingInboxReply(ctx, "a", "b", "why")
	_ = sends.FailPendingInboxReply(ctx, "a", "b", "why")
	_, _ = sends.ClaimPendingInboxCompose(ctx, "a", "b")
	_ = sends.MarkPendingInboxComposeSent(ctx, "a", "b", "<m@x>")
	_ = sends.ReleasePendingInboxCompose(ctx, "a", "b", "why")
	_ = sends.FailPendingInboxCompose(ctx, "a", "b", "why")

	if f.calls != 12 {
		t.Errorf("the remote source answered %d of the 12 calls; the rest still went to the pool", f.calls)
	}
}

// Passing nil is a no-op, not a way to disable the protocol: silently dropping a
// source that was meant to be wired would leave the process reaching a pool it
// was supposed to give up, with nothing saying so.
func TestWithRemoteInboxSendsIgnoresNil(t *testing.T) {
	c := New(nil, nil, nil, "", mail.GoogleOAuth{}, mail.MicrosoftOAuth{}, nil, nil, WithRemoteInboxSends(nil))
	inner, ok := c.(client)
	if !ok {
		t.Fatalf("New returned %T, want client", c)
	}
	if inner.inboxSends != nil {
		t.Error("WithRemoteInboxSends(nil) installed a source")
	}
	if _, ok := inner.inboxSendSource().(localInboxSends); !ok {
		t.Error("WithRemoteInboxSends(nil) left the client without its local source")
	}
}

type countingInboxSends struct{ calls int }

var errCountingInboxSend = errors.New("counting inbox send source")

func (f *countingInboxSends) GetInboxReplyJob(context.Context, string, string) (coreapi.InboxReplyJob, error) {
	f.calls++
	return coreapi.InboxReplyJob{}, errCountingInboxSend
}

func (f *countingInboxSends) RecordInboxReply(context.Context, coreapi.RecordInboxReplyInput) error {
	f.calls++
	return errCountingInboxSend
}

func (f *countingInboxSends) ClaimInboxReply(context.Context, string, string) (bool, error) {
	f.calls++
	return false, errCountingInboxSend
}

func (f *countingInboxSends) ReleaseInboxReply(context.Context, string, string) error {
	f.calls++
	return errCountingInboxSend
}

func (f *countingInboxSends) ClaimPendingInboxReply(context.Context, string, string) (coreapi.PendingInboxReply, error) {
	f.calls++
	return coreapi.PendingInboxReply{}, errCountingInboxSend
}

func (f *countingInboxSends) MarkPendingInboxReplySent(context.Context, string, string, string) error {
	f.calls++
	return errCountingInboxSend
}

func (f *countingInboxSends) ReleasePendingInboxReply(context.Context, string, string, string) error {
	f.calls++
	return errCountingInboxSend
}

func (f *countingInboxSends) FailPendingInboxReply(context.Context, string, string, string) error {
	f.calls++
	return errCountingInboxSend
}

func (f *countingInboxSends) ClaimPendingInboxCompose(context.Context, string, string) (coreapi.PendingInboxCompose, error) {
	f.calls++
	return coreapi.PendingInboxCompose{}, errCountingInboxSend
}

func (f *countingInboxSends) MarkPendingInboxComposeSent(context.Context, string, string, string) error {
	f.calls++
	return errCountingInboxSend
}

func (f *countingInboxSends) ReleasePendingInboxCompose(context.Context, string, string, string) error {
	f.calls++
	return errCountingInboxSend
}

func (f *countingInboxSends) FailPendingInboxCompose(context.Context, string, string, string) error {
	f.calls++
	return errCountingInboxSend
}
