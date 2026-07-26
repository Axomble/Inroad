package mail

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/textproto"
	"syscall"
	"testing"
)

// timeoutErr is a net.Error whose Timeout() reports true — a transient dial
// timeout, safe to retry.
type timeoutErr struct{}

func (timeoutErr) Error() string   { return "i/o timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

// nonTimeoutNetErr is a net.Error whose Timeout() reports false — e.g. a
// permanent address error; must NOT be retried on the timeout branch alone.
type nonTimeoutNetErr struct{}

func (nonTimeoutNetErr) Error() string   { return "no such host" }
func (nonTimeoutNetErr) Timeout() bool   { return false }
func (nonTimeoutNetErr) Temporary() bool { return false }

func TestRetryableClassification(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"net timeout", timeoutErr{}, true},
		{"wrapped net timeout", fmt.Errorf("send: %w", timeoutErr{}), true},
		{"eof during dial", io.EOF, true},
		{"wrapped eof", fmt.Errorf("smtp dial: %w", io.EOF), true},
		{"connection refused", fmt.Errorf("dial tcp: %w", syscall.ECONNREFUSED), true},
		{"connection reset", fmt.Errorf("read: %w", syscall.ECONNRESET), true},
		{"smtp 4xx greylist", &textproto.Error{Code: 421, Msg: "try again later"}, true},
		{"smtp 4xx ratelimit", &textproto.Error{Code: 450, Msg: "mailbox busy"}, true},
		{"smtp 5xx bad recipient", &textproto.Error{Code: 550, Msg: "no such user"}, false},
		{"smtp 5xx policy", &textproto.Error{Code: 554, Msg: "rejected"}, false},
		{"ssrf reject", fmt.Errorf("send: %w", ErrHostNotPermitted), false},
		{"non-timeout net error", nonTimeoutNetErr{}, false},
		{"auth failure", errors.New("smtp auth: 535 authentication failed"), false},
		{"message build error", errors.New("from: invalid address"), false},
		{"unknown/ambiguous", errors.New("something inexplicable happened"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Retryable(tc.err); got != tc.want {
				t.Fatalf("Retryable(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// Guard: ensure the net.Error path only fires on Timeout(), not on any net.Error.
var _ net.Error = timeoutErr{}
var _ net.Error = nonTimeoutNetErr{}
