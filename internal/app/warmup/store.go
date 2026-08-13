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
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/inroad/inroad/internal/platform/db/gen"
)

// ErrMailboxNotInWorkspace is returned by UpsertParticipant when the target
// mailbox does not belong to the caller's workspace. The self-enforcing
// INSERT ... SELECT emits zero rows in that case (pgx.ErrNoRows), which this
// domain surfaces as a distinct sentinel so a future handler can translate it to
// 404 without importing pgx. The package stays self-contained (no coreapi import).
var ErrMailboxNotInWorkspace = errors.New("warmup: mailbox not in workspace")

// Store is the repository interface this domain depends on. It is defined here
// (by the consumer), not by the persistence layer, so the service can be
// unit-tested against a fake without a database. Every tenant method is
// workspace-scoped; the workspace id always comes from the JWT at the handler,
// never from caller-controlled input.
type Store interface {
	UpsertParticipant(ctx context.Context, arg UpsertParams) (Participant, error)
	GetParticipant(ctx context.Context, workspaceID, mailboxID uuid.UUID) (Participant, error)
	DisableParticipant(ctx context.Context, workspaceID, mailboxID uuid.UUID) (int64, error)
	CountEnabledParticipants(ctx context.Context, workspaceID uuid.UUID) (int64, error)
	DailyStats(ctx context.Context, workspaceID, mailboxID uuid.UUID) ([]DayStat, error)
	SentToday(ctx context.Context, workspaceID, mailboxID uuid.UUID) (int32, error)
	ListOverviewRows(ctx context.Context, workspaceID uuid.UUID) ([]OverviewRow, error)
	// MailboxInWorkspace reports whether the mailbox belongs to this workspace.
	// It is the ownership gate for reads whose subject outlives the participant
	// row, where "is a participant" would be the wrong 404 test.
	MailboxInWorkspace(ctx context.Context, workspaceID, mailboxID uuid.UUID) (bool, error)
	// ListTransitions returns one mailbox's decision history, newest first,
	// capped at limit rows.
	ListTransitions(ctx context.Context, workspaceID, mailboxID uuid.UUID, limit int32) ([]Transition, error)
}

// PgStore implements Store by wrapping sqlc-generated queries. It is the only
// place in this domain that knows about gen.Queries or its param structs.
type PgStore struct {
	q *gen.Queries
}

func NewPgStore(q *gen.Queries) *PgStore { return &PgStore{q: q} }

// UpsertParticipant enables warmup for a mailbox or updates its ramp settings.
// The underlying INSERT ... SELECT and ON CONFLICT are both workspace-pinned, so
// a foreign (mailbox, workspace) pair — whether a first insert or a collision on
// an existing row — writes nothing and returns no row. That pgx.ErrNoRows is
// mapped to ErrMailboxNotInWorkspace so the caller can fail closed with a 404.
func (s *PgStore) UpsertParticipant(ctx context.Context, arg UpsertParams) (Participant, error) {
	p, err := s.q.UpsertWarmupParticipant(ctx, gen.UpsertWarmupParticipantParams{
		MailboxID:     arg.MailboxID,
		WorkspaceID:   arg.WorkspaceID,
		StartVolume:   arg.StartVolume,
		MaxVolume:     arg.MaxVolume,
		RampIncrement: arg.RampIncrement,
		ReplyRate:     arg.ReplyRate,
	})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return Participant{}, ErrMailboxNotInWorkspace
	case err != nil:
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

func (s *PgStore) SentToday(ctx context.Context, workspaceID, mailboxID uuid.UUID) (int32, error) {
	return s.q.GetWarmupSentToday(ctx, gen.GetWarmupSentTodayParams{
		MailboxID:   mailboxID,
		WorkspaceID: workspaceID,
	})
}

func (s *PgStore) MailboxInWorkspace(ctx context.Context, workspaceID, mailboxID uuid.UUID) (bool, error) {
	return s.q.WarmupMailboxInWorkspace(ctx, gen.WarmupMailboxInWorkspaceParams{
		ID:          mailboxID,
		WorkspaceID: workspaceID,
	})
}

func (s *PgStore) ListTransitions(ctx context.Context, workspaceID, mailboxID uuid.UUID, limit int32) ([]Transition, error) {
	rows, err := s.q.ListWarmupTransitions(ctx, gen.ListWarmupTransitionsParams{
		WorkspaceID: workspaceID,
		MailboxID:   mailboxID,
		RowLimit:    limit,
	})
	if err != nil {
		return nil, err
	}
	out := make([]Transition, len(rows))
	for i, r := range rows {
		out[i] = transitionFromGen(r)
	}
	return out, nil
}

// ListOverviewRows returns one row per participant enriched with the mailbox
// email and the trailing-7-day placement + today's sent counters — the single
// workspace-pinned read behind GET /warmup/overview (no N+1 over the pool).
func (s *PgStore) ListOverviewRows(ctx context.Context, workspaceID uuid.UUID) ([]OverviewRow, error) {
	rows, err := s.q.ListWarmupOverviewRows(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	out := make([]OverviewRow, len(rows))
	for i, r := range rows {
		out[i] = overviewRowFromGen(r)
	}
	return out, nil
}
