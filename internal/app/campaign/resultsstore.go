package campaign

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/inroad/inroad/internal/platform/db/gen"
)

// PgResultsStore implements ResultsStore over the sqlc-generated aggregates.
type PgResultsStore struct{ q *gen.Queries }

// NewPgResultsStore builds the results store.
func NewPgResultsStore(q *gen.Queries) *PgResultsStore { return &PgResultsStore{q: q} }

func (s *PgResultsStore) SendResults(ctx context.Context, ws, campaignID uuid.UUID) ([]SendResultRow, error) {
	rows, err := s.q.CampaignSendResults(ctx, gen.CampaignSendResultsParams{
		CampaignID: campaignID, WorkspaceID: ws,
	})
	if err != nil {
		return nil, fmt.Errorf("campaign send results: %w", err)
	}
	out := make([]SendResultRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, SendResultRow{
			StepOrder: r.StepOrder, VariantID: variantID(r.VariantID),
			Sent: r.Sent, Opens: r.Opens, Clicks: r.Clicks,
		})
	}
	return out, nil
}

func (s *PgResultsStore) OutcomeResults(ctx context.Context, ws, campaignID uuid.UUID) ([]OutcomeResultRow, error) {
	rows, err := s.q.CampaignOutcomeResults(ctx, gen.CampaignOutcomeResultsParams{
		CampaignID: campaignID, WorkspaceID: ws,
	})
	if err != nil {
		return nil, fmt.Errorf("campaign outcome results: %w", err)
	}
	out := make([]OutcomeResultRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, OutcomeResultRow{
			StepOrder: r.StepOrder, VariantID: variantID(r.VariantID),
			StopReason: r.StopReason, Count: r.N,
		})
	}
	return out, nil
}

// VariantLabels reads the sequencestep domain's table through sqlc rather than
// calling that domain's service, for the same reason ListStepVariants above
// does: app packages do not import each other.
func (s *PgResultsStore) VariantLabels(ctx context.Context, ws, campaignID uuid.UUID) (map[uuid.UUID]VariantLabel, error) {
	rows, err := s.q.ListVariantsByCampaign(ctx, gen.ListVariantsByCampaignParams{
		CampaignID: campaignID, WorkspaceID: ws,
	})
	if err != nil {
		return nil, fmt.Errorf("variant labels: %w", err)
	}
	out := make(map[uuid.UUID]VariantLabel, len(rows))
	for _, r := range rows {
		out[r.ID] = VariantLabel{Label: r.Label, Weight: r.Weight}
	}
	return out, nil
}

// variantID lifts the nullable column into the domain's uuid.Nil convention for
// "the step's own base copy" (migration 000053).
func variantID(v pgtype.UUID) uuid.UUID {
	if !v.Valid {
		return uuid.Nil
	}
	return v.Bytes
}
