// Package notify delivers transactional (system-originated) email — distinct
// from per-user campaign mailboxes. Pluggable via Config.Driver.
package notify

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
)

// ErrNoRecipient is returned by Send for a Message with an empty To. An
// unaddressed message is undeliverable, so it is rejected loudly rather than
// handed to a driver that would drop it (console) or bounce it (smtp).
var ErrNoRecipient = errors.New("notify: message has no recipient")

// Message is a single transactional email, rendered with both a plain-text
// and an HTML body. Build one with a template constructor (VerifyEmail,
// ResetEmail, LoginCodeEmail, InviteEmail) so To is always populated.
type Message struct{ To, Subject, TextBody, HTMLBody string }

// Sender delivers one transactional email. Consumers depend on this
// interface, not a concrete driver.
type Sender interface {
	Send(ctx context.Context, m Message) error
}

// Config configures the transactional sender.
type Config struct {
	Driver       string // "console" | "smtp"
	SMTPHost     string
	SMTPPort     int
	SMTPUsername string
	SMTPPassword string
	From         string
	Logger       *slog.Logger

	// AllowPlaintext is the explicit cleartext opt-out for the smtp driver,
	// mirroring the per-mailbox opt-out on the campaign sender (security
	// Invariant 6). Its zero value REQUIRES TLS, so an absent or false setting
	// can never silently downgrade the connection; only an operator explicitly
	// setting it true relaxes the policy. It exists so a local mail catcher
	// (Mailpit/MailHog, plaintext and no AUTH) can be used in development —
	// never for production delivery.
	AllowPlaintext bool
}

// requireRecipient rejects an unaddressed Message before it reaches the
// driver. The template constructors take the recipient as a parameter, so this
// only fires for a Message literal assembled by hand — it is the runtime half
// of a compile-time guarantee, not the primary defence.
type requireRecipient struct{ next Sender }

func (r requireRecipient) Send(ctx context.Context, m Message) error {
	if m.To == "" {
		return fmt.Errorf("%w (subject %q)", ErrNoRecipient, m.Subject)
	}
	return r.next.Send(ctx, m)
}

// New builds the configured Sender. console (default) logs; smtp dials the
// operator system mailbox. Every driver is wrapped in the recipient guard.
func New(cfg Config) (Sender, error) {
	switch cfg.Driver {
	case "", "console":
		lg := cfg.Logger
		if lg == nil {
			lg = slog.Default()
		}
		// Deliberately logs only the recipient and subject: the bodies carry
		// verify/reset links and login codes, which are bearer credentials and
		// must never reach the logs. To read a message body in development, use
		// the mail catcher wired into the dev compose stack, not the log.
		return requireRecipient{next: &consoleSender{sink: func(m Message) {
			lg.Info("transactional email (console)", "to", m.To, "subject", m.Subject)
		}}}, nil
	case "smtp":
		if cfg.SMTPHost == "" || cfg.From == "" {
			return nil, fmt.Errorf("smtp driver requires SMTP host and From")
		}
		return requireRecipient{next: &smtpSender{cfg: cfg}}, nil
	default:
		return nil, fmt.Errorf("unknown transactional driver %q", cfg.Driver)
	}
}
