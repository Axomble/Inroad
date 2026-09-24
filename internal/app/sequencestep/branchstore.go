package sequencestep

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inroad/inroad/internal/platform/db/gen"
)

// GraphCheck validates a campaign's routing graph as it would be committed: the
// campaign's steps and branches AFTER a mutation, read inside the mutation's own
// transaction. A non-nil error rolls the mutation back and is returned to the
// caller unchanged.
//
// It is a function rather than a rule baked into the store so the store stays
// policy-free: what makes a graph valid is the service's decision
// (internal/platform/seqgraph), the store only guarantees the check sees exactly
// the graph it is about to commit.
type GraphCheck func(steps []gen.SequenceStep, branches []gen.SequenceStepBranch) error

// BranchInput is one step's router as written. A nil exit ends the path.
type BranchInput struct {
	CampaignID    uuid.UUID
	StepID        uuid.UUID
	Condition     string
	WithinDays    *int32
	ReplyLabelKey *string
	YesStepID     *uuid.UUID
	NoStepID      *uuid.UUID
}

// BranchStore is the persistence seam for routers. A distinct interface from
// Store for the reason VariantStore is one: distinct callers, and it keeps the
// graph rules testable without a database.
type BranchStore interface {
	ListBranches(ctx context.Context, ws, campaignID uuid.UUID) ([]gen.SequenceStepBranch, error)
	// ReplyLabelStops reports whether the workspace's label with this key stops
	// the enrollment; found=false when no such label exists.
	ReplyLabelStops(ctx context.Context, ws uuid.UUID, key string) (stops, found bool, err error)
	// TrackingEnabled reports whether the campaign records opens and clicks.
	TrackingEnabled(ctx context.Context, ws, campaignID uuid.UUID) (bool, error)
	// UpsertBranch creates or replaces the router on in.StepID, then runs check
	// on the result, in one transaction holding the campaign's graph lock.
	UpsertBranch(ctx context.Context, ws uuid.UUID, in BranchInput, check GraphCheck) (gen.SequenceStepBranch, error)
	// DeleteBranch removes the router on stepID (a no-op when there is none),
	// then runs check, in one transaction holding the campaign's graph lock.
	// Removing a router is a graph change too: it restores the step's linear
	// fall-through, which can close a loop.
	DeleteBranch(ctx context.Context, ws, campaignID, stepID uuid.UUID, check GraphCheck) error
}

// ErrBranchConflict is an upsert that matched a router row owned by a different
// workspace or campaign. Unreachable through the service (the step is
// ownership-checked first), and reported rather than swallowed so a future
// caller that skips that check fails closed.
var ErrBranchConflict = errors.New("step branch belongs to another campaign")

// ErrTargetGone is an exit whose target step stopped existing between the
// service's pre-check and the write (a concurrent delete): the composite FK
// refused it. Reported as the same unknown-target failure the pre-check gives.
var ErrTargetGone = errors.New("a branch exit points at a step that is no longer in this campaign")

// foreignKeyViolation is PostgreSQL's SQLSTATE for a refused foreign key.
const foreignKeyViolation = "23503"

// PgBranchStore implements BranchStore over sqlc. The pool backs the
// lock-mutate-check transaction.
type PgBranchStore struct {
	pool *pgxpool.Pool
	q    *gen.Queries
}

func NewPgBranchStore(pool *pgxpool.Pool) *PgBranchStore {
	return &PgBranchStore{pool: pool, q: gen.New(pool)}
}

func (s *PgBranchStore) ListBranches(ctx context.Context, ws, campaignID uuid.UUID) ([]gen.SequenceStepBranch, error) {
	return s.q.ListBranchesByCampaign(ctx, gen.ListBranchesByCampaignParams{CampaignID: campaignID, WorkspaceID: ws})
}

func (s *PgBranchStore) ReplyLabelStops(ctx context.Context, ws uuid.UUID, key string) (stops, found bool, err error) {
	stops, err = s.q.ReplyLabelStopsEnrollment(ctx, gen.ReplyLabelStopsEnrollmentParams{WorkspaceID: ws, Key: key})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, false, nil
	}
	if err != nil {
		return false, false, fmt.Errorf("reply label lookup: %w", err)
	}
	return stops, true, nil
}

func (s *PgBranchStore) TrackingEnabled(ctx context.Context, ws, campaignID uuid.UUID) (bool, error) {
	on, err := s.q.CampaignTrackingEnabled(ctx, gen.CampaignTrackingEnabledParams{ID: campaignID, WorkspaceID: ws})
	if err != nil {
		return false, fmt.Errorf("campaign tracking lookup: %w", err)
	}
	return on, nil
}

func (s *PgBranchStore) UpsertBranch(ctx context.Context, ws uuid.UUID, in BranchInput, check GraphCheck) (gen.SequenceStepBranch, error) {
	var out gen.SequenceStepBranch
	err := withGraphLock(ctx, s.pool, s.q, ws, in.CampaignID, check, func(q *gen.Queries) error {
		row, err := q.UpsertBranch(ctx, gen.UpsertBranchParams{
			StepID: in.StepID, WorkspaceID: ws, CampaignID: in.CampaignID, Condition: in.Condition,
			WithinDays: in.WithinDays, ReplyLabelKey: in.ReplyLabelKey,
			YesStepID: nullUUID(in.YesStepID), NoStepID: nullUUID(in.NoStepID),
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrBranchConflict
		}
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == foreignKeyViolation {
			return ErrTargetGone
		}
		if err != nil {
			return fmt.Errorf("upsert branch: %w", err)
		}
		out = row
		return nil
	})
	return out, err
}

func (s *PgBranchStore) DeleteBranch(ctx context.Context, ws, campaignID, stepID uuid.UUID, check GraphCheck) error {
	return withGraphLock(ctx, s.pool, s.q, ws, campaignID, check, func(q *gen.Queries) error {
		if err := q.DeleteBranch(ctx, gen.DeleteBranchParams{StepID: stepID, CampaignID: campaignID, WorkspaceID: ws}); err != nil {
			return fmt.Errorf("delete branch: %w", err)
		}
		return nil
	})
}

// withGraphLock runs mutate and then check in ONE transaction that first takes
// the campaign's graph lock (LockCampaignGraph). Serializing on the campaign is
// what makes the check sound: two edits that are each acyclic alone can close a
// loop together, and without the lock both would pass their own check against a
// graph that no longer exists by the time they commit. Every write is pinned on
// workspace_id by the queries mutate calls; the lock itself matches zero rows for
// a campaign outside ws, which is reported as ErrCampaignNotFound.
func withGraphLock(ctx context.Context, pool *pgxpool.Pool, q *gen.Queries, ws, campaignID uuid.UUID,
	check GraphCheck, mutate func(q *gen.Queries) error) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin graph tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op once committed
	qtx := q.WithTx(tx)

	if _, err := qtx.LockCampaignGraph(ctx, gen.LockCampaignGraphParams{ID: campaignID, WorkspaceID: ws}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrCampaignNotFound
		}
		return fmt.Errorf("lock campaign graph: %w", err)
	}
	if err := mutate(qtx); err != nil {
		return err
	}
	if check != nil {
		steps, err := qtx.ListStepsByCampaign(ctx, gen.ListStepsByCampaignParams{CampaignID: campaignID, WorkspaceID: ws})
		if err != nil {
			return fmt.Errorf("list steps: %w", err)
		}
		branches, err := qtx.ListBranchesByCampaign(ctx, gen.ListBranchesByCampaignParams{CampaignID: campaignID, WorkspaceID: ws})
		if err != nil {
			return fmt.Errorf("list branches: %w", err)
		}
		if err := check(steps, branches); err != nil {
			return err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit graph tx: %w", err)
	}
	return nil
}

// nullUUID converts an optional id into the nullable column value.
func nullUUID(id *uuid.UUID) pgtype.UUID {
	if id == nil {
		return pgtype.UUID{}
	}
	return pgtype.UUID{Bytes: *id, Valid: true}
}
