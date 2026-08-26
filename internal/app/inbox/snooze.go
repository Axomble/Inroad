package inbox

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/inroad/inroad/internal/platform/db/gen"
)

// SnoozeMaxHorizon bounds how far ahead a thread may be snoozed. A snooze is a
// promise to resurface something; a five-year one is indistinguishable from
// deleting it, but without the honesty of saying so. 90 days is long enough
// for "next quarter" and short enough that nothing is silently lost.
const SnoozeMaxHorizon = 90 * 24 * time.Hour

// ErrSnoozeInPast is returned for a snooze_until that has already passed.
// Distinct from ErrValidation because the UI can act on it specifically ("pick
// a future time") — and because a client whose clock is skewed will hit this
// legitimately and deserves a clear reason rather than a generic 400.
var ErrSnoozeInPast = errors.New("inbox: snooze_until must be in the future")

// ErrSnoozeTooFar is returned for a snooze_until beyond SnoozeMaxHorizon.
var ErrSnoozeTooFar = errors.New("inbox: snooze_until is further ahead than 90 days")

// Snooze is one thread's active (or lapsed) snooze.
type Snooze struct {
	ThreadID    uuid.UUID
	WorkspaceID uuid.UUID
	SnoozeUntil time.Time
	// SnoozedBy is nil when the member who snoozed it has since been removed
	// (ON DELETE SET NULL) — the snooze itself must outlive them, or a
	// departure would silently drag threads back into everyone's inbox.
	SnoozedBy *uuid.UUID
	CreatedAt time.Time
}

// Active reports whether the snooze is still in force at `now`. The same rule
// the SQL applies (snooze_until > now()), in Go, for callers holding a Snooze
// they have already read.
func (s Snooze) Active(now time.Time) bool { return s.SnoozeUntil.After(now) }

// SnoozeStore is the snooze half of this domain's persistence, kept as its own
// interface rather than folded into Store: Service.Snooze needs only these
// three methods, and a caller (or test) that has no interest in snoozing
// should not have to implement thread listing to satisfy the compiler.
//
// PgStore implements both.
type SnoozeStore interface {
	UpsertSnooze(ctx context.Context, in UpsertSnoozeInput) (Snooze, error)
	DeleteSnooze(ctx context.Context, workspaceID, threadID uuid.UUID) error
	GetSnooze(ctx context.Context, workspaceID, threadID uuid.UUID) (Snooze, error)
	CountSnoozed(ctx context.Context, workspaceID uuid.UUID) (int64, error)
}

// UpsertSnoozeInput carries one snooze to write.
type UpsertSnoozeInput struct {
	WorkspaceID uuid.UUID
	ThreadID    uuid.UUID
	SnoozeUntil time.Time
	// SnoozedBy is optional: a machine principal (API key) has no user id, and
	// a snooze it sets is still a valid snooze.
	SnoozedBy *uuid.UUID
}

// validate bounds the horizon in both directions, against a caller-supplied
// `now` so the rule is testable at a fixed instant.
func (in UpsertSnoozeInput) validate(now time.Time) error {
	if in.WorkspaceID == uuid.Nil || in.ThreadID == uuid.Nil {
		return fmt.Errorf("%w: workspace_id and thread_id are required", ErrValidation)
	}
	if !in.SnoozeUntil.After(now) {
		return ErrSnoozeInPast
	}
	if in.SnoozeUntil.After(now.Add(SnoozeMaxHorizon)) {
		return ErrSnoozeTooFar
	}
	return nil
}

// Snooze hides a thread from the inbox until SnoozeUntil.
//
// The thread is fetched first, so a snooze against an unknown or foreign id is
// a 404 from GetThread rather than a foreign-key violation from the insert —
// the same "never leak a foreign row's existence" rule the rest of this domain
// follows. That read also means the FK can only fail on a genuine race (the
// thread deleted between the two statements), which surfaces as a plain error.
//
// Re-snoozing an already-snoozed thread replaces the moment rather than
// failing: pushing something further out is the most ordinary snooze action
// there is.
func (s *Service) Snooze(ctx context.Context, in UpsertSnoozeInput) (Snooze, error) {
	if err := in.validate(s.now()); err != nil {
		return Snooze{}, err
	}
	if _, err := s.store.GetThread(ctx, in.WorkspaceID, in.ThreadID); err != nil {
		return Snooze{}, err
	}
	return s.snoozes.UpsertSnooze(ctx, in)
}

// Unsnooze returns a thread to the inbox immediately. Returns ErrNotFound when
// the thread has no snooze — which also covers an unknown or foreign thread id,
// since neither can have one, so this needs no separate existence check.
func (s *Service) Unsnooze(ctx context.Context, workspaceID, threadID uuid.UUID) error {
	return s.snoozes.DeleteSnooze(ctx, workspaceID, threadID)
}

// GetSnooze returns a thread's snooze, or ErrNotFound if it has none.
func (s *Service) GetSnooze(ctx context.Context, workspaceID, threadID uuid.UUID) (Snooze, error) {
	return s.snoozes.GetSnooze(ctx, workspaceID, threadID)
}

// activeSnoozeFor resolves a thread's snooze for display alongside the thread
// itself, collapsing the three uninteresting cases to nil: no snooze store
// configured, no snooze on this thread, or a snooze that has already lapsed.
//
// A read error other than "not found" is returned, not swallowed: the caller
// (GetThreadWithSnooze) decides whether a thread is still worth serving
// without its snooze, and that is not a decision to make silently here.
func (s *Service) activeSnoozeFor(ctx context.Context, workspaceID, threadID uuid.UUID) (*Snooze, error) {
	if s.snoozes == nil {
		return nil, nil
	}
	snooze, err := s.snoozes.GetSnooze(ctx, workspaceID, threadID)
	if err != nil {
		if snoozeNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	if !snooze.Active(s.now()) {
		return nil, nil
	}
	return &snooze, nil
}

// --- PgStore ---

func (s *PgStore) UpsertSnooze(ctx context.Context, in UpsertSnoozeInput) (Snooze, error) {
	row, err := s.q.UpsertInboxThreadSnooze(ctx, gen.UpsertInboxThreadSnoozeParams{
		ThreadID:    in.ThreadID,
		WorkspaceID: in.WorkspaceID,
		SnoozeUntil: pgTimestamptzValue(in.SnoozeUntil),
		SnoozedBy:   pgUUID(in.SnoozedBy),
	})
	if err != nil {
		return Snooze{}, err
	}
	return snoozeFromRow(row), nil
}

func (s *PgStore) DeleteSnooze(ctx context.Context, workspaceID, threadID uuid.UUID) error {
	n, err := s.q.DeleteInboxThreadSnooze(ctx, gen.DeleteInboxThreadSnoozeParams{
		ThreadID: threadID, WorkspaceID: workspaceID,
	})
	return affected(n, err)
}

func (s *PgStore) GetSnooze(ctx context.Context, workspaceID, threadID uuid.UUID) (Snooze, error) {
	row, err := s.q.GetInboxThreadSnooze(ctx, gen.GetInboxThreadSnoozeParams{
		ThreadID: threadID, WorkspaceID: workspaceID,
	})
	if err != nil {
		return Snooze{}, mapNotFound(err)
	}
	return snoozeFromRow(row), nil
}

func (s *PgStore) CountSnoozed(ctx context.Context, workspaceID uuid.UUID) (int64, error) {
	return s.q.CountInboxSnoozedThreads(ctx, workspaceID)
}

func snoozeFromRow(row gen.InboxThreadSnooze) Snooze {
	return Snooze{
		ThreadID:    row.ThreadID,
		WorkspaceID: row.WorkspaceID,
		SnoozeUntil: row.SnoozeUntil.Time,
		SnoozedBy:   uuidValue(row.SnoozedBy),
		CreatedAt:   row.CreatedAt.Time,
	}
}

// snoozeNotFound reports whether err is the "this thread has no snooze" case,
// so a caller assembling a thread's display state can treat it as "not
// snoozed" rather than propagating it as a failure.
func snoozeNotFound(err error) bool {
	return errors.Is(err, ErrNotFound) || errors.Is(err, pgx.ErrNoRows)
}
