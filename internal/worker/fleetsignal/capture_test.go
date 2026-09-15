package fleetsignal

import (
	"context"
	"errors"
	"net/textproto"
	"testing"
	"time"

	"github.com/inroad/inroad/internal/platform/mail"
	"github.com/inroad/inroad/internal/platform/providersignal"
)

type fakeSender struct {
	msgID string
	err   error
	calls int
	got   mail.OutboundJob
}

func (f *fakeSender) Send(_ context.Context, tj mail.OutboundJob, _ mail.Message) (string, error) {
	f.calls++
	f.got = tj
	return f.msgID, f.err
}

type fakeReader struct {
	err   error
	calls int
}

func (f *fakeReader) Fetch(context.Context, mail.IMAPConfig, uint32, int) ([]mail.InboundMessage, uint32, error) {
	f.calls++
	return nil, 7, f.err
}

func (f *fakeReader) CurrentState(context.Context, mail.IMAPConfig) (uint32, uint32, error) {
	f.calls++
	return 7, 9, f.err
}

func newTestCollector() *providersignal.Collector {
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	return providersignal.NewCollector(func() time.Time { return now })
}

func countFor(t *testing.T, c *providersignal.Collector, key providersignal.Key) int64 {
	t.Helper()
	for _, got := range c.Drain().Counts {
		if got.Key == key {
			return got.Events
		}
	}
	return 0
}

func TestSenderRecordsASuccessAndPassesTheResultThrough(t *testing.T) {
	c := newTestCollector()
	inner := &fakeSender{msgID: "<abc@example.com>"}
	s := NewSender(inner, c)

	got, err := s.Send(context.Background(), mail.OutboundJob{Provider: "gmail"}, mail.Message{})
	if err != nil {
		t.Fatalf("Send returned %v, want nil", err)
	}
	if got != "<abc@example.com>" {
		t.Errorf("message id = %q, want the inner sender's", got)
	}
	if inner.calls != 1 {
		t.Errorf("inner sender called %d times, want 1", inner.calls)
	}
	want := providersignal.Key{Provider: providersignal.ProviderGmail, Operation: providersignal.OpSend, Reason: providersignal.ReasonOK}
	if n := countFor(t, c, want); n != 1 {
		t.Errorf("recorded %d of %+v, want 1", n, want)
	}
}

// The decorator must classify the provider's answer, not merely count failures.
func TestSenderClassifiesTheProvidersAnswer(t *testing.T) {
	cases := []struct {
		name     string
		provider string
		err      error
		want     providersignal.Key
	}{
		{
			name: "smtp auth rejection", provider: "smtp",
			err:  &textproto.Error{Code: 535, Msg: "5.7.8 bad credentials"},
			want: providersignal.Key{Provider: providersignal.ProviderSMTP, Operation: providersignal.OpSend, Reason: providersignal.ReasonAuthFailed},
		},
		{
			name: "graph throttle", provider: "m365",
			err:  &mail.APIError{Provider: "m365", Op: "send", Status: 429},
			want: providersignal.Key{Provider: providersignal.ProviderM365, Operation: providersignal.OpSend, Reason: providersignal.ReasonRateLimited},
		},
		{
			name: "gmail quota refusal", provider: "gmail",
			err:  &mail.APIError{Provider: "gmail", Op: "send", Status: 403, Reason: "userRateLimitExceeded"},
			want: providersignal.Key{Provider: providersignal.ProviderGmail, Operation: providersignal.OpSend, Reason: providersignal.ReasonRateLimited},
		},
		{
			// An empty Provider takes MultiSender's default (SMTP) leg, so the
			// label must name that leg and not an empty string.
			name: "unset provider takes the smtp leg", provider: "",
			err:  &textproto.Error{Code: 421, Msg: "try later"},
			want: providersignal.Key{Provider: providersignal.ProviderSMTP, Operation: providersignal.OpSend, Reason: providersignal.ReasonThrottled},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newTestCollector()
			s := NewSender(&fakeSender{err: tc.err}, c)
			if _, err := s.Send(context.Background(), mail.OutboundJob{Provider: tc.provider}, mail.Message{}); !errors.Is(err, tc.err) {
				t.Fatalf("Send returned %v, want the inner error unchanged", err)
			}
			if n := countFor(t, c, tc.want); n != 1 {
				t.Errorf("recorded %d of %+v, want 1", n, tc.want)
			}
		})
	}
}

// Telemetry must never be able to fail a send, which includes never being
// wired at all: a nil collector is the "signals disabled" configuration and has
// to leave the send path byte-for-byte unchanged.
func TestSenderWithNoCollectorStillSends(t *testing.T) {
	inner := &fakeSender{msgID: "<abc@example.com>"}
	s := NewSender(inner, nil)
	got, err := s.Send(context.Background(), mail.OutboundJob{Provider: "smtp"}, mail.Message{})
	if err != nil || got != "<abc@example.com>" {
		t.Fatalf("Send = (%q, %v), want the inner sender's result", got, err)
	}
	if inner.calls != 1 {
		t.Errorf("inner sender called %d times, want 1", inner.calls)
	}
}

// The credential-bearing job must reach the real sender untouched — a decorator
// that dropped a field would silently break every send it observed.
func TestSenderForwardsTheJobUnchanged(t *testing.T) {
	inner := &fakeSender{}
	job := mail.OutboundJob{Provider: "smtp", Host: "smtp.example.com", Port: 587, Username: "u", Password: "p", AllowPlaintext: true}
	if _, err := NewSender(inner, newTestCollector()).Send(context.Background(), job, mail.Message{}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if inner.got != job {
		t.Errorf("inner sender received %+v, want %+v", inner.got, job)
	}
}

// A poll is an authentication to the provider from this worker's IP, so its
// outcome is a per-IP fact even though it delivers no mail.
func TestReaderRecordsPollOutcomes(t *testing.T) {
	c := newTestCollector()
	r := NewInboxReader(&fakeReader{}, c)

	if _, _, err := r.Fetch(context.Background(), mail.IMAPConfig{}, 0, 10); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if _, _, err := r.CurrentState(context.Background(), mail.IMAPConfig{}); err != nil {
		t.Fatalf("CurrentState: %v", err)
	}

	want := providersignal.Key{Provider: providersignal.ProviderSMTP, Operation: providersignal.OpPoll, Reason: providersignal.ReasonOK}
	if n := countFor(t, c, want); n != 2 {
		t.Errorf("recorded %d of %+v, want 2 (Fetch and CurrentState)", n, want)
	}
}

// go-imap v1 discards the IMAP response code (StatusResp.Err returns
// errors.New(r.Info)), so an IMAP AUTHENTICATIONFAILED is NOT structurally
// classifiable and lands in "other". That is a deliberate limit, not an
// oversight: reading it back would mean parsing the server's free text, which is
// what classification at the capture point exists to avoid. The success/failure
// split still answers the question that matters — can this egress IP get in.
func TestReaderRecordsAnUnclassifiableIMAPFailureAsOther(t *testing.T) {
	c := newTestCollector()
	r := NewInboxReader(&fakeReader{err: errors.New("Invalid credentials (Failure)")}, c)

	if _, _, err := r.Fetch(context.Background(), mail.IMAPConfig{}, 0, 10); err == nil {
		t.Fatal("Fetch swallowed the inner error")
	}
	want := providersignal.Key{Provider: providersignal.ProviderSMTP, Operation: providersignal.OpPoll, Reason: providersignal.ReasonOther}
	if n := countFor(t, c, want); n != 1 {
		t.Errorf("recorded %d of %+v, want 1", n, want)
	}
}

// A network-level poll failure IS classifiable, and is the shape a provider that
// has stopped accepting connections from this IP produces.
func TestReaderClassifiesAnUnreachableProvider(t *testing.T) {
	c := newTestCollector()
	r := NewInboxReader(&fakeReader{err: context.DeadlineExceeded}, c)

	if _, _, err := r.CurrentState(context.Background(), mail.IMAPConfig{}); err == nil {
		t.Fatal("CurrentState swallowed the inner error")
	}
	want := providersignal.Key{Provider: providersignal.ProviderSMTP, Operation: providersignal.OpPoll, Reason: providersignal.ReasonUnreachable}
	if n := countFor(t, c, want); n != 1 {
		t.Errorf("recorded %d of %+v, want 1", n, want)
	}
}

func TestReaderWithNoCollectorStillPolls(t *testing.T) {
	inner := &fakeReader{}
	r := NewInboxReader(inner, nil)
	if _, uidValidity, err := r.Fetch(context.Background(), mail.IMAPConfig{}, 0, 10); err != nil || uidValidity != 7 {
		t.Fatalf("Fetch = (uidValidity %d, %v), want the inner reader's result", uidValidity, err)
	}
	if inner.calls != 1 {
		t.Errorf("inner reader called %d times, want 1", inner.calls)
	}
}

// The decorators must satisfy the seams the worker wiring hands them to.
var (
	_ Sender           = (*sender)(nil)
	_ mail.InboxReader = (*inboxReader)(nil)
)
