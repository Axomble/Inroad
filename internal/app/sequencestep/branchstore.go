package sequencestep

import (
	"context"
	"errors"
	"fmt"
	"time"

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
	// Expect is the write's optimistic-concurrency precondition. The zero value
	// is none: the write replaces whatever is there, as it always has.
	Expect BranchPrecondition
}

// BranchPrecondition is what a branch write expects to find on the step before
// it applies, so two editors cannot silently overwrite each other. The zero
// value expects nothing (last writer wins); ExpectNoBranch and ExpectBranchAt
// are the two real preconditions. A struct with unexported fields rather than a
// *time.Time so "no precondition" and "expect no branch" cannot be confused.
type BranchPrecondition struct {
	kind      preconditionKind
	updatedAt time.Time
}

type preconditionKind int

const (
	preconditionNone preconditionKind = iota
	preconditionAbsent
	preconditionAt
)

// ExpectNoBranch applies the write only if the step has no branch: a create
// that must not overwrite a branch someone else just made.
func ExpectNoBranch() BranchPrecondition { return BranchPrecondition{kind: preconditionAbsent} }

// ExpectBranchAt applies the write only if the step's branch exists and its
// updated_at is exactly at (microsecond precision, as stored).
func ExpectBranchAt(at time.Time) BranchPrecondition {
	return BranchPrecondition{kind: preconditionAt, updatedAt: at}
}

// BranchChangedError is a write whose precondition no longer holds: the branch
// was created, replaced or removed since the client read it. Current is the
// branch as it is now (nil when the step has none), read in the same
// transaction that refused the write, so the client can show what won.
type BranchChangedError struct {
	Current *gen.SequenceStepBranch
}

func (e *BranchChangedError) Error() string {
	return "the branch was changed since it was loaded; reload it and try again"
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
	// on the result, in one transaction holding the campaign's graph lock. A
	// precondition in in.Expect that does not hold is a *BranchChangedError and
	// writes nothing.
	UpsertBranch(ctx context.Context, ws uuid.UUID, in BranchInput, check GraphCheck) (gen.SequenceStepBranch, error)
	// DeleteBranch removes the router on stepID (a no-op when there is none),
	// then runs check, in one transaction holding the campaign's graph lock.
	// Removing a router is a graph change too: it restores the step's linear
	// fall-through, which can close a loop. A precondition that does not hold is
	// a *BranchChangedError and removes nothing.
	DeleteBranch(ctx context.Context, ws, campaignID, stepID uuid.UUID, expect BranchPrecondition, check GraphCheck) error
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

// UpsertBranch enforces in.Expect in the write itself — a conditional INSERT or
// UPDATE, not a read followed by a write — so the precondition and the change
// are one statement, under the graph lock besides.
func (s *PgBranchStore) UpsertBranch(ctx context.Context, ws uuid.UUID, in BranchInput, check GraphCheck) (gen.SequenceStepBranch, error) {
	var out gen.SequenceStepBranch
	err := withGraphLock(ctx, s.pool, s.q, ws, in.CampaignID, check, func(q *gen.Queries) error {
		row, err := writeBranch(ctx, q, ws, in)
		if errors.Is(err, pgx.ErrNoRows) {
			return refusedWrite(ctx, q, ws, in)
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

// writeBranch runs the one statement in.Expect calls for. Each returns
// pgx.ErrNoRows when it writes nothing.
func writeBranch(ctx context.Context, q *gen.Queries, ws uuid.UUID, in BranchInput) (gen.SequenceStepBranch, error) {
	yes, no := nullUUID(in.YesStepID), nullUUID(in.NoStepID)
	switch in.Expect.kind {
	case preconditionAbsent:
		return q.InsertBranchIfAbsent(ctx, gen.InsertBranchIfAbsentParams{
			StepID: in.StepID, WorkspaceID: ws, CampaignID: in.CampaignID, Condition: in.Condition,
			WithinDays: in.WithinDays, ReplyLabelKey: in.ReplyLabelKey, YesStepID: yes, NoStepID: no,
		})
	case preconditionAt:
		return q.UpdateBranchIfUnchanged(ctx, gen.UpdateBranchIfUnchangedParams{
			StepID: in.StepID, WorkspaceID: ws, CampaignID: in.CampaignID, Condition: in.Condition,
			WithinDays: in.WithinDays, ReplyLabelKey: in.ReplyLabelKey, YesStepID: yes, NoStepID: no,
			ExpectedUpdatedAt: timestamptz(in.Expect.updatedAt),
		})
	default:
		return q.UpsertBranch(ctx, gen.UpsertBranchParams{
			StepID: in.StepID, WorkspaceID: ws, CampaignID: in.CampaignID, Condition: in.Condition,
			WithinDays: in.WithinDays, ReplyLabelKey: in.ReplyLabelKey, YesStepID: yes, NoStepID: no,
		})
	}
}

// refusedWrite explains a write that matched no row. With a precondition that
// is the precondition failing, reported with the branch as it now is. Without
// one — or when "expect no branch" collided with a row this workspace cannot
// see — it is the tenant pin refusing another owner's row.
func refusedWrite(ctx context.Context, q *gen.Queries, ws uuid.UUID, in BranchInput) error {
	if in.Expect.kind == preconditionNone {
		return ErrBranchConflict
	}
	current, err := currentBranch(ctx, q, ws, in.CampaignID, in.StepID)
	if err != nil {
		return err
	}
	if current == nil && in.Expect.kind == preconditionAbsent {
		return ErrBranchConflict
	}
	return &BranchChangedError{Current: current}
}

// currentBranch reads one step's router inside the caller's transaction; nil
// when the step has none.
func currentBranch(ctx context.Context, q *gen.Queries, ws, campaignID, stepID uuid.UUID) (*gen.SequenceStepBranch, error) {
	row, err := q.GetBranch(ctx, gen.GetBranchParams{StepID: stepID, CampaignID: campaignID, WorkspaceID: ws})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read current branch: %w", err)
	}
	return &row, nil
}

// DeleteBranch, like UpsertBranch, puts an "expect this version" precondition in
// the DELETE's own WHERE clause.
func (s *PgBranchStore) DeleteBranch(ctx context.Context, ws, campaignID, stepID uuid.UUID, expect BranchPrecondition, check GraphCheck) error {
	return withGraphLock(ctx, s.pool, s.q, ws, campaignID, check, func(q *gen.Queries) error {
		switch expect.kind {
		case preconditionAt:
			n, err := q.DeleteBranchIfUnchanged(ctx, gen.DeleteBranchIfUnchangedParams{
				StepID: stepID, CampaignID: campaignID, WorkspaceID: ws, ExpectedUpdatedAt: timestamptz(expect.updatedAt),
			})
			if err != nil {
				return fmt.Errorf("delete branch: %w", err)
			}
			if n == 0 {
				return changedSince(ctx, q, ws, campaignID, stepID, false)
			}
			return nil
		case preconditionAbsent:
			// Expecting no branch, a delete has nothing to remove; it holds iff
			// there is still none, which the graph lock keeps true to commit.
			return changedSince(ctx, q, ws, campaignID, stepID, true)
		default:
			if err := q.DeleteBranch(ctx, gen.DeleteBranchParams{StepID: stepID, CampaignID: campaignID, WorkspaceID: ws}); err != nil {
				return fmt.Errorf("delete branch: %w", err)
			}
			return nil
		}
	})
}

// changedSince reports a refused precondition with the branch as it now is (nil
// when the step has none). absentIsFine is for "expect no branch", where
// finding none means the precondition held after all.
func changedSince(ctx context.Context, q *gen.Queries, ws, campaignID, stepID uuid.UUID, absentIsFine bool) error {
	current, err := currentBranch(ctx, q, ws, campaignID, stepID)
	if err != nil {
		return err
	}
	if current == nil && absentIsFine {
		return nil
	}
	return &BranchChangedError{Current: current}
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

func timestamptz(t time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: t, Valid: true}
}
