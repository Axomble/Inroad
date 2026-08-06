// Package inbox is the unified-inbox domain: one thread per matched-reply
// root_message_id (or one thread per legacy direct-send match, which has no
// root_message_id to anchor on), holding every inbound/outbound message on it.
//
// This package owns storage and the read/mark-read API only. The write path
// (RecordReply) is called by the reply-polling worker (internal/worker/inbox)
// through a later coreapi seam — no worker wiring lives here yet.
//
// Like every domain in this codebase, the service depends on the Store
// interface defined here, not on the concrete sqlc-backed PgStore
// (dependency inversion): PgStore is the only place that knows about
// gen.Queries or its param/row structs.
package inbox

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/inroad/inroad/internal/platform/db/gen"
)

// ErrNotFound is returned when a thread does not exist in the workspace.
var ErrNotFound = errors.New("inbox: thread not found")

// Thread is one unified-inbox conversation: everything sent/received under one
// root_message_id (or, for a legacy direct-send match with no root to anchor
// on, one single reply).
type Thread struct {
	ID             uuid.UUID
	WorkspaceID    uuid.UUID
	MailboxID      uuid.UUID
	CampaignID     *uuid.UUID
	ContactID      *uuid.UUID
	RootMessageID  string
	Subject        string
	LastReplyClass string
	Unread         bool
	LastMessageAt  time.Time
	CreatedAt      time.Time
}

// Message is one inbound or outbound email on a Thread.
type Message struct {
	ID          uuid.UUID
	ThreadID    uuid.UUID
	WorkspaceID uuid.UUID
	Direction   string
	MessageID   string
	FromEmail   string
	FromName    string
	ToEmail     string
	Subject     string
	BodyText    string
	BodyHTML    string
	ReplyClass  string
	OccurredAt  time.Time
	CreatedAt   time.Time
}

// ThreadDetail is a Thread with its full message history, oldest first.
type ThreadDetail struct {
	Thread   Thread
	Messages []Message
}

// ThreadPage is one page of ListThreads, newest first.
type ThreadPage struct {
	Items []Thread
}

// ListFilter narrows ListThreads. MailboxID and ReplyClass are optional exact
// filters; BeforeLastMessageAt/BeforeID are the keyset cursor (both set, or
// both zero, for the first page) — the pair names the row a page continues
// after, per the (last_message_at, id) DESC ordering. Limit is the requested
// page size; a non-positive or over-large value is normalized by the store.
type ListFilter struct {
	MailboxID           *uuid.UUID
	ReplyClass          *string
	BeforeLastMessageAt *time.Time
	BeforeID            *uuid.UUID
	Limit               int32
}

// UpsertThreadInput carries the fields UpsertThread writes on first insert, and
// the (smaller) subset it refreshes on a repeat reply to the same
// root_message_id.
type UpsertThreadInput struct {
	WorkspaceID    uuid.UUID
	MailboxID      uuid.UUID
	CampaignID     *uuid.UUID
	ContactID      *uuid.UUID
	RootMessageID  string
	Subject        string
	LastReplyClass string
}

// InsertMessageInput carries one message to append to a thread. ThreadID and
// WorkspaceID are always set by the Service from the thread UpsertThread just
// returned, never trusted from a caller-supplied value.
type InsertMessageInput struct {
	ThreadID    uuid.UUID
	WorkspaceID uuid.UUID
	Direction   string
	MessageID   string
	FromEmail   string
	FromName    string
	ToEmail     string
	Subject     string
	BodyText    string
	BodyHTML    string
	ReplyClass  string
	OccurredAt  time.Time
}

// Store is the repository interface this domain depends on. It is defined
// here (by the consumer), not by the persistence layer, so Service can be
// unit-tested against a fake without a database. Every method takes the
// workspace id as an explicit argument (from auth.UserFromContext at the HTTP
// boundary, or from the caller's own trusted context in the worker path) and
// every query is filtered by it — see docs/security.md.
type Store interface {
	UpsertThread(ctx context.Context, in UpsertThreadInput) (Thread, error)
	InsertMessage(ctx context.Context, in InsertMessageInput) error
	ListThreads(ctx context.Context, workspaceID uuid.UUID, filter ListFilter) (ThreadPage, error)
	GetThread(ctx context.Context, workspaceID, id uuid.UUID) (ThreadDetail, error)
	SetUnread(ctx context.Context, workspaceID, id uuid.UUID, unread bool) error
}

// DefaultThreadPageLimit is the page size ListThreads uses when the caller
// does not request one (Limit <= 0).
const DefaultThreadPageLimit = int32(25)

// MaxThreadPageLimit bounds the page size ListThreads will ever ask Postgres
// for, so a caller (or an upstream bug) asking for an enormous page cannot
// force an unbounded scan.
const MaxThreadPageLimit = int32(200)

// NormalizeLimit resolves the effective LIMIT for a threads page: the
// caller's request if it is within bounds, DefaultThreadPageLimit if unset
// (<=0), or MaxThreadPageLimit if the caller asked for more than that. Pure
// and store-level (a defensive floor/ceiling independent of any business
// validation Service.ListThreads may also apply).
func NormalizeLimit(requested int32) int32 {
	switch {
	case requested <= 0:
		return DefaultThreadPageLimit
	case requested > MaxThreadPageLimit:
		return MaxThreadPageLimit
	default:
		return requested
	}
}

// PgStore implements Store by wrapping sqlc-generated queries. It is the only
// place in this domain that knows about gen.Queries or its param/row structs.
type PgStore struct {
	q *gen.Queries
}

// NewPgStore builds a PgStore over q.
func NewPgStore(q *gen.Queries) *PgStore { return &PgStore{q: q} }

var _ Store = (*PgStore)(nil)

func (s *PgStore) UpsertThread(ctx context.Context, in UpsertThreadInput) (Thread, error) {
	row, err := s.q.UpsertInboxThread(ctx, gen.UpsertInboxThreadParams{
		WorkspaceID:    in.WorkspaceID,
		MailboxID:      in.MailboxID,
		CampaignID:     pgUUID(in.CampaignID),
		ContactID:      pgUUID(in.ContactID),
		RootMessageID:  in.RootMessageID,
		Subject:        in.Subject,
		LastReplyClass: in.LastReplyClass,
	})
	if err != nil {
		return Thread{}, err
	}
	return threadFromRow(row), nil
}

func (s *PgStore) InsertMessage(ctx context.Context, in InsertMessageInput) error {
	return s.q.InsertInboxMessage(ctx, gen.InsertInboxMessageParams{
		ThreadID:    in.ThreadID,
		WorkspaceID: in.WorkspaceID,
		Direction:   in.Direction,
		MessageID:   in.MessageID,
		FromEmail:   in.FromEmail,
		FromName:    in.FromName,
		ToEmail:     in.ToEmail,
		Subject:     in.Subject,
		BodyText:    in.BodyText,
		BodyHtml:    in.BodyHTML,
		ReplyClass:  in.ReplyClass,
		OccurredAt:  pgtype.Timestamptz{Time: in.OccurredAt, Valid: true},
	})
}

func (s *PgStore) ListThreads(ctx context.Context, workspaceID uuid.UUID, filter ListFilter) (ThreadPage, error) {
	rows, err := s.q.ListInboxThreads(ctx, gen.ListInboxThreadsParams{
		WorkspaceID:         workspaceID,
		MailboxID:           pgUUID(filter.MailboxID),
		ReplyClass:          filter.ReplyClass,
		BeforeLastMessageAt: pgTimestamptz(filter.BeforeLastMessageAt),
		BeforeID:            pgUUID(filter.BeforeID),
		PageLimit:           NormalizeLimit(filter.Limit),
	})
	if err != nil {
		return ThreadPage{}, err
	}
	items := make([]Thread, len(rows))
	for i, row := range rows {
		items[i] = threadFromRow(row)
	}
	return ThreadPage{Items: items}, nil
}

func (s *PgStore) GetThread(ctx context.Context, workspaceID, id uuid.UUID) (ThreadDetail, error) {
	row, err := s.q.GetInboxThread(ctx, gen.GetInboxThreadParams{ID: id, WorkspaceID: workspaceID})
	if err != nil {
		return ThreadDetail{}, mapNotFound(err)
	}
	msgRows, err := s.q.ListInboxMessagesByThread(ctx, gen.ListInboxMessagesByThreadParams{
		ThreadID: id, WorkspaceID: workspaceID,
	})
	if err != nil {
		return ThreadDetail{}, err
	}
	messages := make([]Message, len(msgRows))
	for i, m := range msgRows {
		messages[i] = messageFromRow(m)
	}
	return ThreadDetail{Thread: threadFromRow(row), Messages: messages}, nil
}

func (s *PgStore) SetUnread(ctx context.Context, workspaceID, id uuid.UUID, unread bool) error {
	return s.q.SetInboxThreadUnread(ctx, gen.SetInboxThreadUnreadParams{
		Unread: unread, ID: id, WorkspaceID: workspaceID,
	})
}

// threadFromRow maps a generated inbox_threads row to the domain type.
func threadFromRow(row gen.InboxThread) Thread {
	return Thread{
		ID:             row.ID,
		WorkspaceID:    row.WorkspaceID,
		MailboxID:      row.MailboxID,
		CampaignID:     uuidValue(row.CampaignID),
		ContactID:      uuidValue(row.ContactID),
		RootMessageID:  row.RootMessageID,
		Subject:        row.Subject,
		LastReplyClass: row.LastReplyClass,
		Unread:         row.Unread,
		LastMessageAt:  row.LastMessageAt.Time,
		CreatedAt:      row.CreatedAt.Time,
	}
}

// messageFromRow maps a generated inbox_messages row to the domain type.
func messageFromRow(row gen.InboxMessage) Message {
	return Message{
		ID:          row.ID,
		ThreadID:    row.ThreadID,
		WorkspaceID: row.WorkspaceID,
		Direction:   row.Direction,
		MessageID:   row.MessageID,
		FromEmail:   row.FromEmail,
		FromName:    row.FromName,
		ToEmail:     row.ToEmail,
		Subject:     row.Subject,
		BodyText:    row.BodyText,
		BodyHTML:    row.BodyHtml,
		ReplyClass:  row.ReplyClass,
		OccurredAt:  row.OccurredAt.Time,
		CreatedAt:   row.CreatedAt.Time,
	}
}

// pgUUID converts an optional domain id to the nullable pgtype the generated
// nullable-uuid params use (sqlc emits pgtype.UUID, not *uuid.UUID, for a
// sqlc.narg over the uuid override — see crm/store.go's identical helper).
func pgUUID(id *uuid.UUID) pgtype.UUID {
	if id == nil {
		return pgtype.UUID{}
	}
	return pgtype.UUID{Bytes: *id, Valid: true}
}

// uuidValue is pgUUID's inverse: nil for an absent value, a fresh pointer
// otherwise.
func uuidValue(v pgtype.UUID) *uuid.UUID {
	if !v.Valid {
		return nil
	}
	id := uuid.UUID(v.Bytes)
	return &id
}

// pgTimestamptz converts an optional domain time to the nullable pgtype the
// generated sqlc.narg(...)::timestamptz param uses.
func pgTimestamptz(t *time.Time) pgtype.Timestamptz {
	if t == nil {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: *t, Valid: true}
}

// mapNotFound turns pgx's "no rows" into the domain's ErrNotFound so the
// handler layer never has to know about pgx.
func mapNotFound(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}
