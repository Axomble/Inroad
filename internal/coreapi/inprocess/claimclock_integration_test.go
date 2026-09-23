//go:build integration

package inprocess

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/coreapi"
)

// The claim's not-due gate compares next_due_at — stamped by the DATABASE's
// clock — against the database's own now(). It used to compare against this
// process's time.Now(), so its answer depended on app/DB clock skew: with the
// database ahead, a due-now enrollment came back ClaimDeferred, which is what
// made the ARF integration tests flake on main.
//
// A test cannot move either clock, so these pin the gate to the database's
// clock by choosing due times RELATIVE TO IT, read immediately before the
// claim. Each assertion is deterministic under the database-clock gate; the
// old process-clock gate fails the first whenever the database runs ahead of
// this process and the second whenever it runs behind by more than the margin.
// The measured skew is logged so a failure is diagnosable at a glance.
//
// LIMIT, stated plainly: with the database on the same host (or otherwise
// clock-synced to within a millisecond or so, as a local dev stack usually
// is), the OLD code passes these tests too — there is no skew for it to trip
// on. They regress the bug only where real skew exists (the ~1.7s that made the
// ARF tests flake). The fix's own proof was a mutation run: reinstating the
// process-clock gate with time.Now() shifted by -2s fails the first test, and by
// +10s fails the second. A clock is not injected to make that permanent because
// the fixed gate reads no process clock at all — there would be nothing for the
// injected clock to reach.

// dbClock reads the database's clock_timestamp() and brackets it with this
// process's clock, returning the database's reading and an estimate of the
// skew (database minus process, at the bracket's midpoint).
func dbClock(t *testing.T, ctx context.Context, f poolFixture) (time.Time, time.Duration) {
	t.Helper()
	before := time.Now()
	var dbNow time.Time
	if err := f.pool.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&dbNow); err != nil {
		t.Fatalf("read database clock: %v", err)
	}
	after := time.Now()
	mid := before.Add(after.Sub(before) / 2)
	return dbNow, dbNow.Sub(mid)
}

// A job due a moment ago BY THE DATABASE'S CLOCK must claim. This is exactly
// what a just-enrolled enrollment is (next_due_at = now() at insert), and the
// shape every claim-immediately test in this package has.
func TestClaimDueGateAcceptsWhatTheDatabaseSaysIsDue(t *testing.T) {
	ctx, f := setupPool(t)
	enrollmentID := f.enroll(t, ctx)
	job, err := f.core.GetStepSendJob(ctx, enrollmentID.String(), f.ws.String())
	if err != nil {
		t.Fatalf("GetStepSendJob: %v", err)
	}
	dbNow, skew := dbClock(t, ctx, f)
	t.Logf("database clock minus process clock ~ %v", skew)
	job.NotDueUntil = dbNow.Add(-time.Millisecond)

	outcome, err := f.core.ClaimStepSend(ctx, job)
	if err != nil {
		t.Fatalf("ClaimStepSend: %v", err)
	}
	if outcome != coreapi.ClaimWon {
		t.Fatalf("claim outcome = %v, want ClaimWon: the enrollment is due by the clock that "+
			"stamped its due time (skew ~%v) — the gate is reading some other clock", outcome, skew)
	}
}

// And the mirror: a job due shortly in the future BY THE DATABASE'S CLOCK is
// refused even when this process's clock is ahead enough to call it due — and
// refused without writing a row.
func TestClaimDueGateRefusesWhatTheDatabaseSaysIsNotDue(t *testing.T) {
	ctx, f := setupPool(t)
	enrollmentID := f.enroll(t, ctx)
	job, err := f.core.GetStepSendJob(ctx, enrollmentID.String(), f.ws.String())
	if err != nil {
		t.Fatalf("GetStepSendJob: %v", err)
	}
	dbNow, skew := dbClock(t, ctx, f)
	t.Logf("database clock minus process clock ~ %v", skew)
	// Far enough ahead that the one round trip to the claim cannot cross it.
	job.NotDueUntil = dbNow.Add(5 * time.Second)

	outcome, err := f.core.ClaimStepSend(ctx, job)
	if err != nil {
		t.Fatalf("ClaimStepSend: %v", err)
	}
	if outcome != coreapi.ClaimDeferred {
		t.Fatalf("claim outcome = %v, want ClaimDeferred: the step is not due by the database's "+
			"clock (skew ~%v)", outcome, skew)
	}
	if n := sendRowCount(t, ctx, f, enrollmentID); n != 0 {
		t.Fatalf("a refused claim must write no sends row, got %d", n)
	}
}

// A zero NotDueUntil ("no recorded due time") is due, as it always was: the
// NULL the claim passes the database must not read as "not yet".
func TestClaimDueGateTreatsNoDueTimeAsDue(t *testing.T) {
	ctx, f := setupPool(t)
	enrollmentID := f.enroll(t, ctx)
	job, err := f.core.GetStepSendJob(ctx, enrollmentID.String(), f.ws.String())
	if err != nil {
		t.Fatalf("GetStepSendJob: %v", err)
	}
	job.NotDueUntil = time.Time{}

	outcome, err := f.core.ClaimStepSend(ctx, job)
	if err != nil {
		t.Fatalf("ClaimStepSend: %v", err)
	}
	if outcome != coreapi.ClaimWon {
		t.Fatalf("claim outcome = %v, want ClaimWon for a job with no recorded due time", outcome)
	}
}

// sendTracked reads the claimed row's per-send tracking stamp.
func sendTracked(t *testing.T, ctx context.Context, f poolFixture, sendID string) bool {
	t.Helper()
	id, err := uuid.Parse(sendID)
	if err != nil {
		t.Fatalf("send id: %v", err)
	}
	var tracked bool
	if err := f.pool.QueryRow(ctx,
		`SELECT tracked FROM sends WHERE id = $1 AND workspace_id = $2`, id, f.ws).Scan(&tracked); err != nil {
		t.Fatalf("read sends.tracked: %v", err)
	}
	return tracked
}

// The claim stamps whether THIS message carries tracking, from the job being
// claimed: tracked only with the campaign's flag on AND an HTML body (the pixel
// and link rewriting exist only in HTML). This is the row every "could an open
// have been recorded" aggregate reads instead of the campaign's current flag.
func TestClaimStampsWhetherTheMessageCarriesTracking(t *testing.T) {
	cases := []struct {
		name     string
		tracking bool
		html     string
		want     bool
	}{
		{"tracking on with an HTML body is tracked", true, "<p>Hi</p>", true},
		{"tracking on with a text-only body carries no pixel", true, "", false},
		{"tracking off is untracked", false, "<p>Hi</p>", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, f := setupPool(t)
			enrollmentID := f.enroll(t, ctx)
			job, err := f.core.GetStepSendJob(ctx, enrollmentID.String(), f.ws.String())
			if err != nil {
				t.Fatalf("GetStepSendJob: %v", err)
			}
			job.TrackingEnabled, job.BodyHTML = tc.tracking, tc.html
			job.NotDueUntil = time.Time{} // the due gate is not what this test is about

			outcome, err := f.core.ClaimStepSend(ctx, job)
			if err != nil || outcome != coreapi.ClaimWon {
				t.Fatalf("ClaimStepSend = %v, %v; want ClaimWon", outcome, err)
			}
			if got := sendTracked(t, ctx, f, job.SendID); got != tc.want {
				t.Fatalf("sends.tracked = %v, want %v", got, tc.want)
			}
		})
	}
}

// A reclaim re-stamps tracked, like variant_id: the retry sends the message the
// NEW job describes, and the campaign's toggle may have moved in between.
func TestClaimReclaimRestampsTracked(t *testing.T) {
	ctx := context.Background()
	pool, q := claimConnect(t)
	fx := seedForClaim(t, ctx, q)
	p := stepClaimParams(fx, fx.ws, 1)

	p.Tracked = true
	if _, err := q.ClaimStepSend(ctx, p); err != nil {
		t.Fatalf("fresh claim: %v", err)
	}
	makeStale(t, ctx, pool, p.ID)
	p.Tracked = false
	if _, err := q.ClaimStepSend(ctx, p); err != nil {
		t.Fatalf("stale reclaim: %v", err)
	}
	var tracked bool
	if err := pool.QueryRow(ctx,
		`SELECT tracked FROM sends WHERE id = $1 AND workspace_id = $2`, p.ID, fx.ws).Scan(&tracked); err != nil {
		t.Fatalf("read tracked: %v", err)
	}
	if tracked {
		t.Fatal("sends.tracked kept the first attempt's value; the reclaim must describe what is " +
			"actually about to be sent")
	}
}
