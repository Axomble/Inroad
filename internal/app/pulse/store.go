// Package pulse serves the workspace-wide aggregate read-model behind
// GET /pulse: one small payload answering "is everything okay?" for the
// console chrome (pulse card, nav counts, overview tiles). Read-only —
// this domain owns no table and writes nothing.
package pulse

import (
	"context"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/platform/db/gen"
	"github.com/inroad/inroad/internal/platform/warmup"
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
	InboxCounts(ctx context.Context, workspaceID uuid.UUID) (gen.GetInboxPulseCountsRow, error)
	// WarmupIncidentParticipants is the pool the senders_gated row's cause is inferred
	// from: each live participant's degradation and the fault-dimension values it
	// carries.
	//
	// It reads the WARMUP domain's query rather than a pulse-owned copy of it. pulse
	// and warmup are sibling app packages and must not import each other, so the seam
	// is the shared platform fold (warmup.DetectIncidents) plus the one query — exactly
	// how this domain already gets its warmup counts, and how the two callers of
	// warmup.WorstLanesByDomain already share one lane query. A second SQL definition
	// of "which mail counts as this mailbox's" is how the pulse card and the warmup
	// page would come to disagree about the same pool.
	WarmupIncidentParticipants(ctx context.Context, workspaceID uuid.UUID) ([]warmup.IncidentInput, error)
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

func (s *PgStore) InboxCounts(ctx context.Context, workspaceID uuid.UUID) (gen.GetInboxPulseCountsRow, error) {
	return s.q.GetInboxPulseCounts(ctx, workspaceID)
}

// WarmupIncidentParticipants projects the warmup pool onto the correlation fold's
// input. Degraded is folded by the platform predicate that owns the question, so this
// package never inlines its own opinion about which states and lanes count.
//
// The loop is intentionally the same shape as internal/app/warmup's PgStore method of
// the same purpose; the two cannot share it (sibling app packages) and the part worth
// sharing — the fold — is shared.
func (s *PgStore) WarmupIncidentParticipants(ctx context.Context, workspaceID uuid.UUID) ([]warmup.IncidentInput, error) {
	rows, err := s.q.ListWarmupIncidentParticipants(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	out := make([]warmup.IncidentInput, len(rows))
	for i, r := range rows {
		out[i] = warmup.IncidentInput{
			MailboxID:        r.MailboxID.String(),
			Email:            r.Email,
			Degraded:         warmup.IncidentDegraded(r.HealthState, r.Lane),
			Route:            r.DestinationEsp,
			SigningDomain:    r.DkimDomain,
			ReturnPathDomain: r.ReturnPathDomain,
		}
	}
	return out, nil
}
