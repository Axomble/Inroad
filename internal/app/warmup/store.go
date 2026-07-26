// Package warmup is the control-plane domain that owns mailbox warmup state
// and policy. Following the reference pattern in internal/app/mailbox, the
// domain defines its own repository interface (Store) and the service depends
// on that interface, never on the concrete sqlc-backed struct (dependency
// inversion, trivially unit-testable against a fake).
//
// This file is the persistence layer only (spec §3). Service/handler/routes
// land in later build steps.
package warmup

import (
	"context"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/platform/db/gen"
)

// Store is the repository interface this domain depends on. It is defined here
// (by the consumer), not by the persistence layer, so the service can be
// unit-tested against a fake without a database. Every tenant method is
// workspace-scoped; the workspace id always comes from the JWT at the handler,
// never from caller-controlled input.
type Store interface {
	UpsertParticipant(ctx context.Context, arg UpsertParams) (Participant, error)
	GetParticipant(ctx context.Context, workspaceID, mailboxID uuid.UUID) (Participant, error)
	ListParticipants(ctx context.Context, workspaceID uuid.UUID) ([]Participant, error)
	DisableParticipant(ctx context.Context, workspaceID, mailboxID uuid.UUID) (int64, error)
	CountEnabledParticipants(ctx context.Context, workspaceID uuid.UUID) (int64, error)
	DailyStats(ctx context.Context, workspaceID, mailboxID uuid.UUID) ([]DayStat, error)
	PlacementRates7d(ctx context.Context, workspaceID uuid.UUID) ([]PlacementRate, error)
	SentToday(ctx context.Context, workspaceID, mailboxID uuid.UUID) (int32, error)
}

// PgStore implements Store by wrapping sqlc-generated queries. It is the only
// place in this domain that knows about gen.Queries or its param structs.
type PgStore struct {
	q *gen.Queries
}

func NewPgStore(q *gen.Queries) *PgStore { return &PgStore{q: q} }

// UpsertParticipant enables warmup for a mailbox or updates its ramp settings.
// The underlying query's ON CONFLICT is workspace-pinned, so a cross-workspace
// mailbox_id never updates another tenant's row.
func (s *PgStore) UpsertParticipant(ctx context.Context, arg UpsertParams) (Participant, error) {
	p, err := s.q.UpsertWarmupParticipant(ctx, gen.UpsertWarmupParticipantParams{
		MailboxID:     arg.MailboxID,
		WorkspaceID:   arg.WorkspaceID,
		StartVolume:   arg.StartVolume,
		MaxVolume:     arg.MaxVolume,
		RampIncrement: arg.RampIncrement,
		ReplyRate:     arg.ReplyRate,
	})
	if err != nil {
		return Participant{}, err
	}
	return participantFromGen(p), nil
}

func (s *PgStore) GetParticipant(ctx context.Context, workspaceID, mailboxID uuid.UUID) (Participant, error) {
	p, err := s.q.GetWarmupParticipant(ctx, gen.GetWarmupParticipantParams{
		MailboxID:   mailboxID,
		WorkspaceID: workspaceID,
	})
	if err != nil {
		return Participant{}, err
	}
	return participantFromGen(p), nil
}

func (s *PgStore) ListParticipants(ctx context.Context, workspaceID uuid.UUID) ([]Participant, error) {
	rows, err := s.q.ListWarmupParticipants(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	out := make([]Participant, len(rows))
	for i, p := range rows {
		out[i] = participantFromGen(p)
	}
	return out, nil
}

func (s *PgStore) DisableParticipant(ctx context.Context, workspaceID, mailboxID uuid.UUID) (int64, error) {
	return s.q.DisableWarmupParticipant(ctx, gen.DisableWarmupParticipantParams{
		MailboxID:   mailboxID,
		WorkspaceID: workspaceID,
	})
}

func (s *PgStore) CountEnabledParticipants(ctx context.Context, workspaceID uuid.UUID) (int64, error) {
	return s.q.CountEnabledParticipants(ctx, workspaceID)
}

func (s *PgStore) DailyStats(ctx context.Context, workspaceID, mailboxID uuid.UUID) ([]DayStat, error) {
	rows, err := s.q.GetWarmupDailyStats(ctx, gen.GetWarmupDailyStatsParams{
		MailboxID:   mailboxID,
		WorkspaceID: workspaceID,
	})
	if err != nil {
		return nil, err
	}
	out := make([]DayStat, len(rows))
	for i, r := range rows {
		out[i] = dayStatFromGen(r)
	}
	return out, nil
}

func (s *PgStore) PlacementRates7d(ctx context.Context, workspaceID uuid.UUID) ([]PlacementRate, error) {
	rows, err := s.q.GetWarmupPlacementRates7d(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	out := make([]PlacementRate, len(rows))
	for i, r := range rows {
		out[i] = placementRateFromGen(r)
	}
	return out, nil
}

func (s *PgStore) SentToday(ctx context.Context, workspaceID, mailboxID uuid.UUID) (int32, error) {
	return s.q.GetWarmupSentToday(ctx, gen.GetWarmupSentTodayParams{
		MailboxID:   mailboxID,
		WorkspaceID: workspaceID,
	})
}
