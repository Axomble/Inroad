package mail

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"syscall"
	"testing"
	"time"

	"google.golang.org/api/googleapi"
)

// The four signals this classification exists to tell apart, driven through the
// REAL IMAP client against the in-process server rather than against a
// hand-built error value. A refused connection, a black-holed one, a wrong
// password and a server that speaks no mechanism we implement all arrive here
// as whatever go-imap and net actually produce — which is the only version of
// them that matters, because the classifier's whole job is to read those.
//
// These must not call t.Parallel(): startFakeIMAP swaps a package-level
// transport (see its doc).

// A dead listener is a real ECONNREFUSED from the kernel, not a stubbed reader.
func TestClassifyRefusedConnectionIsTransport(t *testing.T) {
	srv := startFakeIMAP(t, imapScript{User: "u", Pass: "p"})
	srv.stopListening(t)

	r := &NetInboxReader{Timeout: 2 * time.Second}
	_, _, err := r.CurrentState(t.Context(), fakeIMAPConfig("u", "p"))
	if err == nil {
		t.Fatal("a dead listener returned no error")
	}
	if got := ClassifyConnectFailure(err); got != ConnectFailureTransport {
		t.Errorf("ClassifyConnectFailure(%v) = %v, want %v", err, got, ConnectFailureTransport)
	}
}

// Accept-then-stall: the most expensive failure there is, because each attempt
// holds a connection for the whole timeout. It must widen, not be mistaken for
// a credential problem.
func TestClassifySilentServerIsTransport(t *testing.T) {
	startFakeIMAP(t, imapScript{SilentAfterGreeting: true})

	r := &NetInboxReader{Timeout: 500 * time.Millisecond}
	_, _, err := r.CurrentState(t.Context(), fakeIMAPConfig("u", "p"))
	if err == nil {
		t.Fatal("a silent server returned no error")
	}
	if got := ClassifyConnectFailure(err); got != ConnectFailureTransport {
		t.Errorf("ClassifyConnectFailure(%v) = %v, want %v", err, got, ConnectFailureTransport)
	}
}

// A wrong password is not a network outage. It reaches the poller through the
// plain LOGIN command and through a SASL mechanism alike, so both are driven.
func TestClassifyRejectedCredentialIsAuth(t *testing.T) {
	for _, tc := range []struct {
		name   string
		script imapScript
	}{
		{"LOGIN command", imapScript{User: "u", Pass: "right"}},
		{"AUTH=PLAIN, no LOGIN fallback", imapScript{
			User: "u", Pass: "right", Caps: []string{"AUTH=PLAIN", "LOGINDISABLED"},
		}},
		{"AUTH=CRAM-MD5, no LOGIN fallback", imapScript{
			User: "u", Pass: "right", Caps: []string{"AUTH=CRAM-MD5", "LOGINDISABLED"},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			startFakeIMAP(t, tc.script)

			r := &NetInboxReader{Timeout: 2 * time.Second}
			_, _, err := r.CurrentState(t.Context(), fakeIMAPConfig("u", "wrong"))
			if err == nil {
				t.Fatal("a wrong password authenticated")
			}
			if got := ClassifyConnectFailure(err); got != ConnectFailureAuth {
				t.Errorf("ClassifyConnectFailure(%v) = %v, want %v", err, got, ConnectFailureAuth)
			}
			// The credential must not be recoverable from the error a log line
			// would print (docs/security.md, credential handling).
			if msg := err.Error(); containsCommand([]string{msg}, "wrong") {
				t.Errorf("the classified auth error carries the credential: %q", msg)
			}
		})
	}
}

// A server that disables LOGIN and offers only mechanisms this package does not
// implement is the "this server wants GSSAPI" configuration fact: nothing but
// an operator changes the answer, so it is auth, not transport.
func TestClassifyNoSupportedMechanismIsAuth(t *testing.T) {
	startFakeIMAP(t, imapScript{User: "u", Pass: "p", Caps: []string{"AUTH=GSSAPI", "LOGINDISABLED"}})

	r := &NetInboxReader{Timeout: 2 * time.Second}
	_, _, err := r.CurrentState(t.Context(), fakeIMAPConfig("u", "p"))
	if !errors.Is(err, ErrNoIMAPAuthMechanism) {
		t.Fatalf("got %v, want ErrNoIMAPAuthMechanism", err)
	}
	if got := ClassifyConnectFailure(err); got != ConnectFailureAuth {
		t.Errorf("ClassifyConnectFailure(%v) = %v, want %v", err, got, ConnectFailureAuth)
	}
}

// Our own refusal, before any socket exists.
func TestClassifySSRFRefusalIsPolicy(t *testing.T) {
	startFakeIMAP(t, imapScript{User: "u", Pass: "p"})

	r := &NetInboxReader{Timeout: 2 * time.Second} // AllowPrivate false
	_, _, err := r.CurrentState(t.Context(), IMAPConfig{
		Host: "169.254.169.254", Port: 993, Username: "u", Password: "p",
	})
	if !errors.Is(err, ErrHostNotPermitted) {
		t.Fatalf("got %v, want ErrHostNotPermitted", err)
	}
	if got := ClassifyConnectFailure(err); got != ConnectFailurePolicy {
		t.Errorf("ClassifyConnectFailure(%v) = %v, want %v", err, got, ConnectFailurePolicy)
	}
}

// A cancelled context is not the server's fault, and counting it would record a
// failure against a mailbox nothing is wrong with.
//
// The cancellation is delivered where it is actually observable — the DIAL,
// which is the one leg of an IMAP poll that takes a context. Once the session is
// up, go-imap reads under a socket deadline rather than a context (see
// dialIMAP's c.Timeout and newDeadlineConn), so a cancel arriving mid-session
// surfaces as an i/o timeout and classifies as transport. That imprecision is
// bounded — one recorded failure, one rung — and narrowing it would mean
// rebuilding the reader's I/O around the context, which is not this change.
func TestClassifyCancellationIsNotTheServersFault(t *testing.T) {
	startFakeIMAP(t, imapScript{User: "u", Pass: "p"})

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	r := &NetInboxReader{Timeout: 2 * time.Second}
	_, _, err := r.CurrentState(ctx, fakeIMAPConfig("u", "p"))
	if err == nil {
		t.Fatal("a cancelled poll returned no error")
	}
	if got := ClassifyConnectFailure(err); got != ConnectFailureAborted {
		t.Errorf("ClassifyConnectFailure(%v) = %v, want %v", err, got, ConnectFailureAborted)
	}
}

// The ordering rule, stated as a test: an auth-step failure whose real cause is
// the connection dropping is TRANSPORT. Reading the sentinel first would park a
// mailbox on a flaky network at the hour-long cap as though its password were
// wrong.
func TestTransportEvidenceOutranksTheAuthSentinel(t *testing.T) {
	err := fmt.Errorf("%w: imap login: %w", ErrAuthRejected, io.ErrUnexpectedEOF)
	if got := ClassifyConnectFailure(err); got != ConnectFailureTransport {
		t.Errorf("ClassifyConnectFailure(%v) = %v, want %v", err, got, ConnectFailureTransport)
	}
}

// The API transports' answers. 401/403 is their wrong password; 429 and 5xx are
// "come back later", which is what a widening backoff is.
func TestClassifyProviderAPIStatuses(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want ConnectFailure
	}{
		{"graph 401", &APIError{Provider: "m365", Op: "inbox delta", Status: 401}, ConnectFailureAuth},
		{"graph 403", &APIError{Provider: "m365", Op: "inbox delta", Status: 403}, ConnectFailureAuth},
		{"graph 429", &APIError{Provider: "m365", Op: "inbox delta", Status: 429}, ConnectFailureTransport},
		{"graph 503", &APIError{Provider: "m365", Op: "inbox delta", Status: 503}, ConnectFailureTransport},
		{"graph 400", &APIError{Provider: "m365", Op: "inbox delta", Status: 400}, ConnectFailureUnknown},
		{"gmail 401", &googleapi.Error{Code: 401}, ConnectFailureAuth},
		{"gmail 429", &googleapi.Error{Code: 429}, ConnectFailureTransport},
		{"wrapped gmail 500", fmt.Errorf("gmail history: %w", &googleapi.Error{Code: 500}), ConnectFailureTransport},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifyConnectFailure(tc.err); got != tc.want {
				t.Errorf("ClassifyConnectFailure(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// The remaining transport shapes, as the values net and crypto/tls hand us. No
// server can produce a DNS failure or a certificate error against an in-process
// listener, so these are constructed — but they are the library's own types,
// not strings, which is the property that makes the classifier robust.
func TestClassifyTransportShapes(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want ConnectFailure
	}{
		{"nil", nil, ConnectFailureNone},
		{"refused", fmt.Errorf("imap dial: %w", syscall.ECONNREFUSED), ConnectFailureTransport},
		{"reset", fmt.Errorf("imap fetch: %w", syscall.ECONNRESET), ConnectFailureTransport},
		{"host unreachable", fmt.Errorf("imap dial: %w", syscall.EHOSTUNREACH), ConnectFailureTransport},
		{"eof", fmt.Errorf("imap dial: %w", io.EOF), ConnectFailureTransport},
		{"deadline", fmt.Errorf("imap dial: %w", context.DeadlineExceeded), ConnectFailureTransport},
		{"dns", &net.DNSError{Err: "no such host", Name: "mail.example.test", IsNotFound: true}, ConnectFailureTransport},
		{"tls certificate", &tls.CertificateVerificationError{Err: errors.New("expired")}, ConnectFailureTransport},
		{"tls alert", tls.AlertError(80), ConnectFailureTransport},
		{"not a network error at all", errors.New("imap select: NO mailbox does not exist"), ConnectFailureUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifyConnectFailure(tc.err); got != tc.want {
				t.Errorf("ClassifyConnectFailure(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// The labels are logged, so they are part of the operator-facing contract.
func TestConnectFailureLabels(t *testing.T) {
	for kind, want := range map[ConnectFailure]string{
		ConnectFailureNone:      "none",
		ConnectFailureAborted:   "aborted",
		ConnectFailureTransport: "transport",
		ConnectFailureAuth:      "auth",
		ConnectFailurePolicy:    "policy",
		ConnectFailureUnknown:   "unknown",
	} {
		if got := kind.String(); got != want {
			t.Errorf("ConnectFailure(%d).String() = %q, want %q", kind, got, want)
		}
	}
}
