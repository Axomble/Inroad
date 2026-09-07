package webhookwire

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/inroad/inroad/internal/platform/mail"
)

// ErrBlockedURL is returned when a webhook target URL is not a plain http(s)
// URL, or resolves to an address the SSRF guard forbids. It maps to 422 at the
// HTTP boundary and to a terminal failure in the worker.
var ErrBlockedURL = errors.New("webhook: url not permitted")

// VetURL parses raw, requires an http/https scheme and a host, resolves the
// host, and rejects it when ANY resolved IP is loopback, private
// (RFC1918 / ULA fc00::/7), link-local (incl. the cloud metadata endpoint
// 169.254.169.254), unspecified, or multicast.
//
// allowPrivate (INROAD_WEBHOOK_ALLOW_PRIVATE, for local dev) relaxes ONLY the
// loopback/private check — the always-hostile ranges are rejected regardless.
//
// It delegates the address taxonomy and DNS resolution to mail.ClassifyHost, so
// webhook egress, mailbox dials, and AI-provider base URLs all share one guard
// and one resolver. The check runs at endpoint create, at endpoint update, and
// again in the worker immediately before dialing (the DNS-rebinding window).
func VetURL(ctx context.Context, raw string, allowPrivate bool) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, fmt.Errorf("%w: not a valid URL", ErrBlockedURL)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("%w: scheme %q is not http or https", ErrBlockedURL, u.Scheme)
	}
	host := u.Hostname()
	if host == "" {
		return nil, fmt.Errorf("%w: missing host", ErrBlockedURL)
	}
	private, err := mail.ClassifyHost(ctx, host)
	if err != nil {
		// A resolution failure or an unconditionally-hostile record
		// (link-local, multicast, unspecified) — both are a hard reject.
		return nil, fmt.Errorf("%w: %w", ErrBlockedURL, err)
	}
	if private && !allowPrivate {
		return nil, fmt.Errorf("%w: %q resolves to a private or loopback address", ErrBlockedURL, host)
	}
	return u, nil
}
