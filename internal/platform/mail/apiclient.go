package mail

import (
	"context"
	"net"
	"net/http"
	"time"

	"golang.org/x/oauth2"
)

// Timeouts for the provider API legs (Gmail, Microsoft Graph). A provider API
// call is not an SMTP session, so these are not the dial timeouts in guard.go:
// there is no greeting, no multi-command conversation and no per-recipient
// negotiation — one request, one answer, against a large anycast front door.
//
// The ceilings they sit under are the asynq task timeouts in platform/queue
// (sendTimeout 2m, pollTimeout 5m). Every number below is chosen to fail a
// single wedged REQUEST well before the task's own ceiling kills the whole
// pass, so the failure is attributable to one call instead of surfacing as a
// cancelled handler.
const (
	// A TCP connect to Google's or Microsoft's front door is single-digit
	// milliseconds in practice. 10s is headroom for a cold DNS answer on a
	// congested link, and short enough that a blackholed route fails the call
	// rather than parking a worker goroutine.
	apiDialTimeout = 10 * time.Second
	// The handshake is a couple of round trips after connect; same budget as
	// the connect itself for the same reason.
	apiTLSHandshakeTimeout = 10 * time.Second
	// The provider's THINK time — the interval this is the only defence for.
	// users.messages.send and Graph's draft-then-send do real work (MIME parse,
	// spam scan, queueing) before the first header byte; p99 is a few seconds,
	// so 30s absorbs a provider having a bad minute without waiting forever for
	// a header that may never come. http.Client.Timeout does NOT substitute:
	// it also covers the body, so a value loose enough for a large /$value
	// fetch is far too loose to catch a stalled response.
	apiResponseHeaderTimeout = 30 * time.Second
	// Whole-request backstop, headers plus body. Graph's /$value streams an
	// entire RFC822 message (attachments included), so it must be well clear of
	// the header budget — and well UNDER queue.sendTimeout (2m) so a stuck
	// request is reported as a failed call rather than a killed task.
	apiRequestTimeout = 60 * time.Second

	// Connection reuse. http.DefaultTransport allows 2 idle connections per
	// host, which is below both readers' get fan-out (gmailGetConcurrency /
	// graphGetConcurrency, 8): at 2, six of every eight concurrent fetches
	// would pay a fresh TLS handshake. 16 covers the fan-out plus the
	// history/delta call interleaved with it.
	apiMaxIdleConnsPerHost = 16
	apiMaxIdleConns        = 100
	apiIdleConnTimeout     = 90 * time.Second
	apiKeepAlive           = 30 * time.Second
)

// newAPIHTTPClient builds the HTTP client every provider API leg dials through:
// Gmail send/poll/engage and Microsoft Graph send/poll. One constructor rather
// than one per call site, because a copied dialer is a dialer that drifts — and
// the drift is invisible, since neither the bound source address nor a missing
// ResponseHeaderTimeout shows up in any response.
//
// localAddr binds the SOURCE address of the dial to the worker's egress IP
// (mail.ParseEgressIP, INROAD_WORKER_EGRESS_IP), so a mailbox's API traffic
// egresses from the same address as its SMTP/IMAP traffic and the fleet's
// per-mailbox IP affinity holds for API-backed mailboxes too. nil — the common
// case, and every self-hoster who never set the variable — leaves the dialer on
// the OS default route, exactly as before.
//
// SECURITY: like the SMTP/IMAP path (see ParseEgressIP), this sets the SOURCE
// address only and never influences destination selection. Both provider hosts
// are fixed constants, not user input, so there is no SSRF vet to relax here.
func newAPIHTTPClient(localAddr *net.TCPAddr) *http.Client {
	dialer := &net.Dialer{
		Timeout:   apiDialTimeout,
		KeepAlive: apiKeepAlive,
		LocalAddr: localAddr,
	}
	return &http.Client{
		Timeout: apiRequestTimeout,
		Transport: &http.Transport{
			// Kept from http.DefaultTransport: a self-hoster whose egress goes
			// through an outbound proxy configures it with HTTPS_PROXY, and
			// dropping this would route around their only path out.
			Proxy:       http.ProxyFromEnvironment,
			DialContext: dialer.DialContext,
			// Setting DialContext disables net/http's automatic HTTP/2 upgrade
			// unless this is set. Both provider APIs speak h2 and the Google API
			// client expects it; leaving it off would quietly halve the benefit
			// of the connection reuse configured just below.
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          apiMaxIdleConns,
			MaxIdleConnsPerHost:   apiMaxIdleConnsPerHost,
			IdleConnTimeout:       apiIdleConnTimeout,
			TLSHandshakeTimeout:   apiTLSHandshakeTimeout,
			ResponseHeaderTimeout: apiResponseHeaderTimeout,
			ExpectContinueTimeout: 1 * time.Second,
		},
	}
}

// bearerClient wraps base with the OAuth2 transport for one static access token
// (no refresh — the fresh token is minted upstream in coreapi).
//
// It does NOT hand-roll the Authorization header. oauth2.NewClient reads the
// base client out of the context under the oauth2.HTTPClient key and copies its
// Transport, Timeout, Jar and CheckRedirect onto the client it returns, so the
// library's own transport wraps ours and its refresh semantics are untouched.
// Calling oauth2.NewClient WITHOUT seeding that key is the defect this replaces:
// it falls back to http.DefaultClient, and with it http.DefaultTransport — no
// egress binding, no timeouts of our choosing.
//
// A nil base yields a default-route client. That is the zero-value struct a test
// builds as a literal; every production component is built by its constructor
// and carries a client, so the allocation never happens on a real send or poll.
func bearerClient(ctx context.Context, base *http.Client, accessToken string) *http.Client {
	if base == nil {
		base = newAPIHTTPClient(nil)
	}
	return oauth2.NewClient(
		context.WithValue(ctx, oauth2.HTTPClient, base),
		oauth2.StaticTokenSource(&oauth2.Token{AccessToken: accessToken}),
	)
}
