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
	// ClaimSend claims a direct-send for delivery (claim-before-send): the
	// pre-existing 'queued' sends row is moved to 'sending' with a fresh lease,
	// or a STALE 'sending' lease is reclaimed after a crash. Reports whether the
	// claim was won; a false result means another worker owns it or it is already
	// terminal, so the caller must NOT send. Same workspace pinning as GetSendJob.
	ClaimSend(ctx context.Context, sendID, workspaceID string) (claimed bool, err error)
	// ReleaseSend releases a claimed-but-unfinalized send after a RETRYABLE
	// failure, expiring the lease so the asynq retry reclaims it promptly. Only
	// touches a row still in 'sending'. Same workspace pinning.
	ReleaseSend(ctx context.Context, sendID, workspaceID string) error
	// MarkSend finalizes a send to its terminal state (sent/failed/skipped).
	// Same workspace pinning as GetSendJob.
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
	// ClaimStepSend claims one step-send for delivery (claim-before-send): the
	// sends row is inserted 'sending' (fresh claim), or a STALE 'sending' lease is
	// reclaimed after a crash. The returned ClaimOutcome tells the caller what to
	// do WITHOUT it having to distinguish "crashed before delivery" from
	// "delivered, finalize failed":
	//   - ClaimWon: we own the claim; build + send now.
	//   - ClaimAlreadySent: this exact step already delivered (a prior run's
	//     status='sent' UPDATE committed but its cursor advance didn't). The
	//     caller must NOT re-send — it runs ONLY the cursor advance
	//     (recover-forward) via AdvanceStepCursor.
	//   - ClaimSkip: a fresh 'sending' (another worker owns it) or a terminal
	//     'failed'/'skipped' — do nothing.
	// It reuses the immutable values GetStepSendJob resolved (carried on the job)
	// — including the deterministic SendID the tracking tokens embed — so the
	// claimed row's id matches those tokens. workspace_id is pinned on the claim.
	ClaimStepSend(ctx context.Context, job StepSendJob) (ClaimOutcome, error)
	// MarkStepDelivered records a successful delivery in its OWN committed
	// statement — UPDATE sends SET status='sent', message_id, sent_at=now() — so
	// the delivery is durable independently of the cursor advance. Called
	// immediately after Send succeeds and BEFORE AdvanceStepCursor, so if the
	// cursor advance then fails, the asynq retry's claim sees the 'sent' row
	// (ClaimAlreadySent) and recover-forwards instead of re-delivering.
	// workspace_id pinned.
	MarkStepDelivered(ctx context.Context, job StepSendJob, messageID string) error
	// AdvanceStepCursor advances the enrollment cursor via the enrollment state
	// machine (the single current_step transition + cadence point), in its own
	// committed step. Idempotent: current_step is set absolutely, so a retried
	// recover-forward re-advances to the same value. On step 1 it reads the
	// already-'sent' row's message_id to record the thread root. Returns whether
	// the enrollment completed and, if not, when the next step is due. Used by
	// BOTH the normal success path (after MarkStepDelivered) and recover-forward.
	// workspace_id pinned.
	AdvanceStepCursor(ctx context.Context, job StepSendJob) (Advance, error)
	// FinalizeStepSend finalizes the claimed step-send to a NON-'sent' terminal
	// state (permanent 'failed') AND advances the enrollment cursor in ONE
	// transaction (fail-forward). The success path does NOT use this — it splits
	// into MarkStepDelivered + AdvanceStepCursor so a delivered row can never be
	// left unrecorded by a failed combined-tx (the residual double-send this
	// replaced). A failed finalize is atomic with its advance, so a 'failed' row
	// always has an advanced cursor (nothing to recover-forward). Reuses the
	// immutable job values rather than re-querying.
	FinalizeStepSend(ctx context.Context, job StepSendJob, res StepResult) (Advance, error)
	// ReleaseStepSend releases a claimed-but-unfinalized step-send after a
	// RETRYABLE failure, expiring the lease so the asynq retry reclaims it
	// promptly without advancing the cursor. Only touches a row still in
	// 'sending'. workspace_id pinned.
	ReleaseStepSend(ctx context.Context, job StepSendJob) error
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
	// MarkReplied halts the enrollment (if any) on an inbound reply and tags it
	// with the classified reply (class/source/confidence + replied_at). When
	// enrollmentID is "" (the matched send has no enrollment — the legacy
	// direct-send path) there is nothing to stop or tag, so it is a no-op.
	MarkReplied(ctx context.Context, enrollmentID, workspaceID, replyClass, replySource string, confidence float64) error
	// MarkUnsubscribed suppresses the address (reusing the same workspace-scoped,
	// idempotent suppression insert as MarkBounced) and, when enrollmentID is
	// non-empty, halts the enrollment (reason unsubscribed) and tags it
	// class=unsubscribe. The suppression happens EVEN WHEN enrollmentID is ""
	// (a reply-unsubscribe to a legacy direct-send must still suppress the
	// address — compliance).
	MarkUnsubscribed(ctx context.Context, enrollmentID, workspaceID, email string) error
	// RecordReplyClass tags the enrollment with a classified reply WITHOUT
	// stopping it — for automated replies (auto_reply/out_of_office) that must
	// not halt the sequence. A no-op when enrollmentID is "" (nothing to tag).
	RecordReplyClass(ctx context.Context, enrollmentID, workspaceID, class, source string, confidence float64) error
	// MarkBounced records a hard bounce: halts the enrollment (if any) and
	// suppresses the address. hard distinguishes hard from soft bounces; soft
	// bounces are logged by the caller and never reach this method with
	// hard=true.
	MarkBounced(ctx context.Context, enrollmentID, workspaceID, email string, hard bool) error
}

// ClaimOutcome is the result of ClaimStepSend: what the advance handler should
// do about the send row it tried to claim. It lets the handler recover-forward
// (advance the cursor without re-sending) when a prior run delivered but never
// advanced — closing the residual double-send window.
type ClaimOutcome int

const (
	// ClaimSkip: another worker owns a fresh 'sending' lease, or the row is a
	// terminal 'failed'/'skipped'. Do nothing.
	ClaimSkip ClaimOutcome = iota
	// ClaimWon: we own the claim (fresh insert or reclaimed stale lease). Send.
	ClaimWon
	// ClaimAlreadySent: this exact step already delivered ('sent'); advance the
	// cursor only (recover-forward), never re-send.
	ClaimAlreadySent
)

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
	// AllowPlaintext is the persisted per-mailbox cleartext opt-out (mailboxes.
	// allow_plaintext). Threaded into OutboundJob so the send applies the SAME
	// TLS policy the connect-test validated; false keeps TLS enforced.
	AllowPlaintext bool
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
	// AllowPlaintext is the persisted per-mailbox cleartext opt-out (mailboxes.
	// allow_plaintext), threaded into OutboundJob so the send applies the SAME
	// TLS policy the connect-test validated; false keeps TLS enforced.
	AllowPlaintext bool
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
