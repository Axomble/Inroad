package coordinator

import (
	"context"
	"testing"
	"time"

	"github.com/inroad/inroad/internal/platform/warmup"
)

// issue runs one pair request against a pool holding exactly this partner and
// returns the lease id. Failing the request is a test failure: every case here is
// well formed by construction.
func issue(t *testing.T, requester Participant, partner Candidate, now time.Time) string {
	t.Helper()
	req := request(requester)
	req.Now = now
	got, err := NewLocal(&fakePool{candidate: partner, found: true}).RequestPair(context.Background(), req)
	if err != nil {
		t.Fatalf("RequestPair: %v", err)
	}
	return got.Lease.ID
}

// Warmup ticks are asynq tasks and asynq tasks retry. A random lease id would mean
// a retried tick books the same pair twice under two authorities; a derived one
// means the retry produces the lease it already had.
func TestLeaseIDIsTheSameForTheSameRequest(t *testing.T) {
	me := participant("ws-1", "mb-a", warmup.LaneHealthy)
	partner := candidate("ws-1", "mb-b", warmup.LaneHealthy)

	first := issue(t, me, partner, testNow)
	second := issue(t, me, partner, testNow)
	if first != second {
		t.Errorf("same request produced %q then %q — a retry would double-book the pair", first, second)
	}
	if first == "" {
		t.Error("lease id is empty")
	}
}

// Everything the lease is BINDING must change it. An id that ignores one of these
// is an id that authorizes a send it was not issued for.
func TestLeaseIDChangesWithEveryBoundFact(t *testing.T) {
	me := participant("ws-1", "mb-a", warmup.LaneHealthy)
	partner := candidate("ws-1", "mb-b", warmup.LaneHealthy)
	base := issue(t, me, partner, testNow)

	otherWorkspace := participant("ws-2", "mb-a", warmup.LaneHealthy)
	otherWorkspacePartner := candidate("ws-2", "mb-b", warmup.LaneHealthy)

	tests := []struct {
		name      string
		requester Participant
		partner   Candidate
		now       time.Time
	}{
		{"a different partner", me, candidate("ws-1", "mb-c", warmup.LaneHealthy), testNow},
		{"a different requester", participant("ws-1", "mb-z", warmup.LaneHealthy), partner, testNow},
		{"a different workspace", otherWorkspace, otherWorkspacePartner, testNow},
		{"a different lane", participant("ws-1", "mb-a", warmup.LaneProbation), candidate("ws-1", "mb-b", warmup.LaneProbation), testNow},
		{"a different moment", me, partner, testNow.Add(time.Minute)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := issue(t, tt.requester, tt.partner, tt.now); got == base {
				t.Errorf("lease id %q is unchanged by %s", got, tt.name)
			}
		})
	}
}

// Concatenating identifiers to hash them lets the boundary between two fields
// move without changing the bytes: "ab"+"c" and "a"+"bc" are the same input. A
// lease that cannot tell those apart authorizes a pair it was not issued for.
//
// Mailbox ids are UUIDs today, so the collision is not reachable now — which is
// precisely when to close it, because the id is opaque by contract and a remote
// adapter substitutes pseudonyms of its own choosing.
func TestLeaseIDDoesNotConfuseAdjacentIdentifiers(t *testing.T) {
	left := issue(t, participant("ws-1", "ab", warmup.LaneHealthy), candidate("ws-1", "c", warmup.LaneHealthy), testNow)
	right := issue(t, participant("ws-1", "a", warmup.LaneHealthy), candidate("ws-1", "bc", warmup.LaneHealthy), testNow)
	if left == right {
		t.Errorf("(%q,%q) and (%q,%q) share lease id %q", "ab", "c", "a", "bc", left)
	}
}
