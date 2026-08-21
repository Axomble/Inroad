package warmup

import "testing"

// Both axes, independently. The two columns are independent by design — a
// filtering relay lands on health, an authentication fault lands on the lane — so a
// predicate that reads only one of them would miss half the population a shared
// cause shows up in.
func TestIncidentDegradedReadsBothAxes(t *testing.T) {
	cases := []struct {
		name        string
		health      string
		lane        string
		wantDegrade bool
	}{
		{"healthy on both axes", StateHealthy, LaneHealthy, false},
		// Unknown is the ABSENCE of evidence, not bad evidence. A pool that has not
		// been measured yet must not read as a pool that is degrading, or every new
		// workspace opens with an incident on whatever its mailboxes share.
		{"unknown health is not degraded", StateUnknown, LaneHealthy, false},
		{"watch health alone", StateWatch, LaneHealthy, true},
		{"throttled health alone", StateThrottled, LaneHealthy, true},
		{"paused health alone", StatePaused, LaneHealthy, true},
		{"quarantine lane alone", StateHealthy, LaneQuarantine, true},
		{"recovery lane alone", StateHealthy, LaneRecovery, true},
		{"blocked lane alone", StateHealthy, LaneBlocked, true},
		{"both axes at once counts once", StatePaused, LaneBlocked, true},
		// probation and pending_auth are ADMISSION states: a mailbox that has not yet
		// earned the pool is not a mailbox something went wrong with, and counting them
		// would make every freshly connected workspace look like an outage.
		{"probation lane is not a degradation", StateHealthy, LaneProbation, false},
		{"pending_auth lane is not a degradation", StateHealthy, LanePendingAuth, false},
		// The lane vocabulary has its own 'watch', and it is NOT in the set: the lane
		// watch is a sending lane, whereas health watch is the first band of a
		// worsening rate. Reading them alike would fold an eligibility state into a
		// reputation finding.
		{"watch lane is not a degradation", StateHealthy, LaneWatch, false},
		// A mailbox that is not a participant carries no live signal at all.
		{"empty axes", "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IncidentDegraded(tc.health, tc.lane); got != tc.wantDegrade {
				t.Errorf("IncidentDegraded(%q, %q) = %v, want %v", tc.health, tc.lane, got, tc.wantDegrade)
			}
		})
	}
}

// The incident population is DELIBERATELY wider than the policy's own "degraded"
// test, which is `stateRank >= StateThrottled` (see LaneFor). A mailbox on watch is
// not contained by any lane decision, but it is exactly the early signal a shared
// cause produces — so folding the two predicates into one would either drop watch
// from every incident or silently widen a containment rule.
func TestIncidentDegradedIsWiderThanThePolicyContainmentTest(t *testing.T) {
	if !IncidentDegraded(StateWatch, LaneHealthy) {
		t.Fatal("watch must count toward an incident: it is the earliest health band a shared cause moves")
	}
	if stateRank(StateWatch) >= stateRank(StateThrottled) {
		t.Fatal("watch now ranks at or above throttled, so the policy's containment test and this " +
			"predicate agree by accident — re-read why they are separate before merging them")
	}
}
