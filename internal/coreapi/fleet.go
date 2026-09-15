package coreapi

import (
	"context"
	"time"

	"github.com/inroad/inroad/internal/platform/fleetdecision"
)

// ProviderSignalClient is an optional execution-plane capability: reporting one
// window of what mailbox PROVIDERS told this worker.
//
// Kept off Client for the same reason as DeadLetterClient and the capabilities
// around it — Client already carries ~40 methods that 13 test fakes implement in
// full, and widening it for one call site would break every one of them for no
// gain in the seam's expressiveness. A Client without this simply reports no
// signals, which is exactly today's behaviour, and never fails a send.
//
// It is the ONLY way a worker's provider counters reach storage: internal/worker
// reaches data through coreapi and never through platform/db (there is a
// depguard rule), so the in-process implementation here is where the write
// happens.
type ProviderSignalClient interface {
	// RecordWorkerProviderSignals persists one accumulation window.
	//
	// It must never be called on a send path and its failure must never fail one:
	// the worker accumulates in memory and flushes on a timer, so a failed flush
	// loses a window of COUNTS and nothing else. An empty batch is a no-op rather
	// than an error — a quiet window is the normal state of a healthy worker.
	RecordWorkerProviderSignals(ctx context.Context, in WorkerProviderSignals) error
}

// WorkerProviderSignals is one worker's accumulation window.
//
// Counts are DELTAS for [WindowStart, WindowEnd) — never running totals — which
// is what makes aggregation a plain SUM over a time range with no per-worker
// baseline to reconcile, and what makes a worker restart cost at most one
// partial window instead of corrupting a cumulative series.
//
// There is no WorkspaceID, and there is none to add: a worker is global
// infrastructure (the `workers` registry of migration 000017 holds no tenant
// column for the same reason), several workspaces' mailboxes share one worker,
// and a fact about an egress IP's standing with a provider belongs to no tenant.
type WorkerProviderSignals struct {
	WorkerID    string
	WindowStart time.Time
	WindowEnd   time.Time
	Counts      []WorkerProviderSignalCount
}

// WorkerProviderSignalCount is one counter's delta. The three dimensions are
// strings at this seam like every other coreapi value; the worker normalises
// them onto closed vocabularies before they get here
// (internal/platform/providersignal), and the table's CHECK constraints are the
// backstop.
type WorkerProviderSignalCount struct {
	// Provider is the transport leg that ran ("smtp" | "gmail" | "m365").
	Provider string
	// Operation is what it was doing ("send" | "poll").
	Operation string
	// Reason is the classified provider verdict ("ok", "rate_limited", …).
	Reason string
	// Events is how many times this exact outcome occurred in the window.
	Events int64
}

// FleetDecisionRecorder is an optional execution-plane capability: appending one
// automated fleet decision to the log that makes "why is this mailbox on this
// worker?" answerable. Kept off Client for the same reason as
// ProviderSignalClient above.
//
// It takes a platform type rather than a coreapi input struct, unlike
// DeadLetterInput and ComplaintInput, deliberately: fleetdecision.Entry is the
// ONLY way to obtain a valid Reason, and that constraint is the feature. A
// coreapi mirror struct with a plain `Reason string` would hand every call site
// back the ability to write prose claiming a score comparison that never
// happened — which is the single rule the decision log exists to keep. coreapi
// already depends on platform (see cadence.Schedule on StepSendJob), so the
// direction is the allowed one.
type FleetDecisionRecorder interface {
	// RecordFleetDecision appends one decision. It returns
	// fleetdecision.ErrInvalidEntry for an entry that can never be written, so a
	// caller can log and continue instead of retrying forever; every other error
	// is worth a retry.
	//
	// Like the signal flush, this must never fail the operation it describes: a
	// decision that could not be logged is degraded observability, never a
	// mailbox that stopped sending.
	RecordFleetDecision(ctx context.Context, e fleetdecision.Entry) error
}

// FleetRotator is an optional control-plane capability: running one pass of the
// rotation gate, which decides whether any ALREADY PLACED mailbox should move to
// a different worker. Kept off Client for the same reason as the two capability
// interfaces above.
//
// It is the counterpart to AssignMailboxWorker's incumbency rule, not a
// competitor to it. That rule keeps a mailbox on its live worker
// unconditionally, because IP trust accrues per (mailbox, IP) pair at the
// provider and moving discards it — which leaves a worker the provider has
// blocked holding every mailbox already assigned to it, each of them failing.
// Rotation is the ONLY thing that moves those, and it is deliberately hard to
// satisfy (internal/platform/fleetrotate holds the tiers, the residency floor,
// the score margin and the per-tick caps).
type FleetRotator interface {
	// RotateMailboxWorkers runs one tick and reports how many mailboxes it
	// actually moved.
	//
	// It is idempotent in the only sense that matters for a periodic reconcile:
	// two ticks in a row over an unchanged fleet move nothing the second time,
	// because a move resets the residency clock the non-urgent tier is gated on
	// and because an urgent mailbox has, by then, already left. A fleet with at
	// most one live worker is a no-op and not an error — self-host has nowhere
	// to rotate to, and saying so as a failure would turn every tick into an
	// alert.
	RotateMailboxWorkers(ctx context.Context) (moved int64, err error)
}
