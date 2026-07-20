// Package coreapi is the control⇄execution boundary. Workers depend on this
// interface, never on platform/db directly. v1 satisfies it in-process; a
// future HTTP implementation swaps in without changing worker code.
package coreapi

import (
	"context"
	"errors"
)

// ErrCrossTenant is returned when a coreapi implementation detects a row
// whose workspace_id does not match the one the caller pinned. Normally
// impossible with the SQL WHERE clause pin, but the belt-and-braces check
// exists so future refactors that relax the pin still fail closed.
var ErrCrossTenant = errors.New("coreapi: cross-tenant access rejected")

type Client interface {
	// MailboxExists reports whether a mailbox is present and active.
	MailboxExists(ctx context.Context, id string) (bool, error)
	// GetSendJob loads everything the worker needs to send one email.
	// WorkspaceID is pinned in the SQL WHERE clause (defense in depth on top
	// of the unguessable send UUID); mismatch yields a not-found error.
	GetSendJob(ctx context.Context, sendID, workspaceID string) (SendJob, error)
	// MarkSend records the outcome of a send attempt. Same workspace
	// pinning as GetSendJob.
	MarkSend(ctx context.Context, sendID, workspaceID string, res SendResult) error
	// ListStuckQueuedSends returns send ids (with their workspace) that are
	// still 'queued' more than the reconcile window (currently 2 minutes)
	// after creation. Consumed by the periodic sweeper to re-enqueue
	// anything the launcher missed.
	ListStuckQueuedSends(ctx context.Context) ([]StuckSend, error)
}

// StuckSend is a (send id, workspace id) pair from the reconciler query.
type StuckSend struct {
	SendID      string
	WorkspaceID string
}

// SendJob is everything the worker needs to send one email — including the
// decrypted SMTP password (in-memory only, never logged).
type SendJob struct {
	SendID            string
	WorkspaceID       string
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
