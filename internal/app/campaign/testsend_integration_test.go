//go:build integration

package campaign

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/app/suppression"
)

// These tests exercise the test-send suppression-list gate (the security fix:
// POST /campaigns/{id}/test-send must never reach an address the workspace has
// explicitly unsubscribed or bounced) against a REAL suppression row in
// Postgres, wiring the SAME suppression.Store cmd/inroad wires in production
// -- not a fake. Docker must be up.

// recordingTestSendEnqueuer is TestSend's enqueue seam: it never touches
// Redis, it just counts how many times it was asked to enqueue.
type recordingTestSendEnqueuer struct{ calls int }

func (e *recordingTestSendEnqueuer) EnqueueTestSend(string, string, string, string, string) error {
	e.calls++
	return nil
}

// setupTestSendWithStep builds on setupSenders' fixture (a workspace with two
// mailboxes and a campaign whose implicit one-mailbox pool is mailboxA -- an
// eligible sender with zero pool rows configured). store.Create already seeds
// one sequence step (step_order 1) in the same transaction as the campaign, so
// this just reads that step's id back rather than inserting a second one
// (which would collide with sequence_steps_campaign_id_step_order_key).
func setupTestSendWithStep(t *testing.T, ctx context.Context) (senderFixture, uuid.UUID) {
	t.Helper()
	f := setupSenders(t, ctx)
	steps, err := f.store.ListSteps(ctx, f.ws, f.campaignID)
	if err != nil {
		t.Fatalf("ListSteps: %v", err)
	}
	if len(steps) != 1 {
		t.Fatalf("steps = %d, want the 1 step Create seeds", len(steps))
	}
	return f, steps[0].ID
}

// TestTestSendRejectsARealSuppressedRecipient is the end-to-end proof: a
// workspace that genuinely unsubscribed an address (a real suppression row,
// not a fake) must not have a test-send enqueued to it.
func TestTestSendRejectsARealSuppressedRecipient(t *testing.T) {
	ctx := context.Background()
	f, stepID := setupTestSendWithStep(t, ctx)
	suppStore := suppression.NewStore(f.q)

	to := "unsub-" + uuid.NewString() + "@x.test"
	if err := suppStore.Add(ctx, f.ws, to, "unsubscribe"); err != nil {
		t.Fatalf("seed suppression: %v", err)
	}

	enq := &recordingTestSendEnqueuer{}
	svc := NewService(f.store, alwaysOKChecker{}, WithTestSendEnqueuer(enq), WithSuppressionChecker(suppStore))

	err := svc.TestSend(ctx, f.ws, f.campaignID, stepID, to)
	if !errors.Is(err, ErrRecipientSuppressed) {
		t.Fatalf("err = %v, want ErrRecipientSuppressed", err)
	}
	if enq.calls != 0 {
		t.Error("a real suppression row must block the enqueue, not just the fake-checker test")
	}
}

// The lookup is case-insensitive (the underlying index is on lower(email)),
// matching how a real send's suppression check behaves.
func TestTestSendSuppressionCheckIsCaseInsensitive(t *testing.T) {
	ctx := context.Background()
	f, stepID := setupTestSendWithStep(t, ctx)
	suppStore := suppression.NewStore(f.q)

	to := "MixedCase-" + uuid.NewString() + "@X.Test"
	if err := suppStore.Add(ctx, f.ws, to, "bounce"); err != nil {
		t.Fatalf("seed suppression: %v", err)
	}

	enq := &recordingTestSendEnqueuer{}
	svc := NewService(f.store, alwaysOKChecker{}, WithTestSendEnqueuer(enq), WithSuppressionChecker(suppStore))

	// A differently-cased request for the SAME address must still be rejected.
	err := svc.TestSend(ctx, f.ws, f.campaignID, stepID, "mixedcase-"+to[len("MixedCase-"):])
	if !errors.Is(err, ErrRecipientSuppressed) {
		t.Fatalf("err = %v, want ErrRecipientSuppressed for a case-differing match", err)
	}
	if enq.calls != 0 {
		t.Error("a case-insensitive suppression match must block the enqueue")
	}
}

// A non-suppressed address, checked against the real table, still enqueues --
// proving the fix does not false-positive against a genuinely clean recipient.
func TestTestSendAllowsARealUnsuppressedRecipient(t *testing.T) {
	ctx := context.Background()
	f, stepID := setupTestSendWithStep(t, ctx)
	suppStore := suppression.NewStore(f.q)

	enq := &recordingTestSendEnqueuer{}
	svc := NewService(f.store, alwaysOKChecker{}, WithTestSendEnqueuer(enq), WithSuppressionChecker(suppStore))

	to := "clean-" + uuid.NewString() + "@x.test"
	if err := svc.TestSend(ctx, f.ws, f.campaignID, stepID, to); err != nil {
		t.Fatalf("TestSend: %v", err)
	}
	if enq.calls != 1 {
		t.Errorf("enqueue calls = %d, want 1", enq.calls)
	}
}

// The suppression check is workspace-pinned: another workspace's suppression
// of the SAME address must not block this workspace's test-send.
func TestTestSendSuppressionCheckIsWorkspacePinned(t *testing.T) {
	ctx := context.Background()
	f, stepID := setupTestSendWithStep(t, ctx)
	suppStore := suppression.NewStore(f.q)

	other, err := f.q.CreateWorkspace(ctx, "TestSend Suppression Other "+uuid.NewString())
	if err != nil {
		t.Fatalf("other workspace: %v", err)
	}
	to := "shared-" + uuid.NewString() + "@x.test"
	if err := suppStore.Add(ctx, other.ID, to, "unsubscribe"); err != nil {
		t.Fatalf("seed suppression in the other workspace: %v", err)
	}

	enq := &recordingTestSendEnqueuer{}
	svc := NewService(f.store, alwaysOKChecker{}, WithTestSendEnqueuer(enq), WithSuppressionChecker(suppStore))

	if err := svc.TestSend(ctx, f.ws, f.campaignID, stepID, to); err != nil {
		t.Fatalf("TestSend: %v, want it to succeed -- another workspace's suppression must not leak in", err)
	}
	if enq.calls != 1 {
		t.Errorf("enqueue calls = %d, want 1", enq.calls)
	}
}
