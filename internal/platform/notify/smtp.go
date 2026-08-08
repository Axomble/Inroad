package notify

import (
	"context"
	"fmt"
	"time"

	gomail "github.com/wneessen/go-mail"
)

// smtpSender delivers transactional email through the operator's configured
// system mailbox. Unlike per-user campaign mailboxes (see platform/mail),
// this host is operator/env-configured, not caller-supplied, so it is not
// routed through the SSRF guard.
type smtpSender struct{ cfg Config }

// smtpPolicy is the auth mechanism and TLS policy cfg implies. Pure and
// separate from option building so the security-relevant decision can be
// asserted directly, without dialing anything.
//
// Auth is offered only when a username is configured: PLAIN with empty
// credentials fails against a server advertising no AUTH mechanism (every
// local mail catcher), and "no username configured" already meant "send no
// credentials".
//
// TLS is mandatory unless the operator explicitly opted out (security
// Invariant 6). The opt-out is its own explicit setting rather than something
// inferred from the port or from absent credentials, so no misconfiguration
// can silently downgrade a production send to cleartext.
func smtpPolicy(cfg Config) (gomail.SMTPAuthType, gomail.TLSPolicy) {
	auth := gomail.SMTPAuthNoAuth
	if cfg.SMTPUsername != "" {
		auth = gomail.SMTPAuthPlain
	}
	tlsPolicy := gomail.TLSMandatory
	if cfg.AllowPlaintext {
		tlsPolicy = gomail.NoTLS
	}
	return auth, tlsPolicy
}

// clientOptions builds the gomail options for one send.
func (s *smtpSender) clientOptions() []gomail.Option {
	auth, tlsPolicy := smtpPolicy(s.cfg)
	opts := []gomail.Option{
		gomail.WithPort(s.cfg.SMTPPort),
		gomail.WithTimeout(30 * time.Second),
		gomail.WithSMTPAuth(auth),
		gomail.WithTLSPolicy(tlsPolicy),
	}
	if auth != gomail.SMTPAuthNoAuth {
		opts = append(opts,
			gomail.WithUsername(s.cfg.SMTPUsername),
			gomail.WithPassword(s.cfg.SMTPPassword),
		)
	}
	return opts
}

func (s *smtpSender) Send(ctx context.Context, m Message) error {
	msg := gomail.NewMsg()
	if err := msg.From(s.cfg.From); err != nil {
		return fmt.Errorf("from: %w", err)
	}
	if err := msg.To(m.To); err != nil {
		return fmt.Errorf("to: %w", err)
	}
	msg.Subject(m.Subject)
	msg.SetBodyString(gomail.TypeTextPlain, m.TextBody)
	if m.HTMLBody != "" {
		msg.AddAlternativeString(gomail.TypeTextHTML, m.HTMLBody)
	}
	msg.SetMessageID()

	client, err := gomail.NewClient(s.cfg.SMTPHost, s.clientOptions()...)
	if err != nil {
		return fmt.Errorf("smtp client: %w", err)
	}
	return client.DialAndSendWithContext(ctx, msg)
}
