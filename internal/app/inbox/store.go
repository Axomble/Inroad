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
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inroad/inroad/internal/platform/db"
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
	// ContactEmail/ContactFirstName/ContactLastName come from a LEFT JOIN on
	// contacts and are '' (never a nil/pointer) when ContactID is nil — a
	// legacy direct-send match legitimately has no contact to join on. Same
	// "absent is empty string" convention as every other text field in this
	// domain (see inbox_messages' columns).
	ContactEmail     string
	ContactFirstName string
	ContactLastName  string
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
// filters; Query is an optional substring search (see its own comment
// below); BeforeLastMessageAt/BeforeID are the keyset cursor (both set, or
// both zero, for the first page) — the pair names the row a page continues
// after, per the (last_message_at, id) DESC ordering. Limit is the requested
// page size; a non-positive or over-large value is normalized by the store.
type ListFilter struct {
	MailboxID           *uuid.UUID
	ReplyClass          *string
	BeforeLastMessageAt *time.Time
	BeforeID            *uuid.UUID
	// Query is an optional case-insensitive substring match against the
	// thread's subject or its contact's email. "" means no search filter —
	// this domain's usual convention for "absent" text, not a pointer.
	Query string
	Limit int32
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
//
// RecordReply is the atomic combination of UpsertThread + InsertMessage: the
// only write path Service.RecordReply uses. UpsertThread/InsertMessage remain
// on the interface in their own right (Task 3/4's stated contract depends on
// these exact names) but Service no longer calls them separately — a caller
// that did would reopen the non-atomic gap RecordReply exists to close.
type Store interface {
	RecordReply(ctx context.Context, threadIn UpsertThreadInput, msgIn InsertMessageInput) (Thread, error)
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
// pool is kept alongside q so RecordReply can open its own transaction — every
// other method flows through q (following campaign.PgStore's identical shape).
type PgStore struct {
	pool *pgxpool.Pool
	q    *gen.Queries
}

// NewPgStore builds a PgStore over pool.
func NewPgStore(pool *pgxpool.Pool) *PgStore { return &PgStore{pool: pool, q: gen.New(pool)} }

var _ Store = (*PgStore)(nil)

// upsertThreadParams maps UpsertThreadInput to the generated params, shared by
// UpsertThread and RecordReply so the two never drift.
func upsertThreadParams(in UpsertThreadInput) gen.UpsertInboxThreadParams {
	return gen.UpsertInboxThreadParams{
		WorkspaceID:    in.WorkspaceID,
		MailboxID:      in.MailboxID,
		CampaignID:     pgUUID(in.CampaignID),
		ContactID:      pgUUID(in.ContactID),
		RootMessageID:  in.RootMessageID,
		Subject:        in.Subject,
		LastReplyClass: in.LastReplyClass,
	}
}

// insertMessageParams maps InsertMessageInput to the generated params, shared
// by InsertMessage and RecordReply so the two never drift.
func insertMessageParams(in InsertMessageInput) gen.InsertInboxMessageParams {
	return gen.InsertInboxMessageParams{
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
	}
}

// RecordReply upserts the thread and inserts the message in ONE transaction:
// without this, a connection dropped between the two separate calls would
// leave a thread whose last_reply_class/last_message_at/unread reflect a
// reply that was never actually recorded — and because UpsertThread's UPDATE
// SET list only ever touches those three columns (never message content), a
// later real reply to the same root_message_id would silently paper over the
// gap with no way to tell a half-applied reply from a real one. Begin/defer
// Rollback/Commit mirrors campaign.PgStore.DeleteDraft's shape.
func (s *PgStore) RecordReply(ctx context.Context, threadIn UpsertThreadInput, msgIn InsertMessageInput) (Thread, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Thread{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op once committed
	qtx := s.q.WithTx(tx)

	row, err := qtx.UpsertInboxThread(ctx, upsertThreadParams(threadIn))
	if err != nil {
		return Thread{}, err
	}
	th := threadFromRow(row)

	// ThreadID/WorkspaceID always come from the thread just upserted in THIS
	// transaction and the caller's trusted threadIn, never from msgIn — same
	// belt-and-braces the two-call path used to apply at the Service layer,
	// relocated here now that only the store knows the thread id at the right
	// moment.
	msgIn.ThreadID = th.ID
	msgIn.WorkspaceID = threadIn.WorkspaceID
	if err := qtx.InsertInboxMessage(ctx, insertMessageParams(msgIn)); err != nil {
		return Thread{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Thread{}, err
	}
	return th, nil
}

func (s *PgStore) UpsertThread(ctx context.Context, in UpsertThreadInput) (Thread, error) {
	row, err := s.q.UpsertInboxThread(ctx, upsertThreadParams(in))
	if err != nil {
		return Thread{}, err
	}
	return threadFromRow(row), nil
}

func (s *PgStore) InsertMessage(ctx context.Context, in InsertMessageInput) error {
	return s.q.InsertInboxMessage(ctx, insertMessageParams(in))
}

func (s *PgStore) ListThreads(ctx context.Context, workspaceID uuid.UUID, filter ListFilter) (ThreadPage, error) {
	rows, err := s.q.ListInboxThreads(ctx, gen.ListInboxThreadsParams{
		WorkspaceID:         workspaceID,
		MailboxID:           pgUUID(filter.MailboxID),
		ReplyClass:          filter.ReplyClass,
		BeforeLastMessageAt: pgTimestamptz(filter.BeforeLastMessageAt),
		BeforeID:            pgUUID(filter.BeforeID),
		Query:               likeQuery(filter.Query),
		PageLimit:           NormalizeLimit(filter.Limit),
	})
	if err != nil {
		return ThreadPage{}, err
	}
	items := make([]Thread, len(rows))
	for i, row := range rows {
		items[i] = threadFromListRow(row)
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
	return ThreadDetail{Thread: threadFromGetRow(row), Messages: messages}, nil
}

func (s *PgStore) SetUnread(ctx context.Context, workspaceID, id uuid.UUID, unread bool) error {
	return s.q.SetInboxThreadUnread(ctx, gen.SetInboxThreadUnreadParams{
		Unread: unread, ID: id, WorkspaceID: workspaceID,
	})
}

// threadFields is the common shape every sqlc row this domain converts to a
// Thread shares — named fields, not positional parameters, so a future
// reorder of same-typed adjacent fields (e.g. ContactFirstName/
// ContactLastName, both string) fails to compile instead of silently
// swapping them the way a positional-parameter builder would have.
type threadFields struct {
	ID             uuid.UUID
	WorkspaceID    uuid.UUID
	MailboxID      uuid.UUID
	CampaignID     pgtype.UUID
	ContactID      pgtype.UUID
	RootMessageID  string
	Subject        string
	LastReplyClass string
	Unread         bool
	LastMessageAt  pgtype.Timestamptz
	CreatedAt      pgtype.Timestamptz
	// ContactEmail/ContactFirstName/ContactLastName stay at their zero value
	// ("") for threadFromRow, whose row type never joins contacts.
	ContactEmail     string
	ContactFirstName string
	ContactLastName  string
}

// threadFromRow maps a generated inbox_threads row (no contact join — the
// UpsertInboxThread RETURNING clause this backs never joins contacts) to the
// domain type. ContactEmail/ContactFirstName/ContactLastName are left at
// their zero value (""), matching the "absent is empty string" convention;
// callers that need them go through GetThread/ListThreads instead.
func threadFromRow(row gen.InboxThread) Thread {
	return thread(threadFields{
		ID: row.ID, WorkspaceID: row.WorkspaceID, MailboxID: row.MailboxID,
		CampaignID: row.CampaignID, ContactID: row.ContactID, RootMessageID: row.RootMessageID,
		Subject: row.Subject, LastReplyClass: row.LastReplyClass, Unread: row.Unread,
		LastMessageAt: row.LastMessageAt, CreatedAt: row.CreatedAt,
	})
}

// threadFromListRow maps one ListInboxThreads row (sqlc's own row type,
// distinct from InboxThread because the query joins contacts) to the domain
// type.
func threadFromListRow(row gen.ListInboxThreadsRow) Thread {
	return thread(threadFields{
		ID: row.ID, WorkspaceID: row.WorkspaceID, MailboxID: row.MailboxID,
		CampaignID: row.CampaignID, ContactID: row.ContactID, RootMessageID: row.RootMessageID,
		Subject: row.Subject, LastReplyClass: row.LastReplyClass, Unread: row.Unread,
		LastMessageAt: row.LastMessageAt, CreatedAt: row.CreatedAt,
		ContactEmail: row.ContactEmail, ContactFirstName: row.ContactFirstName, ContactLastName: row.ContactLastName,
	})
}

// threadFromGetRow maps the GetInboxThread row (also its own sqlc row type,
// for the same reason as ListInboxThreadsRow) to the domain type.
func threadFromGetRow(row gen.GetInboxThreadRow) Thread {
	return thread(threadFields{
		ID: row.ID, WorkspaceID: row.WorkspaceID, MailboxID: row.MailboxID,
		CampaignID: row.CampaignID, ContactID: row.ContactID, RootMessageID: row.RootMessageID,
		Subject: row.Subject, LastReplyClass: row.LastReplyClass, Unread: row.Unread,
		LastMessageAt: row.LastMessageAt, CreatedAt: row.CreatedAt,
		ContactEmail: row.ContactEmail, ContactFirstName: row.ContactFirstName, ContactLastName: row.ContactLastName,
	})
}

// thread is the one place that assembles a Thread from its scalar
// components, shared by all three row-mapping functions above so they can
// never drift on the field shape (mirrors crm.deal's identical shape, but
// keyed by field name via threadFields rather than by position).
func thread(f threadFields) Thread {
	return Thread{
		ID:               f.ID,
		WorkspaceID:      f.WorkspaceID,
		MailboxID:        f.MailboxID,
		CampaignID:       uuidValue(f.CampaignID),
		ContactID:        uuidValue(f.ContactID),
		RootMessageID:    f.RootMessageID,
		Subject:          f.Subject,
		LastReplyClass:   f.LastReplyClass,
		Unread:           f.Unread,
		LastMessageAt:    f.LastMessageAt.Time,
		CreatedAt:        f.CreatedAt.Time,
		ContactEmail:     f.ContactEmail,
		ContactFirstName: f.ContactFirstName,
		ContactLastName:  f.ContactLastName,
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

// likeQuery escapes filter.Query (a plain string, "" meaning "no search")
// into the optional narg ListInboxThreads' query param expects: nil skips
// the filter's IS NULL guard entirely rather than matching an empty pattern
// against every row. Escaping itself is db.EscapeLike — shared with
// contact.SearchFilter's identical LIKE search, not duplicated here.
func likeQuery(q string) *string {
	if q == "" {
		return nil
	}
	escaped := db.EscapeLike(q)
	return &escaped
}

// mapNotFound turns pgx's "no rows" into the domain's ErrNotFound so the
// handler layer never has to know about pgx.
func mapNotFound(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}
