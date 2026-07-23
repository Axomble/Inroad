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

// ErrNoMatch is returned by FindSendByMessageID when no send matches the
// inbound Message-ID (e.g. a reply/bounce referencing a message this
// workspace never sent).
var ErrNoMatch = errors.New("coreapi: no matching send")

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
	// single insertion point for the current_step transition and cadence, run in
	// one transaction. It takes the StepSendJob returned by GetStepSendJob so the
	// immutable values (campaign/contact/mailbox ids, resolved step, references,
	// next-step delay) are reused rather than re-queried. Returns whether the
	// enrollment completed and, if not, when the next step is due.
	MarkStepSent(ctx context.Context, job StepSendJob, res StepResult) (Advance, error)
	// MarkStepStopped halts an enrollment (the single stop entry point). reason
	// is one of the enrollment stop reasons (e.g. "suppressed").
	MarkStepStopped(ctx context.Context, enrollmentID, workspaceID, reason string) error
	// IncrementEnrollmentCapDeferrals bumps the enrollment's cap-deferral counter
	// and returns the new value. Mirrors IncrementSendAttempts on the direct-send
	// path: the advance handler uses it to break out of the cap-defer loop when a
	// mailbox cap is never clearing.
	IncrementEnrollmentCapDeferrals(ctx context.Context, enrollmentID, workspaceID string) (int, error)
	// ListDueEnrollments returns active enrollments whose next_due_at passed the
	// reconcile window. Consumed by the periodic enrollment sweeper.
	ListDueEnrollments(ctx context.Context) ([]DueEnrollment, error)

	// --- Reply & bounce detection (inbox poll path) ---

	// ListActiveMailboxes returns (id, workspace id) pairs for every mailbox
	// eligible for inbox polling. Consumed by the periodic poll-queue enqueuer.
	ListActiveMailboxes(ctx context.Context) ([]MailboxRef, error)
	// GetInboxPollJob loads one mailbox's IMAP connection details, decrypted
	// credential, and stored poll cursor. workspaceID is pinned in the SQL
	// WHERE (defense in depth on the unguessable mailbox UUID).
	GetInboxPollJob(ctx context.Context, mailboxID, workspaceID string) (InboxPollJob, error)
	// SetInboxCursor persists the IMAP poll cursor after a poll pass, so the
	// next pass resumes from LastSeenUID (or resyncs from scratch if
	// UIDValidity changed underneath it). workspaceID pinned as above.
	SetInboxCursor(ctx context.Context, mailboxID, workspaceID string, lastSeenUID, uidValidity uint32) error
	// SetInboxCursorString persists an opaque provider cursor (Gmail historyId)
	// after a poll pass for API-provider mailboxes; the IMAP UID cursor columns
	// are left untouched. workspaceID pinned as above.
	SetInboxCursorString(ctx context.Context, mailboxID, workspaceID, cursor string) error
	// FindSendByMessageID matches an inbound reply/bounce's Message-ID back to
	// the send that caused it, workspace-scoped. Returns ErrNoMatch when
	// nothing matches.
	FindSendByMessageID(ctx context.Context, workspaceID, messageID string) (SendRef, error)
	// MarkReplied halts the enrollment (if any) on an inbound reply.
	MarkReplied(ctx context.Context, enrollmentID, workspaceID string) error
	// MarkBounced records a hard bounce: halts the enrollment (if any) and
	// suppresses the address. hard distinguishes hard from soft bounces; soft
	// bounces are logged by the caller and never reach this method with
	// hard=true.
	MarkBounced(ctx context.Context, enrollmentID, workspaceID, email string, hard bool) error
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
//
// The worker passes the job back to MarkStepSent unchanged, so it also carries
// the routing/cadence values MarkStepSent needs to record the send and advance
// the cursor without re-querying: CampaignID/ContactID/MailboxID (send row),
// CurrentStep (the cursor before this send), NextDelaySeconds (delay of the step
// after this one; 0 when LastStep), and References (the stored references chain).
type StepSendJob struct {
	Skip         bool
	EnrollmentID string
	WorkspaceID  string
	CampaignID   string
	ContactID    string
	MailboxID    string
	// SendID is generated up front (before the step is sent) so the worker can
	// embed it in tracking tokens at MIME-build time; MarkStepSent writes it as
	// the sends row's id, so the events recorded against it (via the pixel/
	// click endpoints) line up with the eventual send row.
	SendID            string
	CurrentStep       int
	StepOrder         int
	NextDelaySeconds  int
	LastStep          bool
	Suppressed        bool
	EffectiveDailyCap int
	SentToday         int
	ToEmail           string
	Vars              ContactVars
	Subject           string
	ThreadSubject     string
	BodyText          string
	BodyHTML          string
	UnsubURL          string
	InReplyTo         string
	References        string
	// TrackingEnabled mirrors the campaign's tracking_enabled column: when true
	// and BodyHTML is non-empty, the worker rewrites links and appends an open
	// pixel before sending.
	TrackingEnabled bool
	FromEmail       string
	FromName        string
	// Provider selects the send transport ("smtp" | "gmail"). AccessToken is the
	// decrypted OAuth bearer for gmail (nil for smtp); zeroized after use like
	// SMTPPassword. For gmail the SMTP* fields are empty.
	Provider     string
	AccessToken  []byte
	SMTPHost     string
	SMTPPort     int
	SMTPUsername string
	SMTPPassword []byte
	UseTLS       bool
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

// MailboxRef is a (mailbox id, workspace id) pair from ListActiveMailboxes.
type MailboxRef struct {
	ID          string
	WorkspaceID string
}

// InboxPollJob is everything the inbox poller needs to open one mailbox's
// IMAP connection and resume from its stored cursor. Password is []byte (not
// a Go string) so the poller can zeroize it after use — same rationale as
// StepSendJob.SMTPPassword. LastSeenUID/UIDValidity are the persisted poll
// cursor: a UIDVALIDITY change means the server renumbered the mailbox and
// the poller must resync from scratch.
type InboxPollJob struct {
	// Provider selects the inbox transport ("smtp" | "gmail"). For gmail the IMAP
	// fields are zero and AccessToken/Cursor carry the decrypted OAuth bearer and
	// the opaque historyId cursor; AccessToken is zeroized after the poll like
	// Password. For smtp the AccessToken/Cursor fields are empty.
	Provider    string
	AccessToken []byte
	Cursor      string
	Host        string
	Port        int
	Username    string
	Password    []byte
	UseTLS      bool
	LastSeenUID uint32
	UIDValidity uint32
}

// SendRef identifies the send an inbound reply/bounce matched, and the
// enrollment (if any) it belongs to. EnrollmentID is "" when the matched send
// has no enrollment — the legacy direct-send path.
type SendRef struct {
	SendID       string
	EnrollmentID string
	ContactEmail string
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
	// TrackingEnabled mirrors the campaign's tracking_enabled column; see
	// StepSendJob.TrackingEnabled for the injection contract.
	TrackingEnabled bool
	// Provider selects the send transport ("smtp" | "gmail"). AccessToken is the
	// decrypted OAuth bearer for gmail (nil for smtp); zeroized after use like
	// SMTPPassword. For gmail the SMTP* fields are empty.
	Provider    string
	AccessToken []byte
}

// SendResult is the outcome of a single send attempt.
type SendResult struct {
	Status    string // "sent" | "failed"
	MessageID string
	Err       string
}
