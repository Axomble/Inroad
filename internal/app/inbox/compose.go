package inbox

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/platform/db/gen"
)

// Compose bounds. Recipient caps exist so one composed email cannot become a
// bulk send that bypasses the campaign machinery's own throttles, warmup limits
// and suppression accounting — this path is for writing to a person, not a list.
const (
	MaxComposeRecipients = 25
	MaxComposeSubject    = 500
	// MaxComposeDrafts caps how many drafts one user may accumulate, so an
	// autosave loop cannot fill the table.
	MaxComposeDrafts = 100
)

var (
	// ErrNoRecipients is returned when a compose has no To: address.
	ErrNoRecipients = errors.New("inbox: at least one recipient is required")
	// ErrTooManyRecipients is returned when a compose exceeds the per-message cap.
	ErrTooManyRecipients = fmt.Errorf("inbox: at most %d recipients per message", MaxComposeRecipients)
	// ErrInvalidRecipient is returned for an unparseable address.
	ErrInvalidRecipient = errors.New("inbox: one of the recipient addresses is not a valid email")
	// ErrSubjectTooLong is returned for an over-long subject.
	ErrSubjectTooLong = fmt.Errorf("inbox: subject must be at most %d characters", MaxComposeSubject)
	// ErrMailboxRequired is returned when no sending mailbox was chosen.
	ErrMailboxRequired = errors.New("inbox: a sending mailbox is required")
)

// ComposeDraft is one autosaved, half-written email. Per-user: a colleague must
// never resume someone else's unsent mail.
type ComposeDraft struct {
	ID          uuid.UUID
	WorkspaceID uuid.UUID
	UserID      uuid.UUID
	MailboxID   *uuid.UUID
	ToEmails    []string
	CcEmails    []string
	BccEmails   []string
	Subject     string
	BodyText    string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// PendingCompose is a composed (non-reply) email waiting to go out. Shares the
// reply path's lifecycle vocabulary — see PendingStatus* — because the undo
// semantics are identical.
type PendingCompose struct {
	ID           uuid.UUID
	WorkspaceID  uuid.UUID
	MailboxID    uuid.UUID
	ToEmails     []string
	CcEmails     []string
	BccEmails    []string
	Subject      string
	BodyText     string
	Status       string
	SendAfter    time.Time
	SentAt       *time.Time
	MessageID    string
	LastError    string
	CreatedBy    *uuid.UUID
	CreatedAt    time.Time
	MailboxEmail string
}

// Cancellable mirrors PendingReply.Cancellable — the same SQL guard.
func (c PendingCompose) Cancellable() bool { return c.Status == PendingStatusScheduled }

// SaveComposeDraftInput carries one autosave.
type SaveComposeDraftInput struct {
	ID          uuid.UUID
	WorkspaceID uuid.UUID
	UserID      uuid.UUID
	MailboxID   *uuid.UUID
	ToEmails    []string
	CcEmails    []string
	BccEmails   []string
	Subject     string
	BodyText    string
}

// CreatePendingComposeInput carries one composed email to schedule.
type CreatePendingComposeInput struct {
	WorkspaceID uuid.UUID
	MailboxID   uuid.UUID
	ToEmails    []string
	CcEmails    []string
	BccEmails   []string
	Subject     string
	BodyText    string
	SendAfter   time.Time
	CreatedBy   *uuid.UUID
}

// ComposeStore is the compose half of this domain's persistence.
type ComposeStore interface {
	SaveDraft(ctx context.Context, in SaveComposeDraftInput) (ComposeDraft, error)
	ListDrafts(ctx context.Context, workspaceID, userID uuid.UUID, limit int32) ([]ComposeDraft, error)
	GetDraft(ctx context.Context, workspaceID, userID, id uuid.UUID) (ComposeDraft, error)
	DeleteDraft(ctx context.Context, workspaceID, userID, id uuid.UUID) error
	CreatePendingCompose(ctx context.Context, in CreatePendingComposeInput) (PendingCompose, error)
	GetPendingCompose(ctx context.Context, workspaceID, id uuid.UUID) (PendingCompose, error)
	ListPendingComposes(ctx context.Context, workspaceID uuid.UUID, limit int32) ([]PendingCompose, error)
	CancelPendingCompose(ctx context.Context, workspaceID, id uuid.UUID) error
}

// ComposeClaimer is the execution-plane half, split from ComposeStore for the
// same reason PendingReplyClaimer is split from PendingReplyStore: the control
// plane has no business claiming or completing a send.
type ComposeClaimer interface {
	ClaimPendingCompose(ctx context.Context, workspaceID, id uuid.UUID) error
	MarkPendingComposeSent(ctx context.Context, workspaceID, id uuid.UUID, messageID string) error
	ReleasePendingCompose(ctx context.Context, workspaceID, id uuid.UUID, reason string) error
	FailPendingCompose(ctx context.Context, workspaceID, id uuid.UUID, reason string) error
}

// ComposeEnqueuer hands a scheduled compose to the execution plane.
type ComposeEnqueuer interface {
	EnqueuePendingInboxCompose(pendingID, workspaceID string, sendAfter time.Time) error
}

// normalizeRecipients trims, drops blanks, lowercases and de-duplicates a
// recipient list, then validates every survivor.
//
// De-duplication matters beyond tidiness: the same address in To and again in Cc
// would otherwise have the message delivered to them twice and counted twice
// against sending limits.
func normalizeRecipients(raw []string) ([]string, error) {
	seen := make(map[string]bool, len(raw))
	out := make([]string, 0, len(raw))
	for _, candidate := range raw {
		trimmed := strings.TrimSpace(candidate)
		if trimmed == "" {
			continue
		}
		// net/mail parses the full RFC 5322 form, so "Ada <a@x.test>" is
		// accepted and reduced to the address — the composer's chips carry
		// display names, and rejecting them would be wrong.
		addr, err := mail.ParseAddress(trimmed)
		if err != nil {
			return nil, fmt.Errorf("%w: %q", ErrInvalidRecipient, trimmed)
		}
		lowered := strings.ToLower(addr.Address)
		if seen[lowered] {
			continue
		}
		seen[lowered] = true
		out = append(out, lowered)
	}
	return out, nil
}

// validateComposeFields normalizes and bounds everything a send needs, returning
// the values to store.
func validateComposeFields(to, cc, bcc []string, subject, bodyText string) (
	normTo, normCc, normBcc []string, err error,
) {
	if normTo, err = normalizeRecipients(to); err != nil {
		return nil, nil, nil, err
	}
	if normCc, err = normalizeRecipients(cc); err != nil {
		return nil, nil, nil, err
	}
	if normBcc, err = normalizeRecipients(bcc); err != nil {
		return nil, nil, nil, err
	}
	if len(normTo) == 0 {
		return nil, nil, nil, ErrNoRecipients
	}
	// The cap counts EVERY recipient, not just To: a message with 1 To and 40
	// Bcc is a bulk send however it is addressed.
	if len(normTo)+len(normCc)+len(normBcc) > MaxComposeRecipients {
		return nil, nil, nil, ErrTooManyRecipients
	}
	// Runes, not bytes — a subject in a non-Latin script must not be rejected
	// for a byte length it never had.
	if len([]rune(subject)) > MaxComposeSubject {
		return nil, nil, nil, ErrSubjectTooLong
	}
	if err := validateReplyBody(bodyText); err != nil {
		return nil, nil, nil, err
	}
	return normTo, normCc, normBcc, nil
}

// SaveComposeDraft autosaves a half-written email.
//
// Deliberately does NOT validate recipients or body: a draft is by definition
// incomplete, and refusing to save "ada@" mid-typing would lose the operator's
// work at every keystroke. Validation happens at SEND time, where it belongs.
// The subject IS bounded, because an unbounded column is a storage problem
// rather than a correctness one.
func (s *Service) SaveComposeDraft(ctx context.Context, in SaveComposeDraftInput) (ComposeDraft, error) {
	if s.compose == nil {
		return ComposeDraft{}, fmt.Errorf("%w: compose is not configured", ErrValidation)
	}
	if in.UserID == uuid.Nil {
		// A machine principal has no user id, and a draft belongs to a person.
		return ComposeDraft{}, fmt.Errorf("%w: drafts require an authenticated user", ErrValidation)
	}
	if len([]rune(in.Subject)) > MaxComposeSubject {
		return ComposeDraft{}, ErrSubjectTooLong
	}
	if in.ID == uuid.Nil {
		in.ID = uuid.New()
	}

	// The cap is checked against EXISTING drafts, and a save to an id already
	// present is an update rather than a new row — so an operator editing their
	// hundredth draft is never locked out of their own work.
	existing, err := s.compose.ListDrafts(ctx, in.WorkspaceID, in.UserID, MaxComposeDrafts+1)
	if err != nil {
		return ComposeDraft{}, err
	}
	if len(existing) >= MaxComposeDrafts && !containsDraft(existing, in.ID) {
		return ComposeDraft{}, fmt.Errorf("%w: at most %d drafts", ErrValidation, MaxComposeDrafts)
	}

	// Stored as given (only trimmed of blanks): the composer round-trips its own
	// chips, and normalizing mid-typing would rewrite what the operator sees.
	in.ToEmails = compactStrings(in.ToEmails)
	in.CcEmails = compactStrings(in.CcEmails)
	in.BccEmails = compactStrings(in.BccEmails)
	return s.compose.SaveDraft(ctx, in)
}

func containsDraft(drafts []ComposeDraft, id uuid.UUID) bool {
	for _, d := range drafts {
		if d.ID == id {
			return true
		}
	}
	return false
}

// compactStrings drops empty entries without otherwise touching the values.
func compactStrings(in []string) []string {
	out := make([]string, 0, len(in))
	for _, v := range in {
		if strings.TrimSpace(v) != "" {
			out = append(out, strings.TrimSpace(v))
		}
	}
	return out
}

// ListComposeDrafts returns this user's own drafts.
func (s *Service) ListComposeDrafts(ctx context.Context, workspaceID, userID uuid.UUID) ([]ComposeDraft, error) {
	if s.compose == nil || userID == uuid.Nil {
		return nil, nil
	}
	return s.compose.ListDrafts(ctx, workspaceID, userID, MaxComposeDrafts)
}

// DeleteComposeDraft discards a draft.
func (s *Service) DeleteComposeDraft(ctx context.Context, workspaceID, userID, id uuid.UUID) error {
	if s.compose == nil {
		return fmt.Errorf("%w: compose is not configured", ErrValidation)
	}
	if userID == uuid.Nil {
		return ErrNotFound
	}
	return s.compose.DeleteDraft(ctx, workspaceID, userID, id)
}

// ScheduleCompose queues a composed email for delivery.
//
// Every recipient is checked against the workspace's suppression list, and ONE
// suppressed address rejects the whole message rather than being silently
// dropped: the operator wrote to those people deliberately, and quietly not
// delivering to one of them is worse than telling them why.
func (s *Service) ScheduleCompose(
	ctx context.Context,
	workspaceID uuid.UUID,
	in CreatePendingComposeInput,
	sendAt *time.Time,
) (PendingCompose, error) {
	if s.compose == nil || s.pending == nil {
		return PendingCompose{}, fmt.Errorf("%w: compose is not configured", ErrValidation)
	}
	if in.MailboxID == uuid.Nil {
		return PendingCompose{}, ErrMailboxRequired
	}

	to, cc, bcc, err := validateComposeFields(in.ToEmails, in.CcEmails, in.BccEmails, in.Subject, in.BodyText)
	if err != nil {
		return PendingCompose{}, err
	}
	// Bcc is checked too: a suppressed address is suppressed however it is
	// addressed.
	for _, recipient := range append(append(append([]string{}, to...), cc...), bcc...) {
		if err := s.checkRecipientNotSuppressed(ctx, workspaceID, recipient); err != nil {
			return PendingCompose{}, err
		}
	}

	sendAfter, err := s.resolveSendAfter(ctx, workspaceID, sendAt)
	if err != nil {
		return PendingCompose{}, err
	}

	in.WorkspaceID = workspaceID
	in.ToEmails, in.CcEmails, in.BccEmails = to, cc, bcc
	in.SendAfter = sendAfter

	saved, err := s.compose.CreatePendingCompose(ctx, in)
	if err != nil {
		return PendingCompose{}, err
	}
	if s.composeEnq != nil {
		if err := s.composeEnq.EnqueuePendingInboxCompose(saved.ID.String(), workspaceID.String(), sendAfter); err != nil {
			return PendingCompose{}, fmt.Errorf("enqueue pending compose: %w", err)
		}
	}
	return saved, nil
}

// ListPendingComposes returns the workspace's queued composed emails.
func (s *Service) ListPendingComposes(ctx context.Context, workspaceID uuid.UUID, limit int32) ([]PendingCompose, error) {
	if s.compose == nil {
		return nil, nil
	}
	return s.compose.ListPendingComposes(ctx, workspaceID, NormalizePendingLimit(limit))
}

// CancelPendingCompose undoes a queued composed email. Mirrors
// CancelPendingReply, including the ErrPendingNotCancellable distinction.
func (s *Service) CancelPendingCompose(ctx context.Context, workspaceID, id uuid.UUID) error {
	if s.compose == nil {
		return fmt.Errorf("%w: compose is not configured", ErrValidation)
	}
	err := s.compose.CancelPendingCompose(ctx, workspaceID, id)
	if !errors.Is(err, ErrNotFound) {
		return err
	}
	if existing, getErr := s.compose.GetPendingCompose(ctx, workspaceID, id); getErr == nil && !existing.Cancellable() {
		return ErrPendingNotCancellable
	}
	return ErrNotFound
}

// GetPendingCompose reads one queued composed email.
func (s *Service) GetPendingCompose(ctx context.Context, workspaceID, id uuid.UUID) (PendingCompose, error) {
	if s.compose == nil {
		return PendingCompose{}, ErrNotFound
	}
	return s.compose.GetPendingCompose(ctx, workspaceID, id)
}

// --- Execution-plane entry points (called through coreapi, never routed) ---

func (s *Service) ClaimPendingCompose(ctx context.Context, workspaceID, id uuid.UUID) error {
	claimer, ok := s.compose.(ComposeClaimer)
	if !ok {
		return fmt.Errorf("%w: this compose store cannot claim", ErrValidation)
	}
	return claimer.ClaimPendingCompose(ctx, workspaceID, id)
}

func (s *Service) MarkPendingComposeSent(ctx context.Context, workspaceID, id uuid.UUID, messageID string) error {
	claimer, ok := s.compose.(ComposeClaimer)
	if !ok {
		return fmt.Errorf("%w: this compose store cannot claim", ErrValidation)
	}
	return claimer.MarkPendingComposeSent(ctx, workspaceID, id, messageID)
}

func (s *Service) ReleasePendingCompose(ctx context.Context, workspaceID, id uuid.UUID, reason string) error {
	claimer, ok := s.compose.(ComposeClaimer)
	if !ok {
		return fmt.Errorf("%w: this compose store cannot claim", ErrValidation)
	}
	return claimer.ReleasePendingCompose(ctx, workspaceID, id, reason)
}

func (s *Service) FailPendingCompose(ctx context.Context, workspaceID, id uuid.UUID, reason string) error {
	claimer, ok := s.compose.(ComposeClaimer)
	if !ok {
		return fmt.Errorf("%w: this compose store cannot claim", ErrValidation)
	}
	return claimer.FailPendingCompose(ctx, workspaceID, id, reason)
}

// --- PgStore ---

func (s *PgStore) SaveDraft(ctx context.Context, in SaveComposeDraftInput) (ComposeDraft, error) {
	row, err := s.q.UpsertInboxComposeDraft(ctx, gen.UpsertInboxComposeDraftParams{
		ID: in.ID, WorkspaceID: in.WorkspaceID, UserID: in.UserID,
		MailboxID: pgUUID(in.MailboxID),
		ToEmails:  in.ToEmails, CcEmails: in.CcEmails, BccEmails: in.BccEmails,
		Subject: in.Subject, BodyText: in.BodyText,
	})
	if err != nil {
		// The UPDATE arm's WHERE pins workspace AND user, so another user's
		// draft id matches nothing and reads as not-found rather than being
		// overwritten.
		return ComposeDraft{}, mapNotFound(err)
	}
	return draftFromRow(row), nil
}

func (s *PgStore) ListDrafts(ctx context.Context, workspaceID, userID uuid.UUID, limit int32) ([]ComposeDraft, error) {
	rows, err := s.q.ListInboxComposeDrafts(ctx, gen.ListInboxComposeDraftsParams{
		WorkspaceID: workspaceID, UserID: userID, PageLimit: limit,
	})
	if err != nil {
		return nil, err
	}
	out := make([]ComposeDraft, len(rows))
	for i, r := range rows {
		out[i] = draftFromRow(r)
	}
	return out, nil
}

func (s *PgStore) GetDraft(ctx context.Context, workspaceID, userID, id uuid.UUID) (ComposeDraft, error) {
	row, err := s.q.GetInboxComposeDraft(ctx, gen.GetInboxComposeDraftParams{
		ID: id, WorkspaceID: workspaceID, UserID: userID,
	})
	if err != nil {
		return ComposeDraft{}, mapNotFound(err)
	}
	return draftFromRow(row), nil
}

func (s *PgStore) DeleteDraft(ctx context.Context, workspaceID, userID, id uuid.UUID) error {
	n, err := s.q.DeleteInboxComposeDraft(ctx, gen.DeleteInboxComposeDraftParams{
		ID: id, WorkspaceID: workspaceID, UserID: userID,
	})
	return affected(n, err)
}

func (s *PgStore) CreatePendingCompose(ctx context.Context, in CreatePendingComposeInput) (PendingCompose, error) {
	row, err := s.q.CreateInboxPendingCompose(ctx, gen.CreateInboxPendingComposeParams{
		WorkspaceID: in.WorkspaceID, MailboxID: in.MailboxID,
		ToEmails: in.ToEmails, CcEmails: in.CcEmails, BccEmails: in.BccEmails,
		Subject: in.Subject, BodyText: in.BodyText,
		SendAfter: pgTimestamptzValue(in.SendAfter), CreatedBy: pgUUID(in.CreatedBy),
	})
	if err != nil {
		// Zero rows for a mailbox outside the workspace — a cross-tenant
		// mailbox can never become a sending identity.
		return PendingCompose{}, mapNotFound(err)
	}
	return composeFromRow(row), nil
}

func (s *PgStore) GetPendingCompose(ctx context.Context, workspaceID, id uuid.UUID) (PendingCompose, error) {
	row, err := s.q.GetInboxPendingCompose(ctx, gen.GetInboxPendingComposeParams{ID: id, WorkspaceID: workspaceID})
	if err != nil {
		return PendingCompose{}, mapNotFound(err)
	}
	return composeFromRow(row), nil
}

func (s *PgStore) ListPendingComposes(ctx context.Context, workspaceID uuid.UUID, limit int32) ([]PendingCompose, error) {
	rows, err := s.q.ListInboxPendingComposes(ctx, gen.ListInboxPendingComposesParams{
		WorkspaceID: workspaceID, PageLimit: limit,
	})
	if err != nil {
		return nil, err
	}
	out := make([]PendingCompose, len(rows))
	for i, r := range rows {
		out[i] = PendingCompose{
			ID: r.ID, WorkspaceID: r.WorkspaceID, MailboxID: r.MailboxID,
			ToEmails: r.ToEmails, CcEmails: r.CcEmails, BccEmails: r.BccEmails,
			Subject: r.Subject, BodyText: r.BodyText, Status: r.Status,
			SendAfter: r.SendAfter.Time, SentAt: timeValue(r.SentAt),
			MessageID: r.MessageID, LastError: r.LastError,
			CreatedBy: uuidValue(r.CreatedBy), CreatedAt: r.CreatedAt.Time,
			MailboxEmail: r.MailboxEmail,
		}
	}
	return out, nil
}

func (s *PgStore) CancelPendingCompose(ctx context.Context, workspaceID, id uuid.UUID) error {
	n, err := s.q.CancelInboxPendingCompose(ctx, gen.CancelInboxPendingComposeParams{
		ID: id, WorkspaceID: workspaceID,
	})
	return affected(n, err)
}

func (s *PgStore) ClaimPendingCompose(ctx context.Context, workspaceID, id uuid.UUID) error {
	n, err := s.q.ClaimInboxPendingCompose(ctx, gen.ClaimInboxPendingComposeParams{
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

func (s *PgStore) MarkPendingComposeSent(ctx context.Context, workspaceID, id uuid.UUID, messageID string) error {
	n, err := s.q.MarkInboxPendingComposeSent(ctx, gen.MarkInboxPendingComposeSentParams{
		ID: id, WorkspaceID: workspaceID, MessageID: messageID,
	})
	return affected(n, err)
}

func (s *PgStore) ReleasePendingCompose(ctx context.Context, workspaceID, id uuid.UUID, reason string) error {
	return s.q.ReleaseInboxPendingCompose(ctx, gen.ReleaseInboxPendingComposeParams{
		ID: id, WorkspaceID: workspaceID, LastError: truncateError(reason),
	})
}

func (s *PgStore) FailPendingCompose(ctx context.Context, workspaceID, id uuid.UUID, reason string) error {
	return s.q.FailInboxPendingCompose(ctx, gen.FailInboxPendingComposeParams{
		ID: id, WorkspaceID: workspaceID, LastError: truncateError(reason),
	})
}

func draftFromRow(row gen.InboxComposeDraft) ComposeDraft {
	return ComposeDraft{
		ID: row.ID, WorkspaceID: row.WorkspaceID, UserID: row.UserID,
		MailboxID: uuidValue(row.MailboxID),
		ToEmails:  row.ToEmails, CcEmails: row.CcEmails, BccEmails: row.BccEmails,
		Subject: row.Subject, BodyText: row.BodyText,
		CreatedAt: row.CreatedAt.Time, UpdatedAt: row.UpdatedAt.Time,
	}
}

func composeFromRow(row gen.InboxPendingCompose) PendingCompose {
	return PendingCompose{
		ID: row.ID, WorkspaceID: row.WorkspaceID, MailboxID: row.MailboxID,
		ToEmails: row.ToEmails, CcEmails: row.CcEmails, BccEmails: row.BccEmails,
		Subject: row.Subject, BodyText: row.BodyText, Status: row.Status,
		SendAfter: row.SendAfter.Time, SentAt: timeValue(row.SentAt),
		MessageID: row.MessageID, LastError: row.LastError,
		CreatedBy: uuidValue(row.CreatedBy), CreatedAt: row.CreatedAt.Time,
	}
}
