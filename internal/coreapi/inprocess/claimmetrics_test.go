package inprocess

import (
	"testing"

	"github.com/inroad/inroad/internal/platform/metrics"
)

// TestClaimWonOutcomeSeparatesFreshFromReclaimed guards the mapping that makes
// a dying-worker rate visible: winning by INSERT and winning by taking over an
// expired lease are both "the claim succeeded", but only the second means a
// previous worker died mid-send. Collapsing them would hide the single best
// health signal on the send path.
func TestClaimWonOutcomeSeparatesFreshFromReclaimed(t *testing.T) {
	if got := claimWonOutcome(true); got != metrics.ClaimOutcomeWon {
		t.Errorf("fresh insert = %q, want %q", got, metrics.ClaimOutcomeWon)
	}
	if got := claimWonOutcome(false); got != metrics.ClaimOutcomeReclaimed {
		t.Errorf("stale-lease takeover = %q, want %q", got, metrics.ClaimOutcomeReclaimed)
	}
}

// TestStepClaimKindIsFixed: the kind label must be a compile-time constant, not
// derived from anything caller-supplied, or the dimension stops being bounded.
func TestStepClaimKindIsFixed(t *testing.T) {
	if stepClaimKind != "step" {
		t.Fatalf("stepClaimKind = %q, want %q", stepClaimKind, "step")
	}
}

// TestClientWithoutMetricsHasNilMetrics documents the default every test and
// cmd/seed gets: no metrics wired, and the claim path relying on
// *metrics.Metrics' nil-receiver safety rather than its own nil check.
func TestClientWithoutMetricsHasNilMetrics(t *testing.T) {
	var c client
	if c.mtx != nil {
		t.Fatal("zero-value client must have no metrics")
	}
	c.mtx.SendClaimed(stepClaimKind, metrics.ClaimOutcomeWon) // must not panic
}

// TestWithMetricsWiresTheClient proves the option actually sets the field, so a
// composition root that passes it gets claim counting rather than silence.
func TestWithMetricsWiresTheClient(t *testing.T) {
	m := metrics.New()
	var c client
	WithMetrics(m)(&c)
	if c.mtx != m {
		t.Fatal("WithMetrics did not wire the metrics into the client")
	}
}
