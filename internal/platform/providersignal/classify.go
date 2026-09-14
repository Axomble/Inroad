package providersignal

import (
	"context"
	"errors"
	"io"
	"net"
	"net/textproto"
	"strings"
	"syscall"
)

// SMTPReply is the shape of an SMTP transport error that carries the server's
// own reply. It is declared HERE, by the consumer, rather than imported from the
// transport, for three reasons that all matter:
//
//   - this package stays free of go-mail, so nothing in platform/mail has to
//     know that signals exist;
//   - a test can implement it and reach every branch below. The production
//     implementor (*go-mail.SendError) stores its code and enhanced status in
//     unexported fields with no constructor, and the SSRF guard forbids dialing
//     loopback so no in-process SMTP server can produce a real one — without an
//     interface here, every branch under it would be permanently untestable
//     (CONTRIBUTING.md's second "tests that assert nothing" shape);
//   - it is satisfied structurally, so a go-mail upgrade that renames either
//     method is caught by smtpreply_test.go's assertion rather than by silently
//     falling through to ReasonOther in production.
//
// *net/textproto.Error does NOT satisfy it (it carries fields, not methods) and
// is handled separately below.
type SMTPReply interface {
	error
	// ErrorCode is the three-digit reply code, or 0 when the failure was
	// generated client-side and the server never replied.
	ErrorCode() int
	// EnhancedStatusCode is the RFC 3463 status ("4.7.28"), or "" when the
	// server does not advertise ENHANCEDSTATUSCODES or sent none.
	EnhancedStatusCode() string
}

// APIReply is the same seam for the two HTTP API transports (Gmail, Graph).
// ProviderReason is the provider's MACHINE reason token — googleapi's
// `error.errors[].reason`, Graph's `error.code` — never its human message, which
// this package does not read.
type APIReply interface {
	error
	HTTPStatus() int
	ProviderReason() string
}

// smtpAuthCodes are the reply codes that mean "your credentials were not
// accepted" (or "authenticate first"). They are checked BEFORE the enhanced
// status because 530 carries 5.7.0 — "other or undefined security status" —
// which would otherwise classify as a policy block and make a rotated app
// password read as reputation damage.
func isSMTPAuthCode(code int) bool {
	return code == 530 || code == 534 || code == 535
}

// rateReasonTokens are substrings of the MACHINE reason codes the API providers
// use when a rate or quota is the cause: googleapi's rateLimitExceeded /
// userRateLimitExceeded / dailyLimitExceeded / quotaExceeded, and Graph's
// ApplicationThrottled. Matching a token inside a documented identifier is not
// the prose-parsing this package refuses to do — these are enum values, and a
// provider adding `perUserRateLimitExceeded` should classify the same way
// without a code change.
var rateReasonTokens = []string{"ratelimit", "limitexceeded", "quotaexceeded", "throttl"}

// Classify maps one transport outcome onto the persisted vocabulary. A nil error
// is ReasonOK; everything else is matched against the most specific evidence
// available, in descending order of how much the PROVIDER told us:
//
//  1. a structured SMTP reply (code + enhanced status)
//  2. a structured HTTP API reply (status + machine reason)
//  3. our own context (cancellation is a shutdown, not a verdict)
//  4. the network layer (the provider never spoke)
//  5. ReasonOther
//
// It never returns a Reason outside the closed vocabulary, because the persisted
// CHECK constraint mirrors that set and a stray value would fail the batch insert
// and lose an entire window (TestClassifyOnlyReturnsKnownReasons).
func Classify(err error) Reason {
	if err == nil {
		return ReasonOK
	}

	var reply SMTPReply
	if errors.As(err, &reply) {
		return classifySMTP(reply.ErrorCode(), reply.EnhancedStatusCode())
	}
	// The AUTH phase, the connection tester and go-imap's underlying net/smtp
	// surface a bare *textproto.Error, which carries no enhanced status of its
	// own — the provider may have put one in Msg, but reading it back out is
	// prose parsing, so the reply code alone decides.
	var tpErr *textproto.Error
	if errors.As(err, &tpErr) {
		return classifySMTP(tpErr.Code, "")
	}

	var api APIReply
	if errors.As(err, &api) {
		return classifyAPI(api.HTTPStatus(), api.ProviderReason())
	}

	// Our own shutdown cancelling an in-flight call is not something the
	// provider did, and counting it as one would make every graceful stop look
	// like a fleet incident. Checked before the network branch because a
	// cancelled dial also reports as a net.Error timeout.
	if errors.Is(err, context.Canceled) {
		return ReasonOther
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return ReasonUnreachable
	}

	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return ReasonUnreachable
	}
	if errors.Is(err, io.EOF) ||
		errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, syscall.ECONNRESET) {
		return ReasonUnreachable
	}

	return ReasonOther
}

// classifySMTP turns a reply code plus an optional RFC 3463 enhanced status into
// a Reason. enhanced is "" whenever the server does not advertise
// ENHANCEDSTATUSCODES, which is why every branch has a code-only fallback.
func classifySMTP(code int, enhanced string) Reason {
	if isSMTPAuthCode(code) {
		return ReasonAuthFailed
	}
	if r, ok := classifyEnhanced(enhanced); ok {
		return r
	}
	switch {
	case code >= 400 && code < 500:
		// Every 4xx is a transient refusal. Without an enhanced status the
		// server has not told us whether a rate caused it, so it is a throttle
		// rather than a rate limit — guessing here would put ordinary
		// greylisting into the counter the placement scorer will read.
		return ReasonThrottled
	case code >= 500 && code < 600:
		return ReasonRejected
	default:
		// Code 0: go-mail generated this failure itself (an ambiguous or
		// post-DATA error) and no server reply was ever parsed.
		return ReasonOther
	}
}

// classifyEnhanced reads the RFC 3463 status. ok=false means the status was
// absent or in a class this function has no opinion on, and the caller should
// fall back to the reply code.
func classifyEnhanced(enhanced string) (Reason, bool) {
	switch {
	case enhanced == "":
		return "", false
	// The two the fleet exists to watch. 4.7.0 is what Google returns for both
	// "too many login attempts" (454) and "try again later, closing connection"
	// (421); 4.7.28 is RFC 7372's explicit per-IP rate limit.
	case enhanced == "4.7.0" || enhanced == "4.7.28":
		return ReasonRateLimited, true
	// 5.7.8 is "authentication credentials invalid" — belt and braces for a
	// provider that pairs it with a reply code outside the 53x family.
	case enhanced == "5.7.8":
		return ReasonAuthFailed, true
	// Any other transient security/policy status: a refusal we cannot attribute
	// to a rate.
	case strings.HasPrefix(enhanced, "4.7."):
		return ReasonThrottled, true
	// A PERMANENT security status is the per-IP reputation shape: the provider
	// has decided against the connecting identity.
	case strings.HasPrefix(enhanced, "5.7."):
		return ReasonBlocked, true
	case strings.HasPrefix(enhanced, "4."):
		return ReasonThrottled, true
	case strings.HasPrefix(enhanced, "5."):
		return ReasonRejected, true
	default:
		return "", false
	}
}

// classifyAPI turns an HTTP status plus the provider's machine reason token into
// a Reason.
func classifyAPI(status int, reason string) Reason {
	switch {
	case status == 429:
		return ReasonRateLimited
	case status == 401:
		return ReasonAuthFailed
	case status == 403:
		// A 403 is the one status where the reason token is load-bearing: both
		// providers use it for a quota AND for a plain denial, and the two are
		// opposite signals — one says slow down, the other says this identity is
		// not welcome.
		if namesRate(reason) {
			return ReasonRateLimited
		}
		return ReasonBlocked
	case status >= 500 && status < 600:
		// Including 503: the provider is refusing transiently but has not named
		// a rate, which is exactly ReasonThrottled's definition.
		return ReasonThrottled
	case status >= 400 && status < 500:
		return ReasonRejected
	default:
		return ReasonOther
	}
}

// namesRate reports whether a provider's machine reason token names a rate or
// quota. Case- and separator-insensitive so "userRateLimitExceeded",
// "USER_RATE_LIMIT_EXCEEDED" and "ApplicationThrottled" all match.
func namesRate(reason string) bool {
	normalized := strings.ToLower(strings.NewReplacer("_", "", "-", "", " ", "").Replace(reason))
	for _, token := range rateReasonTokens {
		if strings.Contains(normalized, token) {
			return true
		}
	}
	return false
}
