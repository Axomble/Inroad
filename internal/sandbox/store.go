package sandbox

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Store is the persistence seam the simulator depends on. It is defined here,
// by the consumer, so Seeder can be unit-tested against a fake without a
// database — the same dependency-inversion shape every app domain uses.
//
// Every method takes the workspace id explicitly and every statement behind
// it is filtered by that id. The harness is a dev tool, but it writes through
// the same tables the product serves, so it holds the same tenancy invariant:
// a sandbox run must not be able to touch another workspace's rows.
//
// The interface is deliberately small and phrased in terms of what a
// SIMULATED RUN needs ("record a send that already happened"), not in terms
// of tables. That is what lets the fake in the tests be a few maps rather
// than a schema.
type Store interface {
	// RecordSend writes one already-completed outbound step: a 'sent' row with
	// a real Message-ID and a backdated sent_at.
	RecordSend(ctx context.Context, in SendRecord) (uuid.UUID, error)
	// RecordTracking writes one open/click against a send, at its own instant.
	RecordTracking(ctx context.Context, in TrackingRecord) error
	// RecordInboundReply creates or refreshes the thread for a root Message-ID
	// and appends the inbound message to it, at a backdated instant.
	RecordInboundReply(ctx context.Context, in ReplyRecord) error
	// StopEnrollment marks a contact's enrollment terminal ('replied' or
	// 'bounced'), matching what the real reply/bounce handlers do.
	StopEnrollment(ctx context.Context, workspaceID, campaignID, contactID uuid.UUID, reason string, at time.Time) error
	// SuppressContact adds an address to the workspace suppression list, for
	// the unsubscribe and bounce outcomes.
	SuppressContact(ctx context.Context, workspaceID uuid.UUID, email, reason string) error
}

// SendRecord is one completed outbound step. MessageID is the RFC 5322
// Message-ID the send went out under; it is what the reply threads against
// and what the inbox anchors a thread on, so it is generated once and shared
// between the two.
type SendRecord struct {
	WorkspaceID uuid.UUID
	CampaignID  uuid.UUID
	ContactID   uuid.UUID
	MailboxID   uuid.UUID
	ToEmail     string
	StepOrder   int32
	MessageID   string
	// ReferencesHeader is the threading chain: empty on step 1, the root
	// Message-ID on every later step, exactly as the send path builds it.
	ReferencesHeader string
	SentAt           time.Time
}

// TrackingRecord is one open or click against a send. Kind is the
// tracking_event_kind enum value ('open' or 'click').
type TrackingRecord struct {
	WorkspaceID uuid.UUID
	CampaignID  uuid.UUID
	SendID      uuid.UUID
	Kind        string
	URL         string
	UserAgent   string
	At          time.Time
}

// ReplyRecord is one inbound reply landing in the unified inbox.
//
// RootMessageID is the Message-ID of the campaign's STEP 1 send, not of the
// step being replied to: that is what anchors every message in the
// conversation onto a single thread. Getting this wrong is the single easiest
// way to seed an inbox that looks right in the database and renders as a pile
// of one-message threads in the UI.
type ReplyRecord struct {
	WorkspaceID   uuid.UUID
	MailboxID     uuid.UUID
	CampaignID    uuid.UUID
	ContactID     uuid.UUID
	RootMessageID string
	Subject       string
	ReplyClass    string
	MessageID     string
	FromEmail     string
	FromName      string
	ToEmail       string
	BodyText      string
	BodyHTML      string
	OccurredAt    time.Time
}

// PgStore implements Store over a pgx pool. It is the only place in this
// package that knows SQL.
//
// It writes raw SQL rather than going through the sqlc-generated queries, and
// that is a deliberate, contained exception: every write path the product
// owns stamps its timestamps with now(), because in production a send happens
// when it happens. The harness's entire purpose is to produce a believable
// PAST, so it needs to supply sent_at/occurred_at/created_at itself. Reusing
// the generated queries would collapse every seeded event onto one instant —
// the exact failure the harness exists to avoid.
type PgStore struct {
	pool *pgxpool.Pool
}

// NewPgStore builds a PgStore over pool.
func NewPgStore(pool *pgxpool.Pool) *PgStore { return &PgStore{pool: pool} }

var _ Store = (*PgStore)(nil)

// RecordSend inserts the completed send. ON CONFLICT makes a re-run
// idempotent against the (campaign_id, contact_id, step_order) unique index
// the step-send path already relies on, so the harness can be pointed at an
// existing sandbox workspace twice without erroring or duplicating history.
func (s *PgStore) RecordSend(ctx context.Context, in SendRecord) (uuid.UUID, error) {
	var id uuid.UUID
	err := s.pool.QueryRow(ctx, `
		INSERT INTO sends (workspace_id, campaign_id, contact_id, mailbox_id, to_email,
		                   step_order, status, message_id, references_header, created_at, sent_at)
		VALUES ($1, $2, $3, $4, $5, $6, 'sent', $7, $8, $9, $9)
		ON CONFLICT (campaign_id, contact_id, step_order) WHERE step_order IS NOT NULL
		DO UPDATE SET status = 'sent', message_id = EXCLUDED.message_id,
		              references_header = EXCLUDED.references_header,
		              sent_at = EXCLUDED.sent_at, created_at = EXCLUDED.created_at
		WHERE sends.workspace_id = $1
		RETURNING id`,
		in.WorkspaceID, in.CampaignID, in.ContactID, in.MailboxID, in.ToEmail,
		in.StepOrder, in.MessageID, in.ReferencesHeader, in.SentAt,
	).Scan(&id)
	if err != nil {
		return uuid.Nil, fmt.Errorf("insert send step %d for %s: %w", in.StepOrder, in.ToEmail, err)
	}
	return id, nil
}

// RecordTracking inserts one tracking event at its own instant. tracking_events
// is keyed (id, created_at) with no natural unique key, so a re-run would
// duplicate events; the guard is a NOT EXISTS on the same (send, kind, instant)
// triple rather than an ON CONFLICT, which has no index to infer.
func (s *PgStore) RecordTracking(ctx context.Context, in TrackingRecord) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO tracking_events (workspace_id, campaign_id, send_id, kind, url, user_agent, created_at)
		SELECT $1, $2, $3, $4::tracking_event_kind, $5, $6, $7
		WHERE NOT EXISTS (
			SELECT 1 FROM tracking_events e
			WHERE e.send_id = $3 AND e.workspace_id = $1
			  AND e.kind = $4::tracking_event_kind AND e.created_at = $7)`,
		in.WorkspaceID, in.CampaignID, in.SendID, in.Kind, in.URL, in.UserAgent, in.At)
	if err != nil {
		return fmt.Errorf("insert %s event for send %s: %w", in.Kind, in.SendID, err)
	}
	return nil
}

// RecordInboundReply upserts the thread and appends the inbound message in ONE
// transaction, mirroring inbox.PgStore.RecordReply's atomicity for the same
// reason: a thread whose last_message_at claims a reply that was never written
// is worse than no thread at all.
//
// The thread's last_message_at is set to the reply's own occurred_at rather
// than now(), which is what the product's query does — the inbox orders and
// windows threads by that column, so seeding it as "now" would pile every
// seeded thread onto today and make the today/this-week scopes meaningless.
func (s *PgStore) RecordInboundReply(ctx context.Context, in ReplyRecord) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin reply tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op once committed

	var threadID uuid.UUID
	err = tx.QueryRow(ctx, `
		INSERT INTO inbox_threads (workspace_id, mailbox_id, campaign_id, contact_id,
		                           root_message_id, subject, last_reply_class, unread,
		                           last_message_at, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, true, $8, $8)
		ON CONFLICT (workspace_id, mailbox_id, root_message_id) WHERE root_message_id <> ''
		DO UPDATE SET last_reply_class = EXCLUDED.last_reply_class,
		              last_message_at = GREATEST(inbox_threads.last_message_at, EXCLUDED.last_message_at),
		              unread = true
		RETURNING id`,
		in.WorkspaceID, in.MailboxID, in.CampaignID, in.ContactID,
		in.RootMessageID, in.Subject, in.ReplyClass, in.OccurredAt,
	).Scan(&threadID)
	if err != nil {
		return fmt.Errorf("upsert thread %s: %w", in.RootMessageID, err)
	}

	// The message's workspace comes from the caller's trusted input and its
	// thread from the row just upserted in THIS transaction — never from a
	// value that travelled alongside.
	if _, err := tx.Exec(ctx, `
		INSERT INTO inbox_messages (thread_id, workspace_id, direction, message_id,
		                            from_email, from_name, to_email, subject,
		                            body_text, body_html, reply_class, occurred_at, created_at)
		VALUES ($1, $2, 'inbound', $3, $4, $5, $6, $7, $8, $9, $10, $11, $11)
		ON CONFLICT (workspace_id, message_id) WHERE message_id <> '' DO NOTHING`,
		threadID, in.WorkspaceID, in.MessageID, in.FromEmail, in.FromName,
		in.ToEmail, in.Subject, in.BodyText, in.BodyHTML, in.ReplyClass, in.OccurredAt,
	); err != nil {
		return fmt.Errorf("insert inbound message %s: %w", in.MessageID, err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit reply %s: %w", in.MessageID, err)
	}
	return nil
}

// StopEnrollment marks the contact's enrollment terminal. Guarded on
// status='active' exactly as the product's StopEnrollment is, so a re-run
// never rewrites a stop that already landed.
func (s *PgStore) StopEnrollment(ctx context.Context, workspaceID, campaignID, contactID uuid.UUID, reason string, at time.Time) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE sequence_enrollments
		SET status = 'stopped', stop_reason = $4, stopped_at = $5, next_due_at = NULL
		WHERE workspace_id = $1 AND campaign_id = $2 AND contact_id = $3 AND status = 'active'`,
		workspaceID, campaignID, contactID, reason, at)
	if err != nil {
		return fmt.Errorf("stop enrollment (%s): %w", reason, err)
	}
	return nil
}

// SuppressContact adds the address to the workspace's suppression list. The
// unique index is on (workspace_id, lower(email)), so the conflict target
// names the expression rather than the bare column.
func (s *PgStore) SuppressContact(ctx context.Context, workspaceID uuid.UUID, email, reason string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO suppression (workspace_id, email, reason)
		VALUES ($1, $2, $3)
		ON CONFLICT (workspace_id, lower(email)) DO NOTHING`,
		workspaceID, email, reason)
	if err != nil {
		return fmt.Errorf("suppress %s (%s): %w", email, reason, err)
	}
	return nil
}

// EnsureEnrollment creates the active enrollment a simulated journey advances
// through, backdated to when the contact was actually enrolled. Separate from
// the Store interface because it is a setup concern the fake has no need to
// model; the Seeder reaches for it through EnrollmentWriter.
func (s *PgStore) EnsureEnrollment(ctx context.Context, in EnrollmentRecord) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO sequence_enrollments (workspace_id, campaign_id, contact_id, current_step,
		                                  status, enrolled_at, last_sent_at, next_due_at, thread_root_id)
		VALUES ($1, $2, $3, $4, 'active', $5, $6, NULL, $7)
		ON CONFLICT (campaign_id, contact_id) DO UPDATE SET
			current_step = EXCLUDED.current_step,
			last_sent_at = EXCLUDED.last_sent_at,
			thread_root_id = EXCLUDED.thread_root_id
		WHERE sequence_enrollments.workspace_id = $1`,
		in.WorkspaceID, in.CampaignID, in.ContactID, in.CurrentStep,
		in.EnrolledAt, nullableTime(in.LastSentAt), in.ThreadRootID)
	if err != nil {
		return fmt.Errorf("enroll contact %s: %w", in.ContactID, err)
	}
	return nil
}

// EnrollmentRecord is one contact's position in a simulated sequence run.
type EnrollmentRecord struct {
	WorkspaceID uuid.UUID
	CampaignID  uuid.UUID
	ContactID   uuid.UUID
	CurrentStep int32
	EnrolledAt  time.Time
	// LastSentAt is the zero time for a contact whose first step has not gone
	// out yet, which the column represents as NULL.
	LastSentAt   time.Time
	ThreadRootID string
}

// EnrollmentWriter is the seam for the enrollment setup above, kept separate
// from Store so the two can be faked independently.
type EnrollmentWriter interface {
	EnsureEnrollment(ctx context.Context, in EnrollmentRecord) error
}

var _ EnrollmentWriter = (*PgStore)(nil)

// nullableTime maps a zero time.Time onto a SQL NULL, which is how the
// nullable timestamp columns represent "has not happened".
func nullableTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}
