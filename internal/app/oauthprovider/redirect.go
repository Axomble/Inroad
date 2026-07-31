package oauthprovider

import (
	"errors"
	"net/url"
)

// ErrInvalidRedirectURI is returned by validateRedirectURI for any URI the
// allowlist must refuse at registration time.
var ErrInvalidRedirectURI = errors.New("invalid redirect_uri")

// validateRedirectURI enforces the registration-time redirect-URI allowlist policy
// (RFC 6749 §3.1.2 + RFC 8252 native-app loopback), the anti-open-redirect
// foundation: only a URI that passes here is ever stored, and /authorize later
// matches the request's redirect_uri against the stored set by EXACT string
// equality.
//
// Accepted:
//   - Absolute https:// URIs with a host.
//   - Loopback http:// URIs (RFC 8252): host is exactly 127.0.0.1 or localhost
//     (an optional port is allowed; the path is arbitrary).
//
// Rejected (each a distinct anti-abuse reason):
//   - Any non-https, non-loopback-http scheme — notably javascript: and data:
//     (XSS/exfil vectors) and plain http:// to a non-loopback host (cleartext).
//   - A URI carrying a fragment (#…): the authorization response appends query
//     params, and a fragment can shadow/rewrite them.
//   - An opaque or relative URI, or one missing a host where a host is required.
func validateRedirectURI(raw string) error {
	if raw == "" {
		return ErrInvalidRedirectURI
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ErrInvalidRedirectURI
	}
	// Opaque URIs (e.g. "javascript:alert(1)", "mailto:x") have no hierarchical
	// authority; url.Parse fills Opaque, not Host. Reject outright.
	if u.Opaque != "" {
		return ErrInvalidRedirectURI
	}
	// A fragment can shadow the appended query response — never allow one.
	if u.Fragment != "" || rawHasFragment(raw) {
		return ErrInvalidRedirectURI
	}
	switch u.Scheme {
	case "https":
		if u.Hostname() == "" {
			return ErrInvalidRedirectURI
		}
		return nil
	case "http":
		// RFC 8252 native-app loopback exception ONLY.
		if isLoopbackHost(u.Hostname()) {
			return nil
		}
		return ErrInvalidRedirectURI
	default:
		// javascript:, data:, ftp:, custom app schemes, empty scheme (relative): all
		// rejected. (Custom-scheme native redirects are out of scope for P6a.)
		return ErrInvalidRedirectURI
	}
}

// isLoopbackHost reports whether host is a permitted loopback literal. Only the IPv4
// loopback address, its IPv6 form, and the "localhost" name are accepted — not an
// arbitrary 127.0.0.0/8 address string (which url.Hostname would still surface
// verbatim), keeping the allowlist explicit.
func isLoopbackHost(host string) bool {
	switch host {
	case "127.0.0.1", "localhost", "::1":
		return true
	default:
		return false
	}
}

// rawHasFragment reports whether the raw URI text contains a '#'. url.Parse strips a
// trailing empty fragment ("https://x/cb#" yields Fragment==""), so this catches a
// bare-hash URI that the Fragment check alone would miss.
func rawHasFragment(raw string) bool {
	for i := 0; i < len(raw); i++ {
		if raw[i] == '#' {
			return true
		}
	}
	return false
}

// buildRedirect appends the given query parameters to an already-VALIDATED redirect
// URI (one that exact-matched a registered allowlist entry) and returns the full
// target URL. It merges into any query the registered URI already carries and never
// touches the fragment (registration rejected fragments). Only non-empty values are
// added, so an absent `state` is simply not echoed.
func buildRedirect(validatedRedirectURI string, params map[string]string) (string, error) {
	u, err := url.Parse(validatedRedirectURI)
	if err != nil {
		// Unreachable in practice: only a validated URI reaches here. Fail loud.
		return "", err
	}
	q := u.Query()
	for k, v := range params {
		if v != "" {
			q.Set(k, v)
		}
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}
