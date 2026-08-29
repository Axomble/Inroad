package deadletter

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/inroad/inroad/internal/platform/db/gen"
)

// Store is the repository interface this domain depends on (defined by the
// consumer, satisfied by PgStore below). gen.TaskDeadLetter is the persistence
// type — there is no parallel entity struct, per the repo's "sqlc models are
// the persistence type; the interface boundary is where the decoupling lives"
// rule.
//
// Every method takes the workspace as its FIRST argument and no method can be
// called without one: the tenant pin is a property of the interface, not
// something a caller may forget to pass.
type Store interface {
	// Insert records one retry-exhausted task in the lifecycle state the service
	// resolved for it. status is a parameter rather than the column's default
	// because capture is not always "this is replayable": a legacy
	// content-bearing task is stored redacted and already filed — see
	// Service.Capture. It never comes from a request.
	Insert(ctx context.Context, in Capture, status string) (gen.TaskDeadLetter, error)
	// List returns up to q.Limit of the workspace's dead letters, newest first,
	// resuming strictly after q.Cursor when one is given.
	List(ctx context.Context, ws uuid.UUID, q ListQuery) ([]gen.TaskDeadLetter, error)
	// Get loads one dead letter. ok=false means no such row IN THIS WORKSPACE —
	// a row belonging to another tenant is indistinguishable from a row that
	// does not exist, which is the intended behaviour.
	Get(ctx context.Context, ws, id uuid.UUID) (gen.TaskDeadLetter, bool, error)
	// ClaimReplay atomically flips a 'pending' row to 'replayed' and returns it.
	// claimed=false means the row was not pending (already replayed, already
	// discarded, or not in this workspace) — NOT an error. This is the
	// exactly-once guard the whole domain rests on; see the query comment on
	// ClaimTaskDeadLetterReplay.
	ClaimReplay(ctx context.Context, ws, id uuid.UUID) (gen.TaskDeadLetter, bool, error)
	// ReleaseReplay undoes a claim whose subsequent enqueue failed, returning
	// the row to 'pending'. replayedAt identifies the exact claim being undone
	// so a later, successful replay can never be reopened by a straggler.
	ReleaseReplay(ctx context.Context, ws, id uuid.UUID, replayedAt pgtype.Timestamptz) error
	// Discard files a 'pending' row as triaged without re-running it.
	// discarded=false means the row was not pending.
	Discard(ctx context.Context, ws, id uuid.UUID) (bool, error)
}

// ListQuery is one page request against the store. It is deliberately dumb: the
// Limit here is the number of rows to FETCH, cursors arrive already decoded, and
// nothing about lookahead or token encoding lives at this layer — those are the
// service's policy, and keeping them out means the store can be read as "run
// this statement" and nothing else.
type ListQuery struct {
	// Status filters the lifecycle state; "" means any.
	Status string
	// Cursor resumes strictly after a row, or nil for the first page.
	Cursor *Cursor
	// Limit is how many rows to fetch, exactly as passed.
	Limit int32
}

// PgStore implements Store over the sqlc-generated queries.
type PgStore struct{ q *gen.Queries }

// NewPgStore builds a PgStore over the given sqlc queries.
func NewPgStore(q *gen.Queries) *PgStore { return &PgStore{q: q} }

var _ Store = (*PgStore)(nil)

func (s *PgStore) Insert(ctx context.Context, in Capture, status string) (gen.TaskDeadLetter, error) {
	return s.q.InsertTaskDeadLetter(ctx, gen.InsertTaskDeadLetterParams{
		WorkspaceID:  in.WorkspaceID,
		TaskType:     in.TaskType,
		Payload:      in.Payload,
		LastError:    in.LastError,
		AttemptCount: in.AttemptCount,
		Status:       status,
	})
}

func (s *PgStore) List(ctx context.Context, ws uuid.UUID, q ListQuery) ([]gen.TaskDeadLetter, error) {
	params := gen.ListTaskDeadLettersParams{
		WorkspaceID: ws,
		Status:      q.Status,
		PageLimit:   q.Limit,
	}
	if q.Cursor != nil {
		params.Seek = true
		params.CursorTime = pgtype.Timestamptz{Time: q.Cursor.CreatedAt, Valid: true}
		params.CursorID = q.Cursor.ID
	}
	return s.q.ListTaskDeadLetters(ctx, params)
}

func (s *PgStore) Get(ctx context.Context, ws, id uuid.UUID) (gen.TaskDeadLetter, bool, error) {
	row, err := s.q.GetTaskDeadLetter(ctx, gen.GetTaskDeadLetterParams{WorkspaceID: ws, ID: id})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return gen.TaskDeadLetter{}, false, nil
		}
		return gen.TaskDeadLetter{}, false, err
	}
	return row, true, nil
}

// ClaimReplay maps the claim's "no row matched the status='pending' predicate"
// (pgx.ErrNoRows) to claimed=false rather than propagating the sentinel: losing
// a claim race is the expected outcome for the second caller, not a failure.
func (s *PgStore) ClaimReplay(ctx context.Context, ws, id uuid.UUID) (gen.TaskDeadLetter, bool, error) {
	row, err := s.q.ClaimTaskDeadLetterReplay(ctx, gen.ClaimTaskDeadLetterReplayParams{WorkspaceID: ws, ID: id})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return gen.TaskDeadLetter{}, false, nil
		}
		return gen.TaskDeadLetter{}, false, err
	}
	return row, true, nil
}

func (s *PgStore) ReleaseReplay(ctx context.Context, ws, id uuid.UUID, replayedAt pgtype.Timestamptz) error {
	_, err := s.q.ReleaseTaskDeadLetterReplay(ctx, gen.ReleaseTaskDeadLetterReplayParams{
		WorkspaceID: ws, ID: id, ReplayedAt: replayedAt,
	})
	return err
}

func (s *PgStore) Discard(ctx context.Context, ws, id uuid.UUID) (bool, error) {
	rows, err := s.q.DiscardTaskDeadLetter(ctx, gen.DiscardTaskDeadLetterParams{WorkspaceID: ws, ID: id})
	if err != nil {
		return false, err
	}
	return rows > 0, nil
}
