package mail

import (
	"errors"
	"io"
	"net"
	"net/textproto"
	"syscall"

	gomail "github.com/wneessen/go-mail"
)

// Retryable reports whether a send error is transient and safe to retry WITHOUT
// risking a double delivery.
//
// Retryable (transient): a net.Error timeout; connection refused / reset / EOF
// during the dial or greeting (no message data left the process); an SMTP 4xx
// reply (greylisting, rate-limit) from the server BEFORE the message was
// accepted.
//
// NOT retryable: SMTP 5xx (bad recipient, policy), auth failure, message-build
// errors, and the SSRF vetAddr rejection.
//
// Default — unknown/ambiguous, including any error surfaced AFTER the DATA phase
// where the server may already have accepted the message: NOT retryable. This is
// the deliberate "never double, occasionally drop a rare ambiguous send"
// tradeoff: retrying a possibly-delivered message risks a duplicate, so we
// fail-forward (finalize failed + advance) instead. See design spec §3.
func Retryable(err error) bool {
	if err == nil {
		return false
	}

	// An SSRF rejection is a permanent policy decision — never retry it.
	if errors.Is(err, ErrHostNotPermitted) {
		return false
	}

	// Network-layer transients: a timeout, or a refused/reset/EOF while dialing
	// or reading the greeting, means nothing was delivered — safe to retry.
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	if errors.Is(err, io.EOF) ||
		errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, syscall.ECONNRESET) {
		return true
	}

	// go-mail surfaces server replies as *gomail.SendError, which carries the
	// reply code and a temporary flag. A 4xx (or the library's own temp flag)
	// pre-acceptance is retryable; a 5xx or a go-mail-generated (code 0,
	// ambiguous / post-DATA) failure is not.
	var se *gomail.SendError
	if errors.As(err, &se) {
		if se.IsTemp() {
			return true
		}
		if code := se.ErrorCode(); code >= 400 && code < 500 {
			return true
		}
		return false
	}

	// A raw net/smtp reply (used by the connection tester and some transports).
	var tpErr *textproto.Error
	if errors.As(err, &tpErr) {
		return tpErr.Code >= 400 && tpErr.Code < 500
	}

	// Unknown / ambiguous → fail-forward, never retry.
	return false
}
