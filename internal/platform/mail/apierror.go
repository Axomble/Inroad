package mail

import (
	"errors"
	"fmt"

	"google.golang.org/api/googleapi"
)

// APIError is one HTTP API transport failure (Gmail or Microsoft Graph), with
// the provider's answer kept as DATA rather than folded into a sentence.
//
// It exists because both API legs previously reported a non-2xx as
// fmt.Errorf("...unexpected status %d"), which left the status recoverable only
// by parsing English. Nothing downstream could then tell a 429 ("you are sending
// too fast from here") from a 400 ("this message is malformed") — two answers a
// fleet must act on in opposite ways.
//
// Error() prints the provider, the stage and the status and NOTHING ELSE. That
// is the pre-existing promise of the Graph calls, and it is load-bearing: Graph
// echoes request content in its error bodies, and a bearer token reaching a log
// line through an error string is precisely what this type must not enable. The
// cause stays reachable through Unwrap for a caller that deliberately asks.
type APIError struct {
	// Provider is the transport leg ("gmail" | "m365").
	Provider string
	// Op is the stage that failed ("send", "draft", "delete"), so a failure at
	// draft creation is distinguishable from one at send.
	Op string
	// Status is the HTTP status the provider returned.
	Status int
	// Reason is the provider's MACHINE reason token when the client library
	// parsed one (googleapi's error.errors[].reason). It is "" for Graph, whose
	// body is deliberately never read — see the type doc.
	Reason string
	// Err is the underlying cause, reachable via Unwrap but never printed.
	Err error
}

func (e *APIError) Error() string {
	if e.Reason != "" {
		return fmt.Sprintf("%s: %s: status %d (%s)", e.Provider, e.Op, e.Status, e.Reason)
	}
	return fmt.Sprintf("%s: %s: status %d", e.Provider, e.Op, e.Status)
}

// Unwrap exposes the cause for errors.Is/As without printing it.
func (e *APIError) Unwrap() error { return e.Err }

// HTTPStatus and ProviderReason are the accessors the signal classifier consumes
// through its own interface (internal/platform/providersignal.APIReply), so that
// package needs no dependency on this one and a test there can fake this shape.
func (e *APIError) HTTPStatus() int        { return e.Status }
func (e *APIError) ProviderReason() string { return e.Reason }

// gmailSendError converts a Gmail API send failure into an *APIError, keeping
// the status and the first machine reason item the client library parsed.
//
// The Graph leg has no equivalent: it speaks raw HTTP here and builds its
// *APIError directly at each status check, with an empty Reason because Graph's
// error body is deliberately never read (see the APIError doc).
//
// Anything that is NOT an HTTP reply passes through unchanged — a dial timeout
// is not a provider verdict, and giving it a zero status would make a network
// outage read as one. nil passes through as nil so the caller can wrap
// unconditionally.
func gmailSendError(err error) error {
	if err == nil {
		return nil
	}
	var gerr *googleapi.Error
	if !errors.As(err, &gerr) {
		return err
	}
	reason := ""
	if len(gerr.Errors) > 0 {
		reason = gerr.Errors[0].Reason
	}
	return &APIError{Provider: "gmail", Op: "send", Status: gerr.Code, Reason: reason, Err: err}
}
