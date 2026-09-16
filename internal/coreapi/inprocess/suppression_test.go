package inprocess

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/platform/mail"
)

// suppressionCapability is the shape the EXECUTION plane asserts for:
// worker/testsend.Core and worker/inbox's ReplyCore, PendingReplyCore and
// ComposeCore all declare this exact method, and internal/worker/handlers.go
// registers each of those handlers only if the coreapi client satisfies its
// interface. Restated here (coreapi may not import a worker package) because a
// client that quietly stopped satisfying it would not fail to build — it would
// silently register no test-send and no manual-reply handler at all.
type suppressionCapability interface {
	IsSuppressed(ctx context.Context, workspaceID, email string) (bool, error)
}

type fakeSuppressionSource struct {
	gotWS, gotEmail string
	calls           int
	answer          bool
	err             error
}

func (f *fakeSuppressionSource) IsSuppressed(_ context.Context, ws, email string) (bool, error) {
	f.calls++
	f.gotWS, f.gotEmail = ws, email
	return f.answer, f.err
}

// newForSuppression builds a client with NO pool. That is the assertion, not a
// shortcut: the local source would dereference it, so a test that reaches the
// injected source and returns is proof that no query was attempted.
func newForSuppression(opts ...Option) suppressionCapability {
	return New(nil, nil, nil, "", mail.GoogleOAuth{}, mail.MicrosoftOAuth{}, nil, nil, opts...).(suppressionCapability)
}

func TestWithRemoteSuppressionReplacesTheLocalQuery(t *testing.T) {
	ws := uuid.New().String()
	fake := &fakeSuppressionSource{answer: true}
	c := newForSuppression(WithRemoteSuppression(fake))

	got, err := c.IsSuppressed(context.Background(), ws, "ada@example.test")
	if err != nil {
		t.Fatalf("IsSuppressed: %v", err)
	}
	if !got {
		t.Error("suppressed = false, want the injected source's answer")
	}
	if fake.calls != 1 || fake.gotWS != ws || fake.gotEmail != "ada@example.test" {
		t.Errorf("source saw %d calls with %q/%q, want 1 with %q/ada@example.test",
			fake.calls, fake.gotWS, fake.gotEmail, ws)
	}
}

// The source's error is returned verbatim, not translated and not swallowed:
// every call site reads a non-nil error as "do not send", so anything that
// turned one into (false, nil) would deliver mail to a suppressed address.
//
// The BOOL is deliberately not asserted here. This method is a one-line
// delegate, and the zero-value-on-error convention belongs to the leaf that
// produces the answer — remote.Client's own tests assert it returns (false,
// err) on every failure path. Requiring the delegate to normalise would also be
// normalising in the LESS safe direction: `true` alongside an error means "do
// not send", which is the direction every rule in this area leans.
func TestARemoteSuppressionFailurePropagates(t *testing.T) {
	want := errors.New("control plane unreachable")
	c := newForSuppression(WithRemoteSuppression(&fakeSuppressionSource{err: want}))

	if _, err := c.IsSuppressed(context.Background(), uuid.New().String(), "ada@example.test"); !errors.Is(err, want) {
		t.Fatalf("err = %v, want %v", err, want)
	}
}

// Passing nil is a no-op, NOT a way to disable the local source — the same rule
// WithCredentialBroker follows, and for the same reason: silently dropping a
// remote source that was meant to be wired would leave the process reading a
// pool it was supposed to give up, with nothing saying so.
//
// Asserted on the source's TYPE because the behavioural probe needs a database;
// suppression_integration_test.go is that probe.
func TestANilRemoteSuppressionIsANoOpNotADisable(t *testing.T) {
	c := New(nil, nil, nil, "", mail.GoogleOAuth{}, mail.MicrosoftOAuth{}, nil, nil, WithRemoteSuppression(nil))
	if _, ok := c.(client).suppression.(localSuppression); !ok {
		t.Fatalf("suppression source = %T, want localSuppression", c.(client).suppression)
	}
}

// The default — every self-hosted install, every test, cmd/seed and cmd/inroad —
// is the local query. This fails if the source ever defaults to anything else.
func TestTheDefaultSuppressionSourceIsLocal(t *testing.T) {
	c := New(nil, nil, nil, "", mail.GoogleOAuth{}, mail.MicrosoftOAuth{}, nil, nil)
	if _, ok := c.(client).suppression.(localSuppression); !ok {
		t.Fatalf("suppression source = %T, want localSuppression", c.(client).suppression)
	}
}
