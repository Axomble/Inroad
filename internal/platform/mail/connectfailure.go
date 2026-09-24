package mail

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"syscall"

	"google.golang.org/api/googleapi"
)

// ConnectFailure says WHY a provider connection failed, in the four shapes that
// call for different handling. It is the vocabulary the inbox poller's backoff
// is expressed in (internal/worker/inbox), and it lives here because the error
// shapes it reads — go-imap's replies, net's syscall errnos, tls's verification
// errors, googleapi's statuses — are this package's business and nobody else's.
//
// It is deliberately NOT Retryable() (retry.go). That answers a different and
// narrower question about a SEND: "can this be retried without risking a double
// delivery?". A poll cannot deliver anything twice, so nothing here is about
// safety; it is about how soon it is worth dialing again, and about not
// repeating a rejected sign-in.
type ConnectFailure uint8

const (
	// ConnectFailureNone is a nil error.
	ConnectFailureNone ConnectFailure = iota
	// ConnectFailureAborted is OUR OWN context being cancelled — a worker
	// shutting down, or a caller that gave up. It says nothing about the server,
	// and counting it would record a failure against a mailbox nothing is wrong
	// with.
	//
	// It is recognised where cancellation is observable: the DNS resolve and the
	// dial, the two legs that take a context. Once an IMAP session is up,
	// go-imap reads under a socket deadline instead (dialIMAP's c.Timeout and
	// newDeadlineConn), so a cancel arriving mid-session reads as a timeout and
	// lands in ConnectFailureTransport — one recorded failure, one rung, which
	// the next successful poll clears.
	ConnectFailureAborted
	// ConnectFailureTransport is the server being unreachable or unresponsive:
	// refused, reset, black-holed, DNS-less, or a TLS handshake that did not
	// complete. It is the one class that routinely fixes itself.
	ConnectFailureTransport
	// ConnectFailureAuth is the server understanding us perfectly and refusing
	// the credential — a wrong password, a revoked token, or a server that will
	// speak no mechanism we implement. It cannot fix itself, and every repeat is
	// another rejected sign-in against the account.
	ConnectFailureAuth
	// ConnectFailurePolicy is OUR refusal, not the server's: the SSRF guard
	// rejected the host or port. No socket was opened. Nothing but an operator
	// editing the mailbox will change the answer.
	ConnectFailurePolicy
	// ConnectFailureUnknown is everything else — a provider status we do not
	// recognise, a malformed reply, a library error. The caller should assume it
	// will recur, because the alternative assumption is the one that produced the
	// hammering in the first place.
	ConnectFailureUnknown
)

// String is the stable label this classification is logged under.
func (c ConnectFailure) String() string {
	switch c {
	case ConnectFailureNone:
		return "none"
	case ConnectFailureAborted:
		return "aborted"
	case ConnectFailureTransport:
		return "transport"
	case ConnectFailureAuth:
		return "auth"
	case ConnectFailurePolicy:
		return "policy"
	default:
		return "unknown"
	}
}

// ClassifyConnectFailure sorts a provider connection error into one of the
// shapes above.
//
// THE ORDER IS THE DESIGN, not an implementation detail:
//
//   - Cancellation is read first, because a cancelled dial arrives wrapped in
//     the same *net.OpError a refused one does and would otherwise be
//     indistinguishable from the server's fault.
//   - Our own SSRF refusal next: it is a policy verdict, and it never touched
//     the network, so no transport evidence exists to weigh against it.
//   - TRANSPORT BEFORE AUTH, which is the subtle one. An authentication attempt
//     that dies because the connection dropped mid-LOGIN is wrapped as an auth
//     failure (it failed at the auth step) but is transport in substance.
//     Reading transport first means a flaky network is never mistaken for a bad
//     password — and mistaking it for one would park a healthy mailbox on the
//     hour-long cap.
//
// It never inspects error TEXT. Every branch is a sentinel, a type or a status
// code, so a library changing its wording cannot silently reclassify a mailbox.
func ClassifyConnectFailure(err error) ConnectFailure {
	if err == nil {
		return ConnectFailureNone
	}
	if errors.Is(err, context.Canceled) {
		return ConnectFailureAborted
	}
	if errors.Is(err, ErrHostNotPermitted) {
		return ConnectFailurePolicy
	}
	if isTransportFailure(err) {
		return ConnectFailureTransport
	}
	if errors.Is(err, ErrAuthRejected) || errors.Is(err, ErrNoIMAPAuthMechanism) {
		return ConnectFailureAuth
	}
	if status, ok := apiStatus(err); ok {
		return classifyAPIStatus(status)
	}
	return ConnectFailureUnknown
}

// isTransportFailure reports whether the error is the network failing rather
// than anyone's decision.
//
// context.DeadlineExceeded counts, and is the case the fake IMAP server's
// silent-after-greeting mode exists for: a server that completes the TCP
// handshake and then says nothing is the most expensive failure there is,
// because each attempt holds a connection for the whole timeout.
func isTransportFailure(err error) bool {
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	if errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, io.EOF) ||
		errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.ECONNABORTED) ||
		errors.Is(err, syscall.EHOSTUNREACH) ||
		errors.Is(err, syscall.ENETUNREACH) ||
		errors.Is(err, syscall.EPIPE) ||
		errors.Is(err, syscall.ETIMEDOUT) {
		return true
	}
	// A TLS handshake that did not complete: an expired or self-signed
	// certificate, a protocol mismatch, an alert. It is not a credential and it
	// is not our policy — the connection never came up — and it does resolve on
	// its own when the operator's certificate is renewed.
	var certErr *tls.CertificateVerificationError
	if errors.As(err, &certErr) {
		return true
	}
	var recordErr tls.RecordHeaderError
	if errors.As(err, &recordErr) {
		return true
	}
	var alertErr tls.AlertError
	if errors.As(err, &alertErr) {
		return true
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return true
	}
	// The catch-all for a dial/read/write that failed for a reason not named
	// above. Last, so a more specific branch always wins.
	var opErr *net.OpError
	return errors.As(err, &opErr)
}

// apiStatus extracts the HTTP status from a provider API failure, from either
// shape this package produces: its own *APIError, or the googleapi error the
// Gmail client library returns directly.
func apiStatus(err error) (int, bool) {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.Status, true
	}
	var gerr *googleapi.Error
	if errors.As(err, &gerr) {
		return gerr.Code, true
	}
	return 0, false
}

// classifyAPIStatus maps a provider's HTTP answer onto the same four shapes.
//
// 401/403 is the API transport's wrong password: a revoked or unconsented token.
// 429 and 5xx are the provider telling us to come back later, which is exactly
// what a widening backoff does. Anything else is a request we got wrong, which
// will recur until the code changes — unknown, not transport.
func classifyAPIStatus(status int) ConnectFailure {
	switch {
	case status == 401 || status == 403:
		return ConnectFailureAuth
	case status == 429 || status >= 500:
		return ConnectFailureTransport
	default:
		return ConnectFailureUnknown
	}
}
