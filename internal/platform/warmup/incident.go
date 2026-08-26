package warmup

import (
	"sort"
	"strings"
)

// Correlated incidents: when one shared thing breaks, say so once and name it.
//
// The evaluator decides each mailbox independently, which is correct — a mailbox's
// health is its own. The cost is that a shared fault is reported N times and named
// zero times: five mailboxes degrade, five truthful per-mailbox reasons are written,
// and the operator is told "3 throttled, 1 paused" with no cause. This file is the
// fold that recovers the cause from evidence slices B and C already recorded.
//
// It adds no evidence and it decides nothing. See DetectIncidents.

// The fault dimensions, each already a column on warmup_observations except the
// last, which is derived in Go for the reason domain.go gives at length (the public
// suffix list is data that moves, so a stored column would freeze the answer).
const (
	DimensionRoute        = "destination_route"
	DimensionSigning      = "signing_domain"
	DimensionReturnPath   = "return_path_domain"
	DimensionSenderDomain = "sender_domain"
)

// The whole policy surface of this slice, and these three numbers are guesses.
// They are tolerable as guesses because nothing gates on the result — see
// DetectIncidents' contract note — and they are named here, once, so that
// calibrating them later is an edit in one place rather than an archaeology
// exercise.
const (
	// MinIncidentMembers is the fewest degraded participants that can be called
	// correlated at all. Two.
	MinIncidentMembers = 2
	// MinIncidentCohort is the fewest participants that must CARRY a value before
	// concentration in it means anything. A value with two members, both degraded,
	// is not a pattern — it is those two mailboxes restated.
	MinIncidentCohort = 3
	// MinIncidentLift is how much more degraded the inside must be than the outside.
	// This is the constant that kills the vacuous case: when everything is degraded
	// the two rates match, lift is 1, and nothing is reported.
	MinIncidentLift = 2.0
)

// MinIncidentPool is the smallest pool detection can find anything in: a full
// cohort, plus at least one participant outside it to compare against.
//
// Served to clients rather than left for them to derive. A UI has to tell "we
// looked across the degraded mailboxes and found no shared cause" from "this pool
// is too small to look" — different answers that must not render alike — and a
// client-side copy of this number would drift the moment the constants above are
// recalibrated, leaving the UI claiming it searched a pool the server never
// examined.
const MinIncidentPool = MinIncidentCohort + 1

// unresolvedDimensionValues are values that are the ABSENCE of a classification
// rather than one. Grouping on them would correlate on our own ignorance, and it
// would fire hardest on the pools carrying the least data — the opposite of useful.
var unresolvedDimensionValues = map[string]bool{"": true, "unknown": true}

// IncidentInput is one participant reduced to the facts correlation needs: whether
// it is degraded, and which value it carries on each fault dimension.
//
// Deliberately not a generated row type. Detection is a pure fold and belongs in
// this package, so the caller projects its query into this shape rather than this
// package learning what a gen.ListWarmupIncidentRowsRow is.
type IncidentInput struct {
	MailboxID string
	Email     string
	// Degraded is either axis: health_state in {watch, throttled, paused} OR lane in
	// {quarantine, recovery, blocked}. Both, because the axes are independent by
	// design and a shared cause surfaces on either — a filtering relay lands on
	// health, an authentication fault lands on the lane.
	Degraded bool
	// The dimension values as OBSERVED, sender-attributed. Empty or "unknown" means
	// unresolved and is excluded rather than bucketed.
	Route            string
	SigningDomain    string
	ReturnPathDomain string
}

// Incident is one detected correlation, carrying the arithmetic that produced it.
//
// The counts are exported because §8's copy rule requires showing the operator the
// sum rather than a verdict: a lift of 2.1 and a lift of 12 are very different
// findings, and an "incident" badge that hides the difference is the kind of
// confident summary this subsystem has repeatedly had to walk back.
type Incident struct {
	Dimension       string
	Value           string
	Members         []string // degraded mailbox ids, sorted
	CohortSize      int      // participants carrying Value, degraded or not
	DegradedInside  int
	CohortOutside   int
	DegradedOutside int
	Lift            float64
}

// DetectIncidents folds a workspace's participants into the correlations their
// degradations support. Pure: no clock, no I/O, no DB.
//
// **This gates nothing**, and unlike slices A, B and C it needs two independent
// reasons, either of which would be sufficient.
//
// The three constants above are unvalidated guesses. That is tolerable for a
// sentence an operator reads and can dismiss; it is not tolerable for a threshold
// that withholds sending.
//
// And THREE of the four dimensions are influenceable within a workspace, which is a
// weaker attacker than it first looks:
//
//   - destination_route rests on destination_esp — security.md invariant 57, whoever
//     controls a mailbox domain's MX;
//   - signing_domain and return_path_domain are read straight off DKIM-Signature d=
//     and Return-Path by ExtractIdentity, BEFORE and outside the authserv-id trust
//     rule, which gates only the SPF/DKIM/DMARC verdicts. So they need no MX control
//     at all: read/write on ONE warmup recipient mailbox is enough to deliver a
//     crafted copy of a token-carrying message and choose the value recorded against
//     every sender that mails it.
//
// What that CANNOT do is invent a member: Members comes only from participants the
// evaluator already marked degraded, over evidence invariant 52 binds. What it can do
// is decide which correlation ranks highest, and the pulse card names only the
// strongest — so an influenced dimension can displace a true finding from that one
// line. The warmup overview lists every finding with its arithmetic, which is the
// complete view and the reason that displacement is survivable.
//
// None of this expires when calibration data arrives. Slice E cannot gate on a fault
// domain by inheriting "D proved the correlation is real" — it has to bind the
// evidence to something the attacker does not control, the way invariant 52 binds the
// placement axis, and for the identity dimensions that means more than invariant 57.
//
// Results are sorted strongest-first and are deterministic for a given input, so a
// UI and a test see the same order.
func DetectIncidents(participants []IncidentInput) []Incident {
	dimensions := []struct {
		name  string
		value func(IncidentInput) string
	}{
		{DimensionRoute, func(p IncidentInput) string { return p.Route }},
		{DimensionSigning, func(p IncidentInput) string { return p.SigningDomain }},
		{DimensionReturnPath, func(p IncidentInput) string { return p.ReturnPathDomain }},
		// Derived, not observed: one implementation of "what domain is this", which is
		// the rule this package keeps proving it needs.
		{DimensionSenderDomain, func(p IncidentInput) string { return OrganizationalDomain(p.Email) }},
	}

	out := []Incident{}
	for _, d := range dimensions {
		out = append(out, incidentsOnDimension(participants, d.name, d.value)...)
	}

	// Strongest first, then a total order so the result never depends on map
	// iteration. Two dimensions can legitimately report the same fault from
	// different angles; both are shown, because which dimension carries the
	// correlation is the actionable part.
	sort.Slice(out, func(i, j int) bool {
		if out[i].Lift != out[j].Lift {
			return out[i].Lift > out[j].Lift
		}
		if out[i].Dimension != out[j].Dimension {
			return out[i].Dimension < out[j].Dimension
		}
		return out[i].Value < out[j].Value
	})
	return out
}

// incidentsOnDimension tests every value of one dimension for concentration.
func incidentsOnDimension(participants []IncidentInput, dimension string, valueOf func(IncidentInput) string) []Incident {
	cohorts := map[string][]IncidentInput{}
	for _, p := range participants {
		v := strings.ToLower(strings.TrimSpace(valueOf(p)))
		if unresolvedDimensionValues[v] {
			continue
		}
		cohorts[v] = append(cohorts[v], p)
	}

	out := []Incident{}
	for value, cohort := range cohorts {
		if len(cohort) < MinIncidentCohort {
			continue
		}
		inCohort := map[string]bool{}
		members := []string{}
		degradedInside := 0
		for _, p := range cohort {
			inCohort[p.MailboxID] = true
			if p.Degraded {
				degradedInside++
				members = append(members, p.MailboxID)
			}
		}
		if degradedInside < MinIncidentMembers {
			continue
		}

		// The outside is the REST OF THE POOL, including participants whose value on
		// this dimension is unresolved. They are still evidence about whether
		// degradation is concentrated here: a pool where only this cohort is
		// degraded is the finding, whatever the others' dimension values happen to be.
		cohortOutside, degradedOutside := 0, 0
		for _, p := range participants {
			if inCohort[p.MailboxID] {
				continue
			}
			cohortOutside++
			if p.Degraded {
				degradedOutside++
			}
		}
		// No outside means no comparison. This is the single-value workspace — every
		// mailbox on one domain — where concentration is undefined rather than total.
		// Slice C's single-route matrix is the same trap and it must not read as a
		// finding here either.
		//
		// OVER-DETERMINED TODAY, and kept deliberately: incidentLift already excludes
		// this case, because dividing the corrected 0.5 by a zero outside gives +Inf
		// and inside/+Inf is 0, which fails the lift bar. Verified by reverting this
		// guard — no test noticed. It stays because relying on that is relying on IEEE
		// infinity arithmetic for a correctness property, and because it is the one
		// thing here that would still hold if MinIncidentLift were ever lowered to
		// zero, where lift 0 would otherwise be REPORTED.
		if cohortOutside == 0 {
			continue
		}

		lift := incidentLift(degradedInside, len(cohort), degradedOutside, cohortOutside)
		if lift < MinIncidentLift {
			continue
		}
		sort.Strings(members)
		out = append(out, Incident{
			Dimension: dimension, Value: value, Members: members,
			CohortSize: len(cohort), DegradedInside: degradedInside,
			CohortOutside: cohortOutside, DegradedOutside: degradedOutside,
			Lift: lift,
		})
	}
	return out
}

// incidentLift is how many times more degraded the inside is than the outside.
//
// A zero-degradation outside would divide by zero, and that case is the STRONGEST
// possible signal rather than an error, so it gets the standard continuity
// correction: zero out of N is scored as half a case out of N. That keeps the result
// finite (no Inf to serialise into JSON, no special case at every read) and keeps it
// monotone in the evidence — the more clean mailboxes outside the cohort, the higher
// the lift, which is the right direction.
func incidentLift(degradedInside, cohortSize, degradedOutside, cohortOutside int) float64 {
	inside := float64(degradedInside) / float64(cohortSize)
	outsideCases := float64(degradedOutside)
	if outsideCases == 0 {
		outsideCases = 0.5
	}
	return inside / (outsideCases / float64(cohortOutside))
}
