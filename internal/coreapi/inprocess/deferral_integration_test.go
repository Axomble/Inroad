//go:build integration

package inprocess

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/coreapi"
)

// These tests exercise the out-of-office deferral against Postgres: the push
// itself (DeferEnrollment → enrollment.Reschedule → SetDue) and — the part that
// actually makes it safe — the claim-time guard that stops the asynq advance
// task ALREADY queued for the old due time from delivering into the absence.
// Docker must be up.

func nextDueAt(t *testing.T, ctx context.Context, f poolFixture, enrollmentID uuid.UUID) time.Time {
	t.Helper()
	var due time.Time
	if err := f.pool.QueryRow(ctx,
		`SELECT next_due_at FROM sequence_enrollments WHERE id = $1`, enrollmentID).Scan(&due); err != nil {
		t.Fatalf("read next_due_at: %v", err)
	}
	return due
}

func sendRowCount(t *testing.T, ctx context.Context, f poolFixture, enrollmentID uuid.UUID) int {
	t.Helper()
	var n int
	if err := f.pool.QueryRow(ctx,
		`SELECT count(*) FROM sends s
		 JOIN sequence_enrollments e ON e.workspace_id = s.workspace_id
		   AND e.campaign_id = s.campaign_id AND e.contact_id = s.contact_id
		 WHERE e.id = $1`, enrollmentID).Scan(&n); err != nil {
		t.Fatalf("count sends: %v", err)
	}
	return n
}

// TestDeferEnrollmentPushesTheDueTime: the push itself lands, and the
// enrollment stays ACTIVE — an out-of-office is a wait, not a stop.
func TestDeferEnrollmentPushesTheDueTime(t *testing.T) {
	ctx, f := setupPool(t)
	enrollmentID := f.enroll(t, ctx)
	until := time.Now().UTC().Add(72 * time.Hour).Truncate(time.Second)

	if err := f.core.DeferEnrollment(ctx, enrollmentID.String(), f.ws.String(), until); err != nil {
		t.Fatalf("DeferEnrollment: %v", err)
	}
	if got := nextDueAt(t, ctx, f, enrollmentID); !got.UTC().Truncate(time.Second).Equal(until) {
		t.Fatalf("next_due_at = %v, want %v", got, until)
	}
	var status string
	if err := f.pool.QueryRow(ctx,
		`SELECT status FROM sequence_enrollments WHERE id = $1`, enrollmentID).Scan(&status); err != nil {
		t.Fatalf("read status: %v", err)
	}
	if status != "active" {
		t.Fatalf("a deferral must keep the enrollment active, got %q", status)
	}
}

// TestDeferEnrollmentIsWorkspacePinned: a foreign workspace cannot reschedule
// someone else's enrollment (the SetDue WHERE is workspace-pinned).
func TestDeferEnrollmentIsWorkspacePinned(t *testing.T) {
	ctx, f := setupPool(t)
	enrollmentID := f.enroll(t, ctx)
	before := nextDueAt(t, ctx, f, enrollmentID)

	if err := f.core.DeferEnrollment(ctx, enrollmentID.String(), f.foreignWS.String(),
		time.Now().UTC().Add(72*time.Hour)); err != nil {
		t.Fatalf("a cross-tenant defer must be a no-op, not an error: %v", err)
	}
	if got := nextDueAt(t, ctx, f, enrollmentID); !got.Equal(before) {
		t.Fatalf("a foreign workspace moved next_due_at: %v -> %v", before, got)
	}
}

// TestQueuedAdvanceDoesNotFireEarlyAfterDeferral is the correctness trap this
// whole mechanism exists for.
//
// The advance task for the OLD due time is already in asynq when the OOO reply
// arrives; pushing next_due_at out cannot cancel it. So: defer, then replay
// exactly what that queued task does (GetStepSendJob → ClaimStepSend) and prove
// the claim refuses. Nothing may be sent, and no sends row may exist to claim
// later — the step must not go out during the stated absence.
func TestQueuedAdvanceDoesNotFireEarlyAfterDeferral(t *testing.T) {
	ctx, f := setupPool(t)
	enrollmentID := f.enroll(t, ctx)

	if err := f.core.DeferEnrollment(ctx, enrollmentID.String(), f.ws.String(),
		time.Now().UTC().Add(72*time.Hour)); err != nil {
		t.Fatalf("DeferEnrollment: %v", err)
	}

	job, err := f.core.GetStepSendJob(ctx, enrollmentID.String(), f.ws.String())
	if err != nil {
		t.Fatalf("GetStepSendJob: %v", err)
	}
	if job.Skip {
		t.Fatal("a deferred enrollment is still active; the job must not Skip")
	}
	if !job.NotYetDue(time.Now()) {
		t.Fatalf("the job must carry the pushed due time, got NotDueUntil=%v", job.NotDueUntil)
	}

	outcome, err := f.core.ClaimStepSend(ctx, job)
	if err != nil {
		t.Fatalf("ClaimStepSend: %v", err)
	}
	if outcome != coreapi.ClaimDeferred {
		t.Fatalf("claim outcome = %v, want ClaimDeferred — the step would have sent into the absence", outcome)
	}
	if n := sendRowCount(t, ctx, f, enrollmentID); n != 0 {
		t.Fatalf("a refused claim must write no sends row, got %d", n)
	}
}

// TestClaimStillWinsOnceTheDeferralElapses: the guard is a wait, not a block —
// once next_due_at is in the past the same job claims normally.
func TestClaimStillWinsOnceTheDeferralElapses(t *testing.T) {
	ctx, f := setupPool(t)
	enrollmentID := f.enroll(t, ctx)

	if err := f.core.DeferEnrollment(ctx, enrollmentID.String(), f.ws.String(),
		time.Now().UTC().Add(-time.Minute)); err != nil {
		t.Fatalf("DeferEnrollment: %v", err)
	}
	job, err := f.core.GetStepSendJob(ctx, enrollmentID.String(), f.ws.String())
	if err != nil {
		t.Fatalf("GetStepSendJob: %v", err)
	}
	if job.NotYetDue(time.Now()) {
		t.Fatalf("an elapsed deferral must not still be pending, got %v", job.NotDueUntil)
	}
	outcome, err := f.core.ClaimStepSend(ctx, job)
	if err != nil {
		t.Fatalf("ClaimStepSend: %v", err)
	}
	if outcome != coreapi.ClaimWon {
		t.Fatalf("claim outcome = %v, want ClaimWon", outcome)
	}
}
