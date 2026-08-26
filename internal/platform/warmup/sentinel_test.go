package warmup

import (
	"fmt"
	"testing"
)

func peer(lane string) Participant     { return Participant{Lane: lane} }
func sentinel(lane string) Participant { return Participant{Lane: lane, IsSentinel: true} }

// A pool with no sentinels must behave EXACTLY as it did, because that is most
// self-hosted installations and the design requires the no-sentinel case to keep
// working rather than degrade.
func TestPairableIsUnchangedWithoutSentinels(t *testing.T) {
	lanes := []string{LanePendingAuth, LaneProbation, LaneHealthy, LaneWatch, LaneRecovery, LaneQuarantine, LaneBlocked}
	for _, s := range lanes {
		for _, r := range lanes {
			if got, want := Pairable(peer(s), peer(r)), LanesCompatible(s, r); got != want {
				t.Errorf("Pairable(%s, %s) = %v, LanesCompatible = %v — the no-sentinel path "+
					"must be the old rule exactly", s, r, got, want)
			}
		}
	}
}

// The capability sentinels exist for: a degrading mailbox gains something dependable
// to be measured against, instead of only other degrading mailboxes.
func TestPairableLetsASentinelReachAnyLaneThatMaySend(t *testing.T) {
	// pending_auth is absent deliberately: it may not send at all (LaneMaySend), and
	// a sentinel must not become a way around an unauthenticated sending domain. That
	// is asserted separately below.
	for _, lane := range []string{LaneProbation, LaneHealthy, LaneWatch, LaneRecovery} {
		t.Run(lane, func(t *testing.T) {
			if !Pairable(sentinel(LaneHealthy), peer(lane)) {
				t.Errorf("a sentinel could not send to %s", lane)
			}
			if !Pairable(peer(lane), sentinel(LaneHealthy)) {
				t.Errorf("%s could not send to a sentinel", lane)
			}
			// And the point of it: this pairing is one the old rule refused.
			if lane != LaneHealthy && LanesCompatible(LaneHealthy, lane) {
				t.Errorf("fixture broken: %s was already compatible with healthy, so this "+
					"proves nothing about sentinels", lane)
			}
		})
	}
}

// A sentinel is not a way around an unauthenticated sending domain. pending_auth
// means the DNS a sender needs is not published; pairing it with a controlled
// endpoint would produce measurements of mail that should not be leaving at all, and
// invariant 39 keeps that decision in DNS rather than in the pool.
func TestPairableDoesNotLetASentinelBypassPendingAuth(t *testing.T) {
	if Pairable(sentinel(LaneHealthy), peer(LanePendingAuth)) {
		t.Error("a sentinel was allowed to send to a pending_auth mailbox")
	}
	if Pairable(peer(LanePendingAuth), sentinel(LaneHealthy)) {
		t.Error("a pending_auth mailbox was allowed to send to a sentinel")
	}
}

// Containment outranks measurement. A sentinel that could reach into quarantine
// would make the breaker negotiable.
func TestPairableNeverReAdmitsAContainedMailbox(t *testing.T) {
	for _, contained := range []string{LaneQuarantine, LaneBlocked} {
		t.Run(contained, func(t *testing.T) {
			if Pairable(sentinel(LaneHealthy), peer(contained)) {
				t.Errorf("a sentinel reached into %s", contained)
			}
			if Pairable(peer(contained), sentinel(LaneHealthy)) {
				t.Errorf("%s reached a sentinel", contained)
			}
			// Even a contained sentinel stays contained: being a sentinel is not an
			// exemption from its own health.
			if Pairable(sentinel(contained), peer(LaneHealthy)) {
				t.Errorf("a %s SENTINEL was allowed to send", contained)
			}
		})
	}
}

// Two sentinels are still a valid pair — a sentinel is not barred from its own kind.
func TestPairableAllowsTwoSentinels(t *testing.T) {
	if !Pairable(sentinel(LaneHealthy), sentinel(LaneWatch)) {
		t.Error("two sentinels on different lanes could not pair")
	}
}

// Confidence is reported, not applied. Peer-only is not bad evidence — it is
// evidence that is not INDEPENDENT, because a shared cause moves both sides of a
// same-lane comparison at once.
func TestConfidenceOfDistinguishesCorroboratedFromPeerOnly(t *testing.T) {
	if got := ConfidenceOf(0); got != ConfidencePeerOnly {
		t.Errorf("ConfidenceOf(0) = %q, want %q", got, ConfidencePeerOnly)
	}
	for _, n := range []int{1, 40} {
		if got := ConfidenceOf(n); got != ConfidenceSentinelCorroborated {
			t.Errorf("ConfidenceOf(%d) = %q, want %q", n, got, ConfidenceSentinelCorroborated)
		}
	}
	// A negative count is nonsense a caller could produce from a bad scan; it must
	// not read as corroboration.
	if got := ConfidenceOf(-1); got != ConfidencePeerOnly {
		t.Errorf("ConfidenceOf(-1) = %q, want %q — a nonsense count is not corroboration", got, ConfidencePeerOnly)
	}
}

func TestSentinelPoolOversized(t *testing.T) {
	tests := []struct {
		sentinels, pool int
		want            bool
	}{
		{0, 10, false},
		{5, 10, false}, // exactly at the share, which is not over it
		{6, 10, true},
		{1, 1, true}, // a pool of nothing but a sentinel measures nothing
		{0, 0, false},
		{3, 0, false}, // no pool: nothing to be a share of
	}
	for _, tc := range tests {
		t.Run(fmt.Sprintf("%d/%d", tc.sentinels, tc.pool), func(t *testing.T) {
			if got := SentinelPoolOversized(tc.sentinels, tc.pool); got != tc.want {
				t.Errorf("SentinelPoolOversized(%d, %d) = %v, want %v", tc.sentinels, tc.pool, got, tc.want)
			}
		})
	}
}
