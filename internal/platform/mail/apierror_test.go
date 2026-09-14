package mail

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"google.golang.org/api/googleapi"
)

// The Gmail and Graph legs used to throw their HTTP status away into a format
// string ("graph: send: unexpected status %d"), which left every caller with
// prose to parse. APIError keeps the status as data so a caller can act on a 429
// without reading English.
func TestAPIErrorCarriesStatusAndReason(t *testing.T) {
	err := &APIError{Provider: "gmail", Op: "send", Status: 429, Reason: "userRateLimitExceeded"}
	if got := err.HTTPStatus(); got != 429 {
		t.Errorf("HTTPStatus() = %d, want 429", got)
	}
	if got := err.ProviderReason(); got != "userRateLimitExceeded" {
		t.Errorf("ProviderReason() = %q, want %q", got, "userRateLimitExceeded")
	}
}

// The status has to survive wrapping, because every caller wraps.
func TestAPIErrorIsFoundThroughWrapping(t *testing.T) {
	wrapped := fmt.Errorf("send: %w", fmt.Errorf("gmail: %w", &APIError{Provider: "gmail", Op: "send", Status: 429}))
	var target *APIError
	if !errors.As(wrapped, &target) {
		t.Fatalf("errors.As did not find *APIError in %v", wrapped)
	}
	if target.Status != 429 {
		t.Errorf("status = %d, want 429", target.Status)
	}
}

// APIError must keep the existing promise that a non-2xx reports the status
// only, never the response body: Graph echoes request content, and a bearer
// token landing in a log line is the failure this guards against.
func TestAPIErrorMessageOmitsAnyBody(t *testing.T) {
	err := &APIError{
		Provider: "m365",
		Op:       "draft",
		Status:   403,
		Err:      errors.New("Bearer ya29.SECRET-TOKEN-VALUE"),
	}
	msg := err.Error()
	if strings.Contains(msg, "SECRET-TOKEN-VALUE") {
		t.Fatalf("APIError.Error() leaked the wrapped cause: %q", msg)
	}
	for _, want := range []string{"m365", "draft", "403"} {
		if !strings.Contains(msg, want) {
			t.Errorf("APIError.Error() = %q, want it to name %q", msg, want)
		}
	}
	// The cause is still reachable for a caller that deliberately unwraps —
	// it is just not printed by default.
	if !errors.Is(err.Unwrap(), err.Err) {
		t.Error("Unwrap() must expose the cause even though Error() does not print it")
	}
}

// A Gmail API failure arrives as *googleapi.Error, which the library has
// already parsed. Converting it keeps the status AND the machine reason token,
// so a 403 quota refusal is distinguishable from a 403 denial.
func TestAPIErrorFromGoogleKeepsStatusAndReason(t *testing.T) {
	gerr := &googleapi.Error{
		Code:    403,
		Message: "User-rate limit exceeded.",
		Errors:  []googleapi.ErrorItem{{Reason: "userRateLimitExceeded", Message: "User-rate limit exceeded."}},
	}
	converted := apiErrorFrom("gmail", "send", gerr)

	var target *APIError
	if !errors.As(converted, &target) {
		t.Fatalf("apiErrorFrom did not produce an *APIError: %v", converted)
	}
	if target.Status != 403 {
		t.Errorf("status = %d, want 403", target.Status)
	}
	if target.Reason != "userRateLimitExceeded" {
		t.Errorf("reason = %q, want %q", target.Reason, "userRateLimitExceeded")
	}
}

// A googleapi.Error with no Errors items still carries a status worth keeping —
// a 429 typically has no reason item at all.
func TestAPIErrorFromGoogleWithoutReasonItems(t *testing.T) {
	converted := apiErrorFrom("gmail", "send", &googleapi.Error{Code: 429, Message: "Too Many Requests"})
	var target *APIError
	if !errors.As(converted, &target) {
		t.Fatalf("apiErrorFrom did not produce an *APIError: %v", converted)
	}
	if target.Status != 429 || target.Reason != "" {
		t.Errorf("got status=%d reason=%q, want 429 and an empty reason", target.Status, target.Reason)
	}
}

// Anything that is not a provider HTTP reply must pass through UNCHANGED. A
// dial failure is not an API status, and inventing a zero status for it would
// make a network outage read as a provider verdict.
func TestAPIErrorFromPassesThroughNonHTTPErrors(t *testing.T) {
	cause := errors.New("dial tcp: i/o timeout")
	if got := apiErrorFrom("gmail", "send", cause); !errors.Is(got, cause) {
		t.Fatalf("apiErrorFrom rewrote a non-HTTP error: %v", got)
	}
	var target *APIError
	if errors.As(apiErrorFrom("gmail", "send", cause), &target) {
		t.Fatal("a dial failure was turned into an *APIError")
	}
	if got := apiErrorFrom("gmail", "send", nil); got != nil {
		t.Fatalf("apiErrorFrom(nil) = %v, want nil", got)
	}
}
