// Package coreapi is the control⇄execution boundary. Workers depend on this
// interface, never on platform/db directly. v1 satisfies it in-process; a
// future HTTP implementation swaps in without changing worker code.
package coreapi

import "context"

type Client interface {
	// MailboxExists reports whether a mailbox is present and active.
	MailboxExists(ctx context.Context, id string) (bool, error)
	// GetSendJob loads everything the worker needs to send one email.
	GetSendJob(ctx context.Context, sendID string) (SendJob, error)
	// MarkSend records the outcome of a send attempt.
	MarkSend(ctx context.Context, sendID string, res SendResult) error
}

// SendJob is everything the worker needs to send one email — including the
// decrypted SMTP password (in-memory only, never logged).
type SendJob struct {
	SendID            string
	Suppressed        bool
	EffectiveDailyCap int
	SentToday         int
	ToEmail           string
	FirstName         string
	Subject           string
	BodyText          string
	BodyHTML          string
	UnsubURL          string
	FromEmail         string
	FromName          string
	SMTPHost          string
	SMTPPort          int
	SMTPUsername      string
	SMTPPassword      string
	UseTLS            bool
}

// SendResult is the outcome of a single send attempt.
type SendResult struct {
	Status    string // "sent" | "failed"
	MessageID string
	Err       string
}
