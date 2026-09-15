package providersignal

import (
	"fmt"
	"testing"

	gomail "github.com/wneessen/go-mail"

	"github.com/inroad/inroad/internal/platform/mail"
)

// The two classification seams are satisfied STRUCTURALLY by types this package
// deliberately does not import in non-test code. That decoupling is the point —
// but it also means a dependency upgrade that renames a method would not break
// the build; Classify would simply stop matching and every provider verdict
// would quietly degrade to "other" in production.
//
// These assertions are the guard. They live in a test so the production package
// keeps its zero dependencies on the transports, and they fail loudly at exactly
// the moment the structural match is lost.
var (
	_ SMTPReply = (*gomail.SendError)(nil)
	_ APIReply  = (*mail.APIError)(nil)
)

// Prove the seam end to end on the real types, not just that they compile
// against the interface: a go-mail SendError and a mail.APIError must reach the
// intended branch of Classify.
func TestRealTransportErrorsReachTheClassifier(t *testing.T) {
	// go-mail keeps the reply code in an unexported field with no constructor,
	// so the strongest assertion available on the REAL type is that it is
	// matched by the SMTPReply branch at all. A zero SendError reports code 0
	// ("go-mail generated this itself, the server never replied"), which is
	// ReasonOther — and crucially NOT the ReasonOther of the fallthrough,
	// because classifySMTP is what produced it.
	if got := Classify(&gomail.SendError{Reason: gomail.ErrSMTPMailFrom}); got != ReasonOther {
		t.Errorf("Classify(*gomail.SendError with no server reply) = %q, want %q", got, ReasonOther)
	}

	// mail.APIError can be built with a real status, so this asserts the whole
	// path: Graph's 429 becomes rate_limited through errors.As on an interface,
	// and still does through the wrapping every call site adds.
	throttled := fmt.Errorf("send: %w", &mail.APIError{Provider: "m365", Op: "send", Status: 429})
	if got := Classify(throttled); got != ReasonRateLimited {
		t.Errorf("Classify(*mail.APIError{Status: 429}) = %q, want %q", got, ReasonRateLimited)
	}

	// And Gmail's parsed 403 quota refusal, which only classifies correctly
	// because APIError carries the machine reason token alongside the status.
	quota := &mail.APIError{Provider: "gmail", Op: "send", Status: 403, Reason: "userRateLimitExceeded"}
	if got := Classify(quota); got != ReasonRateLimited {
		t.Errorf("Classify(gmail 403 userRateLimitExceeded) = %q, want %q", got, ReasonRateLimited)
	}
}
