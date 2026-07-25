// Package sequencestep manages the ordered steps of a campaign's sequence:
// CRUD scoped to the workspace, with structural edits (create/delete) gated on
// the campaign being draft and content edits allowed live (steps are read at
// send time — see the multi-step sequencing spec §2).
package sequencestep

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inroad/inroad/internal/platform/db/gen"
)

// CreateInput carries the fields needed to append a step. StepOrder is
// assigned by the service (max+1), not the caller.
type CreateInput struct {
	CampaignID   uuid.UUID
	StepOrder    int32
	DelaySeconds int32
	Subject      string
	BodyText     string
	BodyHTML     string
}

// UpdateInput carries the editable (content) fields of a step. step_order is
// intentionally not editable here — reordering is a structural change handled
// separately (and forbidden on a running campaign).
type UpdateInput struct {
	StepID       uuid.UUID
	DelaySeconds int32
	Subject      string
	BodyText     string
	BodyHTML     string
}

// Store is the repository interface this domain depends on (defined by the
// consumer for testability), backed by sqlc.
type Store interface {
	Create(ctx context.Context, ws uuid.UUID, in CreateInput) (gen.SequenceStep, error)
	Get(ctx context.Context, ws, id uuid.UUID) (gen.SequenceStep, error)
	List(ctx context.Context, ws, campaignID uuid.UUID) ([]gen.SequenceStep, error)
	Update(ctx context.Context, ws uuid.UUID, in UpdateInput) (gen.SequenceStep, error)
	Delete(ctx context.Context, ws, id uuid.UUID) error
	MaxStepOrder(ctx context.Context, ws, campaignID uuid.UUID) (int32, error)
	// Reorder rewrites step_order to 1..N to match stepIDs' order, in one
	// transaction, and returns the campaign's steps in the new order. Every
	// write is pinned by campaign_id AND workspace_id. Callers must pre-validate
	// that stepIDs is a permutation of the campaign's current step ids.
	Reorder(ctx context.Context, ws, campaignID uuid.UUID, stepIDs []uuid.UUID) ([]gen.SequenceStep, error)
}

// CampaignChecker reports a campaign's status (and existence) within the
// workspace. Implemented in cmd/inroad wiring by an adapter over the campaign
// store, so this domain doesn't import the campaign package (app/* isolation).
type CampaignChecker interface {
	CampaignStatus(ctx context.Context, ws, campaignID uuid.UUID) (string, error)
}

// PgStore implements Store over the sqlc-generated queries. The pool backs
// Reorder's multi-statement transaction; every other method flows through the
// pool-bound *gen.Queries.
type PgStore struct {
	pool *pgxpool.Pool
	q    *gen.Queries
}

func NewPgStore(pool *pgxpool.Pool) *PgStore { return &PgStore{pool: pool, q: gen.New(pool)} }

func (s *PgStore) Create(ctx context.Context, ws uuid.UUID, in CreateInput) (gen.SequenceStep, error) {
	return s.q.CreateStep(ctx, gen.CreateStepParams{
		WorkspaceID: ws, CampaignID: in.CampaignID, StepOrder: in.StepOrder,
		DelaySeconds: in.DelaySeconds, Subject: in.Subject, BodyText: in.BodyText, BodyHtml: in.BodyHTML,
	})
}
func (s *PgStore) Get(ctx context.Context, ws, id uuid.UUID) (gen.SequenceStep, error) {
	return s.q.GetStep(ctx, gen.GetStepParams{ID: id, WorkspaceID: ws})
}
func (s *PgStore) List(ctx context.Context, ws, campaignID uuid.UUID) ([]gen.SequenceStep, error) {
	return s.q.ListStepsByCampaign(ctx, gen.ListStepsByCampaignParams{CampaignID: campaignID, WorkspaceID: ws})
}
func (s *PgStore) Update(ctx context.Context, ws uuid.UUID, in UpdateInput) (gen.SequenceStep, error) {
	return s.q.UpdateStep(ctx, gen.UpdateStepParams{
		ID: in.StepID, WorkspaceID: ws, DelaySeconds: in.DelaySeconds,
		Subject: in.Subject, BodyText: in.BodyText, BodyHtml: in.BodyHTML,
	})
}
func (s *PgStore) Delete(ctx context.Context, ws, id uuid.UUID) error {
	return s.q.DeleteStep(ctx, gen.DeleteStepParams{ID: id, WorkspaceID: ws})
}
func (s *PgStore) MaxStepOrder(ctx context.Context, ws, campaignID uuid.UUID) (int32, error) {
	return s.q.MaxStepOrder(ctx, gen.MaxStepOrderParams{CampaignID: campaignID, WorkspaceID: ws})
}

// Reorder rewrites step_order to 1..N in one transaction. Because
// UNIQUE(campaign_id, step_order) is checked per-row, it first shifts every
// step clear of the target range (by the current max), then stamps each id its
// final 1-based position. All writes are pinned by campaign_id AND
// workspace_id, so a foreign id would update zero rows (belt-and-braces on top
// of the service's permutation check). Either every write commits or none does.
func (s *PgStore) Reorder(ctx context.Context, ws, campaignID uuid.UUID, stepIDs []uuid.UUID) ([]gen.SequenceStep, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin reorder tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op once committed
	qtx := s.q.WithTx(tx)

	maxOrder, err := qtx.MaxStepOrder(ctx, gen.MaxStepOrderParams{CampaignID: campaignID, WorkspaceID: ws})
	if err != nil {
		return nil, fmt.Errorf("max step order: %w", err)
	}
	if err := qtx.ShiftStepOrders(ctx, gen.ShiftStepOrdersParams{
		CampaignID: campaignID, WorkspaceID: ws, StepOrder: maxOrder,
	}); err != nil {
		return nil, fmt.Errorf("shift step orders: %w", err)
	}
	for i, id := range stepIDs {
		if err := qtx.SetStepOrder(ctx, gen.SetStepOrderParams{
			ID: id, CampaignID: campaignID, WorkspaceID: ws, StepOrder: int32(i + 1),
		}); err != nil {
			return nil, fmt.Errorf("set step order: %w", err)
		}
	}
	steps, err := qtx.ListStepsByCampaign(ctx, gen.ListStepsByCampaignParams{CampaignID: campaignID, WorkspaceID: ws})
	if err != nil {
		return nil, fmt.Errorf("list steps: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit reorder tx: %w", err)
	}
	return steps, nil
}
