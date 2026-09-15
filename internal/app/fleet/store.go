// Package fleet serves the operator read surface over the per-IP worker fleet:
// which workers this workspace's mail egresses from and how the providers are
// treating them, why a given mailbox sits on the worker it does, and whether the
// deployment's periodic sweeps are still running.
//
// It is READ-ONLY and owns no table. Every row it serves was already being
// written by somebody else and had no reader at all:
//
//   - `workers` + `mailbox_worker_assignments` (migration 000017) — the registry
//     and the pins, read until now only by the placement path itself.
//   - `worker_provider_signals` (migration 20260914150140) — per-worker,
//     per-provider verdict counts, flushed by every worker on a timer.
//   - `fleet_decisions` (same migration) — every automated placement decision
//     with the prose explaining it. Its one read query,
//     ListFleetDecisionsForMailbox, had no production caller before this domain;
//     the query comment says the question it answers, and this package is what
//     asks it.
//   - `scheduled_job_runs` (migration 20260908123022) — which had an Insert and
//     a Purge and NO read query whatsoever. ListScheduledJobHealth is added with
//     this domain; the migration's own comment anticipated it.
//
// WHY THE WHOLE SURFACE IS ADMIN-SESSION-ONLY, not scope-gated: see Routes.
package fleet

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/inroad/inroad/internal/platform/db/gen"
)

// Store is the repository interface this domain depends on — defined here by
// the consumer, satisfied by PgStore below. The sqlc rows are the persistence
// type; there is no parallel entity struct, per the repo's "the interface
// boundary is where the decoupling lives" rule.
//
// Three of the four methods take the workspace as their FIRST argument, so the
// tenant pin is a property of the interface rather than something a caller may
// forget. ScheduledJobs is the deliberate exception and says why on itself.
type Store interface {
	// Workers returns every worker this workspace has a mailbox pinned to, with
	// that workspace's own footprint on each. A workspace with no assignments
	// gets an empty slice, never the fleet.
	Workers(ctx context.Context, ws uuid.UUID) ([]gen.ListWorkspaceFleetWorkersRow, error)
	// ProviderSignals rolls up what the providers told those same workers since
	// `since`, per (worker, provider, operation). Scoped to the same worker set
	// inside the statement, not by an id list the caller assembles.
	ProviderSignals(ctx context.Context, ws uuid.UUID, since time.Time) ([]gen.RollupWorkspaceFleetProviderSignalsRow, error)
	// DecisionsForMailbox returns the placement decisions recorded for one
	// mailbox, newest first. Zero rows means "no decision for that mailbox IN
	// THIS WORKSPACE" — a mailbox belonging to another tenant is
	// indistinguishable from one that has never been placed, which is intended.
	DecisionsForMailbox(ctx context.Context, ws, mailboxID uuid.UUID, limit int32) ([]gen.FleetDecision, error)
	// ScheduledJobs returns one health row per periodic sweep.
	//
	// It takes NO workspace, and that is not an oversight: a periodic reconcile
	// runs once per deployment, `scheduled_job_runs` carries no workspace_id and
	// could not honestly carry one (see the table's migration), so there is
	// nothing to pin. Adding a workspace parameter the query then ignored would
	// be worse than omitting it — it would read like a pin while filtering
	// nothing, which is the exact shape the tenancy guard exists to catch.
	// Authorization for this read is the route's, not the query's: see Routes.
	ScheduledJobs(ctx context.Context, since time.Time) ([]gen.ListScheduledJobHealthRow, error)
}

// PgStore implements Store over the sqlc-generated queries. It is the only
// place in this domain that knows about gen.Queries.
type PgStore struct{ q *gen.Queries }

// NewPgStore builds a PgStore over the given sqlc queries.
func NewPgStore(q *gen.Queries) *PgStore { return &PgStore{q: q} }

var _ Store = (*PgStore)(nil)

func (s *PgStore) Workers(ctx context.Context, ws uuid.UUID) ([]gen.ListWorkspaceFleetWorkersRow, error) {
	return s.q.ListWorkspaceFleetWorkers(ctx, ws)
}

func (s *PgStore) ProviderSignals(ctx context.Context, ws uuid.UUID, since time.Time) ([]gen.RollupWorkspaceFleetProviderSignalsRow, error) {
	return s.q.RollupWorkspaceFleetProviderSignals(ctx, gen.RollupWorkspaceFleetProviderSignalsParams{
		WorkspaceID:  ws,
		SignalsSince: timestamptz(since),
	})
}

func (s *PgStore) DecisionsForMailbox(ctx context.Context, ws, mailboxID uuid.UUID, limit int32) ([]gen.FleetDecision, error) {
	return s.q.ListFleetDecisionsForMailbox(ctx, gen.ListFleetDecisionsForMailboxParams{
		MailboxID:   mailboxID,
		WorkspaceID: ws,
		RowLimit:    limit,
	})
}

func (s *PgStore) ScheduledJobs(ctx context.Context, since time.Time) ([]gen.ListScheduledJobHealthRow, error) {
	return s.q.ListScheduledJobHealth(ctx, timestamptz(since))
}

// timestamptz wraps an always-valid instant for pgx. Every time this domain
// passes down is a computed window bound, never an optional one, so there is no
// NULL case to model.
func timestamptz(t time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: t, Valid: true}
}
