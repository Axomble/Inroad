package mail

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"

	gmail "google.golang.org/api/gmail/v1"
	"google.golang.org/api/option"
)

// GmailSender sends mail through the Gmail API using a per-call access token.
// No SSRF vetting: the host is Google's fixed API endpoint, not user input.
type GmailSender struct {
	// httpClient is the egress-bound, timeout-bounded client every API call
	// dials through (newAPIHTTPClient). Built once by NewGmailSender and reused,
	// so its connection pool survives across sends.
	httpClient *http.Client
	// transmitFn transmits the assembled RFC822 message over the wire. nil
	// selects the real Gmail API call (transmitGmail); tests stub it to assert
	// message assembly + Message-ID without a network round trip. Mirrors the
	// dial seam NetSender uses to stay unit-testable.
	transmitFn func(ctx context.Context, accessToken string, raw []byte) error
}

// NewGmailSender returns a GmailSender that talks to the real Gmail API,
// egressing from localAddr (mail.ParseEgressIP; nil = OS default route).
//
// The egress address is a CONSTRUCTOR argument rather than a settable field
// like NetSender.LocalAddr, because the HTTP transport — and with it the
// connection pool — is built once here. A field assignable after construction
// would either be read too late to matter or force a fresh transport per call,
// and the first of those is precisely the silent no-op this exists to fix.
func NewGmailSender(localAddr *net.TCPAddr) *GmailSender {
	return &GmailSender{httpClient: newAPIHTTPClient(localAddr)}
}

// Send builds the RFC822 message (reusing buildMessage — same headers,
// threading, Message-ID as the SMTP path), then hands the serialized bytes to
// the transport. It returns our own Message-ID header (Gmail preserves supplied
// headers), NOT Gmail's resource id, so reply matching (FindSendByMessageID)
// keys on the same value across transports.
func (g *GmailSender) Send(ctx context.Context, accessToken string, msg Message) (string, error) {
	m, err := buildMessage(msg)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if _, err := m.WriteTo(&buf); err != nil {
		return "", fmt.Errorf("gmail: serialize: %w", err)
	}
	if err := g.transmit(ctx, accessToken, buf.Bytes()); err != nil {
		return "", err
	}
	return m.GetMessageID(), nil
}

// transmit dispatches to the stub when a test set one, otherwise to the real
// wire call with this sender's egress-bound client. Mirrors the accessor shape
// GmailReader/GmailEngager already use, which is what keeps the client threaded
// through the seam instead of being rebuilt inside transmitGmail.
func (g *GmailSender) transmit(ctx context.Context, accessToken string, raw []byte) error {
	if g.transmitFn != nil {
		return g.transmitFn(ctx, accessToken, raw)
	}
	return transmitGmail(ctx, g.httpClient, accessToken, raw)
}

// transmitGmail is the real wire call: a static-token HTTP client (no refresh —
// the fresh token is minted upstream in coreapi) over the caller's egress-bound
// base client, driving users.messages.send with the base64url-encoded RAW message.
func transmitGmail(ctx context.Context, hc *http.Client, accessToken string, raw []byte) error {
	srv, err := gmail.NewService(ctx, option.WithHTTPClient(bearerClient(ctx, hc, accessToken)))
	if err != nil {
		return fmt.Errorf("gmail: service: %w", err)
	}
	enc := base64.URLEncoding.EncodeToString(raw)
	if _, err := srv.Users.Messages.Send("me", &gmail.Message{Raw: enc}).Context(ctx).Do(); err != nil {
		// gmailSendError keeps the HTTP status and Google's machine reason token as
		// data when this was a provider reply, so the fleet's per-worker signal
		// collector can tell a 429 rate limit from a 400 malformed request without
		// reading the message. A dial failure is not an API reply and passes
		// through unchanged.
		return fmt.Errorf("gmail: send: %w", gmailSendError(err))
	}
	return nil
}
