package warmup

// Exposure ceilings: how much of a campaign a DEGRADING fault domain may carry.
//
// The engine already has a breaker. LaneMaySend cuts a quarantined or blocked domain
// off entirely, and NewLeadsWithheld stops it taking new leads. What it has never had
// is anything in between: a domain in `watch` — one the evaluator has decided is
// deteriorating — carries exactly the same share of a campaign as a healthy one, right
// up until the moment it carries none. The design pairs "exposure budget" with
// "circuit breaker" for that reason; this is the budget.
//
// It reads a LANE, not evidence. The lane is the evaluator's conclusion over
// invariant-52-bound observations, and the fault domain it applies to is the sender's
// organizational domain, derived from mailboxes.email — our own record. Neither is
// route-derived, neither comes from a message, so nothing here reaches the influence
// security.md invariants 57, 58 and 59 constrain. That distinction is what makes this
// safe to act on when the incident fold and the observer detector are not.

// Share ceilings by lane. Guesses, and cheap ones: they narrow an already-eligible
// candidate list, so being wrong picks a different healthy mailbox rather than
// withholding a send. The ordering is the part that is not a guess — a deteriorating
// domain must not carry more than a healthy one.
const (
	// WatchExposureCeiling applies to a domain the evaluator has put on watch: still
	// sending, and no longer trusted with the bulk of a campaign.
	WatchExposureCeiling = 0.35
	// RecoveryExposureCeiling applies to a domain re-earning trust after containment.
	// Lower than watch because recovery is the more fragile state: it has already
	// failed once, and the qualified clean window it needs is easier to reach on
	// less traffic, not more.
	RecoveryExposureCeiling = 0.20
)

// ExposureCeiling is the most of a campaign's assigned volume this lane's domains
// should carry, or 0 when this package has no opinion and the caller's flat default
// applies.
//
// Zero deliberately means "no opinion" rather than "may not send". Containment is
// LaneMaySend's decision and stays there: a ceiling of zero here would be a second
// implementation of "may this send", and two things that must agree about that is the
// shape every repeated defect in this subsystem has taken. A quarantined domain never
// reaches this function with anything to narrow, because the gate removed it first.
func ExposureCeiling(lane string) float64 {
	switch normalizeLane(lane) {
	case LaneWatch:
		return WatchExposureCeiling
	case LaneRecovery:
		return RecoveryExposureCeiling
	default:
		return 0
	}
}
