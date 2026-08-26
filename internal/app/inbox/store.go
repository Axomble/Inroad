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
	"sort"
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
	// ReplyLabel is the workspace's reply-label row resolved from
	// LastReplyClass (a LEFT JOIN on (workspace_id, key)), for display. nil
	// when no label in the workspace claims the key — a deleted custom
	// label whose key survives on historical rows, or a row from before a
	// join populated this (threadFromRow, backing UpsertThread/RecordReply's
	// return value, never joins reply_labels at all). Callers degrade to
	// the raw LastReplyClass key, per the OpenAPI contract's
	// InboxThreadSummary.reply_label doc.
	ReplyLabel *ReplyLabelRef
}

// ReplyLabelRef is a reply label's display fields, resolved for one thread's
// LastReplyClass. Key is deliberately duplicated from Thread.LastReplyClass
// (rather than requiring the caller to already have it) so a ReplyLabelRef
// is self-contained wherever it travels, mirroring coreapi.ReplyLabel's
// identical "flatten to what the caller needs" shape.
type ReplyLabelRef struct {
	Key   string
	Label string
	Color string
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
	// Snooze is the thread's snooze if one is still in force, else nil. Set by
	// Service.GetThread (not by the Store, which reads only thread/message
	// rows), so a lapsed snooze arrives as nil rather than as a stale row the
	// reader would have to date-check itself.
	Snooze *Snooze
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
	// UnreadOnly restricts the page to unread threads. A bool rather than a
	// *bool because there is no third state to express: the rail either asks
	// for the unread scope or it doesn't, and "explicitly only READ threads"
	// is not a scope this product offers.
	UnreadOnly bool
	// SinceLastMessageAt restricts the page to threads whose last_message_at
	// is at or after it — how the "today" and "this week" scopes are
	// expressed. nil for no lower bound. Deliberately separate from the
	// BeforeLastMessageAt keyset cursor: this one bounds the SCOPE, that one
	// names a position within it, and a page deep into "today" needs both at
	// once.
	SinceLastMessageAt *time.Time
	// AwaitingReplyOnly restricts the page to threads whose newest message is
	// inbound — the contact spoke last, so the thread is waiting on us.
	AwaitingReplyOnly bool
	// SnoozeHidden excludes threads still snoozed; SnoozedOnly keeps only
	// those. Three states, not one bool, because "neither" is a real and
	// distinct case: a search should find a snoozed thread (hiding it would
	// look like data loss), while every rail scope hides them. Setting both is
	// a contradiction the store rejects rather than silently resolving.
	SnoozeHidden bool
	SnoozedOnly  bool
	Limit        int32
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
	// RecordOutboundReply appends an OUTBOUND message to an existing thread and
	// bumps its last_message_at, in ONE transaction — see Service.
	// RecordOutboundReply's doc for why this never flips unread or
	// last_reply_class the way RecordReply's inbound path does.
	RecordOutboundReply(ctx context.Context, threadID, workspaceID uuid.UUID, msgIn InsertMessageInput) error
	// GetOverview returns the workspace's scope counts — see overview.go.
	GetOverview(ctx context.Context, workspaceID uuid.UUID, window OverviewWindow) (Overview, error)
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

// RecordOutboundReply inserts the manual reply's outbound message and bumps
// the thread's last_message_at in ONE transaction, the same rationale as
// RecordReply: a connection dropped between the two separate writes would
// leave a thread whose last_message_at reflects a reply that was never
// actually recorded. Deliberately does NOT upsert the thread the way
// RecordReply does (there is nothing to create — the thread already exists,
// or GetThread in Service.Reply would already have 404'd) and does not touch
// unread/last_reply_class — see the Store interface's doc comment.
func (s *PgStore) RecordOutboundReply(ctx context.Context, threadID, workspaceID uuid.UUID, msgIn InsertMessageInput) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op once committed
	qtx := s.q.WithTx(tx)

	msgIn.ThreadID = threadID
	msgIn.WorkspaceID = workspaceID
	if err := qtx.InsertInboxMessage(ctx, insertMessageParams(msgIn)); err != nil {
		return err
	}
	if err := qtx.BumpInboxThreadLastMessageAt(ctx, gen.BumpInboxThreadLastMessageAtParams{
		ID: threadID, WorkspaceID: workspaceID,
	}); err != nil {
		return err
	}
	return tx.Commit(ctx)
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
		UnreadOnly:          filter.UnreadOnly,
		SinceLastMessageAt:  pgTimestamptz(filter.SinceLastMessageAt),
		AwaitingReplyOnly:   filter.AwaitingReplyOnly,
		SnoozeHidden:        filter.SnoozeHidden,
		SnoozedOnly:         filter.SnoozedOnly,
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
	th := threadFromGetRow(row)

	msgRows, err := s.q.ListInboxMessagesByThread(ctx, gen.ListInboxMessagesByThreadParams{
		ThreadID: id, WorkspaceID: workspaceID,
	})
	if err != nil {
		return ThreadDetail{}, err
	}
	inbound := make([]Message, len(msgRows))
	for i, m := range msgRows {
		inbound[i] = messageFromRow(m)
	}

	outbound, err := s.outboundLeg(ctx, workspaceID, th)
	if err != nil {
		return ThreadDetail{}, err
	}

	return ThreadDetail{Thread: th, Messages: mergeMessagesByOccurredAt(inbound, outbound)}, nil
}

// outboundLeg synthesizes a thread's outbound leg (design spec's "Data
// model" section — the original sent message + any follow-up steps already
// sent) from sends/sequence_steps at READ time, rather than duplicating it
// into inbox_messages: the send content already lives in sequence_steps. A
// thread missing either link — a legacy direct-send match, or (defensively)
// one with only one of the two set — has nothing for the join to match and
// returns no outbound messages, not an error.
func (s *PgStore) outboundLeg(ctx context.Context, workspaceID uuid.UUID, th Thread) ([]Message, error) {
	if th.CampaignID == nil || th.ContactID == nil {
		return nil, nil
	}
	rows, err := s.q.ListSentOutboundStepsForThread(ctx, gen.ListSentOutboundStepsForThreadParams{
		WorkspaceID: workspaceID, CampaignID: *th.CampaignID, ContactID: *th.ContactID,
	})
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}

	// The step-1 subject is the anchor "Re: <this>" resolves against for a
	// later step that left its own subject empty — the SAME rule
	// stepsendjob.go's replySubject applies at send time (ported here, not
	// imported: app/* never depends on coreapi/inprocess — see replySubject's
	// own comment), so the reader shows the identical subject line the
	// recipient's mail client actually threaded on.
	threadSubject := ""
	step1, err := s.q.GetStepByOrder(ctx, gen.GetStepByOrderParams{
		CampaignID: *th.CampaignID, WorkspaceID: workspaceID, StepOrder: 1,
	})
	switch {
	case err == nil:
		threadSubject = step1.Subject
	case errors.Is(err, pgx.ErrNoRows):
		// Step 1 was deleted after sending: leave threadSubject "" so
		// replySubject yields a bare "Re: " rather than failing the whole read.
	default:
		return nil, err
	}

	messages := make([]Message, len(rows))
	for i, r := range rows {
		messages[i] = outboundMessageFromRow(r, threadSubject)
	}
	return messages, nil
}

// SetUnread flips a thread's read state. workspace_id is pinned in the
// UPDATE's own WHERE clause (not merely checked in Go), so a foreign
// workspace's call matches zero rows; :execrows lets us tell that apart from
// a real update and map it to ErrNotFound, mirroring crm.PgStore's identical
// affected() pattern.
func (s *PgStore) SetUnread(ctx context.Context, workspaceID, id uuid.UUID, unread bool) error {
	n, err := s.q.SetInboxThreadUnread(ctx, gen.SetInboxThreadUnreadParams{
		Unread: unread, ID: id, WorkspaceID: workspaceID,
	})
	return affected(n, err)
}

// affected turns a sqlc :execrows result into the domain's ErrNotFound when
// zero rows matched — mirrors crm.PgStore's identical helper (this domain has
// only the one :execrows call site so far; a second would promote this to a
// shared package rather than duplicate it there too).
func affected(n int64, err error) error {
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
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
	// ReplyLabelLabel/ReplyLabelColor are nil for threadFromRow (no
	// reply_labels join either) and whenever the join misses.
	ReplyLabelLabel *string
	ReplyLabelColor *string
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
		ReplyLabelLabel: row.ReplyLabelLabel, ReplyLabelColor: row.ReplyLabelColor,
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
		ReplyLabelLabel: row.ReplyLabelLabel, ReplyLabelColor: row.ReplyLabelColor,
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
		ReplyLabel:       replyLabelRef(f.LastReplyClass, f.ReplyLabelLabel, f.ReplyLabelColor),
	}
}

// replyLabelRef builds a *ReplyLabelRef from the reply_labels LEFT JOIN's
// nullable columns. nil when the join missed (no label in the workspace
// claims key) — label/color are set together by the query (both come from
// the same joined row), so checking either is equivalent; label is checked
// as the representative.
func replyLabelRef(key string, label, color *string) *ReplyLabelRef {
	if label == nil {
		return nil
	}
	return &ReplyLabelRef{Key: key, Label: *label, Color: derefOrEmpty(color)}
}

// derefOrEmpty dereferences a possibly-nil *string, defensively: label/color
// are always set together by the join this backs, so color should never be
// nil when label isn't, but this avoids a nil-pointer panic if that ever
// changes.
func derefOrEmpty(s *string) string {
	if s == nil {
		return ""
	}
	return *s
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

// outboundMessageFromRow maps one ListSentOutboundStepsForThread row to the
// domain Message. ReplyClass is left "" — an outbound send has no reply
// classification — matching this domain's "absent is empty string"
// convention.
func outboundMessageFromRow(r gen.ListSentOutboundStepsForThreadRow, threadSubject string) Message {
	return Message{
		Direction:  "outbound",
		MessageID:  r.MessageID,
		FromEmail:  r.FromEmail,
		FromName:   r.FromName,
		ToEmail:    r.ToEmail,
		Subject:    replySubject(int(r.StepOrder), r.StepSubject, threadSubject),
		BodyText:   r.StepBodyText,
		BodyHTML:   r.StepBodyHtml,
		OccurredAt: r.SentAt.Time,
		CreatedAt:  r.CreatedAt.Time,
	}
}

// replySubject synthesizes a step's effective subject for the outbound leg.
// Ported from (not imported — app/* never depends on coreapi/inprocess)
// internal/coreapi/inprocess/stepsendjob.go's identical send-time rule (spec
// A5), so the reader's subject line always matches what the recipient's mail
// client actually threaded on: step 1 always uses its own subject; from step
// 2, an empty step subject means "reply in the same thread" and resolves to
// "Re: <step 1 subject>" (threadSubject); a non-empty step subject is a
// deliberate new one and is used verbatim.
func replySubject(order int, stepSubject, threadSubject string) string {
	if order <= 1 || stepSubject != "" {
		return stepSubject
	}
	return "Re: " + threadSubject
}

// mergeMessagesByOccurredAt interleaves the stored inbound messages with the
// synthesized outbound leg by time, ascending — the same ordering
// ListInboxMessagesByThread's own ORDER BY occurred_at already gives the
// inbound-only case, now applied across both legs together. Stable so two
// messages landing at the exact same instant keep the order they were
// appended in (inbound before outbound) rather than an arbitrary one.
func mergeMessagesByOccurredAt(inbound, outbound []Message) []Message {
	merged := make([]Message, 0, len(inbound)+len(outbound))
	merged = append(merged, inbound...)
	merged = append(merged, outbound...)
	sort.SliceStable(merged, func(i, j int) bool { return merged[i].OccurredAt.Before(merged[j].OccurredAt) })
	return merged
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

// pgTimestamptzValue converts a required (non-pointer) domain time to the
// pgtype the generated params use — pgTimestamptz's counterpart for the
// overview window's boundaries, which are never absent.
func pgTimestamptzValue(t time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: t, Valid: true}
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
