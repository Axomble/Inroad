package inbox

import (
	"context"
	"errors"
	"testing"

	"github.com/hibiken/asynq"

	"github.com/inroad/inroad/internal/coreapi"
)

// stubPendingSweepCore answers the one scan the sweep makes, and records the
// window it was asked with — the window is not decoration, it is the thing that
// keeps the sweep off a live lease, so a handler that quietly dropped it would
// be a handler that scans wider than it documents.
type stubPendingSweepCore struct {
	rows []coreapi.StrandedPendingSend
	err  error
	// seen is the window the handler passed through, and calls counts the scans
	// (exactly one per tick).
	seen  coreapi.StrandedPendingWindow
	calls int
}

func (s *stubPendingSweepCore) ListStrandedPendingInboxSends(_ context.Context, w coreapi.StrandedPendingWindow) ([]coreapi.StrandedPendingSend, error) {
	s.calls++
	s.seen = w
	return s.rows, s.err
}

// stubRescueEnqueuer records which rescue each candidate produced, so a test can
// assert a reply went to the reply task type and a compose to the compose one.
// Crossing those would deliver a composed email through the reply handler, which
// would read a thread that does not exist.
type stubRescueEnqueuer struct {
	replies  []string
	composes []string
	failIDs  map[string]bool
}

func (s *stubRescueEnqueuer) EnqueueStrandedPendingInboxReply(_ context.Context, pendingID, _ string) error {
	if s.failIDs[pendingID] {
		return errors.New("redis is down")
	}
	s.replies = append(s.replies, pendingID)
	return nil
}

func (s *stubRescueEnqueuer) EnqueueStrandedPendingInboxCompose(_ context.Context, pendingID, _ string) error {
	if s.failIDs[pendingID] {
		return errors.New("redis is down")
	}
	s.composes = append(s.composes, pendingID)
	return nil
}

func runPendingSweep(t *testing.T, core PendingSweepCore, enq PendingSweepEnqueuer) error {
	t.Helper()
	h := PendingSweepHandler(core, enq, DefaultPendingSweepWindow, nil)
	return h(context.Background(), asynq.NewTask("inbox:pending_send_sweep", nil))
}

// The sweep's whole output is a re-enqueue per stranded row, routed to the task
// type that matches the row's table.
func TestTheSweepReDrivesEachStrandedRowThroughItsOwnSendTask(t *testing.T) {
	core := &stubPendingSweepCore{rows: []coreapi.StrandedPendingSend{
		{Kind: coreapi.StrandedPendingKindReply, ID: "r1", WorkspaceID: "ws1", Status: "scheduled"},
		{Kind: coreapi.StrandedPendingKindCompose, ID: "c1", WorkspaceID: "ws1", Status: "sending"},
		{Kind: coreapi.StrandedPendingKindReply, ID: "r2", WorkspaceID: "ws2", Status: "sending"},
	}}
	enq := &stubRescueEnqueuer{}
	if err := runPendingSweep(t, core, enq); err != nil {
		t.Fatalf("handler: %v", err)
	}
	if got := len(enq.replies); got != 2 {
		t.Errorf("re-drove %d replies (%v), want 2 — a stranded reply nothing re-enqueues is a "+
			"reply the operator was told was sent and that never leaves", got, enq.replies)
	}
	if got := len(enq.composes); got != 1 {
		t.Errorf("re-drove %d composes (%v), want 1", got, enq.composes)
	}
	if enq.composes[0] != "c1" {
		t.Errorf("compose rescue carried %q, want c1 — a compose re-driven through the reply "+
			"handler would look for a thread that does not exist", enq.composes[0])
	}
}

// The window reaches the scan intact. It is the only input that decides which
// rows are nominated, and "the sweep respects the lease" is a claim about THIS
// value arriving where the lease is added to it.
func TestTheSweepPassesItsWindowToTheScan(t *testing.T) {
	core := &stubPendingSweepCore{}
	if err := runPendingSweep(t, core, &stubRescueEnqueuer{}); err != nil {
		t.Fatalf("handler: %v", err)
	}
	if core.calls != 1 {
		t.Fatalf("scanned %d times, want exactly 1 per tick", core.calls)
	}
	if core.seen != DefaultPendingSweepWindow {
		t.Errorf("scan window = %+v, want %+v", core.seen, DefaultPendingSweepWindow)
	}
	if DefaultPendingSweepWindow.LeaseGrace <= 0 {
		t.Error("LeaseGrace must be positive: a zero grace makes the sweep's predicate exactly " +
			"the claim guard's rather than strictly narrower than it")
	}
}

// Nothing stranded is the normal tick, and it must be silent and cheap: no
// enqueue, no error, nothing for an operator to interpret.
func TestASweepWithNothingStrandedEnqueuesNothing(t *testing.T) {
	enq := &stubRescueEnqueuer{}
	if err := runPendingSweep(t, &stubPendingSweepCore{}, enq); err != nil {
		t.Fatalf("handler: %v", err)
	}
	if len(enq.replies) != 0 || len(enq.composes) != 0 {
		t.Errorf("an empty scan enqueued %v / %v, want nothing", enq.replies, enq.composes)
	}
}

// A failed SCAN is returned so asynq retries the tick and the run ledger records
// an error. The alternative — swallowing it — would make a sweep that has
// stopped seeing anything indistinguishable from a sweep with nothing to do,
// which is the exact failure this sweep exists to prevent one layer down.
func TestAFailedScanFailsTheTick(t *testing.T) {
	sentinel := errors.New("scan exploded")
	err := runPendingSweep(t, &stubPendingSweepCore{err: sentinel}, &stubRescueEnqueuer{})
	if !errors.Is(err, sentinel) {
		t.Fatalf("handler returned %v, want the scan error wrapped", err)
	}
}

// One failed enqueue must not abandon the rest of the tick. The row it failed on
// is untouched, so it is still stranded and the next tick re-nominates it —
// whereas aborting would strand every row behind it too.
func TestOneFailedRescueDoesNotAbandonTheRest(t *testing.T) {
	core := &stubPendingSweepCore{rows: []coreapi.StrandedPendingSend{
		{Kind: coreapi.StrandedPendingKindReply, ID: "r1", WorkspaceID: "ws1", Status: "scheduled"},
		{Kind: coreapi.StrandedPendingKindReply, ID: "r2", WorkspaceID: "ws1", Status: "scheduled"},
		{Kind: coreapi.StrandedPendingKindReply, ID: "r3", WorkspaceID: "ws1", Status: "scheduled"},
	}}
	enq := &stubRescueEnqueuer{failIDs: map[string]bool{"r2": true}}
	if err := runPendingSweep(t, core, enq); err != nil {
		t.Fatalf("a partial enqueue failure must not fail the tick: %v", err)
	}
	if len(enq.replies) != 2 || enq.replies[0] != "r1" || enq.replies[1] != "r3" {
		t.Errorf("re-drove %v, want [r1 r3] — the row after the failure was skipped", enq.replies)
	}
}

// A kind this worker does not know is skipped and logged, never guessed at. It
// is reachable only from a control plane newer than the worker, and enqueuing a
// reply task for a row that is not in the reply table would fail the claim and
// look like an undo.
func TestAnUnknownKindIsSkippedRatherThanGuessed(t *testing.T) {
	core := &stubPendingSweepCore{rows: []coreapi.StrandedPendingSend{
		{Kind: "digest", ID: "x1", WorkspaceID: "ws1", Status: "scheduled"},
		{Kind: coreapi.StrandedPendingKindReply, ID: "r1", WorkspaceID: "ws1", Status: "scheduled"},
	}}
	enq := &stubRescueEnqueuer{}
	if err := runPendingSweep(t, core, enq); err != nil {
		t.Fatalf("handler: %v", err)
	}
	if len(enq.replies) != 1 || enq.replies[0] != "r1" {
		t.Errorf("replies = %v, want only r1", enq.replies)
	}
	if len(enq.composes) != 0 {
		t.Errorf("an unknown kind was enqueued as a compose: %v", enq.composes)
	}
}

// A cancelled context stops the tick mid-loop instead of grinding through a
// bounded-but-large candidate list against a closing Redis connection. Nothing
// is lost: the rows not reached were never enqueued and are still stranded.
func TestACancelledTickStopsWithoutFailing(t *testing.T) {
	core := &stubPendingSweepCore{rows: []coreapi.StrandedPendingSend{
		{Kind: coreapi.StrandedPendingKindReply, ID: "r1", WorkspaceID: "ws1", Status: "scheduled"},
		{Kind: coreapi.StrandedPendingKindReply, ID: "r2", WorkspaceID: "ws1", Status: "scheduled"},
	}}
	enq := &stubRescueEnqueuer{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	h := PendingSweepHandler(core, enq, DefaultPendingSweepWindow, nil)
	if err := h(ctx, asynq.NewTask("inbox:pending_send_sweep", nil)); err != nil {
		t.Fatalf("a cancelled tick is not a failed tick: %v", err)
	}
	if len(enq.replies) != 0 {
		t.Errorf("enqueued %v after cancellation, want nothing", enq.replies)
	}
}

// The sweep must remain incapable of sending. This is a TYPE-level assertion
// rather than a behavioural one on purpose: the guarantee is that
// PendingSweepCore exposes no claim, no mark, no release and no send, so no
// future edit to the handler body can add a second delivery path without first
// widening this interface — which is a visible change in review.
func TestTheSweepsCapabilityIsReadOnly(t *testing.T) {
	var core PendingSweepCore = &stubPendingSweepCore{}
	if _, isClaimer := core.(interface {
		ClaimPendingInboxReply(context.Context, string, string) (coreapi.PendingInboxReply, error)
	}); isClaimer {
		t.Error("PendingSweepCore reaches a claim — the sweep must hand ids to the ordinary " +
			"send task and never take a lease of its own")
	}
	if _, isSender := core.(Mailer); isSender {
		t.Error("PendingSweepCore reaches a Sender — a sweep that can dial is a second send path")
	}
}
