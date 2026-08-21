package warmup

// Who counts as degrading, for correlation purposes.
//
// This is the ONE place the two axes are folded into the single boolean
// IncidentInput.Degraded carries, so a caller never inlines "state is one of these
// three or lane is one of those three" at a read site — two copies of that
// comparison in two app packages is the "two things that must agree" shape this
// subsystem keeps paying for.

// incidentDegradedHealth are the health_state values that mean this mailbox's
// outbound mail is measurably worse than it was.
//
// 'unknown' is deliberately absent: it is the ABSENCE of qualified evidence, not
// bad evidence, and including it would open an incident on every freshly connected
// workspace — where every mailbox is unknown and they all share a domain, which is
// precisely the vacuous finding the concentration test exists to kill.
var incidentDegradedHealth = map[string]bool{
	StateWatch:     true,
	StateThrottled: true,
	StatePaused:    true,
}

// incidentDegradedLanes are the pool lanes that mean containment has acted.
//
// probation, pending_auth and the lane-side 'watch' are absent, and each for its
// own reason: the first two are ADMISSION states (a mailbox that has not yet earned
// the pool is not one something went wrong with), and lane 'watch' is an
// eligibility state that shares only a spelling with health 'watch'.
var incidentDegradedLanes = map[string]bool{
	LaneQuarantine: true,
	LaneRecovery:   true,
	LaneBlocked:    true,
}

// IncidentDegraded reports whether a participant belongs to the degraded
// population a correlated incident is computed over.
//
// EITHER axis is sufficient, because the two are independent by design and a
// shared cause surfaces on whichever one it happens to hit: a filtering relay lands
// on health, an authentication fault lands on the lane.
//
// This is NOT the policy's own containment test. LaneFor asks
// `stateRank(health) >= stateRank(StateThrottled)`, which excludes watch, because
// it is deciding whether to withhold traffic. This predicate is deciding whether to
// EXPLAIN something, so it starts one band earlier — watch is the first thing a
// shared cause moves. Merging the two would either drop watch from every incident
// or widen a containment rule by accident; keep them apart.
func IncidentDegraded(healthState, lane string) bool {
	return incidentDegradedHealth[healthState] || incidentDegradedLanes[lane]
}
