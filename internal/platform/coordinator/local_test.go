package coordinator

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/inroad/inroad/internal/platform/warmup"
)

var testNow = time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC)

// fakePool is the caller's own pool, stubbed. It records what it was asked so a
// test can prove the coordinator refused BEFORE reaching it — which is the whole
// point of the requester-side gates.
type fakePool struct {
	candidate Candidate
	found     bool
	err       error

	calls int
	got   PairRequest
}

func (p *fakePool) SelectPartner(_ context.Context, req PairRequest) (Candidate, bool, error) {
	p.calls++
	p.got = req
	return p.candidate, p.found, p.err
}

func participant(workspace, id, lane string) Participant {
	return Participant{WorkspaceID: workspace, ID: id, Lane: lane}
}

func candidate(workspace, id, lane string) Candidate {
	return Candidate{Participant: participant(workspace, id, lane), Address: id + "@pool.test"}
}

func request(requester Participant) PairRequest {
	return PairRequest{
		Requester:   requester,
		Constraints: Constraints{CooldownSince: testNow.Add(-7 * 24 * time.Hour), MaxPairSendsPerDay: 2},
		Now:         testNow,
	}
}

// The happy path: a same-workspace, same-lane peer becomes an assignment carrying
// the partner's address and a lease the EXISTING claim-time check validates.
func TestLocalCoordinatorAssignsASameWorkspacePartner(t *testing.T) {
	pool := &fakePool{candidate: candidate("ws-1", "mb-b", warmup.LaneHealthy), found: true}
	got, err := NewLocal(pool).RequestPair(context.Background(), request(participant("ws-1", "mb-a", warmup.LaneHealthy)))
	if err != nil {
		t.Fatalf("RequestPair: %v", err)
	}
	if got.Partner.ID != "mb-b" || got.Partner.Address != "mb-b@pool.test" {
		t.Errorf("partner = %+v, want mb-b / mb-b@pool.test", got.Partner)
	}
	if got.Lease.ID == "" {
		t.Error("lease has no id: nothing can reference or revoke this authority")
	}
	if ok, code := warmup.LeaseValid(got.Lease.Terms, warmup.LaneHealthy, testNow); !ok {
		t.Errorf("issued lease is already invalid at issue time: %s", code)
	}
	if got.Lease.Terms.ExpiresAt != testNow.Add(warmup.LeaseLifetime) {
		t.Errorf("ExpiresAt = %v, want now+warmup.LeaseLifetime — the expiry arithmetic has one home", got.Lease.Terms.ExpiresAt)
	}
	if got.Lease.Terms.IssuedPolicyVersion != warmup.PolicyVersion {
		t.Errorf("IssuedPolicyVersion = %q, want %q", got.Lease.Terms.IssuedPolicyVersion, warmup.PolicyVersion)
	}
}

// A sentinel is the one bridge across lanes, and the rule for that lives in
// warmup.Pairable. The coordinator must consult it rather than restate it.
func TestLocalCoordinatorAssignsASentinelInAnotherLane(t *testing.T) {
	c := candidate("ws-1", "mb-s", warmup.LaneHealthy)
	c.IsSentinel = true
	pool := &fakePool{candidate: c, found: true}
	got, err := NewLocal(pool).RequestPair(context.Background(), request(participant("ws-1", "mb-a", warmup.LaneWatch)))
	if err != nil {
		t.Fatalf("RequestPair: %v", err)
	}
	if got.Partner.ID != "mb-s" {
		t.Errorf("partner = %q, want the sentinel mb-s", got.Partner.ID)
	}
}

func TestLocalCoordinatorRefusals(t *testing.T) {
	poolErr := errors.New("pool exploded")

	selfPair := candidate("ws-1", "mb-a", warmup.LaneHealthy)
	noAddress := candidate("ws-1", "mb-b", warmup.LaneHealthy)
	noAddress.Address = ""
	noID := candidate("ws-1", "", warmup.LaneHealthy)
	noLane := candidate("ws-1", "mb-b", "")

	tests := []struct {
		name      string
		requester Participant
		pool      *fakePool
		want      error
		wantCalls int
	}{
		{
			name:      "no eligible partner is a refusal, not a failure",
			requester: participant("ws-1", "mb-a", warmup.LaneHealthy),
			pool:      &fakePool{},
			want:      ErrNoPartner,
			wantCalls: 1,
		},
		{
			name:      "a candidate from another workspace is cross-tenant",
			requester: participant("ws-1", "mb-a", warmup.LaneHealthy),
			pool:      &fakePool{candidate: candidate("ws-2", "mb-b", warmup.LaneHealthy), found: true},
			want:      ErrCrossTenant,
			wantCalls: 1,
		},
		{
			name:      "an incompatible lane is a pool bug, refused loudly",
			requester: participant("ws-1", "mb-a", warmup.LaneHealthy),
			pool:      &fakePool{candidate: candidate("ws-1", "mb-b", warmup.LaneWatch), found: true},
			want:      ErrInvalidCandidate,
			wantCalls: 1,
		},
		{
			name:      "a partner with no address cannot be mailed",
			requester: participant("ws-1", "mb-a", warmup.LaneHealthy),
			pool:      &fakePool{candidate: noAddress, found: true},
			want:      ErrInvalidCandidate,
			wantCalls: 1,
		},
		{
			name:      "a partner with no id cannot be referenced again",
			requester: participant("ws-1", "mb-a", warmup.LaneHealthy),
			pool:      &fakePool{candidate: noID, found: true},
			want:      ErrInvalidCandidate,
			wantCalls: 1,
		},
		{
			// The requester is on PROBATION on purpose. An empty lane normalizes
			// to probation inside warmup, so warmup.Pairable would find these two
			// compatible and hand out a partner whose containment state nobody
			// supplied. Against a healthy requester this case passes by accident.
			name:      "a partner with no lane is unusable, never assumed compatible",
			requester: participant("ws-1", "mb-a", warmup.LaneProbation),
			pool:      &fakePool{candidate: noLane, found: true},
			want:      ErrInvalidCandidate,
			wantCalls: 1,
		},
		{
			name:      "a mailbox may not be paired with itself",
			requester: participant("ws-1", "mb-a", warmup.LaneHealthy),
			pool:      &fakePool{candidate: selfPair, found: true},
			want:      ErrInvalidCandidate,
			wantCalls: 1,
		},
		{
			name:      "a pool failure propagates, and is not mistaken for an empty pool",
			requester: participant("ws-1", "mb-a", warmup.LaneHealthy),
			pool:      &fakePool{err: poolErr},
			want:      poolErr,
			wantCalls: 1,
		},
		{
			name:      "a quarantined requester never reaches the pool",
			requester: participant("ws-1", "mb-a", warmup.LaneQuarantine),
			pool:      &fakePool{candidate: candidate("ws-1", "mb-b", warmup.LaneHealthy), found: true},
			want:      ErrInvalidRequest,
			wantCalls: 0,
		},
		{
			name:      "a request with no workspace never reaches the pool",
			requester: participant("", "mb-a", warmup.LaneHealthy),
			pool:      &fakePool{candidate: candidate("ws-1", "mb-b", warmup.LaneHealthy), found: true},
			want:      ErrInvalidRequest,
			wantCalls: 0,
		},
		{
			// An empty lane normalizes to probation inside warmup, so accepting
			// one would have the coordinator answer a containment question by
			// default. Refused instead.
			name:      "a request with no lane never reaches the pool",
			requester: participant("ws-1", "mb-a", ""),
			pool:      &fakePool{candidate: candidate("ws-1", "mb-b", warmup.LaneHealthy), found: true},
			want:      ErrInvalidRequest,
			wantCalls: 0,
		},
		{
			name:      "a request with no participant id never reaches the pool",
			requester: participant("ws-1", "", warmup.LaneHealthy),
			pool:      &fakePool{candidate: candidate("ws-1", "mb-b", warmup.LaneHealthy), found: true},
			want:      ErrInvalidRequest,
			wantCalls: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NewLocal(tt.pool).RequestPair(context.Background(), request(tt.requester))
			if !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
			if got != (Assignment{}) {
				t.Errorf("a refusal returned partner data: %+v", got)
			}
			if tt.pool.calls != tt.wantCalls {
				t.Errorf("pool called %d times, want %d", tt.pool.calls, tt.wantCalls)
			}
		})
	}
}

// A zero clock would mint a lease that expired in 1970 (or, read the other way,
// one whose expiry says nothing). Callers pass their clock everywhere else in this
// subsystem; an unset one is a caller bug, not a default.
func TestLocalCoordinatorRefusesAZeroClock(t *testing.T) {
	pool := &fakePool{candidate: candidate("ws-1", "mb-b", warmup.LaneHealthy), found: true}
	req := request(participant("ws-1", "mb-a", warmup.LaneHealthy))
	req.Now = time.Time{}
	if _, err := NewLocal(pool).RequestPair(context.Background(), req); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("err = %v, want ErrInvalidRequest", err)
	}
	if pool.calls != 0 {
		t.Errorf("pool called %d times, want 0", pool.calls)
	}
}

// The constraints are the caller's policy and must reach the pool unchanged: a
// coordinator may tighten what it was asked for, never loosen it, and the local
// one has no reason to touch them at all.
func TestLocalCoordinatorPassesTheCallersConstraintsToThePool(t *testing.T) {
	pool := &fakePool{candidate: candidate("ws-1", "mb-b", warmup.LaneHealthy), found: true}
	req := request(participant("ws-1", "mb-a", warmup.LaneHealthy))
	if _, err := NewLocal(pool).RequestPair(context.Background(), req); err != nil {
		t.Fatalf("RequestPair: %v", err)
	}
	if pool.got.Constraints != req.Constraints {
		t.Errorf("pool saw constraints %+v, want %+v", pool.got.Constraints, req.Constraints)
	}
}
