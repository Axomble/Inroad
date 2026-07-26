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

	// --- Worker routing (per-IP egress; spec §15) ---

	// UpsertWorkerHeartbeat refreshes this worker's row in the GLOBAL `workers`
	// registry (worker_id, egress_ip, last_seen_at=now()) on each heartbeat tick,
	// so AssignMailboxWorker can tell which workers are live. `workers` is global
	// infrastructure state, NOT tenant data — no workspace pin (security
	// invariant §17.9).
	UpsertWorkerHeartbeat(ctx context.Context, workerID, egressIP string) error
	// AssignMailboxWorker resolves the destination queue for a mailbox's outbound
	// traffic, pinning it to ONE worker's egress IP (the deliverability win). It
	// is idempotent: an existing assignment is returned unchanged. A first
	// assignment picks the least-loaded worker with a live heartbeat and persists
	// it. When NO worker has a live heartbeat (single-node dev), it returns ""
	// (the shared default queue) WITHOUT persisting, so everything still runs on
	// one process and a real worker can claim the mailbox once it comes online.
	// workspace-pinned — mailbox_worker_assignments is tenant data. The returned
	// queueName is "w:<worker_id>" or "" for the default queue; it is derived
	// server-side from the assignment, never from client input (invariant §17.8).
	AssignMailboxWorker(ctx context.Context, mailboxID, workspaceID string) (queueName string, err error)

	// --- Warmup send path (warmup:tick; spec §4/§6) ---

	// GetWarmupSendJob picks the next warmup action for a warming mailbox: it
	// selects a healthy same-workspace partner (recent-partner-avoidance), decides
	// new-thread vs reply deterministically (participant.reply_rate + open threads),
	// resolves content from the injected library, builds threading headers + a
	// signed X-Inroad-Warmup receipt token, and loads the decrypted transport for
	// the FROM mailbox. Read-only w.r.t. warmup_sends (no row yet); it MAY open a
	// warmup_threads row when starting a new thread so the job can carry a valid
	// thread id. workspaceID is pinned in every SQL WHERE. Returns Skip=true when
	// the mailbox is paused, over today's target, no longer enabled, or has no
	// eligible partner.
	GetWarmupSendJob(ctx context.Context, mailboxID, workspaceID string) (WarmupSendJob, error)
	// ClaimWarmupSend inserts/reclaims the warmup_sends row 'sending' with a lease
	// (claim-before-send), same ClaimOutcome semantics as ClaimStepSend:
	// ClaimWon (fresh insert or reclaimed stale/queued lease → send now),
	// ClaimAlreadySent (this exact send already 'sent' → do NOT re-send), or
	// ClaimSkip (a fresh 'sending' another worker owns, or a terminal state). The
	// row id is the deterministic SendID on the job; workspace_id is pinned.
	ClaimWarmupSend(ctx context.Context, job WarmupSendJob) (ClaimOutcome, error)
	// MarkWarmupSent finalizes the claimed row to 'sent' + records message_id,
	// advances the thread turn (and sets root_message_id on turn 0), and increments
	// warmup_daily_stats.sent — all in ONE transaction, exactly once (idempotent on
	// re-run). workspace_id is pinned.
	MarkWarmupSent(ctx context.Context, job WarmupSendJob, messageID string) error
	// ReleaseWarmupSend releases a claimed-but-unsent row after a RETRYABLE failure
	// (back to 'queued', lease cleared) so the asynq retry reclaims it promptly. No
	// thread advance. workspace_id is pinned.
	ReleaseWarmupSend(ctx context.Context, job WarmupSendJob) error
	// FailWarmupSend finalizes the claimed row to 'failed' after a PERMANENT failure
	// (no thread advance, no stat bump). workspace_id is pinned.
	FailWarmupSend(ctx context.Context, job WarmupSendJob, errMsg string) error
	// NextWarmupDue returns when this mailbox should send its next warmup email,
	// applying the ramp target, per-day volume factor, inter-send spacing, health
	// pause, and the waking-hours window. sendNow reports whether one is due right
	// now (under target and inside the window). Pure policy over warmup_daily_stats
	// + the participant; workspace_id is pinned.
	NextWarmupDue(ctx context.Context, mailboxID, workspaceID string) (due time.Time, sendNow bool, err error)

	// --- Warmup receipt + engagement path (inbox poll → warmup:engage; spec §4/§8) ---

	// RecordWarmupReceipt is called by the inbox poller when it detects a warmup
	// message (verified X-Inroad-Warmup token, §7). It idempotently upserts the
	// warmup_receipts row (UNIQUE on (warmup_send_id, recipient_mailbox)) and, ONLY
	// on a genuinely NEW receipt, bumps the RECIPIENT's warmup_daily_stats
	// received/inbox/spam for its placement observation and returns the deterministic
	// engagement plan (rescue-if-spam, always mark-read, seeded reply decision, and
	// the humanized engage dwell). A DUPLICATE receipt (a re-poll) returns a
	// zero-value WarmupEngagePlan so it can never double-engage or double-count. The
	// receipt insert is self-enforcing on the recipient's workspace, so a foreign
	// recipient yields ErrCrossTenant. workspace_id is pinned.
	RecordWarmupReceipt(ctx context.Context, in WarmupReceiptInput) (WarmupEngagePlan, error)
	// GetWarmupEngageJob loads what the engage worker needs to act on one received
	// warmup message: the recipient's decrypted send transport (for the reply), the
	// placement-derived source folder, and — when the deterministic plan replies AND
	// the thread still has a turn — the reply subject/body/threading headers and a
	// FRESH signed receipt token for the reply send. The rescue/reply flags are
	// recomputed deterministically from the receipt (same seed as RecordWarmupReceipt),
	// so no plan state is persisted. workspace_id is pinned; a foreign/vanished
	// receipt yields a not-found error.
	GetWarmupEngageJob(ctx context.Context, receiptID, workspaceID string) (WarmupEngageJob, error)
	// MarkWarmupEngaged flips warmup_receipts.engaged=true (and, when the engagement
	// replied, bumps the recipient's daily replies counter) so a retried engage is a
	// no-op. The flip is guarded on NOT engaged, so a second call is idempotent (no
	// double reply-count). workspace_id is pinned.
	MarkWarmupEngaged(ctx context.Context, receiptID, workspaceID string, replied bool) error

	// --- Warmup scheduling fan-out (warmup:sweep; spec §4/§8) ---

	// ListDueWarmupMailboxes returns (mailbox, workspace) for every enabled,
	// non-paused participant, the coarse sweep fan-out. Precise ramp/window due-gating
	// is delegated to NextWarmupDue in the send handler, so this only excludes states
	// that can never send now (disabled, or a live pause window). Global fan-out.
	ListDueWarmupMailboxes(ctx context.Context) ([]MailboxRef, error)
	// EvaluateWarmupHealth recomputes each enabled participant's health_state from its
	// trailing-window signals (spam-placement rate over warmup_daily_stats; bounce and
	// invalid-token signals are 0 in v1, which has no persistence for them) and
	// persists ONLY actual state transitions (escalation to the worst warranted level,
	// or a one-level recovery step on a clean window), setting the pause window per
	// the resulting state. Called on the sweep tick. Global fan-out; each write is
	// workspace-pinned.
	EvaluateWarmupHealth(ctx context.Context) error
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

// WarmupSendJob is everything the warmup:tick worker needs to send one warmup
// email (a new-thread opener or a reply), mirroring StepSendJob. Skip is true when
// GetWarmupSendJob found nothing to do (mailbox paused/over-target/disabled, or no
// eligible partner) — the worker no-ops. The worker passes the job back to
// ClaimWarmupSend / MarkWarmupSent / Release / Fail unchanged, so it carries every
// id those finalizers need without re-querying.
//
// Secrets are []byte (AccessToken, SMTPPassword) so the worker can zeroize them
// after one send — a Go string would be immutable and linger in memory until GC.
type WarmupSendJob struct {
	Skip        bool
	WorkspaceID string
	// FromMailbox / ToMailbox identify the two participants; ThreadID is the thread
	// this send belongs to (an existing open thread for a reply, or a freshly
	// opened one for a new-thread send).
	FromMailbox string
	ToMailbox   string
	ThreadID    string
	IsReply     bool
	// SendID is the deterministic warmup_sends row id, derived up front (before the
	// send) so it can be embedded in the receipt token — ClaimWarmupSend writes it
	// as the row id so a retried tick reclaims the SAME row. Derived from
	// (from_mailbox, UTC day, today's send index), the stable tuple available
	// read-side; see the inprocess deriveWarmupSendID doc for why this replaces the
	// spec's dueUnix (GetWarmupSendJob's signature carries no tick time).
	SendID string
	// ToEmail / FromEmail / FromName address the message envelope.
	ToEmail   string
	FromEmail string
	FromName  string
	Subject   string
	BodyText  string
	BodyHTML  string
	// InReplyTo / References thread a reply to the conversation root; empty for a
	// new-thread opener.
	InReplyTo  string
	References string
	// Token is the signed X-Inroad-Warmup receipt header value the poller verifies.
	Token string
	// Provider selects the send transport ("smtp" | "gmail" | "m365"). AccessToken
	// is the decrypted OAuth bearer for API providers (nil for smtp); zeroized after
	// use like SMTPPassword. For API providers the SMTP* fields are empty.
	Provider     string
	AccessToken  []byte
	SMTPHost     string
	SMTPPort     int
	SMTPUsername string
	SMTPPassword []byte
	// AllowPlaintext is the persisted per-mailbox cleartext opt-out; threaded into
	// the outbound job so the send applies the SAME TLS policy the connect-test
	// validated. False keeps TLS enforced.
	AllowPlaintext bool
}

// WarmupReceiptInput is the poller's report of one detected warmup message: the
// pinned workspace, the warmup_send_id decoded from the verified X-Inroad-Warmup
// token, the recipient mailbox that observed it, and its Placement (one of
// "inbox" | "spam" | "other"). SourceFolder + MessageID are the receipt locator:
// the provider folder the message was found in (e.g. "INBOX" | "Junk" | "SPAM" |
// "JunkEmail") and its RFC822 Message-ID, persisted so the engage worker (C5b)
// can relocate/rescue/mark-read the exact message. All ids are strings at the
// seam; the impl parses and pins them.
type WarmupReceiptInput struct {
	WorkspaceID      string
	WarmupSendID     string
	RecipientMailbox string
	Placement        string
	SourceFolder     string
	MessageID        string
}

// WarmupEngagePlan is what a recipient should do about a newly received warmup
// message, returned by RecordWarmupReceipt. It is deterministic in the receipt
// (so it is reproducible and needs no persistence) and ZERO-VALUED for a duplicate
// receipt (nothing to do). The worker enqueues warmup:engage after EngageAfter and
// carries ReceiptID so GetWarmupEngageJob can reload the target.
type WarmupEngagePlan struct {
	ReceiptID string
	// DoRescue moves the message out of spam (set only when Placement was "spam").
	DoRescue bool
	// DoMarkRead clears the unread flag (always true — a real recipient reads it).
	DoMarkRead bool
	// DoReply sends a threaded reply, decided by the recipient's reply_rate via the
	// deterministic seeded ReplyDecision.
	DoReply bool
	// EngageAfter is the humanized dwell before the recipient acts (heavy-tailed,
	// deterministic in the receipt id).
	EngageAfter time.Duration
}

// WarmupEngageJob is everything the warmup:engage worker needs to act on one
// received warmup message. The transport fields are the RECIPIENT's decrypted send
// transport (the reply is a new warmup send FROM the recipient); AccessToken /
// SMTPPassword are []byte so the worker can zeroize them after use, like
// WarmupSendJob. SourceFolder is the placement the message landed in ("inbox" |
// "spam" | "other"); the engager maps it to a provider folder. The Do* flags are
// recomputed deterministically from the receipt. The Reply* fields and Token are
// populated ONLY when DoReply is true and the thread still has a turn to send.
type WarmupEngageJob struct {
	// Provider selects the send transport ("smtp" | "gmail" | "m365"). AccessToken
	// is the decrypted OAuth bearer for API providers (nil for smtp); zeroized after
	// use like SMTPPassword. For API providers the SMTP* fields are empty.
	Provider     string
	AccessToken  []byte
	SMTPHost     string
	SMTPPort     int
	SMTPUsername string
	SMTPPassword []byte
	// AllowPlaintext is the recipient mailbox's cleartext opt-out; threaded into the
	// reply's outbound job so it applies the SAME TLS policy the connect-test validated.
	AllowPlaintext bool
	// SourceFolder is where the message was observed ("inbox" | "spam" | "other").
	SourceFolder string
	// DoRescue / DoMarkRead / DoReply mirror the plan, recomputed from the receipt.
	DoRescue   bool
	DoMarkRead bool
	DoReply    bool
	// ReplySubject / ReplyBody / InReplyTo / References build the threaded reply;
	// empty unless DoReply is true.
	ReplySubject string
	ReplyBody    string
	InReplyTo    string
	References   string
	// Token is the signed X-Inroad-Warmup receipt header for the reply send; empty
	// unless DoReply is true.
	Token string
}
