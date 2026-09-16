package credbroker

import (
	"crypto/subtle"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
)

// The FLEET CHANNEL's two shared rules: how its base URL and token are
// validated, and how a request on it is authenticated.
//
// They live in this package because this package already owns them — MinTokenLen
// is what internal/platform/config validates INROAD_FLEET_BROKER_TOKEN against,
// and the https rule is what NewHTTPOpener has always enforced. They are
// EXPORTED because the credential broker is no longer the only transport on the
// channel: internal/coreapi/remote (the coreapi remote transport, slice 1) is
// served on the same listener, at the same base URL, under the same token, by
// deliberate decision — see cmd/inroad/fleetlistener.go for why one surface
// rather than two.
//
// One implementation, not two, is the point. A second constant-time compare is
// a second chance to write `==`, and a second URL check is a second chance to
// forget that a missing scheme must be refused rather than assumed.

// ParseEndpoint validates a fleet-channel base URL and bearer token and returns
// the base URL normalised (trailing slash removed) for path concatenation.
//
// https is REQUIRED unless allowPlaintext is explicitly set. The channel
// carries the bearer token, decrypted SMTP passwords and OAuth access tokens,
// and now a workspace's suppression answers, so a plaintext hop puts in the
// clear exactly the material the channel exists to protect. The shape and the
// reasoning are storage.FromEnv's INROAD_S3_ENDPOINT rule
// (docs/security.md invariant 6): a missing scheme is refused rather than
// assumed, because assuming is how a typo becomes a silent downgrade, and the
// opt-out has to be CHOSEN — it exists for a control plane reachable only over
// a trusted private network.
//
// The URL is OPERATOR-supplied, never user-supplied, so it does not go through
// (and does not need) the mail.vetAddr SSRF guard — the same reasoning
// invariant 6 applies to the S3 endpoint.
func ParseEndpoint(baseURL, token string, allowPlaintext bool) (string, error) {
	if len(token) < MinTokenLen {
		return "", ErrWeakToken
	}
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil {
		return "", fmt.Errorf("credbroker: fleet url: %w", err)
	}
	// Scheme first: "control.example:8090" parses with Scheme="control.example"
	// and no host, and the useful thing to tell an operator who wrote that is
	// that the scheme is missing, not that the host is.
	switch u.Scheme {
	case "https":
	case "http":
		if !allowPlaintext {
			return "", ErrInsecureURL
		}
	default:
		return "", ErrInsecureURL
	}
	if u.Host == "" {
		return "", fmt.Errorf("credbroker: fleet url %q has no host", baseURL)
	}
	return strings.TrimSuffix(u.String(), "/"), nil
}

// RequireToken returns the fleet channel's authentication middleware: a
// constant-time comparison against the shared bearer token, run BEFORE the body
// is read so an unauthenticated caller cannot make the process parse anything.
//
// Authentication is a single shared token rather than per-worker identity. That
// is honestly weaker — every fleet host holds the same credential, so revoking
// one revokes all, and the control plane cannot tell which host is asking — but
// per-worker identity only buys something once a worker can be scoped to a
// subset of mailboxes, and today it cannot be (see HTTPOpener's doc). The
// shared token is the right size for the boundary that actually exists.
//
// A weak token is refused by the handler constructors that call this, not here:
// middleware that could fail has no good way to say so.
func RequireToken(token string, logger *slog.Logger) func(http.Handler) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			presented, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
			if !ok || subtle.ConstantTimeCompare([]byte(presented), []byte(token)) != 1 {
				// The remote address only. Never the presented token, not even a
				// prefix: a partial secret in a log is still a secret in a log.
				logger.Warn("fleet channel: rejected an unauthenticated request",
					"remote", r.RemoteAddr, "path", r.URL.Path)
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Cache-Control", "no-store")
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
