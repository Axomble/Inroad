// Package remote is the coreapi seam's REMOTE transport: the execution plane
// asks the control plane over HTTP instead of reaching into Postgres itself.
//
// Why it exists. internal/coreapi is documented as the control⇄execution
// boundary "in-process now, HTTP later", and the plane split is only half
// built: since #207 a role=send worker holds no master key, so the ciphertext
// it can read it cannot decrypt — but cmd/worker still opens a pgxpool, so it
// can still READ every workspace's contacts, message bodies and reply text.
// Until that pool is gone the split is an operational lever, not a containment
// boundary, and a worker cannot run on a host the operator does not control.
// This package is the first of the slices that make it true.
//
// Scope, deliberately. Slice 1 carries exactly ONE method, IsSuppressed. It is
// read-only, side-effect free, answers a single boolean, and sits directly in
// front of every send — so it exercises the latency and the correctness of a
// network hop on the hot path without the claim-protocol risk that moving the
// send claim would carry. The pool is untouched; later slices port more
// methods, and the pool goes when nothing needs it.
//
// No cache. A TTL on a suppression answer is a real correctness knob — a stale
// negative is mail delivered to someone who opted out — and deserves its own
// decision when the measured cost of the direct call justifies it, not a
// default chosen while wiring the transport. One call per check.
//
// Conventions are credbroker's, reused rather than reinvented: one file for the
// wire shapes so the two sides cannot drift, fixed error strings so the seam is
// not a probe oracle, and workspace-pinning that ADDS to the tenant pin rather
// than replacing it (the handler pins exactly as the in-process path does, and
// the request carries the workspace so it can).
//
// Layering: this is an implementation of the coreapi seam, so it sits beside
// coreapi/inprocess. It imports internal/platform/credbroker for the fleet
// channel's shared URL/token rules — the two transports share one listener and
// one token by decision (see cmd/inroad/fleetlistener.go), so they share one
// validator.
package remote

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/platform/credbroker"
)

// ErrUnauthorized is a 401/403 from the control plane — a wrong or missing
// token. Distinguished from a transport failure so an operator reading a
// worker's logs can tell "my token is wrong" from "the control plane is down".
var ErrUnauthorized = errors.New("coreapi remote: rejected by the control plane")

// Timeouts. Every one is chosen here rather than inherited, and every one is
// sized against the asynq ceiling the call runs INSIDE (queue.sendTimeout 2m,
// queue.pollTimeout 5m — internal/platform/queue). A wedged coreapi call must
// fail the CALL: attributable, retried by asynq, one line in a log. If it
// instead burned the task's whole budget, the operator would see a killed
// handler with no cause, which is the failure mode #215 sized the provider API
// legs (internal/platform/mail/apiclient.go) to avoid.
const (
	// requestTimeout bounds the whole call, headers and body. The control
	// plane's work is one indexed SELECT on (workspace_id, lower(email)) and
	// the answer is a single boolean, so 10s is already generous — it is a
	// backstop for a control plane having a bad moment, not a budget anyone
	// expects to spend. At ~8% of the tightest ceiling above it, the rest of a
	// send's two minutes is still there for the SMTP conversation this check
	// precedes (mail's SMTP dial timeout alone is 30s).
	requestTimeout = 10 * time.Second
	// dialTimeout bounds getting a TCP connection to the control plane. It is
	// on the operator's own network — the fleet listener is bound to an address
	// only the fleet can reach — so a connect is single-digit milliseconds in
	// practice. 5s is headroom for a cold DNS answer on a congested link and
	// short enough that a blackholed route fails the call instead of parking a
	// send slot behind it.
	dialTimeout = 5 * time.Second
	// tlsHandshakeTimeout is a couple of round trips after connect; same budget
	// as the connect for the same reason.
	tlsHandshakeTimeout = 5 * time.Second
	// responseHeaderTimeout is the control plane's THINK time, and the interval
	// requestTimeout does not usefully separate: a body that never arrives is
	// not covered by a connect timeout, and http.Client.Timeout covers the body
	// too, so a value loose enough for a large response is far too loose to
	// catch a stalled one. One indexed SELECT that has not begun answering in
	// 5s means an overloaded control plane, and a suppression check must fail
	// the send rather than hold its slot waiting for it to recover.
	responseHeaderTimeout = 5 * time.Second

	// Connection reuse. This runs once per send on a worker whose concurrency
	// is 10 (INROAD_WORKER_CONCURRENCY), so without pooling every send would
	// pay a fresh TLS handshake — two extra round trips on the hot path, which
	// would be a self-inflicted argument for the cache this slice deliberately
	// does not have. http.DefaultTransport allows 2 idle connections per host;
	// 16 covers the fan-out with headroom.
	maxIdleConnsPerHost = 16
	maxIdleConns        = 32
	idleConnTimeout     = 90 * time.Second
	keepAlive           = 30 * time.Second

	// maxResponseBytes caps what a compromised or malfunctioning control plane
	// can make a worker allocate. This response is a single boolean — about
	// twenty bytes — so 4 KiB is two orders of magnitude of headroom.
	maxResponseBytes = 4 << 10
)

// Client is the EXECUTION plane's coreapi transport: it asks the control plane
// rather than querying Postgres. A process wired with this for a given method
// needs no pool, no credentials and no schema knowledge to answer it — only the
// fleet URL and the fleet token.
//
// What it does NOT do yet, stated plainly because the opposite is easy to
// assume from the package existing: it does not remove the worker's database
// access. cmd/worker still opens a pgxpool for every method this transport has
// not yet taken over, which today is all but one. The containment claim becomes
// true when the pool is gone, not when this type is constructed.
type Client struct {
	baseURL string
	token   string
	hc      *http.Client
}

// NewClient builds the remote coreapi client. baseURL is the control plane's
// fleet listener (scheme + host, no path) and token is the shared bearer
// credential — the SAME two values the credential broker uses, because they are
// the same listener and the same token by decision. Validation is
// credbroker.ParseEndpoint's, so a plaintext URL or a weak token is refused
// identically on both transports.
func NewClient(baseURL, token string, allowPlaintext bool) (*Client, error) {
	base, err := credbroker.ParseEndpoint(baseURL, token, allowPlaintext)
	if err != nil {
		return nil, err
	}
	return &Client{
		baseURL: base,
		token:   token,
		hc: &http.Client{
			Timeout: requestTimeout,
			// The control plane does not redirect. Following one would replay
			// the fleet bearer token at whatever host the redirect named, and
			// accept that host's answer as the workspace's suppression state.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
			Transport: &http.Transport{
				// Kept from http.DefaultTransport: an operator whose egress goes
				// through an outbound proxy configures it with HTTPS_PROXY, and
				// dropping this would route around their only path out.
				Proxy: http.ProxyFromEnvironment,
				DialContext: (&net.Dialer{
					Timeout:   dialTimeout,
					KeepAlive: keepAlive,
				}).DialContext,
				// Setting DialContext disables net/http's automatic HTTP/2
				// upgrade unless this is set, which would quietly halve the
				// benefit of the connection reuse configured just below.
				ForceAttemptHTTP2:     true,
				MaxIdleConns:          maxIdleConns,
				MaxIdleConnsPerHost:   maxIdleConnsPerHost,
				IdleConnTimeout:       idleConnTimeout,
				TLSHandshakeTimeout:   tlsHandshakeTimeout,
				ResponseHeaderTimeout: responseHeaderTimeout,
				ExpectContinueTimeout: 1 * time.Second,
			},
		},
	}, nil
}

// IsSuppressed reports whether `email` is on the workspace's suppression list.
//
// The signature is the one the execution plane already consumes (worker/testsend
// .Core, worker/inbox's three Core interfaces) so this is substitutable for the
// in-process implementation without a worker package changing.
//
// FAIL CLOSED. Every failure — unreachable control plane, a 500, a rejected
// token, a malformed id — returns (false, err), and every call site treats a
// non-nil error as "do not send" rather than reading the bool. A false NEGATIVE
// here is mail delivered to someone who opted out, so the answer is never
// guessed, never cached and never read from a local table as a fallback.
func (c *Client) IsSuppressed(ctx context.Context, workspaceID, email string) (bool, error) {
	// Parsed here so a malformed id costs no round trip and never reaches the
	// control plane — the in-process implementation parses it before touching
	// the query for the same reason, and behaviour parity is the whole point of
	// this transport. The handler parses it again; a server may not trust its
	// client.
	if _, err := uuid.Parse(workspaceID); err != nil {
		return false, fmt.Errorf("coreapi remote: workspace id: %w", err)
	}
	// email is passed through UNVALIDATED, deliberately. Whatever string the
	// worker would have handed the local query, the control plane hands to the
	// same query; an extra check on this side would be a behaviour difference
	// between the two transports, which is the one thing a transport must not
	// introduce. The request body cap bounds its size.
	var out suppressionResponse
	if err := c.post(ctx, PathSuppressionCheck, suppressionRequest{WorkspaceID: workspaceID, Email: email}, &out); err != nil {
		return false, err
	}
	return out.Suppressed, nil
}

// post performs one coreapi call. It never logs the request or the response:
// the request names a tenant's contact address.
func (c *Client) post(ctx context.Context, path string, in, out any) error {
	body, err := json.Marshal(in)
	if err != nil {
		return fmt.Errorf("coreapi remote: encode request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("coreapi remote: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.token)

	resp, err := c.hc.Do(req)
	if err != nil {
		return fmt.Errorf("coreapi remote: %s: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized, http.StatusForbidden:
		return ErrUnauthorized
	default:
		// The status only. The control plane's error text is not ours to relay
		// into a worker's logs, and relaying it is how an upstream string
		// carrying request detail ends up in a log aggregator.
		return fmt.Errorf("coreapi remote: %s: control plane returned %d", path, resp.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponseBytes)).Decode(out); err != nil {
		return fmt.Errorf("coreapi remote: decode response: %w", err)
	}
	return nil
}
