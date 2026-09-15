package fleet

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/inroad/inroad/internal/platform/db/gen"
)

// fakeStore records what the service asked for and returns what it was primed
// with. The window arithmetic and the liveness rule are the whole of this
// domain's policy, and both are invisible in the response — they show up only as
// the `since` the store is handed and the boolean derived from a timestamp — so
// the fake captures the arguments rather than only serving rows.
type fakeStore struct {
	workers  []gen.ListWorkspaceFleetWorkersRow
	signals  []gen.RollupWorkspaceFleetProviderSignalsRow
	decision []gen.FleetDecision
	jobs     []gen.ListScheduledJobHealthRow

	workersErr  error
	signalsErr  error
	decisionErr error
	jobsErr     error

	// Captured arguments.
	workersWS    uuid.UUID
	signalsWS    uuid.UUID
	signalsSince time.Time
	signalsCalls int
	decisionWS   uuid.UUID
	decisionMbx  uuid.UUID
	decisionLim  int32
	jobsSince    time.Time
}

func (f *fakeStore) Workers(_ context.Context, ws uuid.UUID) ([]gen.ListWorkspaceFleetWorkersRow, error) {
	f.workersWS = ws
	return f.workers, f.workersErr
}

func (f *fakeStore) ProviderSignals(_ context.Context, ws uuid.UUID, since time.Time) ([]gen.RollupWorkspaceFleetProviderSignalsRow, error) {
	f.signalsCalls++
	f.signalsWS, f.signalsSince = ws, since
	return f.signals, f.signalsErr
}

func (f *fakeStore) DecisionsForMailbox(_ context.Context, ws, mailboxID uuid.UUID, limit int32) ([]gen.FleetDecision, error) {
	f.decisionWS, f.decisionMbx, f.decisionLim = ws, mailboxID, limit
	return f.decision, f.decisionErr
}

func (f *fakeStore) ScheduledJobs(_ context.Context, since time.Time) ([]gen.ListScheduledJobHealthRow, error) {
	f.jobsSince = since
	return f.jobs, f.jobsErr
}

// frozen is the clock every test runs against, so a boundary assertion is exact
// rather than "within a tolerance".
var frozen = time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)

func newTestService(store Store) *Service {
	return NewService(store, WithClock(func() time.Time { return frozen }))
}

func ts(t time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: t, Valid: true} }

func workerRow(id string, lastSeen time.Time) gen.ListWorkspaceFleetWorkersRow {
	return gen.ListWorkspaceFleetWorkersRow{
		WorkerID:           id,
		EgressIp:           "203.0.113.7",
		IDFamily:           "ipv4",
		LastSeenAt:         ts(lastSeen),
		WorkspaceMailboxes: 3,
		DegradedMailboxes:  1,
		FirstAssignedAt:    ts(frozen.Add(-72 * time.Hour)),
	}
}

// Liveness is the field an operator scans first, and it is the one that decides
// whether a row means "this is where my mail goes" or "nothing will ever be
// placed here again". The boundary is asserted from both sides because the
// off-by-one is the only interesting way to get it wrong, and because it has to
// match the assigner's own `last_seen_at >= live_since` — a worker this screen
// calls live while the assigner treats it as dead is the most misleading thing
// this surface could say.
func TestLivenessBoundaryMatchesTheAssignersInclusiveComparison(t *testing.T) {
	cases := []struct {
		name     string
		lastSeen time.Time
		want     bool
	}{
		{"fresh heartbeat", frozen.Add(-time.Minute), true},
		{"exactly on the boundary", frozen.Add(-liveWindow), true},
		{"one nanosecond past the boundary", frozen.Add(-liveWindow - time.Nanosecond), false},
		{"long gone", frozen.Add(-24 * time.Hour), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := &fakeStore{workers: []gen.ListWorkspaceFleetWorkersRow{workerRow("w-1", tc.lastSeen)}}
			got, err := newTestService(store).Workers(context.Background(), uuid.New(), 0)
			if err != nil {
				t.Fatalf("Workers: %v", err)
			}
			if len(got) != 1 {
				t.Fatalf("got %d workers, want 1", len(got))
			}
			if got[0].Live != tc.want {
				t.Errorf("Live = %v, want %v for a heartbeat at %s with now = %s",
					got[0].Live, tc.want, tc.lastSeen, frozen)
			}
		})
	}
}

// Every signal row must land on the worker it names and on no other. A rollup
// attributed to the wrong worker is worse than a missing one: it would show a
// healthy worker's numbers on a failing worker's row, which is the exact
// misreading this screen exists to prevent.
func TestSignalsAreStitchedOntoTheWorkerTheyName(t *testing.T) {
	store := &fakeStore{
		workers: []gen.ListWorkspaceFleetWorkersRow{
			workerRow("w-1", frozen),
			workerRow("w-2", frozen),
			workerRow("w-3", frozen),
		},
		signals: []gen.RollupWorkspaceFleetProviderSignalsRow{
			{WorkerID: "w-1", Provider: "smtp", Operation: "send", Attempts: 100, Successes: 90, AuthFailures: 4},
			{WorkerID: "w-1", Provider: "gmail", Operation: "poll", Attempts: 20, Successes: 20},
			{WorkerID: "w-3", Provider: "m365", Operation: "send", Attempts: 7, Blocked: 7},
		},
	}

	got, err := newTestService(store).Workers(context.Background(), uuid.New(), 0)
	if err != nil {
		t.Fatalf("Workers: %v", err)
	}

	byID := map[string][]gen.RollupWorkspaceFleetProviderSignalsRow{}
	for _, w := range got {
		byID[w.WorkerID] = w.Signals
	}
	if len(byID["w-1"]) != 2 {
		t.Errorf("w-1 carries %d signal rows, want 2: %+v", len(byID["w-1"]), byID["w-1"])
	}
	if len(byID["w-3"]) != 1 || byID["w-3"][0].Blocked != 7 {
		t.Errorf("w-3 signals = %+v, want one row with 7 blocked", byID["w-3"])
	}
	// The quiet worker is the branch a happy-path test misses: an empty slice
	// and a zeroed row are different facts — "reported nothing" versus "reported
	// nothing but failures" — and collapsing them would let an idle worker
	// render as a perfect one.
	if byID["w-2"] == nil {
		// nil is acceptable in the service type; what must NOT happen is w-2
		// inheriting another worker's counters.
		return
	}
	if len(byID["w-2"]) != 0 {
		t.Errorf("w-2 reported no signals but carries %+v", byID["w-2"])
	}
}

// A workspace with nothing pinned must not issue the signal query at all: the
// rollup is scoped by that same (empty) assignment set, so it could only ever
// return nothing while scanning worker_provider_signals to prove it.
func TestAnEmptyFleetSkipsTheSignalQueryEntirely(t *testing.T) {
	store := &fakeStore{}
	got, err := newTestService(store).Workers(context.Background(), uuid.New(), 0)
	if err != nil {
		t.Fatalf("Workers: %v", err)
	}
	if got == nil {
		t.Error("returned a nil slice; the handler relies on a non-nil one to serialise []")
	}
	if len(got) != 0 {
		t.Errorf("got %d workers for a workspace with no assignments, want 0", len(got))
	}
	if store.signalsCalls != 0 {
		t.Errorf("issued the signal rollup %d times for an empty fleet, want 0", store.signalsCalls)
	}
}

// The window is policy, and it is invisible in the response — the only evidence
// it was applied correctly is the instant the store was handed. Asserting the
// returned rows instead would pass with the clamp deleted.
func TestWindowClampingDecidesTheInstantTheStoreIsAsked(t *testing.T) {
	cases := []struct {
		name string
		ask  time.Duration
		want time.Duration
	}{
		{"absent becomes the default", 0, defaultWindow},
		{"negative becomes the default", -5 * time.Hour, defaultWindow},
		{"below the floor is raised", time.Minute, minWindow},
		{"in range is honoured", 6 * time.Hour, 6 * time.Hour},
		{"above the ceiling is capped at retention", 365 * 24 * time.Hour, maxWindow},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := &fakeStore{workers: []gen.ListWorkspaceFleetWorkersRow{workerRow("w-1", frozen)}}
			if _, err := newTestService(store).Workers(context.Background(), uuid.New(), tc.ask); err != nil {
				t.Fatalf("Workers: %v", err)
			}
			if want := frozen.Add(-tc.want); !store.signalsSince.Equal(want) {
				t.Errorf("asked the store for signals since %s, want %s (window %s)",
					store.signalsSince, want, tc.want)
			}

			jobStore := &fakeStore{}
			if _, err := newTestService(jobStore).ScheduledJobs(context.Background(), tc.ask); err != nil {
				t.Fatalf("ScheduledJobs: %v", err)
			}
			if want := frozen.Add(-tc.want); !jobStore.jobsSince.Equal(want) {
				t.Errorf("asked the store for job runs since %s, want %s (window %s)",
					jobStore.jobsSince, want, tc.want)
			}
		})
	}
}

// The workspace reaching the store must be the one the caller was pinned to, on
// every read that has one. This is the cheap half of the tenancy guarantee — the
// expensive half (that the SQL actually filters) is in the integration test.
func TestTheCallersWorkspaceIsWhatReachesTheStore(t *testing.T) {
	ws, mailbox := uuid.New(), uuid.New()
	store := &fakeStore{workers: []gen.ListWorkspaceFleetWorkersRow{workerRow("w-1", frozen)}}
	svc := newTestService(store)

	if _, err := svc.Workers(context.Background(), ws, 0); err != nil {
		t.Fatalf("Workers: %v", err)
	}
	if store.workersWS != ws {
		t.Errorf("worker list ran for workspace %s, want %s", store.workersWS, ws)
	}
	if store.signalsWS != ws {
		t.Errorf("signal rollup ran for workspace %s, want %s", store.signalsWS, ws)
	}

	if _, err := svc.DecisionsForMailbox(context.Background(), ws, mailbox, 0); err != nil {
		t.Fatalf("DecisionsForMailbox: %v", err)
	}
	if store.decisionWS != ws {
		t.Errorf("decision read ran for workspace %s, want %s", store.decisionWS, ws)
	}
	if store.decisionMbx != mailbox {
		t.Errorf("decision read named mailbox %s, want %s", store.decisionMbx, mailbox)
	}
}

func TestDecisionLimitIsClamped(t *testing.T) {
	cases := []struct {
		name      string
		ask, want int32
	}{
		{"absent becomes the default", 0, defaultDecisionLimit},
		{"negative becomes the default", -1, defaultDecisionLimit},
		{"in range is honoured", 10, 10},
		{"above the ceiling is capped", 100_000, maxDecisionLimit},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := &fakeStore{}
			if _, err := newTestService(store).DecisionsForMailbox(context.Background(), uuid.New(), uuid.New(), tc.ask); err != nil {
				t.Fatalf("DecisionsForMailbox: %v", err)
			}
			if store.decisionLim != tc.want {
				t.Errorf("asked the store for %d rows, want %d", store.decisionLim, tc.want)
			}
		})
	}
}

// Every store failure must propagate, wrapped, rather than degrading to an empty
// list. An operator shown "no workers" when the query failed would conclude their
// fleet is gone.
func TestStoreFailuresPropagateRatherThanRenderingAsEmpty(t *testing.T) {
	boom := errors.New("connection refused")

	t.Run("worker list", func(t *testing.T) {
		got, err := newTestService(&fakeStore{workersErr: boom}).Workers(context.Background(), uuid.New(), 0)
		if !errors.Is(err, boom) {
			t.Errorf("err = %v, want it to wrap %v", err, boom)
		}
		if got != nil {
			t.Errorf("returned %v alongside an error; a caller ignoring err would render a fleet that was never read", got)
		}
	})

	t.Run("signal rollup", func(t *testing.T) {
		store := &fakeStore{
			workers:    []gen.ListWorkspaceFleetWorkersRow{workerRow("w-1", frozen)},
			signalsErr: boom,
		}
		if _, err := newTestService(store).Workers(context.Background(), uuid.New(), 0); !errors.Is(err, boom) {
			t.Errorf("err = %v, want it to wrap %v — a worker rendered with no signals because the "+
				"rollup failed is indistinguishable from a quiet one", err, boom)
		}
	})

	t.Run("decisions", func(t *testing.T) {
		_, err := newTestService(&fakeStore{decisionErr: boom}).DecisionsForMailbox(context.Background(), uuid.New(), uuid.New(), 0)
		if !errors.Is(err, boom) {
			t.Errorf("err = %v, want it to wrap %v", err, boom)
		}
	})

	t.Run("scheduled jobs", func(t *testing.T) {
		_, err := newTestService(&fakeStore{jobsErr: boom}).ScheduledJobs(context.Background(), 0)
		if !errors.Is(err, boom) {
			t.Errorf("err = %v, want it to wrap %v", err, boom)
		}
	})
}
