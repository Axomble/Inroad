package coreapi

import "time"

// The stranded-manual-send sweep's vocabulary: what the control plane hands the
// execution plane when it finds a human's reply or composed email that nothing
// is going to deliver.
//
// It is NOT a coreapi.Client method. Like maintenance.Cleaner, fleet.Rotator and
// recipientesp.Core, the capability is consumed through a narrow interface
// defined by its consumer (internal/worker/inbox.PendingSweepCore) and satisfied
// by the in-process client via type assertion at registration — widening Client,
// which already carries ~40 methods and is implemented in full by a dozen test
// fakes, would break all of them to serve one sweep.

// StrandedPendingKind names which table a stranded row came from, and therefore
// which task type rescues it. Plain string constants rather than a defined type,
// matching this subsystem's own convention for the status column (see
// internal/app/inbox's PendingStatus*): the values are the database's and they
// travel unchanged.
const (
	StrandedPendingKindReply   = "reply"
	StrandedPendingKindCompose = "compose"
)

// StrandedPendingSend names one manual send the sweep should re-drive. It is an
// IDENTIFIER, never content: no body, no subject, no recipient. The row remains
// the single source of truth for what to send and whether to send it at all, and
// the rescue task the sweep enqueues carries only these ids — exactly as the
// original task did, and for the same reason (see queue.InboxPendingReplySend-
// Payload: a payload is copied verbatim into task_dead_letters and served under
// campaigns:read).
//
// Status is carried for the LOG LINE, so an operator reading "the sweep rescued
// 3 rows" can tell a lost enqueue ('scheduled') from an abandoned lease
// ('sending') without a second query. Nothing branches on it.
type StrandedPendingSend struct {
	Kind        string
	ID          string
	WorkspaceID string
	Status      string
}

// StrandedPendingWindow is the pair of ages that decide when a pending send has
// stopped making progress on its own.
//
// A struct rather than two positional time.Duration arguments because they are
// adjacent and interchangeable to the compiler, and swapping them silently
// changes which rows the sweep touches — the one class of mistake here that
// produces a wrong answer instead of a failure.
type StrandedPendingWindow struct {
	// OverdueAfter is how long a row may sit 'scheduled' past its send_after
	// before the sweep assumes nothing is going to pick it up. It is noise
	// control rather than safety: a 'scheduled' row has no claim outstanding, so
	// re-driving one can never race a worker — but a row whose own task is
	// merely in asynq's retry backoff does not need the help.
	OverdueAfter time.Duration
	// LeaseGrace is how long PAST ITS LEASE a 'sending' row must sit before the
	// sweep treats the claim as abandoned. The control plane adds the lease
	// itself, so this knob cannot be set low enough to nominate a row whose
	// lease is still live — which is the guarantee the whole sweep rests on.
	LeaseGrace time.Duration
}
