package captcha

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// siteverifyURL is the Cloudflare Turnstile server-side validation endpoint. It
// is a fixed provider host and never has user input interpolated into it.
const siteverifyURL = "https://challenges.cloudflare.com/turnstile/v0/siteverify"

// siteverifyTimeout bounds a single validation round-trip so a stuck provider
// can't hang a login request; on timeout Verify fails closed (rejects).
const siteverifyTimeout = 5 * time.Second

// turnstile validates client tokens against Cloudflare Turnstile's siteverify
// endpoint using the configured secret.
type turnstile struct {
	secret   string
	client   *http.Client
	endpoint string
}

// NewTurnstile builds a configured Verifier posting to Turnstile's siteverify
// endpoint with secret. client is INJECTED (never http.DefaultClient) so a test
// can stub the transport; a nil client falls back to a private client with a
// bounded timeout. A blank secret is a programming error — the caller wires the
// no-op verifier when unconfigured, not this one.
func NewTurnstile(secret string, client *http.Client) Verifier {
	if client == nil {
		client = &http.Client{Timeout: siteverifyTimeout}
	}
	return &turnstile{secret: secret, client: client, endpoint: siteverifyURL}
}

// siteverifyResponse is the subset of Turnstile's JSON we consume.
type siteverifyResponse struct {
	Success    bool     `json:"success"`
	ErrorCodes []string `json:"error-codes"`
}

// Verify posts token (and the optional client ip) to siteverify. A blank token is
// rejected without a round-trip. Any transport error, a non-2xx status, or an
// unparseable body fails CLOSED (returns a non-nil error → reject); a well-formed
// response with success=false returns ErrRejected. The secret is never logged and
// never placed in the error.
func (t *turnstile) Verify(ctx context.Context, token, ip string) error {
	if token == "" {
		return ErrRejected // no challenge solved: reject without calling the provider
	}
	ctx, cancel := context.WithTimeout(ctx, siteverifyTimeout)
	defer cancel()

	form := url.Values{"secret": {t.secret}, "response": {token}}
	if ip != "" {
		form.Set("remoteip", ip)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("captcha: build siteverify request: %w", err) // fail closed
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := t.client.Do(req)
	if err != nil {
		return fmt.Errorf("captcha: siteverify request: %w", err) // network error → fail closed
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("captcha: siteverify status %d", resp.StatusCode) // 5xx/4xx → fail closed
	}

	var out siteverifyResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return fmt.Errorf("captcha: decode siteverify response: %w", err) // fail closed
	}
	if !out.Success {
		return ErrRejected
	}
	return nil
}
