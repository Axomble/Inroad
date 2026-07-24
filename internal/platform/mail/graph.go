package mail

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"net/http"

	"golang.org/x/oauth2"
)

// graphSendMailURL is the fixed Microsoft Graph endpoint for sending a
// pre-built MIME message. The host is not user input, so no SSRF vetting is
// needed (mirrors GmailSender's use of Google's fixed API host).
const graphSendMailURL = "https://graph.microsoft.com/v1.0/me/sendMail"

// GraphSender sends mail through the Microsoft Graph API using a per-call access
// token. No SSRF vetting: the host is Graph's fixed API endpoint, not user input.
type GraphSender struct {
	// transmitFn transmits the base64-encoded RFC822 message over the wire. nil
	// selects the real Graph API call (transmitGraph); tests stub it to assert
	// message assembly + Message-ID without a network round trip. Mirrors the
	// dial seam NetSender/GmailSender use to stay unit-testable.
	transmitFn func(ctx context.Context, accessToken string, rawB64 []byte) error
}

// NewGraphSender returns a GraphSender that talks to the real Graph API.
func NewGraphSender() *GraphSender { return &GraphSender{} }

// Send builds the RFC822 message (reusing buildMessage — same headers,
// threading, Message-ID as the SMTP and Gmail paths), serializes it, and
// base64-encodes the MIME (STANDARD base64, per Graph's sendMail contract —
// NOT the URL encoding Gmail uses). It returns our own Message-ID header (Graph
// preserves supplied headers), NOT a Graph resource id, so reply matching
// (FindSendByMessageID) keys on the same value across transports.
func (g *GraphSender) Send(ctx context.Context, accessToken string, msg Message) (string, error) {
	m, err := buildMessage(msg)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if _, err := m.WriteTo(&buf); err != nil {
		return "", fmt.Errorf("graph: serialize: %w", err)
	}
	// Graph's POST /me/sendMail with Content-Type: text/plain takes the base64 of
	// the raw MIME as the request body. Standard base64, not URL-safe.
	enc := base64.StdEncoding.EncodeToString(buf.Bytes())
	transmit := g.transmitFn
	if transmit == nil {
		transmit = transmitGraph
	}
	if err := transmit(ctx, accessToken, []byte(enc)); err != nil {
		return "", err
	}
	return m.GetMessageID(), nil
}

// transmitGraph is the real wire call: a static-token HTTP client (no refresh —
// the fresh token is minted upstream in coreapi) POSTs the base64 MIME to
// Graph's sendMail. A 202 Accepted is success. On a non-2xx the status is
// reported without the response body, so a bearer token echoed back by Graph
// never lands in logs or errors.
func transmitGraph(ctx context.Context, accessToken string, rawB64 []byte) error {
	client := oauth2.NewClient(ctx, oauth2.StaticTokenSource(&oauth2.Token{AccessToken: accessToken}))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, graphSendMailURL, bytes.NewReader(rawB64))
	if err != nil {
		return fmt.Errorf("graph: request: %w", err)
	}
	req.Header.Set("Content-Type", "text/plain")
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("graph: send: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("graph: send: unexpected status %d", resp.StatusCode)
	}
	return nil
}
