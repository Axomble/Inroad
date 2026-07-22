package mail

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"

	"golang.org/x/oauth2"
	gmail "google.golang.org/api/gmail/v1"
	"google.golang.org/api/option"
)

// GmailSender sends mail through the Gmail API using a per-call access token.
// No SSRF vetting: the host is Google's fixed API endpoint, not user input.
type GmailSender struct {
	// transmitFn transmits the assembled RFC822 message over the wire. nil
	// selects the real Gmail API call (transmitGmail); tests stub it to assert
	// message assembly + Message-ID without a network round trip. Mirrors the
	// dial seam NetSender uses to stay unit-testable.
	transmitFn func(ctx context.Context, accessToken string, raw []byte) error
}

// NewGmailSender returns a GmailSender that talks to the real Gmail API.
func NewGmailSender() *GmailSender { return &GmailSender{} }

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
	transmit := g.transmitFn
	if transmit == nil {
		transmit = transmitGmail
	}
	if err := transmit(ctx, accessToken, buf.Bytes()); err != nil {
		return "", err
	}
	return m.GetMessageID(), nil
}

// transmitGmail is the real wire call: a static-token HTTP client (no refresh —
// the fresh token is minted upstream in coreapi) drives users.messages.send with
// the base64url-encoded RAW message.
func transmitGmail(ctx context.Context, accessToken string, raw []byte) error {
	client := oauth2.NewClient(ctx, oauth2.StaticTokenSource(&oauth2.Token{AccessToken: accessToken}))
	srv, err := gmail.NewService(ctx, option.WithHTTPClient(client))
	if err != nil {
		return fmt.Errorf("gmail: service: %w", err)
	}
	enc := base64.URLEncoding.EncodeToString(raw)
	if _, err := srv.Users.Messages.Send("me", &gmail.Message{Raw: enc}).Context(ctx).Do(); err != nil {
		return fmt.Errorf("gmail: send: %w", err)
	}
	return nil
}
