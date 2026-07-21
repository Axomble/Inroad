// Package coreapi is the control⇄execution boundary. Workers depend on this
// interface, never on platform/db directly. v1 satisfies it in-process; a
// future HTTP implementation swaps in without changing worker code.
package coreapi

import (
	"context"
	"errors"
	"time"
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
	// IncrementSendAttempts bumps the send's attempts counter and returns
	// the new value. Used by the cap-exceeded re-enqueue path to break out
	// of the loop when a send keeps hitting a daily cap it will never
	// clear.
	IncrementSendAttempts(ctx context.Context, sendID, workspaceID string) (int, error)

	// --- Multi-step sequencing (sequence:advance path) ---

	// GetStepSendJob loads everything needed to send the enrollment's next due
	// step (current_step+1): resolved step content, personalization vars,
	// threading headers, cap gate, and decrypted transport. Read-only — it
	// creates no rows, so a suppressed/capped step leaves no orphan. workspaceID
	// is pinned in the SQL WHERE (defense in depth on the enrollment UUID).
	GetStepSendJob(ctx context.Context, enrollmentID, workspaceID string) (StepSendJob, error)
	// MarkStepSent records the step's send (one sends row, with result) and
	// advances the enrollment cursor via the enrollment state machine — the
	// single insertion point for the current_step transition and cadence.
	// Returns whether the enrollment completed and, if not, when the next step
	// is due.
	MarkStepSent(ctx context.Context, enrollmentID, workspaceID string, res StepResult) (Advance, error)
	// MarkStepStopped halts an enrollment (the single stop entry point). reason
	// is one of the enrollment stop reasons (e.g. "suppressed").
	MarkStepStopped(ctx context.Context, enrollmentID, workspaceID, reason string) error
	// ListDueEnrollments returns active enrollments whose next_due_at passed the
	// reconcile window. Consumed by the periodic enrollment sweeper.
	ListDueEnrollments(ctx context.Context) ([]DueEnrollment, error)
}

// ContactVars are the personalization values for a contact, applied worker-side
// to the raw step templates ({{first_name}}, {{custom.<key>}}, …).
type ContactVars struct {
	FirstName string
	LastName  string
	Email     string
	Company   string
	Custom    map[string]string
}

// StepSendJob is everything the sequence:advance worker needs to send one
// step-email. Skip is true when the enrollment is no longer active (stopped or
// completed) or has no next step — the worker no-ops. SMTPPassword is []byte so
// the worker can zeroize it after use.
type StepSendJob struct {
	Skip              bool
	EnrollmentID      string
	WorkspaceID       string
	StepOrder         int
	LastStep          bool
	Suppressed        bool
	EffectiveDailyCap int
	SentToday         int
	ToEmail           string
	Vars              ContactVars
	Subject           string
	BodyText          string
	BodyHTML          string
	UnsubURL          string
	InReplyTo         string
	References        string
	FromEmail         string
	FromName          string
	SMTPHost          string
	SMTPPort          int
	SMTPUsername      string
	SMTPPassword      []byte
	UseTLS            bool
}

// StepResult is the outcome of one step send.
type StepResult struct {
	Status    string // "sent" | "failed"
	MessageID string
	Err       string
}

// Advance tells the worker whether the enrollment finished and, if not, when
// the next step is due (so it can schedule the next sequence:advance).
type Advance struct {
	Completed bool
	NextDueAt time.Time
}

// DueEnrollment is an (enrollment id, workspace id) pair from the sweeper query.
type DueEnrollment struct {
	EnrollmentID string
	WorkspaceID  string
}

// StuckSend is a (send id, workspace id) pair from the reconciler query.
type StuckSend struct {
	SendID      string
	WorkspaceID string
}

// SendJob is everything the worker needs to send one email — including the
// decrypted SMTP password (in-memory only, never logged). SMTPPassword is
// a []byte so the worker can zeroize it after use; a Go string would be
// immutable and hang around in memory until GC.
type SendJob struct {
	SendID            string
	WorkspaceID       string
	Attempts          int
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
	SMTPPassword      []byte
	UseTLS            bool
}

// SendResult is the outcome of a single send attempt.
type SendResult struct {
	Status    string // "sent" | "failed"
	MessageID string
	Err       string
}
