// Package notify delivers transactional (system-originated) email — distinct
// from per-user campaign mailboxes. Pluggable via Config.Driver.
package notify

import (
	"context"
	"fmt"
	"log/slog"
)

// Message is a single transactional email, rendered with both a plain-text
// and an HTML body.
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
}

// New builds the configured Sender. console (default) logs; smtp dials the
// operator system mailbox.
func New(cfg Config) (Sender, error) {
	switch cfg.Driver {
	case "", "console":
		lg := cfg.Logger
		if lg == nil {
			lg = slog.Default()
		}
		return &consoleSender{sink: func(m Message) {
			lg.Info("transactional email (console)", "to", m.To, "subject", m.Subject)
		}}, nil
	case "smtp":
		if cfg.SMTPHost == "" || cfg.From == "" {
			return nil, fmt.Errorf("smtp driver requires SMTP host and From")
		}
		return &smtpSender{cfg: cfg}, nil
	default:
		return nil, fmt.Errorf("unknown transactional driver %q", cfg.Driver)
	}
}
