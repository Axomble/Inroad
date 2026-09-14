package providersignal

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/textproto"
	"syscall"
	"testing"
)

// fakeSMTPReply is a test double for the SMTPReply seam. It exists because the
// production implementor (*go-mail.SendError) keeps its reply code and enhanced
// status in UNEXPORTED fields with no constructor, so a test cannot build one
// carrying "421 4.7.0" — and the SSRF guard forbids dialing loopback, so there
// is no in-process SMTP server to obtain a real one from either. Classifying
// through a consumer-defined interface rather than the concrete type is what
// makes this branch reachable at all (see CONTRIBUTING.md, "a concrete
// dependency that tests satisfy with nil makes every branch behind it
// unreachable"). seam_test.go asserts the real type still satisfies it.
type fakeSMTPReply struct {
	code     int
	enhanced string
}

func (f fakeSMTPReply) Error() string              { return fmt.Sprintf("smtp reply %d %s", f.code, f.enhanced) }
func (f fakeSMTPReply) ErrorCode() int             { return f.code }
func (f fakeSMTPReply) EnhancedStatusCode() string { return f.enhanced }

// fakeAPIReply is the same idea for the HTTP API seam.
type fakeAPIReply struct {
	status int
	reason string
}

func (f fakeAPIReply) Error() string          { return fmt.Sprintf("api %d %s", f.status, f.reason) }
func (f fakeAPIReply) HTTPStatus() int        { return f.status }
func (f fakeAPIReply) ProviderReason() string { return f.reason }

// timeoutErr is a net.Error whose Timeout() reports true.
type timeoutErr struct{}

func (timeoutErr) Error() string   { return "i/o timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

func TestClassify(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want Reason
	}{
		{"success", nil, ReasonOK},

		// SMTP auth rejection. The reply CODE decides before the enhanced status
		// does, because 530 carries 5.7.0 ("undefined security status") and would
		// otherwise read as a policy block rather than as "authenticate first".
		{"smtp 535 auth", &textproto.Error{Code: 535, Msg: "5.7.8 Username and Password not accepted"}, ReasonAuthFailed},
		{"smtp 534 auth", &textproto.Error{Code: 534, Msg: "5.7.9 Application-specific password required"}, ReasonAuthFailed},
		{"smtp 530 auth required", fakeSMTPReply{code: 530, enhanced: "5.7.0"}, ReasonAuthFailed},
		{"wrapped smtp auth", fmt.Errorf("send: %w", &textproto.Error{Code: 535, Msg: "bad creds"}), ReasonAuthFailed},

		// The two enhanced codes the fleet exists to watch: per-auth rate and
		// per-IP rate.
		{"smtp 454 4.7.0 auth rate", fakeSMTPReply{code: 454, enhanced: "4.7.0"}, ReasonRateLimited},
		{"smtp 421 4.7.0 connection rate", fakeSMTPReply{code: 421, enhanced: "4.7.0"}, ReasonRateLimited},
		{"smtp 450 4.7.28 ip rate", fakeSMTPReply{code: 450, enhanced: "4.7.28"}, ReasonRateLimited},

		// Other transient security/policy deferrals are throttles we cannot
		// attribute to a rate, and plain 4xx congestion is the same bucket.
		{"smtp 4.7.1 transient policy", fakeSMTPReply{code: 450, enhanced: "4.7.1"}, ReasonThrottled},
		{"smtp 421 no enhanced", fakeSMTPReply{code: 421, enhanced: ""}, ReasonThrottled},
		{"smtp 4.4.5 congestion", fakeSMTPReply{code: 452, enhanced: "4.4.5"}, ReasonThrottled},
		{"smtp 4xx textproto", &textproto.Error{Code: 451, Msg: "try later"}, ReasonThrottled},

		// A permanent security refusal is the per-IP reputation shape.
		{"smtp 5.7.1 blocked", fakeSMTPReply{code: 550, enhanced: "5.7.1"}, ReasonBlocked},
		{"smtp 5.7.26 unauthenticated", fakeSMTPReply{code: 550, enhanced: "5.7.26"}, ReasonBlocked},

		// A permanent non-security 5xx is about the RECIPIENT, not this IP.
		{"smtp 550 no such user", &textproto.Error{Code: 550, Msg: "5.1.1 no such user"}, ReasonRejected},
		{"smtp 552 too large", fakeSMTPReply{code: 552, enhanced: "5.3.4"}, ReasonRejected},

		// go-mail generates code 0 for its own (ambiguous, often post-DATA)
		// failures: the server said nothing we can attribute.
		{"smtp generated no reply", fakeSMTPReply{code: 0, enhanced: ""}, ReasonOther},

		// HTTP API legs (Gmail, Graph).
		{"api 429", fakeAPIReply{status: 429}, ReasonRateLimited},
		{"api 401", fakeAPIReply{status: 401, reason: "authError"}, ReasonAuthFailed},
		{"api 403 rate reason", fakeAPIReply{status: 403, reason: "userRateLimitExceeded"}, ReasonRateLimited},
		{"api 403 daily limit reason", fakeAPIReply{status: 403, reason: "dailyLimitExceeded"}, ReasonRateLimited},
		{"api 403 throttle reason", fakeAPIReply{status: 403, reason: "ApplicationThrottled"}, ReasonRateLimited},
		{"api 403 plain denial", fakeAPIReply{status: 403, reason: "ErrorAccessDenied"}, ReasonBlocked},
		{"api 503", fakeAPIReply{status: 503, reason: "backendError"}, ReasonThrottled},
		{"api 500", fakeAPIReply{status: 500}, ReasonThrottled},
		{"api 400", fakeAPIReply{status: 400, reason: "invalidArgument"}, ReasonRejected},
		{"wrapped api 429", fmt.Errorf("gmail: send: %w", fakeAPIReply{status: 429}), ReasonRateLimited},

		// Network-layer outcomes: the provider never spoke. Still a per-IP fact.
		{"dial timeout", timeoutErr{}, ReasonUnreachable},
		{"deadline exceeded", fmt.Errorf("send: %w", context.DeadlineExceeded), ReasonUnreachable},
		{"connection refused", fmt.Errorf("dial tcp: %w", syscall.ECONNREFUSED), ReasonUnreachable},
		{"connection reset", fmt.Errorf("read: %w", syscall.ECONNRESET), ReasonUnreachable},
		{"eof", fmt.Errorf("smtp dial: %w", io.EOF), ReasonUnreachable},

		// Our own shutdown is not a provider verdict.
		{"context cancelled", fmt.Errorf("send: %w", context.Canceled), ReasonOther},

		// Anything we cannot attribute — including a message-build failure and an
		// SSRF rejection, neither of which the provider ever saw.
		{"message build failure", errors.New("from: invalid address"), ReasonOther},
		{"unknown", errors.New("something inexplicable happened"), ReasonOther},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Classify(tc.err); got != tc.want {
				t.Fatalf("Classify(%v) = %q, want %q", tc.err, got, tc.want)
			}
		})
	}
}

// A reply code in the auth family must win over its enhanced status. Without
// this precedence a 530 5.7.0 ("authenticate first") lands in the same bucket
// as a 550 5.7.1 IP block, and the fleet reads a misconfigured credential as
// reputation damage.
func TestAuthReplyCodeBeatsEnhancedStatus(t *testing.T) {
	if got := Classify(fakeSMTPReply{code: 530, enhanced: "5.7.0"}); got != ReasonAuthFailed {
		t.Fatalf("530 5.7.0 = %q, want %q", got, ReasonAuthFailed)
	}
	// The same enhanced status on a code OUTSIDE the auth family stays a block.
	if got := Classify(fakeSMTPReply{code: 550, enhanced: "5.7.0"}); got != ReasonBlocked {
		t.Fatalf("550 5.7.0 = %q, want %q", got, ReasonBlocked)
	}
}

// Every Reason the classifier can return must be in the persisted vocabulary,
// or the batch insert fails its CHECK and a whole window of counts is dropped.
func TestClassifyOnlyReturnsKnownReasons(t *testing.T) {
	errs := []error{
		nil,
		&textproto.Error{Code: 535},
		fakeSMTPReply{code: 421, enhanced: "4.7.0"},
		fakeAPIReply{status: 429},
		timeoutErr{},
		errors.New("unknown"),
	}
	for _, err := range errs {
		r := Classify(err)
		if !r.Known() {
			t.Fatalf("Classify(%v) returned %q, which is outside the persisted vocabulary", err, r)
		}
	}
}

// Guard: the net.Error branch must fire on Timeout() only, so a non-timeout
// net.Error does not masquerade as an unreachable host.
var _ net.Error = timeoutErr{}
