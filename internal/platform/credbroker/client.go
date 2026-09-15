package credbroker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
)

// MinTokenLen is the floor for the shared broker token. 32 bytes of the
// operator's choosing, matching the INROAD_JWT_SECRET discipline of refusing an
// explicitly-set weak secret rather than accepting it: this token is the only
// thing between a network peer and every credential in the installation, so a
// guessable one is worse than no broker at all.
const MinTokenLen = 32

var (
	// ErrUnauthorized is a 401/403 from the broker — a wrong or missing token.
	// Distinguished from a transport failure so an operator reading a worker's
	// logs can tell "my token is wrong" from "the control plane is down".
	ErrUnauthorized = errors.New("credbroker: rejected by the control plane")
	// ErrNotFound is a 404: the named mailbox or endpoint does not exist in that
	// workspace. Distinguished so a caller can treat a vanished subject as
	// terminal rather than retrying it forever.
	ErrNotFound = errors.New("credbroker: subject not found")
	// ErrInsecureURL refuses a plaintext broker URL. See NewHTTPOpener.
	ErrInsecureURL = errors.New("credbroker: broker url must be https")
	// ErrWeakToken refuses a token below MinTokenLen.
	ErrWeakToken = fmt.Errorf("credbroker: token must be at least %d bytes", MinTokenLen)
)

// Timeouts. Every one is chosen here rather than inherited: a credential open
// sits directly in front of an SMTP dial, so an unbounded wait is a stalled
// send slot, not merely a slow request.
const (
	// requestTimeout bounds the whole call. The control plane's work is one
	// indexed SELECT plus an AES-GCM open — or, for an expired OAuth token, one
	// round trip to Google/Microsoft, which is the slow case this budget is
	// actually sized for.
	requestTimeout = 20 * time.Second
	// handshakeTimeout and headerTimeout bound the two phases Client.Timeout
	// alone does not usefully separate. A body that never arrives is not covered
	// by a connect timeout.
	handshakeTimeout = 10 * time.Second
	headerTimeout    = 15 * time.Second
	// maxResponseBytes caps what a compromised or malfunctioning control plane
	// can make a worker allocate. A credential response is a few hundred bytes;
	// 64 KiB is four orders of magnitude of headroom.
	maxResponseBytes = 64 << 10
)

// HTTPOpener is the EXECUTION plane's credbroker.Opener: it asks the control
// plane to open a credential instead of opening one itself. A process wired
// with this holds no master key, no keyring and no DEK — only a bearer token
// that names the fleet, which an operator can revoke by rotating one value
// instead of re-encrypting every workspace DEK in the installation.
//
// What it does NOT do, stated plainly because the opposite is easy to assume:
// it does not reduce what a LIVE compromised worker can reach. Today every
// send-role worker consumes the shared send queue and may legitimately be
// handed any mailbox's job, so the broker must answer for any mailbox the
// token names. Scoping a worker to the mailboxes actually routed to it needs
// per-worker identity AND per-mailbox routing; neither exists yet. What this
// removes is the OFFLINE capability: a stolen disk, image or environment file
// no longer decrypts anything, ever.
type HTTPOpener struct {
	baseURL string
	token   string
	hc      *http.Client
}

var _ Opener = (*HTTPOpener)(nil)

// NewHTTPOpener builds the remote opener. baseURL is the control plane's
// credential-broker listener (scheme + host, no path); token is the shared
// bearer credential both sides hold.
//
// https is REQUIRED unless allowPlaintext is explicitly set. This channel
// carries the bearer token AND decrypted SMTP passwords and OAuth access
// tokens, so a plaintext hop puts in the clear exactly the material the rest of
// this change exists to protect. The reasoning and the shape are the same as
// storage.FromEnv's INROAD_S3_ENDPOINT rule (docs/security.md invariant 6): a
// missing scheme is refused rather than assumed, because assuming is how a typo
// becomes a silent downgrade, and the opt-out has to be CHOSEN — it exists for
// a control plane reachable only over a trusted private network.
//
// The broker URL is OPERATOR-supplied, never user-supplied, so it does not go
// through (and does not need) the mail.vetAddr SSRF guard — the same reasoning
// invariant 6 applies to the S3 endpoint.
func NewHTTPOpener(baseURL, token string, allowPlaintext bool) (*HTTPOpener, error) {
	if len(token) < MinTokenLen {
		return nil, ErrWeakToken
	}
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil {
		return nil, fmt.Errorf("credbroker: broker url: %w", err)
	}
	// Scheme first: "control.example:8090" parses with Scheme="control.example"
	// and no host, and the useful thing to tell an operator who wrote that is
	// that the scheme is missing, not that the host is.
	switch u.Scheme {
	case "https":
	case "http":
		if !allowPlaintext {
			return nil, ErrInsecureURL
		}
	default:
		return nil, ErrInsecureURL
	}
	if u.Host == "" {
		return nil, fmt.Errorf("credbroker: broker url %q has no host", baseURL)
	}
	return &HTTPOpener{
		baseURL: strings.TrimSuffix(u.String(), "/"),
		token:   token,
		hc: &http.Client{
			Timeout: requestTimeout,
			// A broker does not redirect. Following one would replay the bearer
			// token — and accept a credential — from wherever it pointed.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
			Transport: &http.Transport{
				TLSHandshakeTimeout:   handshakeTimeout,
				ResponseHeaderTimeout: headerTimeout,
				MaxIdleConns:          10,
			},
		},
	}, nil
}

// OpenMailbox asks the control plane for the mailbox's decrypted transport
// credential. ref.Provider and ref.Sealed are DELIBERATELY not sent: see
// mailboxRequest.
func (h *HTTPOpener) OpenMailbox(ctx context.Context, ref MailboxRef) (MailboxSecret, error) {
	var out mailboxResponse
	err := h.post(ctx, PathMailbox, mailboxRequest{
		WorkspaceID: ref.WorkspaceID.String(),
		MailboxID:   ref.MailboxID.String(),
	}, &out)
	if err != nil {
		return MailboxSecret{}, err
	}
	// A conversion, not a field-by-field copy: mailboxResponse is deliberately a
	// 1:1 mirror of MailboxSecret, so if either side gains a field the other
	// does not, this stops compiling instead of silently dropping it.
	return MailboxSecret(out), nil
}

// OpenWebhookEndpointSecret asks the control plane for the endpoint's HMAC
// signing secret. sealed is ignored for the same reason ref.Sealed is.
func (h *HTTPOpener) OpenWebhookEndpointSecret(ctx context.Context, workspaceID, endpointID uuid.UUID, _ []byte) ([]byte, error) {
	var out webhookEndpointResponse
	err := h.post(ctx, PathWebhookEndpoint, webhookEndpointRequest{
		WorkspaceID: workspaceID.String(),
		EndpointID:  endpointID.String(),
	}, &out)
	if err != nil {
		return nil, err
	}
	return out.Secret, nil
}

// post performs one broker call. It never logs the request or the response:
// the response body IS a credential, and the request names a tenant's mailbox.
func (h *HTTPOpener) post(ctx context.Context, path string, in, out any) error {
	body, err := json.Marshal(in)
	if err != nil {
		return fmt.Errorf("credbroker: encode request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("credbroker: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+h.token)

	resp, err := h.hc.Do(req)
	if err != nil {
		return fmt.Errorf("credbroker: %s: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized, http.StatusForbidden:
		return ErrUnauthorized
	case http.StatusNotFound:
		return ErrNotFound
	default:
		// The status only. A broker's error text is not ours to relay into a
		// worker's logs, and relaying it is how an upstream string carrying
		// request detail ends up in a log aggregator.
		return fmt.Errorf("credbroker: %s: control plane returned %d", path, resp.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponseBytes)).Decode(out); err != nil {
		return fmt.Errorf("credbroker: decode response: %w", err)
	}
	return nil
}
