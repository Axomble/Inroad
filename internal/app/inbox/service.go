package inbox

import (
	"context"
	"errors"
	"fmt"

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
	return s.store.ListThreads(ctx, workspaceID, filter)
}

// GetThread returns one thread with its full message history, scoped to
// workspaceID. Returns ErrNotFound for an unknown id or one belonging to
// another workspace — the two are indistinguishable by design (Invariant:
// never leak a foreign row's existence).
func (s *Service) GetThread(ctx context.Context, workspaceID, id uuid.UUID) (ThreadDetail, error) {
	return s.store.GetThread(ctx, workspaceID, id)
}

// SetUnread flips a thread's read state, scoped to workspaceID.
func (s *Service) SetUnread(ctx context.Context, workspaceID, id uuid.UUID, unread bool) error {
	return s.store.SetUnread(ctx, workspaceID, id, unread)
}
