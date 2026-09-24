package mail

import (
	"context"
	"fmt"
	"net"
	"time"

	gomail "github.com/wneessen/go-mail"
)

// Message is a single outbound email.
type Message struct {
	FromEmail, FromName string
	To                  string
	Subject             string
	BodyText, BodyHTML  string
	ListUnsubscribe     string // full URL for the List-Unsubscribe header + footer
	// Threading headers for multi-step sequences. InReplyTo is the parent
	// step's Message-ID; References is the accumulated chain. Both empty on a
	// thread's first message. Dormant until reply detection lands, but sent
	// now so replies thread correctly in the recipient's client.
	InReplyTo  string
	References string
	// ExtraHeaders are additional generic headers set verbatim on the outgoing
	// message. Every transport (SMTP/Gmail/Graph) assembles through buildMessage,
	// so a header set here reaches the wire regardless of provider — used for the
	// warmup receipt header (X-Inroad-Warmup) the inbox poller verifies. nil for
	// ordinary sends, which keeps their serialized output byte-for-byte unchanged.
	ExtraHeaders map[string]string
}

// NetSender sends mail over SMTP, applying the same SSRF host vetting as the
// connection tester (see vetAddr in guard.go).
type NetSender struct {
	Timeout      time.Duration
	AllowPrivate bool
	// LocalAddr optionally binds the SOURCE address of every SMTP dial to the
	// worker's egress IP (spec §15). nil = OS default route. Source-only: it is
	// applied to the net.Dialer AFTER vetAddr has vetted the destination, so it
	// never relaxes the SSRF destination check (spec §17.7).
	LocalAddr *net.TCPAddr
}

// NewNetSender returns a NetSender with a sane default send timeout.
// allowPrivate permits RFC1918/ULA hosts (default for self-hosted Core; Cloud
// deployments pass false).
func NewNetSender(allowPrivate bool) *NetSender {
	return &NetSender{Timeout: 30 * time.Second, AllowPrivate: allowPrivate}
}

// Send delivers msg over SMTP using cfg. It applies the same SSRF vetting
// used for connection testing before ever dialing out. TLS is enforced by
// default (security Invariant 6): port 465 uses implicit TLS, every other port
// requires STARTTLS (TLSMandatory) — cleartext is used ONLY when the caller
// explicitly opts out via cfg.AllowPlaintext. On success it returns the
// generated Message-ID.
//
// The vetted ip:port is dialed directly via WithDialContextFunc; cfg.Host is
// preserved only as the TLS SNI / AUTH server name. This closes the
// DNS-rebinding window between validation and connection: the underlying
// gomail client never re-resolves the hostname.
//
// ctx bounds the whole exchange, not just the SSRF-vetting DNS lookup: the dial,
// the greeting, AUTH and DATA all run under DialAndSendWithContext, with gomail's
// WithTimeout as the ceiling. The comment here used to say gomail had no
// context-aware entry point; DialAndSendWithContext has existed since well
// before the version in this module graph, and using DialAndSend meant an
// abandoned request or a shutting-down worker waited out the full timeout
// against a stalled server.
//
// The EHLO name is the envelope sender's DOMAIN (smtpHELO), not the machine's
// hostname. go-mail defaults to os.Hostname(), which in a container is a random
// hex id — and an unqualified, unrelated HELO is what Postfix's
// reject_non_fqdn_helo_hostname and most commercial filters act on, so the same
// message that delivers from a laptop is filtered from a deployment.
func (s *NetSender) Send(ctx context.Context, cfg SMTPConfig, msg Message) (string, error) {
	addr, err := vetAddr(ctx, cfg.Host, cfg.Port, allowedSMTPPorts, s.AllowPrivate)
	if err != nil {
		return "", err
	}

	m, err := buildMessage(msg)
	if err != nil {
		return "", err
	}

	dialer := &net.Dialer{Timeout: s.Timeout}
	if s.LocalAddr != nil {
		// Bind the source address only; addr is the already-vetted DESTINATION,
		// so this narrows egress without touching the SSRF vet.
		dialer.LocalAddr = s.LocalAddr
	}

	client, err := newSMTPClient(cfg, s.Timeout, smtpHELO(cfg, msg.FromEmail), smtpDialFunc(addr, dialer))
	if err != nil {
		return "", err
	}
	if err := client.DialAndSendWithContext(ctx, m); err != nil {
		return "", fmt.Errorf("send: %w", err)
	}
	return m.GetMessageID(), nil
}

// buildMessage assembles the gomail message (headers, bodies, threading)
// without dialing, so the composition — including In-Reply-To/References — is
// unit-testable independent of any SMTP server.
func buildMessage(msg Message) (*gomail.Msg, error) {
	m := gomail.NewMsg()
	if err := m.FromFormat(msg.FromName, msg.FromEmail); err != nil {
		return nil, fmt.Errorf("from: %w", err)
	}
	if err := m.To(msg.To); err != nil {
		return nil, fmt.Errorf("to: %w", err)
	}
	m.Subject(msg.Subject)
	if msg.BodyText != "" {
		m.SetBodyString(gomail.TypeTextPlain, msg.BodyText)
	}
	if msg.BodyHTML != "" {
		if msg.BodyText != "" {
			m.AddAlternativeString(gomail.TypeTextHTML, msg.BodyHTML)
		} else {
			m.SetBodyString(gomail.TypeTextHTML, msg.BodyHTML)
		}
	}
	// Custom generic headers (e.g. the warmup receipt token) set verbatim, and
	// set FIRST so the dedicated threading/unsub headers below win last-write on
	// any key collision — a caller-supplied ExtraHeader can never clobber them.
	// nil ExtraHeaders is a no-op, keeping ordinary sends byte-for-byte unchanged.
	for k, v := range msg.ExtraHeaders {
		m.SetGenHeader(gomail.Header(k), v)
	}
	if msg.ListUnsubscribe != "" {
		m.SetListUnsubscribe(msg.ListUnsubscribe)
		m.SetListUnsubscribePost()
	}
	// Threading: set only when present (a thread's first message has neither),
	// so root messages don't carry empty In-Reply-To/References headers.
	if msg.InReplyTo != "" {
		m.SetGenHeader(gomail.HeaderInReplyTo, msg.InReplyTo)
	}
	if msg.References != "" {
		m.SetGenHeader(gomail.HeaderReferences, msg.References)
	}
	m.SetMessageID()
	return m, nil
}
