package inbox

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// ErrValidation is returned when a caller's input is malformed — a message
// direction outside the migration's CHECK constraint, a missing workspace or
// mailbox id, or a keyset cursor with only half its pair set.
var ErrValidation = errors.New("inbox: invalid input")

// errHalfSetCursor is the one message both Service.ListThreads and the
// handler's own query-param parsing use for a half-set keyset cursor, so a
// caller sees the SAME clear explanation regardless of which layer catches
// it (the handler catches it first at the HTTP boundary; this Service check
// is the belt-and-braces backstop for any other caller of ListThreads).
const errHalfSetCursor = "before_last_message_at and before_id must be set together"

// Service implements the unified-inbox use cases. It depends on the Store
// interface (never the concrete PgStore), so it is unit-testable against a
// fake without a database.
//
// suppression/replyEnq back Reply (see reply.go) and drafter backs DraftReply
// (see draft.go). All three are OPTIONAL (nil-safe — see
// checkRecipientNotSuppressed, Reply's own nil check, and DraftReply's),
// injected via ServiceOption rather than added as NewService parameters, so
// every existing caller of NewService(store) — and every existing unit test —
// keeps compiling unchanged. Mirrors campaign.Service's identical shape.
type Service struct {
	store       Store
	suppression SuppressionChecker
	replyEnq    ReplyEnqueuer
	drafter     ReplyDrafter
	// snoozes backs Snooze/Unsnooze/GetSnooze (see snooze.go). Optional like
	// the rest: nil when a caller never snoozes, which keeps every existing
	// NewService(store) call site compiling. The snooze methods are the only
	// ones that touch it, and they are unreachable without a route mounted.
	snoozes SnoozeStore
	// labels backs the label use cases (see label.go). Optional on the same
	// terms as snoozes.
	labels LabelStore
	// pending/pendingEnq back the deferred-send use cases (see pending.go).
	// Optional: without them the immediate Reply path is unaffected.
	pending    PendingReplyStore
	pendingEnq PendingReplyEnqueuer
	// compose/composeEnq back writing a NEW email (see compose.go). Optional on
	// the same terms as everything else here.
	compose    ComposeStore
	composeEnq ComposeEnqueuer
	// clock is the Service's source of "now", injected so time-bounded rules
	// (the snooze horizon) are testable at a fixed instant rather than
	// reaching for the process clock. nil means time.Now — see now().
	clock func() time.Time
}

// NewService builds a Service over store, applying any ServiceOptions (see
// WithSuppressionChecker / WithReplyEnqueuer in reply.go).
func NewService(store Store, opts ...ServiceOption) *Service {
	s := &Service{store: store}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// WithSnoozeStore supplies the snooze persistence. Without it, the snooze use
// cases have nowhere to write; cmd/inroad always passes one (PgStore
// implements both interfaces).
func WithSnoozeStore(snoozes SnoozeStore) ServiceOption {
	return func(s *Service) { s.snoozes = snoozes }
}

// WithLabelStore supplies the label persistence. PgStore implements it
// alongside Store and SnoozeStore.
func WithLabelStore(labels LabelStore) ServiceOption {
	return func(s *Service) { s.labels = labels }
}

// WithPendingReplyStore supplies the deferred-send persistence. PgStore
// implements it alongside the other three store interfaces.
func WithPendingReplyStore(pending PendingReplyStore) ServiceOption {
	return func(s *Service) { s.pending = pending }
}

// WithPendingReplyEnqueuer supplies the delayed-task publisher for deferred
// sends. Without it a scheduled row is still created (and a sweeper would pick
// it up), but nothing is enqueued — which is why cmd/inroad always passes one.
func WithPendingReplyEnqueuer(enq PendingReplyEnqueuer) ServiceOption {
	return func(s *Service) { s.pendingEnq = enq }
}

// WithComposeStore supplies the compose persistence (drafts + queued composed
// emails). PgStore implements it alongside the other stores.
func WithComposeStore(compose ComposeStore) ServiceOption {
	return func(s *Service) { s.compose = compose }
}

// WithComposeEnqueuer supplies the delayed-task publisher for composed emails.
func WithComposeEnqueuer(enq ComposeEnqueuer) ServiceOption {
	return func(s *Service) { s.composeEnq = enq }
}

// WithClock overrides the Service's source of "now". Test-facing; production
// leaves it unset and gets time.Now.
func WithClock(clock func() time.Time) ServiceOption {
	return func(s *Service) { s.clock = clock }
}

// now reads the injected clock, defaulting to time.Now so an option-free
// Service behaves exactly as it did before the clock existed.
func (s *Service) now() time.Time {
	if s.clock == nil {
		return time.Now()
	}
	return s.clock()
}

// RecordReply carries one matched (or legacy) reply: the thread it belongs to
// (created or refreshed) and the message itself.
type RecordReplyInput struct {
	WorkspaceID    uuid.UUID
	MailboxID      uuid.UUID
	CampaignID     *uuid.UUID
	ContactID      *uuid.UUID
	RootMessageID  string
	Subject        string
	LastReplyClass string
	Message        InsertMessageInput
}

func (in RecordReplyInput) validate() error {
	if in.WorkspaceID == uuid.Nil {
		return fmt.Errorf("%w: workspace_id is required", ErrValidation)
	}
	if in.MailboxID == uuid.Nil {
		return fmt.Errorf("%w: mailbox_id is required", ErrValidation)
	}
	if in.Message.Direction != "inbound" && in.Message.Direction != "outbound" {
		return fmt.Errorf("%w: message direction must be inbound or outbound", ErrValidation)
	}
	return nil
}

// RecordReply upserts the thread for RootMessageID (creating it on the first
// reply, refreshing last_reply_class/last_message_at/unread on every later
// one — see the migration's partial unique index and the sqlc query it
// backs) and appends the message to it, atomically: a thin pass-through to
// Store.RecordReply, which is the ONE call that does both writes in a single
// transaction. Service does not call UpsertThread/InsertMessage separately —
// doing so here would reopen the non-atomic gap RecordReply exists to close
// (a dropped connection between the two leaving a thread whose
// last_reply_class/last_message_at/unread reflect a reply with no
// corresponding message row).
func (s *Service) RecordReply(ctx context.Context, in RecordReplyInput) (Thread, error) {
	if err := in.validate(); err != nil {
		return Thread{}, err
	}
	return s.store.RecordReply(ctx, UpsertThreadInput{
		WorkspaceID:    in.WorkspaceID,
		MailboxID:      in.MailboxID,
		CampaignID:     in.CampaignID,
		ContactID:      in.ContactID,
		RootMessageID:  in.RootMessageID,
		Subject:        in.Subject,
		LastReplyClass: in.LastReplyClass,
	}, in.Message)
}

// ListThreads returns one page of the workspace's threads. A malformed
// keyset (exactly one of BeforeLastMessageAt/BeforeID set) is rejected rather
// than silently treated as the first page, which would hide a client bug as
// a lost place in the list.
func (s *Service) ListThreads(ctx context.Context, workspaceID uuid.UUID, filter ListFilter) (ThreadPage, error) {
	if (filter.BeforeLastMessageAt == nil) != (filter.BeforeID == nil) {
		return ThreadPage{}, fmt.Errorf("%w: %s", ErrValidation, errHalfSetCursor)
	}
	// "Hide the snoozed" and "show only the snoozed" together can only ever
	// match nothing. Rejected rather than served as an empty page, which would
	// present a caller's bug as a legitimately empty inbox.
	if filter.SnoozeHidden && filter.SnoozedOnly {
		return ThreadPage{}, fmt.Errorf("%w: snooze_hidden and snoozed_only are mutually exclusive", ErrValidation)
	}
	page, err := s.store.ListThreads(ctx, workspaceID, filter)
	if err != nil {
		return ThreadPage{}, err
	}
	if err := s.attachLabels(ctx, workspaceID, page.Items); err != nil {
		return ThreadPage{}, err
	}
	return page, nil
}

// attachLabels fills in every listed thread's labels with ONE query for the
// whole page. A query per row would be a textbook N+1 on the inbox's hottest
// read; a JOIN in ListInboxThreads would instead multiply the page's rows by
// each thread's label count and break the keyset's LIMIT.
//
// No-op when no label store is configured, so a Service built without one
// lists threads exactly as it did before labels existed.
func (s *Service) attachLabels(ctx context.Context, workspaceID uuid.UUID, threads []Thread) error {
	if s.labels == nil || len(threads) == 0 {
		return nil
	}
	ids := make([]uuid.UUID, len(threads))
	for i, t := range threads {
		ids[i] = t.ID
	}
	byThread, err := s.labels.LabelsForThreads(ctx, workspaceID, ids)
	if err != nil {
		return err
	}
	for i := range threads {
		threads[i].Labels = byThread[threads[i].ID]
	}
	return nil
}

// GetThread returns one thread with its full message history, scoped to
// workspaceID. Returns ErrNotFound for an unknown id or one belonging to
// another workspace — the two are indistinguishable by design (Invariant:
// never leak a foreign row's existence).
func (s *Service) GetThread(ctx context.Context, workspaceID, id uuid.UUID) (ThreadDetail, error) {
	detail, err := s.store.GetThread(ctx, workspaceID, id)
	if err != nil {
		return ThreadDetail{}, err
	}
	// Resolved here rather than joined in the store's query because whether a
	// snooze is still IN FORCE depends on the Service's clock — see
	// activeSnoozeFor.
	//
	// A failure here IS propagated rather than degraded to nil: reporting "not
	// snoozed" when we could not determine it would show the operator a Snooze
	// button for a thread that is already snoozed, and the resulting re-snooze
	// would silently overwrite the moment a colleague chose. A 500 they can
	// retry is better than a confident wrong answer.
	snooze, err := s.activeSnoozeFor(ctx, workspaceID, id)
	if err != nil {
		return ThreadDetail{}, err
	}
	detail.Snooze = snooze

	labels, err := s.labelsForThread(ctx, workspaceID, id)
	if err != nil {
		return ThreadDetail{}, err
	}
	detail.Labels = labels

	pending, err := s.pendingReplyForThread(ctx, workspaceID, id)
	if err != nil {
		return ThreadDetail{}, err
	}
	detail.PendingReply = pending

	// Mirrored onto the embedded Thread as well, so a caller holding either the
	// detail or its Thread sees the same labels rather than having to know which
	// field the list path populates.
	detail.Thread.Labels = labels
	return detail, nil
}

// SetUnread flips a thread's read state, scoped to workspaceID.
func (s *Service) SetUnread(ctx context.Context, workspaceID, id uuid.UUID, unread bool) error {
	return s.store.SetUnread(ctx, workspaceID, id, unread)
}
