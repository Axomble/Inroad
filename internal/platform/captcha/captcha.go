// Package captcha is the human-verification seam guarding the account-creation
// and credential-guessing surfaces (register / login / email-OTP start).
//
// It is fail-closed by construction: Verify returns nil ONLY when a challenge
// genuinely passed. A configured verifier rejects a missing/invalid token AND a
// siteverify transport failure (a network or upstream 5xx error must never fall
// open). An UNCONFIGURED deployment uses the no-op verifier, which always passes,
// so self-hosters who don't run a captcha provider are never blocked.
package captcha

import (
	"context"
	"errors"
)

// ErrRejected is returned by Verify when a challenge did not pass (missing token,
// invalid token, or the provider reported failure). Callers reject the request.
var ErrRejected = errors.New("captcha verification failed")

// Verifier decides whether a client-supplied captcha token proves a human. A nil
// error means the challenge passed; any non-nil error means REJECT — including a
// provider/transport failure, so an outage fails closed rather than open. ip is
// the resolved client IP (may be "") passed through to providers that accept it.
type Verifier interface {
	Verify(ctx context.Context, token, ip string) error
}

// noop always passes. It backs an UNCONFIGURED deployment so the captcha gate is
// effectively off (self-hosters without a provider aren't blocked).
type noop struct{}

// NewNoop returns a Verifier that accepts every request. Used when no captcha
// provider secret is configured.
func NewNoop() Verifier { return noop{} }

func (noop) Verify(context.Context, string, string) error { return nil }
