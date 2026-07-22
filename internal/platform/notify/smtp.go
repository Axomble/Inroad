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

	client, err := gomail.NewClient(s.cfg.SMTPHost,
		gomail.WithPort(s.cfg.SMTPPort),
		gomail.WithUsername(s.cfg.SMTPUsername),
		gomail.WithPassword(s.cfg.SMTPPassword),
		gomail.WithSMTPAuth(gomail.SMTPAuthPlain),
		gomail.WithTLSPolicy(gomail.TLSMandatory),
		gomail.WithTimeout(30*time.Second),
	)
	if err != nil {
		return fmt.Errorf("smtp client: %w", err)
	}
	return client.DialAndSendWithContext(ctx, msg)
}
