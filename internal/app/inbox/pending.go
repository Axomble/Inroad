package inbox

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/inroad/inroad/internal/platform/db/gen"
)

// Pending-reply statuses. Strings rather than an enum type because they are the
// database's own CHECK values and travel to the wire unchanged; a Go enum would
// add a mapping layer in both directions for no gain.
const (
	// PendingStatusScheduled is waiting for send_after to arrive. The only
	// cancellable state.
	PendingStatusScheduled = "scheduled"
	// PendingStatusSending is claimed by a worker — past the point of no return,
	// because the SMTP conversation may already be open.
	PendingStatusSending = "sending"
	PendingStatusSent    = "sent"
	PendingStatusFailed  = "failed"
	// PendingStatusCancelled means the operator undid it before it went out.
	PendingStatusCancelled = "cancelled"
)

// MaxScheduleHorizon bounds how far ahead a reply may be scheduled. Longer than
// the snooze horizon on purpose: a snooze is a reminder to yourself, while this
// is mail that will actually leave — a year-out send is far more likely to be a
// mistake than an intent, and the body would be stale long before it landed.
const MaxScheduleHorizon = 30 * 24 * time.Hour

// MaxOutstandingPendingSends caps how many replies one workspace may have
// queued at once.
//
// Without it an inbox:send credential can accumulate rows and long-dated asynq
// tasks up to the 30-day horizon — each carrying a body up to 100KB — and the
// partial indexes that serve the outbox are precisely the ones that grow. The
// label taxonomy already guards itself this way (MaxLabelsPerWorkspace); this
// table is a bigger target and had no ceiling at all.
//
// Generous enough that no honest operator meets it: it bounds abuse, not use.
const MaxOutstandingPendingSends = 500

// PendingReplyLeaseSeconds is how long a claimed send may sit in 'sending'
// before another worker may reclaim it. Comfortably longer than the queue's own
// 2-minute per-attempt send timeout, so a slow-but-alive SMTP conversation is
// never stolen from underneath itself.
const PendingReplyLeaseSeconds = 300

// DefaultUndoSendSeconds mirrors the column default, for a workspace that has
// never configured one.
const DefaultUndoSendSeconds = 10

// MaxUndoSendSeconds mirrors the column's CHECK.
const MaxUndoSendSeconds = 120

var (
	// ErrScheduleInPast is returned for a send_at that has already passed.
	ErrScheduleInPast = errors.New("inbox: send_at must be in the future")
	// ErrScheduleTooFar is returned for a send_at beyond MaxScheduleHorizon.
	ErrScheduleTooFar = errors.New("inbox: send_at is further ahead than 30 days")
	// ErrPendingNotCancellable is returned when a reply can no longer be
	// undone — already sending, already sent, or already cancelled. Its own
	// sentinel because the UI must say WHY rather than showing a failed click.
	ErrPendingNotCancellable = errors.New("inbox: this reply is already on its way and can no longer be cancelled")
	// ErrPendingNotClaimable is the EXECUTION plane's counterpart: the guarded
	// claim matched no row. Covers cancelled, already sent, not yet due, and
	// held by another worker's live lease — deliberately one error, because the
	// worker's response to all four is identical (stop, do not retry), and
	// splitting them would invite a caller to treat one as retryable, which is
	// the mistake that double-sends mail.
	ErrPendingNotClaimable = errors.New("inbox: pending reply is not claimable")
)

// PendingReply is one manual reply waiting to go out.
type PendingReply struct {
	ID          uuid.UUID
	WorkspaceID uuid.UUID
	ThreadID    uuid.UUID
	BodyText    string
	Status      string
	SendAfter   time.Time
	SentAt      *time.Time
	MessageID   string
	LastError   string
	CreatedBy   *uuid.UUID
	CreatedAt   time.Time
	// ThreadSubject/ContactEmail are joined for the outbox display and are ""
	// on a row read without the join (GetPendingReply).
	ThreadSubject string
	ContactEmail  string
}

// Cancellable reports whether an undo would still succeed. Mirrors the SQL
// guard on CancelInboxPendingReply, so the UI can hide the control rather than
// offering one that will fail.
func (p PendingReply) Cancellable() bool { return p.Status == PendingStatusScheduled }

// PendingReplyStore is the deferred-send half of this domain's persistence.
type PendingReplyStore interface {
	CreatePendingReply(ctx context.Context, in CreatePendingReplyInput) (PendingReply, error)
	GetPendingReply(ctx context.Context, workspaceID, id uuid.UUID) (PendingReply, error)
	ListPendingReplies(ctx context.Context, workspaceID uuid.UUID, limit int32) ([]PendingReply, error)
	CountPendingReplies(ctx context.Context, workspaceID uuid.UUID) (int64, error)
	PendingReplyForThread(ctx context.Context, workspaceID, threadID uuid.UUID) (PendingReply, error)
	CancelPendingReply(ctx context.Context, workspaceID, id uuid.UUID) error
	// UndoWindow returns the workspace's configured undo delay.
	UndoWindow(ctx context.Context, workspaceID uuid.UUID) (time.Duration, error)
	SetUndoWindow(ctx context.Context, workspaceID uuid.UUID, seconds int32) error
}

// PendingReplyEnqueuer hands a queued send to the execution plane. Its
// arguments are the whole contract, and the absence in them is the point: two
// ids and an instant, never the body. The row is the single source of truth for
// what to send, so the task is only a pointer to it — see queueReply.
type PendingReplyEnqueuer interface {
	EnqueuePendingInboxReply(pendingID, workspaceID string, sendAfter time.Time) error
}

// CreatePendingReplyInput carries one reply to schedule.
type CreatePendingReplyInput struct {
	WorkspaceID uuid.UUID
	ThreadID    uuid.UUID
	BodyText    string
	SendAfter   time.Time
	CreatedBy   *uuid.UUID
}

// DefaultPendingReplyPageLimit bounds the outbox list when a caller asks for no
// size; MaxPendingReplyPageLimit is the ceiling. The outbox is a transient
// working set, so both are far smaller than the thread list's.
const (
	DefaultPendingReplyPageLimit = int32(50)
	MaxPendingReplyPageLimit     = int32(200)
)

// ScheduleReply defers a manual reply instead of sending it now.
//
// sendAt nil means "as soon as the undo window allows" — the ordinary Send
// button. A non-nil sendAt is an explicit schedule and is bounded.
//
// The validation, row write and enqueue are queueReply's, shared verbatim with
// the immediate path: the two differ only in the send_after they resolve, and
// letting them differ in anything else is how "accepted for scheduling but
// rejected at send time" gets reintroduced.
func (s *Service) ScheduleReply(
	ctx context.Context,
	workspaceID, threadID uuid.UUID,
	bodyText string,
	sendAt *time.Time,
	createdBy *uuid.UUID,
) (PendingReply, error) {
	if s.pending == nil {
		return PendingReply{}, fmt.Errorf("%w: deferred replies are not configured", ErrValidation)
	}
	// Resolved first because it reads the workspace's undo window, which
	// queueReply has no business knowing about; a bad explicit instant is the
	// caller's own input and is worth reporting ahead of anything else.
	sendAfter, err := s.resolveSendAfter(ctx, workspaceID, sendAt)
	if err != nil {
		return PendingReply{}, err
	}
	// Marking read is deliberately NOT done here, unlike the immediate Reply
	// path. An undone send should not leave the thread read: the operator
	// changed their mind, and the thread is still theirs to deal with. It moves
	// to the moment the send actually lands (RecordOutboundReply's caller).
	return s.queueReply(ctx, workspaceID, threadID, bodyText, sendAfter, createdBy)
}

// queueReply is the ONE path a manual reply takes into the queue, shared by the
// immediate (Reply) and deferred (ScheduleReply) entry points. Everything
// between validating the body and handing the queue a row id lives here, so the
// two cannot drift: a body accepted for scheduling is one the immediate path
// would also have accepted, and neither can acquire a check the other lacks.
//
// The body is written to an inbox_pending_replies row and the queue is handed
// that row's ID. That is the shape, and it is load-bearing rather than
// incidental: a task payload is stored byte-for-byte in task_dead_letters on
// terminal failure and served by GET /dead-letters under campaigns:read — an
// OAuth-grantable scope, while inbox:read deliberately is not, because reply
// bodies are correspondence. A body in a payload is therefore a body in a
// response to a scope structurally denied it.
//
// Every validation runs BEFORE the row is created: rejecting at queue time is
// far better than accepting and silently dropping the send later when the
// worker discovers the problem, and a row written then abandoned would sit in
// the operator's outbox offering an Undo for mail that is going nowhere.
//
// The suppression check is re-run by the worker as well. That is not redundant:
// with any delay at all the race it guards is real, so a contact who
// unsubscribes between queueing and sending is a case rather than a theory.
func (s *Service) queueReply(
	ctx context.Context,
	workspaceID, threadID uuid.UUID,
	bodyText string,
	sendAfter time.Time,
	createdBy *uuid.UUID,
) (PendingReply, error) {
	// ScheduleReply checks this first (it reads the undo window before getting
	// here), so in practice this guard is the immediate path's. There is no safe
	// "unwired" default for actually sending mail: without somewhere to put the
	// body, failing is the only honest answer.
	if s.pending == nil {
		return PendingReply{}, errors.New("inbox: reply sending is not configured")
	}
	if err := validateReplyBody(bodyText); err != nil {
		return PendingReply{}, err
	}

	detail, err := s.store.GetThread(ctx, workspaceID, threadID)
	if err != nil {
		return PendingReply{}, err
	}
	latest, ok := latestInboundMessage(detail.Messages)
	if !ok {
		return PendingReply{}, ErrNoInboundMessage
	}
	if err := s.checkRecipientNotSuppressed(ctx, workspaceID, latest.FromEmail); err != nil {
		return PendingReply{}, err
	}

	if err := s.checkOutstandingSendCapacity(ctx, workspaceID); err != nil {
		return PendingReply{}, err
	}

	saved, err := s.pending.CreatePendingReply(ctx, CreatePendingReplyInput{
		WorkspaceID: workspaceID,
		ThreadID:    threadID,
		BodyText:    bodyText,
		SendAfter:   sendAfter,
		CreatedBy:   createdBy,
	})
	if err != nil {
		return PendingReply{}, err
	}

	// Enqueued AFTER the row exists, and the row — not the queue — is the
	// authority: a task that fires early finds send_after in the future and
	// declines to claim.
	//
	// An enqueue failure is RETURNED rather than swallowed, which leaves a
	// 'scheduled' row nothing will ever pick up. That is the honest tradeoff
	// today: reporting failure to the operator (who still has their text, and
	// can retry) beats reporting success for a reply that will never leave.
	// A periodic sweeper over `scheduled` rows past their send_after would make
	// the row self-healing and is the obvious next increment — it does not exist
	// yet, so this must not pretend the row is safe on its own.
	if s.pendingEnq != nil {
		if err := s.pendingEnq.EnqueuePendingInboxReply(saved.ID.String(), workspaceID.String(), sendAfter); err != nil {
			// The row exists but nothing will ever claim it. Marked failed
			// before returning, so the outbox tells the truth: leaving it
			// `scheduled` would show the operator a reply that looks in flight,
			// counts toward their outbox, and offers an Undo — for mail that is
			// never going to leave. Wrong in the direction that matters.
			if claimer, ok := s.pending.(PendingReplyClaimer); ok {
				if failErr := claimer.FailPendingReply(ctx, workspaceID, saved.ID,
					"could not be queued for delivery — please send it again"); failErr != nil {
					slog.ErrorContext(ctx, "inbox_pending_reply_orphan_mark_failed",
						"pending_id", saved.ID, "err", failErr)
				}
			}
			return PendingReply{}, fmt.Errorf("enqueue pending reply: %w", err)
		}
	}

	return saved, nil
}

// checkOutstandingSendCapacity refuses a new queued send once the workspace is
// at MaxOutstandingPendingSends. Counts only what is still waiting, so a busy
// workspace that actually delivers its mail is never blocked.
func (s *Service) checkOutstandingSendCapacity(ctx context.Context, workspaceID uuid.UUID) error {
	outstanding, err := s.pending.CountPendingReplies(ctx, workspaceID)
	if err != nil {
		return err
	}
	if outstanding >= MaxOutstandingPendingSends {
		return fmt.Errorf("%w: this workspace already has %d replies queued for delivery",
			ErrValidation, outstanding)
	}
	return nil
}

// immediateSendClaimSlack backdates the send_after of a reply that is due NOW,
// so the row is claimable the instant its task fires.
//
// The two clocks do not agree. send_after is stamped from the APP clock
// (Service.now), while ClaimInboxPendingReply guards on `send_after <= now()`
// evaluated on the DATABASE clock (queries/inbox.sql). An instant equal to "now"
// therefore loses to any forward skew between them: the task fires, the guarded
// UPDATE matches no row, ErrPendingNotClaimable is not retryable, and the row
// sits 'scheduled' forever — there is no sweeper over stranded rows (see
// queueReply). The operator is told the reply was sent and it never leaves.
//
// Thirty seconds is far beyond any sane NTP-synced skew and costs nothing: the
// row is already due, so the only effect is that the claim's guard is satisfied
// rather than raced. It is applied ONLY where the resolved instant is not in the
// future — a genuinely scheduled send keeps its exact moment, because the undo
// window is a promise to the operator and nothing may shorten it. The SQL guard
// itself is deliberately NOT relaxed: it is what stops a task that fires early
// from delivering a scheduled reply ahead of time.
const immediateSendClaimSlack = 30 * time.Second

// immediateSendAfter is the send_after of a reply that should go out at once.
func immediateSendAfter(now time.Time) time.Time { return now.Add(-immediateSendClaimSlack) }

// resolveSendAfter turns an optional explicit instant into the moment the send
// should leave, bounding an explicit one and applying the workspace's undo
// window otherwise.
func (s *Service) resolveSendAfter(ctx context.Context, workspaceID uuid.UUID, sendAt *time.Time) (time.Time, error) {
	now := s.now()
	if sendAt == nil {
		window, err := s.pending.UndoWindow(ctx, workspaceID)
		if err != nil {
			return time.Time{}, err
		}
		resolved := now.Add(window)
		if !resolved.After(now) {
			// A workspace that opted out of undo (window 0) wants this gone now,
			// which means the row must be claimable now — see
			// immediateSendClaimSlack for why "now" is not good enough.
			return immediateSendAfter(now), nil
		}
		return resolved, nil
	}
	if !sendAt.After(now) {
		return time.Time{}, ErrScheduleInPast
	}
	if sendAt.After(now.Add(MaxScheduleHorizon)) {
		return time.Time{}, ErrScheduleTooFar
	}
	return *sendAt, nil
}

// CancelPendingReply undoes a scheduled reply.
//
// Returns ErrPendingNotCancellable — not ErrNotFound — when the row exists but
// has moved past 'scheduled', so the UI can explain that the mail is already on
// its way rather than implying the reply never existed.
func (s *Service) CancelPendingReply(ctx context.Context, workspaceID, id uuid.UUID) error {
	if s.pending == nil {
		return fmt.Errorf("%w: deferred replies are not configured", ErrValidation)
	}
	err := s.pending.CancelPendingReply(ctx, workspaceID, id)
	if !errors.Is(err, ErrNotFound) {
		return err
	}
	// The UPDATE matched nothing: either no such row in this workspace, or a row
	// past the cancellable point. Only a read can tell those apart, and the
	// distinction is worth a second query because the two need different copy.
	if existing, getErr := s.pending.GetPendingReply(ctx, workspaceID, id); getErr == nil && !existing.Cancellable() {
		return ErrPendingNotCancellable
	}
	return ErrNotFound
}

// ListPendingReplies returns the workspace's outbox — everything still waiting.
func (s *Service) ListPendingReplies(ctx context.Context, workspaceID uuid.UUID, limit int32) ([]PendingReply, error) {
	if s.pending == nil {
		return nil, nil
	}
	return s.pending.ListPendingReplies(ctx, workspaceID, NormalizePendingLimit(limit))
}

// NormalizePendingLimit clamps an outbox page size into bounds.
func NormalizePendingLimit(requested int32) int32 {
	switch {
	case requested <= 0:
		return DefaultPendingReplyPageLimit
	case requested > MaxPendingReplyPageLimit:
		return MaxPendingReplyPageLimit
	default:
		return requested
	}
}

// pendingReplyForThread resolves a thread's in-flight reply for display,
// collapsing "none" and "no store configured" to nil.
func (s *Service) pendingReplyForThread(ctx context.Context, workspaceID, threadID uuid.UUID) (*PendingReply, error) {
	if s.pending == nil {
		return nil, nil
	}
	p, err := s.pending.PendingReplyForThread(ctx, workspaceID, threadID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &p, nil
}

// UndoWindow reports the workspace's configured undo delay, for the settings UI.
func (s *Service) UndoWindow(ctx context.Context, workspaceID uuid.UUID) (time.Duration, error) {
	if s.pending == nil {
		return 0, nil
	}
	return s.pending.UndoWindow(ctx, workspaceID)
}

// SetUndoWindow configures the workspace's undo delay.
func (s *Service) SetUndoWindow(ctx context.Context, workspaceID uuid.UUID, seconds int32) error {
	if s.pending == nil {
		return fmt.Errorf("%w: deferred replies are not configured", ErrValidation)
	}
	if seconds < 0 || seconds > MaxUndoSendSeconds {
		return fmt.Errorf("%w: undo_send_seconds must be between 0 and %d", ErrValidation, MaxUndoSendSeconds)
	}
	return s.pending.SetUndoWindow(ctx, workspaceID, seconds)
}

// ClaimPendingReply transitions a due reply to 'sending' for exactly one
// worker. Returns ErrPendingNotClaimable when the guarded UPDATE matched no row
// — cancelled, already sent, not yet due, or held by a live lease.
//
// Execution-plane entry point (called through coreapi), not an HTTP use case:
// there is no route for it, and none should exist.
func (s *Service) ClaimPendingReply(ctx context.Context, workspaceID, id uuid.UUID) error {
	if s.pending == nil {
		return fmt.Errorf("%w: deferred replies are not configured", ErrValidation)
	}
	claimer, ok := s.pending.(PendingReplyClaimer)
	if !ok {
		return fmt.Errorf("%w: this pending-reply store cannot claim", ErrValidation)
	}
	return claimer.ClaimPendingReply(ctx, workspaceID, id)
}

// MarkPendingReplySent completes a claimed reply.
func (s *Service) MarkPendingReplySent(ctx context.Context, workspaceID, id uuid.UUID, messageID string) error {
	claimer, ok := s.pending.(PendingReplyClaimer)
	if !ok {
		return fmt.Errorf("%w: this pending-reply store cannot claim", ErrValidation)
	}
	return claimer.MarkPendingReplySent(ctx, workspaceID, id, messageID)
}

// ReleasePendingReply returns a claimed reply to 'scheduled' after a transient
// failure, so the next attempt can claim it without waiting out the lease.
func (s *Service) ReleasePendingReply(ctx context.Context, workspaceID, id uuid.UUID, reason string) error {
	claimer, ok := s.pending.(PendingReplyClaimer)
	if !ok {
		return fmt.Errorf("%w: this pending-reply store cannot claim", ErrValidation)
	}
	return claimer.ReleasePendingReply(ctx, workspaceID, id, reason)
}

// FailPendingReply marks a claimed reply permanently failed.
func (s *Service) FailPendingReply(ctx context.Context, workspaceID, id uuid.UUID, reason string) error {
	claimer, ok := s.pending.(PendingReplyClaimer)
	if !ok {
		return fmt.Errorf("%w: this pending-reply store cannot claim", ErrValidation)
	}
	return claimer.FailPendingReply(ctx, workspaceID, id, reason)
}

// GetPendingReply reads one pending reply. Used by the execution plane after
// claiming (to resolve the body) and by CancelPendingReply to tell "no such
// row" from "past the cancellable point".
func (s *Service) GetPendingReply(ctx context.Context, workspaceID, id uuid.UUID) (PendingReply, error) {
	if s.pending == nil {
		return PendingReply{}, ErrNotFound
	}
	return s.pending.GetPendingReply(ctx, workspaceID, id)
}

// PendingReplyClaimer is the delivery half of the pending-reply store, split
// from PendingReplyStore because only the EXECUTION plane needs it: a control-
// plane caller (the HTTP handlers) has no business claiming or completing a
// send, and keeping the two interfaces apart makes that structural rather than
// a matter of discipline. PgStore implements both.
type PendingReplyClaimer interface {
	ClaimPendingReply(ctx context.Context, workspaceID, id uuid.UUID) error
	MarkPendingReplySent(ctx context.Context, workspaceID, id uuid.UUID, messageID string) error
	ReleasePendingReply(ctx context.Context, workspaceID, id uuid.UUID, reason string) error
	FailPendingReply(ctx context.Context, workspaceID, id uuid.UUID, reason string) error
}

// --- PgStore ---

func (s *PgStore) CreatePendingReply(ctx context.Context, in CreatePendingReplyInput) (PendingReply, error) {
	row, err := s.q.CreateInboxPendingReply(ctx, gen.CreateInboxPendingReplyParams{
		WorkspaceID: in.WorkspaceID,
		ThreadID:    in.ThreadID,
		BodyText:    in.BodyText,
		SendAfter:   pgTimestamptzValue(in.SendAfter),
		CreatedBy:   pgUUID(in.CreatedBy),
	})
	if err != nil {
		// The INSERT … SELECT writes zero rows for a thread outside the
		// workspace, which sqlc surfaces as no-rows: a foreign thread reads as
		// "not found", never as a foreign-key error that would confirm it exists.
		return PendingReply{}, mapNotFound(err)
	}
	return pendingFromRow(row), nil
}

func (s *PgStore) GetPendingReply(ctx context.Context, workspaceID, id uuid.UUID) (PendingReply, error) {
	row, err := s.q.GetInboxPendingReply(ctx, gen.GetInboxPendingReplyParams{ID: id, WorkspaceID: workspaceID})
	if err != nil {
		return PendingReply{}, mapNotFound(err)
	}
	return pendingFromRow(row), nil
}

func (s *PgStore) ListPendingReplies(ctx context.Context, workspaceID uuid.UUID, limit int32) ([]PendingReply, error) {
	rows, err := s.q.ListInboxPendingReplies(ctx, gen.ListInboxPendingRepliesParams{
		WorkspaceID: workspaceID, PageLimit: limit,
	})
	if err != nil {
		return nil, err
	}
	out := make([]PendingReply, len(rows))
	for i, r := range rows {
		out[i] = PendingReply{
			ID: r.ID, WorkspaceID: r.WorkspaceID, ThreadID: r.ThreadID, BodyText: r.BodyText,
			Status: r.Status, SendAfter: r.SendAfter.Time, SentAt: timeValue(r.SentAt),
			MessageID: r.MessageID, LastError: r.LastError, CreatedBy: uuidValue(r.CreatedBy),
			CreatedAt: r.CreatedAt.Time, ThreadSubject: r.ThreadSubject, ContactEmail: r.ContactEmail,
		}
	}
	return out, nil
}

func (s *PgStore) CountPendingReplies(ctx context.Context, workspaceID uuid.UUID) (int64, error) {
	return s.q.CountInboxPendingReplies(ctx, workspaceID)
}

func (s *PgStore) PendingReplyForThread(ctx context.Context, workspaceID, threadID uuid.UUID) (PendingReply, error) {
	row, err := s.q.GetPendingReplyForInboxThread(ctx, gen.GetPendingReplyForInboxThreadParams{
		ThreadID: threadID, WorkspaceID: workspaceID,
	})
	if err != nil {
		return PendingReply{}, mapNotFound(err)
	}
	return pendingFromRow(row), nil
}

func (s *PgStore) CancelPendingReply(ctx context.Context, workspaceID, id uuid.UUID) error {
	n, err := s.q.CancelInboxPendingReply(ctx, gen.CancelInboxPendingReplyParams{
		ID: id, WorkspaceID: workspaceID,
	})
	return affected(n, err)
}

func (s *PgStore) UndoWindow(ctx context.Context, workspaceID uuid.UUID) (time.Duration, error) {
	row, err := s.q.GetWorkspaceInboxSettings(ctx, workspaceID)
	if err != nil {
		// No row means the workspace has never configured one — the default,
		// not an error. Matching the column default keeps "unset" and
		// "explicitly set to the default" indistinguishable, as they should be.
		if errors.Is(mapNotFound(err), ErrNotFound) {
			return DefaultUndoSendSeconds * time.Second, nil
		}
		return 0, err
	}
	return time.Duration(row.UndoSendSeconds) * time.Second, nil
}

func (s *PgStore) SetUndoWindow(ctx context.Context, workspaceID uuid.UUID, seconds int32) error {
	_, err := s.q.UpsertWorkspaceInboxSettings(ctx, gen.UpsertWorkspaceInboxSettingsParams{
		WorkspaceID: workspaceID, UndoSendSeconds: seconds,
	})
	return err
}

func pendingFromRow(row gen.InboxPendingReply) PendingReply {
	return PendingReply{
		ID:          row.ID,
		WorkspaceID: row.WorkspaceID,
		ThreadID:    row.ThreadID,
		BodyText:    row.BodyText,
		Status:      row.Status,
		SendAfter:   row.SendAfter.Time,
		SentAt:      timeValue(row.SentAt),
		MessageID:   row.MessageID,
		LastError:   row.LastError,
		CreatedBy:   uuidValue(row.CreatedBy),
		CreatedAt:   row.CreatedAt.Time,
	}
}

// timeValue is pgTimestamptz's inverse for a nullable column: nil when absent.
func timeValue(v pgtype.Timestamptz) *time.Time {
	if !v.Valid {
		return nil
	}
	t := v.Time
	return &t
}

// ClaimPendingReply moves a due row to 'sending'. affected() maps "no rows" to
// ErrNotFound, which this remaps to ErrPendingNotClaimable: the row may well
// exist, it simply is not claimable, and reporting "not found" to a worker
// would send it looking for a bug that is not there.
func (s *PgStore) ClaimPendingReply(ctx context.Context, workspaceID, id uuid.UUID) error {
	n, err := s.q.ClaimInboxPendingReply(ctx, gen.ClaimInboxPendingReplyParams{
		ID: id, WorkspaceID: workspaceID, LeaseSeconds: PendingReplyLeaseSeconds,
	})
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrPendingNotClaimable
	}
	return nil
}

func (s *PgStore) MarkPendingReplySent(ctx context.Context, workspaceID, id uuid.UUID, messageID string) error {
	n, err := s.q.MarkInboxPendingReplySent(ctx, gen.MarkInboxPendingReplySentParams{
		ID: id, WorkspaceID: workspaceID, MessageID: messageID,
	})
	return affected(n, err)
}

func (s *PgStore) ReleasePendingReply(ctx context.Context, workspaceID, id uuid.UUID, reason string) error {
	return s.q.ReleaseInboxPendingReply(ctx, gen.ReleaseInboxPendingReplyParams{
		ID: id, WorkspaceID: workspaceID, LastError: truncateError(reason),
	})
}

func (s *PgStore) FailPendingReply(ctx context.Context, workspaceID, id uuid.UUID, reason string) error {
	return s.q.FailInboxPendingReply(ctx, gen.FailInboxPendingReplyParams{
		ID: id, WorkspaceID: workspaceID, LastError: truncateError(reason),
	})
}

// maxLastErrorLength bounds what a provider's error message can write into the
// row. A misbehaving SMTP server can return a great deal of text, and the
// column is only ever read by a human glancing at the outbox.
const maxLastErrorLength = 500

func truncateError(reason string) string {
	if len(reason) <= maxLastErrorLength {
		return reason
	}
	return reason[:maxLastErrorLength]
}
