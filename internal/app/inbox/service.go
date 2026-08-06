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

// Service implements the unified-inbox use cases. It depends on the Store
// interface (never the concrete PgStore), so it is unit-testable against a
// fake without a database.
type Service struct {
	store Store
}

// NewService builds a Service over store.
func NewService(store Store) *Service { return &Service{store: store} }

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
// backs) and appends the message to it. WorkspaceID always comes from
// in.WorkspaceID, the trusted caller-supplied workspace — never from
// in.Message, whose ThreadID/WorkspaceID this overwrites before it reaches
// the store.
func (s *Service) RecordReply(ctx context.Context, in RecordReplyInput) (Thread, error) {
	if err := in.validate(); err != nil {
		return Thread{}, err
	}
	th, err := s.store.UpsertThread(ctx, UpsertThreadInput{
		WorkspaceID:    in.WorkspaceID,
		MailboxID:      in.MailboxID,
		CampaignID:     in.CampaignID,
		ContactID:      in.ContactID,
		RootMessageID:  in.RootMessageID,
		Subject:        in.Subject,
		LastReplyClass: in.LastReplyClass,
	})
	if err != nil {
		return Thread{}, err
	}
	msg := in.Message
	msg.ThreadID = th.ID
	msg.WorkspaceID = in.WorkspaceID
	if err := s.store.InsertMessage(ctx, msg); err != nil {
		return Thread{}, err
	}
	return th, nil
}

// ListThreads returns one page of the workspace's threads. A malformed
// keyset (exactly one of BeforeLastMessageAt/BeforeID set) is rejected rather
// than silently treated as the first page, which would hide a client bug as
// a lost place in the list.
func (s *Service) ListThreads(ctx context.Context, workspaceID uuid.UUID, filter ListFilter) (ThreadPage, error) {
	if (filter.BeforeLastMessageAt == nil) != (filter.BeforeID == nil) {
		return ThreadPage{}, fmt.Errorf("%w: before_last_message_at and before_id must be set together", ErrValidation)
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
