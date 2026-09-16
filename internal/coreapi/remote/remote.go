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
// Scope, deliberately, slice by slice.
//
// Slice 1 carried exactly ONE method, IsSuppressed: read-only, side-effect
// free, a single boolean, directly in front of every send — so it exercised the
// latency and the correctness of a network hop on the hot path without the
// claim-protocol risk that moving the send claim would carry.
//
// Slice 2 adds the eight per-message job READS (jobs.go): the whole question
// "what is the work, and what do I need to do it". Still nothing that CLAIMS,
// marks, finalizes, advances or fails — those carry the idempotency risk, and
// keeping them separate is what stops the dangerous work sitting behind the
// boring work's review. The pool is still opened for them and for every
// periodic sweep; it goes when nothing needs it.
//
// No credential crosses this wire. A job response carries a mailbox's host,
// port, username and TLS policy and no secret at all; the worker opens the
// secret through internal/platform/credbroker, on this same listener, by
// mailbox id. jobs.go has the full argument for why that beats inlining it.
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
	"github.com/jackc/pgx/v5"

	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/platform/credbroker"
)

// A note on the pgx import, because a package whose whole point is "needs no
// database" importing a database driver deserves one. Nothing here opens a
// connection: pgx.ErrNoRows is a package-level sentinel VALUE, and it is
// re-minted on this side because internal/worker/webhook branches on it
// (deliver.go: a delivery deleted after its task was queued must be dropped,
// not retried to exhaustion). The transport's job is to reproduce the
// in-process behaviour exactly, including which errors a consumer can tell
// apart — inventing a different sentinel would mean changing that consumer to
// check for something the in-process path never returns. Moving the concept
// onto a coreapi-owned error is the right cleanup and is deliberately not
// bundled into this slice.

// ErrUnauthorized is a 401/403 from the control plane — a wrong or missing
// token. Distinguished from a transport failure so an operator reading a
// worker's logs can tell "my token is wrong" from "the control plane is down".
var ErrUnauthorized = errors.New("coreapi remote: rejected by the control plane")

// ErrNoCredentialSource refuses a client built without a credential broker. The
// job routes answer with everything about a send except its credential, so a
// client that cannot open one is a client that will fetch work it cannot do —
// and the failure would surface at a mailbox dial rather than at startup.
var ErrNoCredentialSource = errors.New(
	"coreapi remote: the client needs a credential broker: job responses carry no credential, and a fleet worker obtains one through internal/platform/credbroker")

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

// Job-fetch budgets. A job build is not a boolean lookup and is not sized like
// one, so it gets its own http.Client rather than loosening slice 1's numbers
// for a call that did not need it. Two clients means two connection pools to
// the same host, which costs a handful of idle sockets and keeps each call
// class failing on its own schedule.
const (
	// jobRequestTimeout bounds a whole job fetch. The control plane's work is
	// several indexed SELECTs plus opening the mailbox credential, and THAT is
	// what this is sized for: an expired OAuth access token makes one round trip
	// to Google or Microsoft inside the handler. credbroker chose 20s for the
	// identical reason and this matches it deliberately — the same work, behind
	// the same listener, should not time out at two different moments.
	//
	// It is ~17% of the tightest asynq ceiling above it (queue.sendTimeout, 2m)
	// and 7% of queue.pollTimeout, so a wedged fetch fails the CALL — visible,
	// attributed, retried — rather than burning the task's whole budget and
	// leaving an operator with a killed handler and no cause.
	jobRequestTimeout = 20 * time.Second
	// jobResponseHeaderTimeout is the control plane's THINK time before the
	// first byte, and it has to clear the same OAuth refresh: a 5s bound here
	// would fail exactly the jobs whose token had just expired, which is a
	// periodic, mailbox-correlated failure that would read as a provider
	// problem. Matches credbroker's headerTimeout.
	jobResponseHeaderTimeout = 15 * time.Second
	// jobMaxResponseBytes caps a job body. Measured rather than guessed: a
	// realistic sequence step (2.3 KiB of subject + text + HTML) encodes to
	// ~4.8 KiB, of which ~1.9 KiB is ids, vars and the compiled schedule; the
	// ceiling is set by what the API will ACCEPT for a step, which is httpx's
	// 1 MiB JSON request cap, and 1 MiB of HTML encodes to ~2.8 MiB here
	// because encoding/json escapes every < > & to \uXXXX.
	//
	// 8 MiB is therefore ~2.8x the largest job the product can legitimately
	// produce. Sizing it tighter would mean an operator who pasted a very large
	// HTML email got a campaign that silently never sent on a fleet worker,
	// which is a worse failure than the allocation this bounds.
	jobMaxResponseBytes = 8 << 20
)

// callBudget pairs an http.Client with the response cap that matches the class
// of call it serves. It exists so post takes a budget instead of two more
// positional parameters, and so the two classes cannot borrow each other's.
type callBudget struct {
	hc       *http.Client
	maxBytes int64
}

// Client is the EXECUTION plane's coreapi transport: it asks the control plane
// rather than querying Postgres. A process wired with this for a given method
// needs no pool, no credentials and no schema knowledge to answer it — only the
// fleet URL and the fleet token.
//
// What it does NOT do yet, stated plainly because the opposite is easy to
// assume from the package existing: it does not remove the worker's database
// access. cmd/worker still opens a pgxpool for every method this transport has
// not yet taken over — every claim, mark, finalize and advance, and every
// periodic sweep. The containment claim becomes true when the pool is gone, not
// when this type is constructed.
type Client struct {
	baseURL string
	token   string
	// check is the budget for a single-fact lookup (IsSuppressed). jobs is the
	// budget for a job build. See callBudget.
	check callBudget
	jobs  callBudget
	// creds opens the decrypted credentials the job responses deliberately do
	// NOT carry. It is the credential broker — the same one this process
	// already had to be configured with, since a worker may only read coreapi
	// remotely in the one role where brokering is mandatory — so there is ONE
	// channel in the installation that hands out a plaintext secret, not two.
	//
	// Required, never optional: a client built without it could fetch a job and
	// then have nothing to send it with, which is a failure discovered at the
	// dial rather than at startup.
	creds credbroker.Opener
}

// NewClient builds the remote coreapi client. baseURL is the control plane's
// fleet listener (scheme + host, no path) and token is the shared bearer
// credential — the SAME two values the credential broker uses, because they are
// the same listener and the same token by decision. Validation is
// credbroker.ParseEndpoint's, so a plaintext URL or a weak token is refused
// identically on both transports.
//
// creds is the credential broker this worker already holds. It is a parameter
// rather than an option because it is not optional: the job routes answer with
// everything about a send EXCEPT its credential, so a client that could not
// open one would build jobs it cannot use.
func NewClient(baseURL, token string, allowPlaintext bool, creds credbroker.Opener) (*Client, error) {
	base, err := credbroker.ParseEndpoint(baseURL, token, allowPlaintext)
	if err != nil {
		return nil, err
	}
	if creds == nil {
		return nil, ErrNoCredentialSource
	}
	return &Client{
		baseURL: base,
		token:   token,
		check: callBudget{
			hc:       newHTTPClient(requestTimeout, responseHeaderTimeout),
			maxBytes: maxResponseBytes,
		},
		jobs: callBudget{
			hc:       newHTTPClient(jobRequestTimeout, jobResponseHeaderTimeout),
			maxBytes: jobMaxResponseBytes,
		},
		creds: creds,
	}, nil
}

// newHTTPClient builds one of the two transports. Everything except the two
// timeouts that differ by call class is identical by construction, so the
// budgets cannot drift apart on a setting neither of them meant to change.
func newHTTPClient(timeout, headerTimeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		// The control plane does not redirect. Following one would replay
		// the fleet bearer token at whatever host the redirect named, and
		// accept that host's answer as the workspace's own data.
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
			ResponseHeaderTimeout: headerTimeout,
			ExpectContinueTimeout: 1 * time.Second,
		},
	}
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
	if err := c.post(ctx, c.check, PathSuppressionCheck, suppressionRequest{WorkspaceID: workspaceID, Email: email}, &out); err != nil {
		return false, err
	}
	return out.Suppressed, nil
}

// post performs one coreapi call under the budget its call class was sized
// for. It never logs the request or the response: a request names a tenant's
// contact address or one of its rows, and a response is that tenant's data.
func (c *Client) post(ctx context.Context, b callBudget, path string, in, out any) error {
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

	resp, err := b.hc.Do(req)
	if err != nil {
		return fmt.Errorf("coreapi remote: %s: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized, http.StatusForbidden:
		return ErrUnauthorized
	case http.StatusNotFound:
		return notFound(path, io.LimitReader(resp.Body, maxResponseBytes))
	default:
		// The status only. The control plane's error text is not ours to relay
		// into a worker's logs, and relaying it is how an upstream string
		// carrying request detail ends up in a log aggregator.
		return fmt.Errorf("coreapi remote: %s: control plane returned %d", path, resp.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, b.maxBytes)).Decode(out); err != nil {
		return fmt.Errorf("coreapi remote: decode response: %w", err)
	}
	return nil
}

// notFound turns a 404 into the sentinel the in-process path would have
// returned, so a consumer's errors.Is keeps working across the wire.
//
// It reads the body ONLY to recover the handler's own fixed code. Nothing from
// it is relayed: the returned message is this package's, and an unrecognised
// body yields a plain error rather than a guess. That last clause is the
// load-bearing one — a 404 from an older control plane that does not serve the
// route at all is net/http's plain-text "404 page not found", and mapping THAT
// to pgx.ErrNoRows would make internal/worker/webhook drop every delivery as
// "already gone" instead of failing loudly.
func notFound(path string, body io.Reader) error {
	var e errorResponse
	// A decode failure is expected for a non-JSON 404 and is not itself the
	// error worth reporting; falling through to the generic message below is.
	_ = json.NewDecoder(body).Decode(&e)
	switch e.Code {
	case codeNotFound:
		return fmt.Errorf("coreapi remote: %s: %w", path, pgx.ErrNoRows)
	case codeNoMatch:
		return coreapi.ErrNoMatch
	default:
		return fmt.Errorf("coreapi remote: %s: control plane returned 404 with no known reason", path)
	}
}
