package fleet

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/platform/db/gen"
)

// liveWindow is how recently a worker must have heartbeated to be reported
// live.
//
// It MIRRORS the placement path's own window (workerLiveWindow in
// internal/coreapi/inprocess) and must keep mirroring it. That constant is
// unexported and sits in the execution-plane seam, which an app/* domain does
// not import, so the value is restated rather than shared — and restating it is
// the lesser evil, because the alternative is a screen whose "live" disagrees
// with the assigner's. A worker this page calls live while the assigner treats
// it as dead is the single most misleading thing this surface could say: the
// operator would see a healthy row for a worker no mailbox will ever be placed
// on again.
//
// The value itself: a worker heartbeats every 5 minutes (cmd/worker), so 15
// minutes tolerates two missed ticks before its mailboxes become eligible for
// reassignment.
const liveWindow = 15 * time.Minute

// Window bounds for the two time-ranged reads. Both are clamped rather than
// rejected, following the same rule the dead-letter list uses for its page size:
// a window the server picked for you is still an answer to the question you
// asked, whereas a 422 on a typo'd query parameter is a screen that will not
// load.
//
// The maximum is the retention of the shorter-lived of the two tables
// (worker_provider_signals purges at 30 days, scheduled_job_runs at 30 days), so
// asking for more than this can only ever return the same rows while scanning
// more of the index. It is a cap on wasted work, not a policy.
const (
	defaultWindow = 24 * time.Hour
	minWindow     = time.Hour
	maxWindow     = 30 * 24 * time.Hour
)

// Decision-page bounds. Small: this is one mailbox's placement history, and the
// question ("why is it here?") is answered by the most recent handful. The cap
// is what stops a caller asking for the whole 90-day retention in one response.
const (
	defaultDecisionLimit = 50
	maxDecisionLimit     = 200
)

// Service holds this domain's read policy: what a window means, what counts as
// live, and how the two halves of a worker's health get stitched together. It
// depends on the Store interface, never on the sqlc-backed struct.
type Service struct {
	store Store
	now   func() time.Time
}

// ServiceOption customises a Service at construction.
type ServiceOption func(*Service)

// WithClock replaces the time source, so liveness and window arithmetic are
// assertable without sleeping.
func WithClock(now func() time.Time) ServiceOption {
	return func(s *Service) { s.now = now }
}

// NewService builds the read service over a Store.
func NewService(store Store, opts ...ServiceOption) *Service {
	s := &Service{store: store, now: time.Now}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// WorkerHealth is one worker as this surface reports it: the persistence row
// exactly as stored, plus the two things the service computed over it.
//
// Embedding rather than restating the seven stored fields is deliberate — a
// parallel struct listing them again is the "hand-copy a shape that already
// exists" duplication the repo's rules call out, and it would drift the first
// time a column is added.
type WorkerHealth struct {
	gen.ListWorkspaceFleetWorkersRow
	// Live is LastSeenAt measured against liveWindow. Reported alongside the raw
	// timestamp, never instead of it: the verdict is what an operator scans for,
	// the timestamp is what they need when the verdict surprises them.
	Live bool
	// Signals is this worker's provider rollup over the requested window, one
	// entry per (provider, operation) that recorded anything. Empty means the
	// worker reported no verdict at all in the window — which is a real and
	// distinct state from "reported only failures", and the reason this is an
	// empty slice rather than a zeroed row.
	Signals []gen.RollupWorkspaceFleetProviderSignalsRow
}

// Workers answers "is my fleet healthy?" for one workspace: every worker it has
// a mailbox pinned to, with liveness and the provider-signal rollup over
// `window`.
//
// The two reads are sequential rather than concurrent on purpose. They are both
// indexed lookups over a fleet that is tens of rows, the second is scoped by the
// same workspace predicate as the first, and running them in parallel would buy
// microseconds in exchange for a goroutine and an error-joining path on a screen
// nobody loads in a loop.
func (s *Service) Workers(ctx context.Context, ws uuid.UUID, window time.Duration) ([]WorkerHealth, error) {
	window = clampWindow(window)
	rows, err := s.store.Workers(ctx, ws)
	if err != nil {
		return nil, fmt.Errorf("fleet: list workers: %w", err)
	}
	// Short-circuit before the second query: a workspace with no assignments has
	// no worker set for the signal rollup to be scoped to, so that read could
	// only ever return nothing.
	if len(rows) == 0 {
		return []WorkerHealth{}, nil
	}

	now := s.now()
	signals, err := s.store.ProviderSignals(ctx, ws, now.Add(-window))
	if err != nil {
		return nil, fmt.Errorf("fleet: roll up provider signals: %w", err)
	}
	byWorker := make(map[string][]gen.RollupWorkspaceFleetProviderSignalsRow, len(rows))
	for _, sig := range signals {
		byWorker[sig.WorkerID] = append(byWorker[sig.WorkerID], sig)
	}

	liveSince := now.Add(-liveWindow)
	out := make([]WorkerHealth, 0, len(rows))
	for _, row := range rows {
		out = append(out, WorkerHealth{
			ListWorkspaceFleetWorkersRow: row,
			// !Before rather than After, so a heartbeat landing exactly on the
			// boundary counts as live — matching the query the assigner uses,
			// which is `last_seen_at >= live_since`.
			Live:    row.LastSeenAt.Valid && !row.LastSeenAt.Time.Before(liveSince),
			Signals: byWorker[row.WorkerID],
		})
	}
	return out, nil
}

// DecisionsForMailbox answers "why is this mailbox on this worker?" — the
// question fleet_decisions was created to make answerable, and the caller
// ListFleetDecisionsForMailbox was written for and did not have.
//
// Zero rows is a legitimate answer, not a not-found: a mailbox that has never
// been placed (never sent, so never resolved a queue) has no decisions, and so
// does a mailbox belonging to another tenant. Collapsing those two into a 404
// would both lie about the first and turn this into an existence oracle for the
// second.
func (s *Service) DecisionsForMailbox(ctx context.Context, ws, mailboxID uuid.UUID, limit int32) ([]gen.FleetDecision, error) {
	rows, err := s.store.DecisionsForMailbox(ctx, ws, mailboxID, clampDecisionLimit(limit))
	if err != nil {
		return nil, fmt.Errorf("fleet: list decisions: %w", err)
	}
	return rows, nil
}

// ScheduledJobs answers "are the sweeps actually running?".
//
// There is no workspace parameter because there is no workspace in the data —
// see Store.ScheduledJobs. The authorization for this read lives entirely on the
// route (admin session only); this method must never be reachable from a
// scope-gated one.
func (s *Service) ScheduledJobs(ctx context.Context, window time.Duration) ([]gen.ListScheduledJobHealthRow, error) {
	rows, err := s.store.ScheduledJobs(ctx, s.now().Add(-clampWindow(window)))
	if err != nil {
		return nil, fmt.Errorf("fleet: list scheduled job health: %w", err)
	}
	return rows, nil
}

// clampWindow maps any requested window — including the zero value a missing or
// unparseable query parameter produces — onto the supported range.
func clampWindow(d time.Duration) time.Duration {
	switch {
	case d <= 0:
		return defaultWindow
	case d < minWindow:
		return minWindow
	case d > maxWindow:
		return maxWindow
	default:
		return d
	}
}

func clampDecisionLimit(n int32) int32 {
	switch {
	case n <= 0:
		return defaultDecisionLimit
	case n > maxDecisionLimit:
		return maxDecisionLimit
	default:
		return n
	}
}
