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
	pwarmup "github.com/inroad/inroad/internal/platform/warmup"
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
	// ListContentVersionStats returns the workspace's trailing-7-day placement split
	// by which library template produced it — the counts warmup.FoldContentVersions
	// turns into per-version rates.
	ListContentVersionStats(ctx context.Context, workspaceID uuid.UUID) ([]pwarmup.ContentVersionStat, error)
	// ListRoutes returns one mailbox's trailing-7-day placement counters split by
	// the destination each message was delivered to, ordered by destination_esp.
	ListRoutes(ctx context.Context, workspaceID, mailboxID uuid.UUID) ([]RouteRow, error)
	// ListIncidentParticipants returns the workspace's live pool already projected
	// onto the correlation fold's input: whether each participant is degraded, and
	// the most recent RESOLVED value it carries on each observed fault dimension.
	//
	// The platform type crosses the seam deliberately rather than being copied into a
	// third domain struct with the same four fields. It documents itself as "one
	// participant reduced to the facts correlation needs", which is exactly what this
	// read produces, and a domain-owned twin would only be a shape to keep in step.
	ListIncidentParticipants(ctx context.Context, workspaceID uuid.UUID) ([]pwarmup.IncidentInput, error)
	// ListObserverStats returns every mailbox's placement REPORTING record over the
	// trailing 7 days, grouped by (observer, the observer's own receiving provider) —
	// the input warmup.DiscountObservers judges. The platform type crosses the seam
	// for the same reason IncidentInput does: it already documents itself as "one
	// observer reduced to the facts the trust rule needs", and a domain-owned twin
	// with the same four fields would only be a shape to keep in step.
	ListObserverStats(ctx context.Context, workspaceID uuid.UUID) ([]pwarmup.ObserverStats, error)
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

// ListRoutes returns the mailbox's trailing-7-day placement counters grouped by
// the destination its mail was delivered to — the read behind the detail
// endpoint's route matrix. Workspace-pinned, and empty for a mailbox with no
// observations in the window (which the service renders as `routes: []`).
func (s *PgStore) ListRoutes(ctx context.Context, workspaceID, mailboxID uuid.UUID) ([]RouteRow, error) {
	rows, err := s.q.ListWarmupRoutes(ctx, gen.ListWarmupRoutesParams{
		WorkspaceID: workspaceID,
		MailboxID:   mailboxID,
	})
	if err != nil {
		return nil, err
	}
	out := make([]RouteRow, len(rows))
	for i, r := range rows {
		out[i] = routeRowFromGen(r)
	}
	return out, nil
}

// ListIncidentParticipants reads the workspace's live pool and projects it onto the
// correlation fold's input shape.
//
// Degraded is folded HERE, by the one platform predicate that owns the question, so
// no read site ever inlines "state is one of these or lane is one of those". The
// projection is deliberately the same shape as internal/app/pulse's
// WarmupIncidentParticipants, which reads the same query for the attention row: the
// two are sibling app packages and cannot import each other, exactly as the callers of
// warmup.WorstLanesByDomain already duplicate their four-line fold input. Keep them
// in step — the SHARED part is the platform fold, not this loop.
func (s *PgStore) ListIncidentParticipants(ctx context.Context, workspaceID uuid.UUID) ([]pwarmup.IncidentInput, error) {
	rows, err := s.q.ListWarmupIncidentParticipants(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	out := make([]pwarmup.IncidentInput, len(rows))
	for i, r := range rows {
		out[i] = pwarmup.IncidentInput{
			MailboxID:        r.MailboxID.String(),
			Email:            r.Email,
			Degraded:         pwarmup.IncidentDegraded(r.HealthState, r.Lane),
			Route:            r.DestinationEsp,
			SigningDomain:    r.DkimDomain,
			ReturnPathDomain: r.ReturnPathDomain,
		}
	}
	return out, nil
}

// ListObserverStats projects the workspace's observer reporting record onto the
// trust rule's input shape.
//
// The same loop lives in coreapi's snapshot refresh, which reads the same query to
// bind the exclusion array. That is the duplication this package already accepts for
// the incident fold: the SHARED part is the platform detector, and an app package and
// the control<->execution seam cannot import each other's projection.
func (s *PgStore) ListObserverStats(ctx context.Context, workspaceID uuid.UUID) ([]pwarmup.ObserverStats, error) {
	rows, err := s.q.ListWarmupObserverStats(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	out := make([]pwarmup.ObserverStats, 0, len(rows))
	for _, r := range rows {
		// The column is nullable (a deleted observer is SET NULL) even though the
		// query filters those rows out, so it decodes as pgtype.UUID. An invalid one
		// is skipped rather than published as the zero uuid, which would name a
		// mailbox nobody owns in the discounted list.
		if !r.ObserverMailboxID.Valid {
			continue
		}
		out = append(out, pwarmup.ObserverStats{
			ObserverMailboxID: uuid.UUID(r.ObserverMailboxID.Bytes).String(),
			Cohort:            r.DestinationEsp,
			Spam:              int(r.Spam),
			Total:             int(r.Total),
		})
	}
	return out, nil
}

// ListOverviewRows returns one row per participant enriched with the mailbox
// email and the trailing-7-day placement + today's sent counters — the single
// workspace-pinned read behind GET /warmup/overview (no N+1 over the pool).
// ListContentVersionStats projects the grouped rows into the platform type, so the
// fold sees no generated struct and this package owns no rate arithmetic.
func (s *PgStore) ListContentVersionStats(ctx context.Context, workspaceID uuid.UUID) ([]pwarmup.ContentVersionStat, error) {
	rows, err := s.q.ListWarmupContentVersions(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	out := make([]pwarmup.ContentVersionStat, len(rows))
	for i, r := range rows {
		out[i] = pwarmup.ContentVersionStat{
			Version: r.ContentVersion, Inbox: int(r.Inbox7d), Spam: int(r.Spam7d),
		}
	}
	return out, nil
}

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
