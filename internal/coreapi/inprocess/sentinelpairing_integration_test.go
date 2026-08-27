//go:build integration

package inprocess

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/platform/warmup"
)

// Sentinel pairing over real rows: the SQL half of warmup.Pairable.
//
// The rule lives in two places by necessity — Go states it, the partner-selection
// query enforces it — so these tests exist to keep the two from drifting. That
// duplication is the shape this subsystem's defects keep taking, and the only
// defence is a fixture that would notice.

// setLane forces a participant's lane, which the evaluator would otherwise own.
func setLane(t *testing.T, ctx context.Context, f warmupFixture, mailbox uuid.UUID, lane string) {
	t.Helper()
	if _, err := f.raw.Exec(ctx,
		`UPDATE warmup_participants SET lane = $3 WHERE workspace_id = $1 AND mailbox_id = $2`,
		f.ws1, mailbox, lane); err != nil {
		t.Fatalf("set lane %s on %s: %v", lane, mailbox, err)
	}
}

func setSentinel(t *testing.T, ctx context.Context, f warmupFixture, mailbox uuid.UUID, on bool) {
	t.Helper()
	if _, err := f.raw.Exec(ctx,
		`UPDATE warmup_participants SET is_sentinel = $3 WHERE workspace_id = $1 AND mailbox_id = $2`,
		f.ws1, mailbox, on); err != nil {
		t.Fatalf("set is_sentinel on %s: %v", mailbox, err)
	}
}

// pairs reports whether the sender can find any partner at all right now.
func pairs(t *testing.T, ctx context.Context, f warmupFixture, sender uuid.UUID) bool {
	t.Helper()
	job, err := f.core.GetWarmupSendJob(ctx, sender.String(), f.ws1.String())
	if err != nil {
		t.Fatalf("GetWarmupSendJob(%s): %v", sender, err)
	}
	return !job.Skip
}

// The capability sentinels exist for. A watch mailbox in a healthy pool has no
// same-lane peer, so today it cannot be measured at all; a sentinel gives it one.
func TestAWatchMailboxCanPairWithASentinelButNotAHealthyPeer(t *testing.T) {
	ctx, f := setupWarmup(t)
	setLane(t, ctx, f, f.a, warmup.LaneWatch)
	setLane(t, ctx, f, f.b, warmup.LaneHealthy)

	if pairs(t, ctx, f, f.a) {
		t.Fatal("a watch mailbox paired with a healthy peer; without that isolation this " +
			"test cannot show what the sentinel flag adds")
	}

	setSentinel(t, ctx, f, f.b, true)

	if !pairs(t, ctx, f, f.a) {
		t.Error("a watch mailbox could not pair with a sentinel")
	}
}

// Containment outranks measurement, in SQL as in Go. A sentinel that could reach
// past the breaker would make the breaker negotiable.
func TestASentinelNeverReachesAContainedMailbox(t *testing.T) {
	for _, contained := range []string{warmup.LaneQuarantine, warmup.LaneBlocked, warmup.LanePendingAuth} {
		t.Run(contained, func(t *testing.T) {
			ctx, f := setupWarmup(t)
			setLane(t, ctx, f, f.a, warmup.LaneHealthy)
			setSentinel(t, ctx, f, f.a, true)
			setLane(t, ctx, f, f.b, contained)

			if pairs(t, ctx, f, f.a) {
				t.Errorf("a sentinel paired with a %s mailbox", contained)
			}

			// And the contained side cannot reach out to the sentinel either.
			setSentinel(t, ctx, f, f.b, true)
			if pairs(t, ctx, f, f.b) {
				t.Errorf("a %s SENTINEL was allowed to send — being a sentinel is not an "+
					"exemption from its own containment", contained)
			}
		})
	}
}

// A pool with no sentinels behaves exactly as it did. This is most self-hosted
// installations, and the regression it guards is the one most likely to ship.
func TestASentinelFreePoolPairsExactlyAsBefore(t *testing.T) {
	ctx, f := setupWarmup(t)

	for _, lane := range []string{warmup.LaneHealthy, warmup.LaneWatch, warmup.LaneProbation, warmup.LaneRecovery} {
		setLane(t, ctx, f, f.a, lane)
		setLane(t, ctx, f, f.b, lane)
		if !pairs(t, ctx, f, f.a) {
			t.Errorf("same-lane %s pair was refused", lane)
		}
	}

	// And across lanes it is still refused, with no sentinel to widen it.
	setLane(t, ctx, f, f.a, warmup.LaneWatch)
	setLane(t, ctx, f, f.b, warmup.LaneProbation)
	if pairs(t, ctx, f, f.a) {
		t.Error("a cross-lane pair was allowed with no sentinel in the pool")
	}
}
