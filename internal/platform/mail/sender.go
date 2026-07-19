package mail

import (
	"fmt"
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
func (s *NetSender) Send(cfg SMTPConfig, msg Message) (string, error) {
	if _, err := vetAddr(cfg.Host, cfg.Port, allowedSMTPPorts, s.AllowPrivate); err != nil {
		return "", err
	}

	m := gomail.NewMsg()
	if err := m.FromFormat(msg.FromName, msg.FromEmail); err != nil {
		return "", fmt.Errorf("from: %w", err)
	}
	if err := m.To(msg.To); err != nil {
		return "", fmt.Errorf("to: %w", err)
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
	m.SetMessageID()

	opts := []gomail.Option{
		gomail.WithPort(cfg.Port),
		gomail.WithUsername(cfg.Username),
		gomail.WithPassword(cfg.Password),
		gomail.WithSMTPAuth(gomail.SMTPAuthPlain),
		gomail.WithTimeout(s.Timeout),
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
