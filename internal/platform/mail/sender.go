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
}

// NetSender sends mail over SMTP, applying the same SSRF host vetting as the
// connection tester (see vetAddr in guard.go).
type NetSender struct {
	Timeout      time.Duration
	AllowPrivate bool
}

// NewNetSender returns a NetSender with a sane default send timeout.
// allowPrivate permits RFC1918/ULA hosts (default for self-hosted Core; Cloud
// deployments pass false).
func NewNetSender(allowPrivate bool) *NetSender {
	return &NetSender{Timeout: 30 * time.Second, AllowPrivate: allowPrivate}
}

// Send delivers msg over SMTP using cfg. It applies the same SSRF vetting
// used for connection testing before ever dialing out. Port 465 uses
// implicit TLS; other ports use STARTTLS when UseTLS is set, or no
// encryption otherwise. On success it returns the generated Message-ID.
//
// The vetted ip:port is dialed directly via WithDialContextFunc; cfg.Host is
// preserved only as the TLS SNI / AUTH server name. This closes the
// DNS-rebinding window between validation and connection: the underlying
// gomail client never re-resolves the hostname.
func (s *NetSender) Send(cfg SMTPConfig, msg Message) (string, error) {
	addr, err := vetAddr(cfg.Host, cfg.Port, allowedSMTPPorts, s.AllowPrivate)
	if err != nil {
		return "", err
	}

	m, err := buildMessage(msg)
	if err != nil {
		return "", err
	}

	dialer := &net.Dialer{Timeout: s.Timeout}
	dialFn := func(ctx context.Context, _, _ string) (net.Conn, error) {
		// Ignore gomail's address argument (built from cfg.Host); always dial the
		// pre-vetted ip:port instead so hostname re-resolution can't slip in.
		return dialer.DialContext(ctx, "tcp", addr)
	}

	opts := []gomail.Option{
		gomail.WithPort(cfg.Port),
		gomail.WithUsername(cfg.Username),
		gomail.WithPassword(cfg.Password),
		gomail.WithSMTPAuth(gomail.SMTPAuthPlain),
		gomail.WithTimeout(s.Timeout),
		gomail.WithDialContextFunc(dialFn),
	}
	switch {
	case cfg.Port == 465:
		opts = append(opts, gomail.WithSSLPort(false))
	case cfg.UseTLS:
		opts = append(opts, gomail.WithTLSPolicy(gomail.TLSMandatory))
	default:
		opts = append(opts, gomail.WithTLSPolicy(gomail.NoTLS))
	}

	client, err := gomail.NewClient(cfg.Host, opts...)
	if err != nil {
		return "", fmt.Errorf("smtp client: %w", err)
	}
	if err := client.DialAndSend(m); err != nil {
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
