package sequencestep

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/inroad/inroad/internal/platform/db/gen"
)

// uniqueViolation is Postgres 23505. UNIQUE (step_id, label) is what actually
// decides whether a label is free, so a pre-check SELECT would only widen the
// race rather than close it; this translates the constraint instead.
const uniqueViolation = "23505"

// PgVariantStore implements VariantStore over the sqlc-generated queries.
type PgVariantStore struct{ q *gen.Queries }

// NewPgVariantStore builds the variant store.
func NewPgVariantStore(q *gen.Queries) *PgVariantStore { return &PgVariantStore{q: q} }

func (s *PgVariantStore) Get(ctx context.Context, ws, id uuid.UUID) (Variant, error) {
	row, err := s.q.GetStepVariant(ctx, gen.GetStepVariantParams{ID: id, WorkspaceID: ws})
	if errors.Is(err, pgx.ErrNoRows) {
		return Variant{}, ErrVariantNotFound
	}
	if err != nil {
		return Variant{}, fmt.Errorf("get step variant: %w", err)
	}
	return Variant{
		ID: row.ID, StepID: row.StepID, Label: row.Label, Weight: row.Weight,
		Subject: row.Subject, BodyText: row.BodyText, BodyHTML: row.BodyHtml,
	}, nil
}

func (s *PgVariantStore) ListForStep(ctx context.Context, ws, stepID uuid.UUID) ([]Variant, error) {
	rows, err := s.q.ListStepVariants(ctx, gen.ListStepVariantsParams{StepID: stepID, WorkspaceID: ws})
	if err != nil {
		return nil, fmt.Errorf("list step variants: %w", err)
	}
	out := make([]Variant, 0, len(rows))
	for _, r := range rows {
		out = append(out, Variant{
			ID: r.ID, StepID: r.StepID, Label: r.Label, Weight: r.Weight,
			Subject: r.Subject, BodyText: r.BodyText, BodyHTML: r.BodyHtml,
		})
	}
	return out, nil
}

// ListForCampaign returns every variant in the campaign, keyed by step id, so
// the step editor renders the whole sequence from one round trip instead of one
// query per step.
func (s *PgVariantStore) ListForCampaign(ctx context.Context, ws, campaignID uuid.UUID) (map[uuid.UUID][]Variant, error) {
	rows, err := s.q.ListVariantsByCampaign(ctx, gen.ListVariantsByCampaignParams{
		CampaignID: campaignID, WorkspaceID: ws,
	})
	if err != nil {
		return nil, fmt.Errorf("list campaign variants: %w", err)
	}
	out := make(map[uuid.UUID][]Variant, len(rows))
	for _, r := range rows {
		out[r.StepID] = append(out[r.StepID], Variant{
			ID: r.ID, StepID: r.StepID, Label: r.Label, Weight: r.Weight,
			Subject: r.Subject, BodyText: r.BodyText, BodyHTML: r.BodyHtml,
		})
	}
	return out, nil
}

func (s *PgVariantStore) Create(ctx context.Context, ws, stepID uuid.UUID, in VariantInput) (Variant, error) {
	row, err := s.q.CreateStepVariant(ctx, gen.CreateStepVariantParams{
		WorkspaceID: ws, StepID: stepID, Label: in.Label, Weight: in.Weight,
		Subject: in.Subject, BodyText: in.BodyText, BodyHtml: in.BodyHTML,
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
			return Variant{}, ErrLabelTaken
		}
		return Variant{}, fmt.Errorf("create step variant: %w", err)
	}
	return Variant{
		ID: row.ID, StepID: row.StepID, Label: row.Label, Weight: row.Weight,
		Subject: row.Subject, BodyText: row.BodyText, BodyHTML: row.BodyHtml,
	}, nil
}

func (s *PgVariantStore) Update(ctx context.Context, ws, id uuid.UUID, in VariantInput) (Variant, error) {
	row, err := s.q.UpdateStepVariant(ctx, gen.UpdateStepVariantParams{
		ID: id, WorkspaceID: ws, Label: in.Label, Weight: in.Weight,
		Subject: in.Subject, BodyText: in.BodyText, BodyHtml: in.BodyHTML,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Variant{}, ErrVariantNotFound
	}
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
			return Variant{}, ErrLabelTaken
		}
		return Variant{}, fmt.Errorf("update step variant: %w", err)
	}
	return Variant{
		ID: row.ID, StepID: row.StepID, Label: row.Label, Weight: row.Weight,
		Subject: row.Subject, BodyText: row.BodyText, BodyHTML: row.BodyHtml,
	}, nil
}

func (s *PgVariantStore) Delete(ctx context.Context, ws, id uuid.UUID) error {
	n, err := s.q.DeleteStepVariant(ctx, gen.DeleteStepVariantParams{ID: id, WorkspaceID: ws})
	if err != nil {
		return fmt.Errorf("delete step variant: %w", err)
	}
	if n == 0 {
		return ErrVariantNotFound
	}
	return nil
}

func (s *PgVariantStore) SetBaseWeight(ctx context.Context, ws, stepID uuid.UUID, weight int32) error {
	n, err := s.q.SetStepVariantWeight(ctx, gen.SetStepVariantWeightParams{
		ID: stepID, WorkspaceID: ws, VariantWeight: weight,
	})
	if err != nil {
		return fmt.Errorf("set base variant weight: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *PgVariantStore) SentCount(ctx context.Context, ws, variantID uuid.UUID) (int64, error) {
	n, err := s.q.CountVariantsSent(ctx, gen.CountVariantsSentParams{
		WorkspaceID: ws, VariantID: pgtype.UUID{Bytes: variantID, Valid: true},
	})
	if err != nil {
		return 0, fmt.Errorf("count variant sends: %w", err)
	}
	return n, nil
}
