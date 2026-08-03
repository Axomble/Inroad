// Package pulse serves the workspace-wide aggregate read-model behind
// GET /pulse: one small payload answering "is everything okay?" for the
// console chrome (pulse card, nav counts, overview tiles). Read-only —
// this domain owns no table and writes nothing.
package pulse

import (
	"context"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/platform/db/gen"
)

// Store is the repository interface this domain depends on, defined here (by
// the consumer) so the service unit-tests against a fake without a database.
// Every method is pinned to the caller's workspace.
type Store interface {
	MailboxCounts(ctx context.Context, workspaceID uuid.UUID) (gen.GetPulseMailboxCountsRow, error)
	WarmupCounts(ctx context.Context, workspaceID uuid.UUID) (gen.GetPulseWarmupCountsRow, error)
	CampaignCounts(ctx context.Context, workspaceID uuid.UUID) (gen.GetPulseCampaignCountsRow, error)
	ContactCount(ctx context.Context, workspaceID uuid.UUID) (int64, error)
	SentToday(ctx context.Context, workspaceID uuid.UUID) (int64, error)
	SenderCapacities(ctx context.Context, workspaceID uuid.UUID) ([]gen.ListPulseSenderCapacityRow, error)
	DmarcAttention(ctx context.Context, workspaceID uuid.UUID) (gen.GetPulseDmarcAttentionRow, error)
}

// PgStore implements Store by delegating to the sqlc-generated pulse queries;
// it is the only place in this domain that knows about gen.Queries.
type PgStore struct {
	q *gen.Queries
}

func NewPgStore(q *gen.Queries) *PgStore { return &PgStore{q: q} }

func (s *PgStore) MailboxCounts(ctx context.Context, workspaceID uuid.UUID) (gen.GetPulseMailboxCountsRow, error) {
	return s.q.GetPulseMailboxCounts(ctx, workspaceID)
}

func (s *PgStore) WarmupCounts(ctx context.Context, workspaceID uuid.UUID) (gen.GetPulseWarmupCountsRow, error) {
	return s.q.GetPulseWarmupCounts(ctx, workspaceID)
}

func (s *PgStore) CampaignCounts(ctx context.Context, workspaceID uuid.UUID) (gen.GetPulseCampaignCountsRow, error) {
	return s.q.GetPulseCampaignCounts(ctx, workspaceID)
}

func (s *PgStore) ContactCount(ctx context.Context, workspaceID uuid.UUID) (int64, error) {
	return s.q.CountPulseContacts(ctx, workspaceID)
}

func (s *PgStore) SentToday(ctx context.Context, workspaceID uuid.UUID) (int64, error) {
	return s.q.CountPulseSentToday(ctx, workspaceID)
}

func (s *PgStore) SenderCapacities(ctx context.Context, workspaceID uuid.UUID) ([]gen.ListPulseSenderCapacityRow, error) {
	return s.q.ListPulseSenderCapacity(ctx, workspaceID)
}

func (s *PgStore) DmarcAttention(ctx context.Context, workspaceID uuid.UUID) (gen.GetPulseDmarcAttentionRow, error) {
	return s.q.GetPulseDmarcAttention(ctx, workspaceID)
}
