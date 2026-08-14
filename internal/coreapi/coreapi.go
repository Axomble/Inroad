// Package coreapi is the control⇄execution boundary. Workers depend on this
// interface, never on platform/db directly. v1 satisfies it in-process; a
// future HTTP implementation swaps in without changing worker code.
package coreapi

import (
	"context"
	"errors"
	"time"

	"github.com/inroad/inroad/internal/platform/cadence"
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

// CRMCaptureClient is an optional execution-plane capability. Keeping it
// separate from Client lets inbox workers feature-detect CRM capture without
// forcing every worker fake and future remote client to implement it.
type CRMCaptureClient interface {
	CaptureCRMReply(context.Context, CRMReplyInput) error
}

type CRMReplyInput struct {
	WorkspaceID       string
	EnrollmentID      string
	SendID            string
	ThreadRef         string
	MessageID         string
	Subject           string
	SenderEmail       string
	RecipientEmail    string
	SenderDisplayName string
	ReplyClass        string
	OccurredAt        time.Time
}

// InboxCaptureClient is an optional execution-plane capability, same reasoning
// as CRMCaptureClient above: kept separate from Client so a worker fake that
// doesn't care about inbox storage doesn't have to implement it.
type InboxCaptureClient interface {
	StoreInboundMessage(context.Context, InboxMessageInput) error
}

// WarmupEvidenceClient is an optional inbox capability. It keeps token failures
// and warmup DSNs on the control-plane side without widening Client for workers
// that never inspect inbound mail.
type WarmupEvidenceClient interface {
	RecordWarmupTokenFailure(ctx context.Context, workspaceID, recipientMailbox, fingerprint, reasonCode string) error
	RecordWarmupHardBounce(ctx context.Context, workspaceID, messageID, observerMailbox string) (matched bool, err error)
}

// ReplyLabelClient is an optional execution-plane capability (same reasoning as
// the two above) that resolves a classified reply key to the workspace's label
// row, so the inbox poller can dispatch on the label's ROLE FLAGS instead of a
// hardcoded class switch.
//
// ok=false is NOT an error: it means no label in the workspace claims the key
// (a deleted custom label whose key survives on historical rows, or a core that
// predates the taxonomy). The caller must then fall back to its pre-taxonomy
// behaviour rather than inventing a label.
type ReplyLabelClient interface {
	ResolveReplyLabel(ctx context.Context, workspaceID, key string) (ReplyLabel, bool, error)
}

// ReplyLabel is the automation contract of one reply label, flattened to the
// role flags the execution plane acts on. It deliberately omits the display
// fields (label/color/position) — the worker never renders a label, and a seam
// type that carries only what the caller may act on cannot grow a display
// dependency by accident.
type ReplyLabel struct {
	// Key is the stable machine key the classifier produced and historical
	// rows store as free text.
	Key string
	// StopsEnrollment halts the enrollment (MarkReplied).
	StopsEnrollment bool
	// IsAutomated marks the machine-generated family (OOO/auto-reply): never a
	// human reply, so it never stops the sequence.
	IsAutomated bool
	// SuppressesContact suppresses the address then stops (compliance).
	SuppressesContact bool
	// CapturesDeal opens/updates a CRM deal from the reply.
	CapturesDeal bool
	// DefersEnrollment pushes the next step past a stated return date instead
	// of stopping. Only meaningful on an automated (non-stopping) label.
	DefersEnrollment bool
}

// InboxMessageInput is one inbound reply the poller matched (or a legacy
// direct-send match with no enrollment). CampaignID/ContactID are *string,
// not an empty-string sentinel: they mirror inbox.RecordReplyInput's
// *uuid.UUID nilability at this string-typed seam, so "no match" is
// unambiguous even though a matched id is never itself the empty string.
type InboxMessageInput struct {
	WorkspaceID string
	MailboxID   string
	CampaignID  *string // nil when the match has no campaign (legacy direct-send)
	ContactID   *string // nil likewise
	// RootMessageID is sends.message_id this thread anchors on; "" for a
	// legacy match (RootMessageID has no pointer form: "" is itself the
	// domain's documented legacy sentinel — see inbox's partial unique index).
	RootMessageID string
	Subject       string
	MessageID     string
	FromEmail     string
	FromName      string
	ToEmail       string
	BodyText      string
	BodyHTML      string
	ReplyClass    string
	OccurredAt    time.Time
}

type Client interface {
	// MailboxExists reports whether a mailbox is present and active.
	MailboxExists(ctx context.Context, id string) (bool, error)

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
	// DeferEnrollment pushes an ACTIVE enrollment's next_due_at out to until,
	// leaving it active — the out-of-office path, where the contact stated a
	// return date and the sequence should resume after it rather than stop.
	// Delegates to the enrollment state machine's Reschedule, so it is the same
	// single SetDue entry point the launch stagger uses; a non-active enrollment
	// matches 0 rows and is a safe no-op.
	//
	// Pushing next_due_at does NOT cancel the asynq advance task already queued
	// for the OLD time. The claim-time guard (see StepSendJob.NotDueUntil /
	// NotYetDue, enforced in ClaimStepSend) is what actually stops that task
	// from firing into the stated absence; this method only moves the date.
	DeferEnrollment(ctx context.Context, enrollmentID, workspaceID string, until time.Time) error
	// IncrementEnrollmentCapDeferrals bumps the enrollment's cap-deferral counter
	// and returns the new value. The advance handler uses it to break out of the
	// cap-defer loop when a mailbox cap is never clearing.
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
	// on a genuinely NEW receipt, bumps recipient volume, records sender-attributed
	// placement evidence, and returns the deterministic
	// engagement plan (rescue-if-spam, always mark-read, seeded reply decision, and
	// the humanized engage dwell). A DUPLICATE receipt (a re-poll) returns a
	// stored deterministic plan while still unengaged, then a zero plan after
	// engagement. The receipt insert proves the send's intended recipient as well
	// as workspace ownership; foreign recipients fail closed. workspace_id is pinned.
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
	// qualified placement, bounce, complaint, and trusted token evidence, and
	// persists ONLY actual state transitions (escalation to the worst warranted level,
	// or a one-level recovery step on a clean window), setting the pause window per
	// the resulting state. Called on the sweep tick. Global fan-out; each write is
	// workspace-pinned.
	EvaluateWarmupHealth(ctx context.Context) error

	// --- Sending-domain authentication (domainauth:sweep) ---

	// ListStaleSendingDomains returns every domain the deployment sends from
	// whose last COMPLETED check is older than staleAfter (or which has never
	// been checked), each paired with its workspace. The domain set is derived
	// from mailboxes.email, so a newly connected mailbox's domain appears
	// immediately. Global fan-out — domain authentication is infrastructure
	// maintenance, not a tenant read — and each returned row carries the
	// workspace its write-back is pinned to.
	ListStaleSendingDomains(ctx context.Context, staleAfter time.Duration) ([]SendingDomainRef, error)
	// RecordSendingDomainAuth persists one COMPLETED check, workspace-pinned.
	// A result whose State is "unknown" is REJECTED here (a no-op, not an
	// error): a transient resolver failure must never overwrite a known-good
	// verdict, and it must not stamp checked_at, or the sweep would wait out the
	// staleness window on an answer it never got. The sweep handler skips those
	// too — this is the belt-and-braces half of that rule.
	RecordSendingDomainAuth(ctx context.Context, in SendingDomainAuth) error
}

// BreakerResult is the outcome of one campaign circuit-breaker evaluation.
//
// It is a seam TYPE here but the method that returns it is deliberately NOT on
// Client: the breaker is consumed through a one-method interface defined by the
// worker package that needs it (worker/deliverability.Breaker), satisfied by the
// in-process client via type assertion at the composition root — the same shape
// as maintenance.Cleaner. Client already carries 40 methods that 13 test fakes
// implement in full, so adding a 41st to serve one call site would break every
// one of them for no gain in the seam's expressiveness.
//
// Paused is true only for the evaluation that ACTUALLY stopped the campaign, so
// exactly one caller ever reports the pause. The remaining fields explain why it
// fired: which rate, its observed value, the threshold it crossed, and the sample
// it was judged on.
type BreakerResult struct {
	Paused    bool
	Reason    string
	Metric    string
	Value     float64
	Threshold float64
	Delivered int
}

// SendingDomainRef is a (workspace id, domain) pair from the staleness scan.
// Strings at the seam, like every other coreapi id; the implementation parses
// and pins them.
type SendingDomainRef struct {
	WorkspaceID string
	Domain      string
}

// SendingDomainAuth is one completed check, reported by the sweep. State is the
// verdict the checker computed ("passing" | "failing"; "unknown" is not
// persisted). DKIM is carried for display only and never contributes to State —
// selectors are not discoverable from DNS, so DKIMFound=false means "none of the
// probed selectors matched", not "unsigned".
type SendingDomainAuth struct {
	WorkspaceID  string
	Domain       string
	State        string
	SPFFound     bool
	SPFRecord    string
	DKIMFound    bool
	DKIMSelector string
	DMARCFound   bool
	DMARCPolicy  string
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
	// ClaimDeferred: the mailbox's minimum send interval has not elapsed. The
	// worker should re-enqueue this enrollment without sending or advancing.
	ClaimDeferred
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
	SendID string
	// VariantID is the A/B variant whose copy Subject/BodyText/BodyHTML carry,
	// or "" when the step's own base content was selected (see migration 000053:
	// a step IS variant A). It is written to sends.variant_id at claim time so
	// results can be attributed per variant.
	//
	// Selection happens in the CONTROL plane, alongside the step content it
	// chooses, and travels here already decided. The worker must not re-select:
	// it has no reason to reach the variant rows, and a second roll could
	// disagree with the copy already in this job.
	VariantID        string
	CurrentStep      int
	StepOrder        int
	NextDelaySeconds int
	LastStep         bool
	Suppressed       bool
	// MailboxRemoved means this thread's sending mailbox has been deleted, so the
	// enrollment's pin was cleared (ON DELETE SET NULL) and the sequence cannot
	// legitimately continue: a follow-up would go out from a different address
	// carrying In-Reply-To/References for a Message-ID that address never sent.
	// Handled like Suppressed — the worker stops the enrollment and sends nothing.
	MailboxRemoved bool
	// CampaignLimited means the campaign has reached campaigns.daily_limit for the
	// UTC day: its whole pool is still under its per-mailbox caps, but the campaign
	// as a whole may not send more today. HealthPaused means the mailbox this
	// thread must send from has been paused by the warmup engine.
	//
	// Both defer the step (backoff + cap_deferrals) exactly as an over-cap mailbox
	// does, and both are carried EXPLICITLY rather than expressed by reporting
	// SentToday >= EffectiveDailyCap: those two numbers reach the logs and describe
	// the mailbox, so a campaign-wide limit or a health pause must not masquerade as
	// a mailbox that has used up its cap.
	CampaignLimited bool
	// NewLeadLimited means the campaign has reached campaigns.max_new_leads_per_day
	// for the UTC day and THIS job is a step-1 send (a brand-new contact starting
	// the sequence). It is narrower than CampaignLimited: a follow-up step (step
	// 2+) is never gated by it, so a sequence already in flight keeps replying on
	// schedule while the campaign is closed to new contacts. Deferred exactly like
	// CampaignLimited (backoff snapped into the send window, never a failure).
	NewLeadLimited bool
	HealthPaused   bool
	// CampaignPaused means the campaign is not 'running' — paused (by hand or by the
	// deliverability circuit breaker), or still draft, or done. It gates the send
	// itself: without it a breaker-paused campaign kept sending, because every
	// mid-sequence enrollment is 'active' at the moment of the pause and each
	// successful send re-enqueues the next advance, so the chain is
	// self-perpetuating.
	//
	// Carried EXPLICITLY, like the two flags above, rather than expressed as Skip:
	// Skip means "nothing to do here ever" and leaves the enrollment where it is,
	// whereas a pause is a condition that CLEARS. The enrollment has to wait and
	// resume, so the worker defers it (see the blocked branch in advance.go).
	CampaignPaused bool
	// NotDueUntil is the enrollment's persisted next_due_at, carried so the
	// claim can refuse a step that is not due yet. It exists because pushing
	// next_due_at out (DeferEnrollment, the out-of-office path) cannot cancel
	// the asynq advance task ALREADY queued for the old time: without this
	// guard that task fires on schedule and sends into the stated absence.
	// Zero when the enrollment has no due time recorded.
	NotDueUntil        time.Time
	EffectiveDailyCap  int
	SentToday          int
	MinIntervalSeconds int
	ToEmail            string
	Vars               ContactVars
	Subject            string
	ThreadSubject      string
	BodyText           string
	BodyHTML           string
	UnsubURL           string
	InReplyTo          string
	References         string
	// TrackingEnabled mirrors the campaign's tracking_enabled column: when true
	// and BodyHTML is non-empty, the worker rewrites links and appends an open
	// pixel before sending.
	TrackingEnabled bool
	// Schedule is the campaign's sending window, carried on the job so
	// MarkStepSent can place the NEXT step's due time inside it without a second
	// round trip. Compiled (and therefore validated) at job-build time, before the
	// send happens, so a corrupted schedule stops the send rather than being
	// discovered after the message is already out.
	Schedule  cadence.Schedule
	FromEmail string
	FromName  string
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

// NotYetDue reports whether this step's enrollment is scheduled for a moment
// still in the future — i.e. an advance task queued for an earlier due time is
// trying to send early. Pure and clock-injected so both the claim (which
// enforces it) and the worker (which reschedules on it) read one rule.
//
// A zero NotDueUntil means "no recorded due time", which is never "not due":
// the pre-taxonomy behaviour is to send.
func (j StepSendJob) NotYetDue(now time.Time) bool {
	return !j.NotDueUntil.IsZero() && now.Before(j.NotDueUntil)
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
// has no enrollment — the legacy direct-send path. MailboxID/CampaignID/
// ContactID/MessageID are the send row's own columns (all NOT NULL on sends),
// carried so the inbox poller can store the matched reply against the right
// mailbox/campaign/contact and anchor the thread on the send's own outbound
// Message-ID (MessageID — the reply's In-Reply-To/References target, i.e. the
// thread's root_message_id) without a second lookup.
type SendRef struct {
	SendID       string
	EnrollmentID string
	ContactEmail string
	MailboxID    string
	CampaignID   string
	ContactID    string
	MessageID    string
}

// SenderTransport is one resolved mailbox's send identity plus its decrypted
// credential, for a control-plane-triggered ad hoc send with no existing
// sends/enrollment row (currently: the testsend:send task only). Mirrors the
// transport fields on StepSendJob/WarmupSendJob rather than embedding
// platform/mail's OutboundJob, so this package stays free of that dependency
// like every other job type here; the worker maps it onto mail.OutboundJob.
//
// It is NOT a coreapi.Client method: resolving it is consumed through the
// narrow, consumer-defined internal/worker/testsend.Core interface (the same
// "avoid widening Client's ~40-method surface for one call site" trade as
// BreakerResult), satisfied by the in-process client via type assertion.
type SenderTransport struct {
	FromEmail string
	FromName  string
	// Provider selects the send transport ("smtp" | "gmail" | "m365").
	// AccessToken is the decrypted OAuth bearer for gmail/m365 (nil for smtp);
	// the worker zeroizes it after use, like every other job's credential.
	Provider       string
	AccessToken    []byte
	SMTPHost       string
	SMTPPort       int
	SMTPUsername   string
	SMTPPassword   []byte
	AllowPlaintext bool
}

// TestSendContent is one test-send's raw (unrendered) step content plus the
// preview personalization vars: the campaign list's first contact's
// first_name/company, or the synthetic fallback (first_name=Alex,
// company=Acme) substituted by the loader when the list has no contact yet --
// so the worker never has to know that policy. Rendering ({{first_name}}/
// {{company}} substitution, HTML-escaped in the HTML body) happens in
// internal/worker/testsend, through the SAME personalize package every real
// send renders through.
type TestSendContent struct {
	Subject   string
	BodyText  string
	BodyHTML  string
	FirstName string
	Company   string
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
	// The LEASE this send was decided under. ClaimWarmupSend refuses the send if
	// the sender's lane has moved, the policy version has moved, or the expiry has
	// passed — so an assignment cannot fire under a decision that no longer holds
	// (reputation design acceptance criterion 7). LeaseExpiresAt is minted by the
	// DATABASE at issue and compared against the DATABASE clock at claim; it never
	// passes through a Go clock.
	IssuedLane          string
	IssuedPolicyVersion string
	LeaseExpiresAt      time.Time
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
// "inbox" | "tabbed" | "spam" | "other"). SourceFolder + MessageID are the receipt
// locator: the provider folder the message was found in (e.g. "INBOX" | "Junk" |
// "SPAM" | "JunkEmail") and its RFC822 Message-ID, persisted so the engage worker
// (C5b) can relocate/rescue/mark-read the exact message. All ids are strings at the
// seam; the impl parses and pins them.
//
// "tabbed" is reported ONLY when a provider positively identified a tab (today:
// Gmail's CATEGORY_* labels). "inbox" keeps meaning "landed in the inbox" and is
// NOT redefined as "primary", because one value cannot mean "primary inbox" on
// Gmail and "inbox, tab unknowable" on IMAP.
type WarmupReceiptInput struct {
	WorkspaceID      string
	WarmupSendID     string
	RecipientMailbox string
	Placement        string
	SourceFolder     string
	MessageID        string
	// TabCapable reports whether the READING PATH that produced this observation
	// could have identified a tab at all — true for the Gmail API reader, false for
	// IMAP (no such concept) and Graph (inferenceClassification is a relevance
	// guess, not a delivery category).
	//
	// It is a property of the reader, so it is recorded with the evidence rather than
	// derived from mailboxes.provider afterwards: a mailbox migrated between
	// providers would otherwise make historical observations claim a capability the
	// reader never had. It is also the tabbed rate's denominator, which is why
	// pooling non-capable observations into it would dilute the rate toward zero and
	// make an untested pool read clean.
	//
	// The reader here is the RECIPIENT's poller, while the placement it produces is
	// attributed to the SENDER — so the aggregated rate is gated on what a mailbox's
	// PARTNERS could see, not on its own provider. A Gmail sender whose warmup peers
	// are all IMAP therefore has no measurable tabbed rate, which is the honest
	// answer and not a statement about its own mailbox.
	TabCapable bool
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
	// EngageAfter is the humanized delay before the recipient acts — heavy-tailed and
	// deterministic in the receipt id, but drawn from a distribution matched to what
	// the engagement will DO. A passive-only engagement (DoReply false) uses the short
	// read dwell, on the order of a minute or two. An engagement that replies uses the
	// much longer reply latency (tens of minutes to hours) kept inside waking hours,
	// because one asynq task delivers the whole engagement as one human sitting.
	EngageAfter time.Duration
}

// WarmupEngageJob is everything the warmup:engage worker needs to act on one
// received warmup message. It carries the RECIPIENT's own decrypted transport
// (engagement acts on the recipient's own mailbox): the IMAP fields drive the
// smtp/imap engager's mark-read/rescue, AccessToken the Gmail engager, and the SMTP
// fields the reply send. AccessToken / SMTPPassword are []byte so the worker
// zeroizes them after use, like WarmupSendJob. The Do* flags are recomputed
// deterministically from the receipt.
type WarmupEngageJob struct {
	// Provider selects both the engage transport (IMAP-modify for smtp, API-modify
	// for gmail, unsupported for m365) and the reply-send transport ("smtp" |
	// "gmail" | "m365"). AccessToken is the decrypted OAuth bearer for API providers
	// (nil for smtp), used for BOTH the Gmail modify calls and the reply send;
	// zeroized after use like SMTPPassword.
	Provider    string
	AccessToken []byte
	// IMAPHost/Port/Username are the recipient's IMAP-MODIFY transport (mark-read /
	// rescue) for smtp mailboxes; empty for API providers.
	IMAPHost     string
	IMAPPort     int
	IMAPUsername string
	// SMTPHost/Port/Username are the recipient's SMTP transport for the reply send;
	// empty for API providers.
	SMTPHost     string
	SMTPPort     int
	SMTPUsername string
	// SMTPPassword is the recipient's single decrypted mailbox secret. A mailbox uses
	// ONE password for both IMAP and SMTP, so the engage worker feeds this same slice
	// to the IMAP-modify dial and the reply send; zeroized once after use.
	SMTPPassword []byte
	// AllowPlaintext is the recipient mailbox's cleartext opt-out; threaded into the
	// reply's outbound job so it applies the SAME TLS policy the connect-test validated.
	AllowPlaintext bool
	// SourceFolder is the ACTUAL provider folder the message was found in (INBOX / a
	// junk folder name), stored on the C5a receipt; the engager locates + rescues the
	// message by it. MessageID is the received message's RFC822 Message-ID, also from
	// the receipt, used to locate the exact message. Both are attacker-influenceable
	// inbound content — the engager passes them as literal protocol arguments.
	SourceFolder string
	MessageID    string
	// DoRescue / DoMarkRead / DoReply mirror the plan, recomputed from the receipt.
	DoRescue   bool
	DoMarkRead bool
	DoReply    bool
	// ReplySend is the fully-formed NEW warmup send FROM the recipient (its own
	// deterministic SendID, threading headers, and signed X-Inroad-Warmup token),
	// populated ONLY when DoReply is true AND the thread still has a turn to send. The
	// engage worker claims → sends → finalizes it exactly like a tick send. Its
	// transport fields reuse the same decrypted secret slices above (zeroized once).
	ReplySend WarmupSendJob
}
