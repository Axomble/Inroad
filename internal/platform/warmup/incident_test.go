package warmup

import (
	"fmt"
	"math"
	"testing"
)

// p builds one participant. Email defaults to a distinct per-id address on its own
// organizational domain, so the sender-domain dimension does NOT accidentally
// correlate every fixture that forgot to set it — a shared default would make every
// test a sender-domain incident and hide whichever dimension was under test.
func p(id string, degraded bool, route, signing, returnPath string) IncidentInput {
	return IncidentInput{
		MailboxID: id, Email: id + "@" + id + ".test", Degraded: degraded,
		Route: route, SigningDomain: signing, ReturnPathDomain: returnPath,
	}
}

// clean returns n undegraded participants, each on its own everything, as the
// "rest of the pool" that concentration is measured against.
func clean(n int) []IncidentInput {
	out := make([]IncidentInput, 0, n)
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("clean-%02d", i)
		out = append(out, p(id, false, "google", "sign-"+id+".test", "rp-"+id+".test"))
	}
	return out
}

func find(t *testing.T, incidents []Incident, dimension, value string) Incident {
	t.Helper()
	for _, in := range incidents {
		if in.Dimension == dimension && in.Value == value {
			return in
		}
	}
	t.Fatalf("no %s incident on %q; got %+v", dimension, value, incidents)
	return Incident{}
}

func assertNone(t *testing.T, incidents []Incident, dimension string) {
	t.Helper()
	for _, in := range incidents {
		if in.Dimension == dimension {
			t.Errorf("expected no %s incident, got %+v", dimension, in)
		}
	}
}

// The finding this slice exists to produce: degradation concentrated in one route
// while the rest of the pool is fine.
func TestDetectIncidentsReportsAConcentratedRoute(t *testing.T) {
	pool := clean(20)
	pool[0].Degraded = true // one unrelated degradation outside the cohort
	for i := 0; i < 5; i++ {
		pool = append(pool, p(fmt.Sprintf("ms-%d", i), i < 4, "microsoft", fmt.Sprintf("s%d.test", i), fmt.Sprintf("r%d.test", i)))
	}

	got := find(t, DetectIncidents(pool), DimensionRoute, "microsoft")

	if got.CohortSize != 5 || got.DegradedInside != 4 {
		t.Errorf("cohort = %d/%d degraded, want 4/5", got.DegradedInside, got.CohortSize)
	}
	if got.CohortOutside != 20 || got.DegradedOutside != 1 {
		t.Errorf("outside = %d/%d degraded, want 1/20", got.DegradedOutside, got.CohortOutside)
	}
	// 0.8 inside against 0.05 outside.
	if math.Abs(got.Lift-16) > 0.001 {
		t.Errorf("lift = %v, want 16", got.Lift)
	}
	if len(got.Members) != 4 {
		t.Errorf("members = %v, want the 4 degraded cohort members", got.Members)
	}
}

// THE vacuity guard, and the reason lift exists rather than a bare count.
//
// When everything is degraded, every dimension is trivially "correlated" — and it
// would be loudest on the smallest pools, which is exactly when an operator can
// least afford to be misdirected by a root-cause finding that only restates "your
// mailboxes are degraded".
func TestDetectIncidentsReportsNothingWhenEverythingIsDegraded(t *testing.T) {
	var pool []IncidentInput
	for i := 0; i < 5; i++ {
		pool = append(pool, IncidentInput{
			MailboxID: fmt.Sprintf("a%d", i), Email: fmt.Sprintf("a%d@acme.test", i), Degraded: true,
			Route: "google", SigningDomain: "mail.acme.test", ReturnPathDomain: "bounce.acme.test",
		})
	}
	for i := 0; i < 5; i++ {
		pool = append(pool, IncidentInput{
			MailboxID: fmt.Sprintf("b%d", i), Email: fmt.Sprintf("b%d@other.test", i), Degraded: true,
			Route: "microsoft", SigningDomain: "mail.other.test", ReturnPathDomain: "bounce.other.test",
		})
	}

	if got := DetectIncidents(pool); len(got) != 0 {
		t.Errorf("a uniformly degraded pool produced %d incidents; every dimension is "+
			"trivially correlated here and none of them is a finding: %+v", len(got), got)
	}
}

// A workspace where every mailbox shares the value has no outside to compare
// against. Concentration is UNDEFINED, not total — the same trap as slice C's
// single-route matrix, where one clean row is not a clean matrix.
func TestDetectIncidentsNeedsAnOutsideToCompareAgainst(t *testing.T) {
	var pool []IncidentInput
	for i := 0; i < 4; i++ {
		pool = append(pool, IncidentInput{
			MailboxID: fmt.Sprintf("m%d", i), Email: fmt.Sprintf("m%d@acme.test", i),
			Degraded: i < 3, Route: "google", SigningDomain: "mail.acme.test", ReturnPathDomain: "b.acme.test",
		})
	}

	for _, d := range []string{DimensionRoute, DimensionSigning, DimensionReturnPath, DimensionSenderDomain} {
		assertNone(t, DetectIncidents(pool), d)
	}
}

func TestDetectIncidentsThresholds(t *testing.T) {
	tests := []struct {
		name    string
		cohort  int
		bad     int
		outside int
		badOut  int
		want    bool
	}{
		// One degraded member is not a correlation however clean the rest of the pool.
		{"one degraded member is below MinIncidentMembers", 5, 1, 20, 0, false},
		{"two degraded members clear MinIncidentMembers", 5, 2, 20, 0, true},
		// A two-member value, both degraded, is those two mailboxes restated.
		{"a two-member cohort is below MinIncidentCohort", 2, 2, 20, 0, false},
		{"a three-member cohort clears MinIncidentCohort", 3, 2, 20, 0, true},
		// 0.5 inside against 0.3 outside is 1.67x — real, and not enough.
		{"lift just under the bar is not reported", 4, 2, 10, 3, false},
		// 0.5 against 0.25 is exactly 2x, and the bar is inclusive.
		{"lift exactly at the bar is reported", 4, 2, 8, 2, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pool := clean(tc.outside)
			for i := 0; i < tc.badOut; i++ {
				pool[i].Degraded = true
			}
			for i := 0; i < tc.cohort; i++ {
				pool = append(pool, p(fmt.Sprintf("c%d", i), i < tc.bad, "microsoft",
					fmt.Sprintf("s%d.test", i), fmt.Sprintf("r%d.test", i)))
			}

			var found bool
			for _, in := range DetectIncidents(pool) {
				if in.Dimension == DimensionRoute && in.Value == "microsoft" {
					found = true
				}
			}
			if found != tc.want {
				t.Errorf("reported = %v, want %v (inside %d/%d, outside %d/%d)",
					found, tc.want, tc.bad, tc.cohort, tc.badOut, tc.outside)
			}
		})
	}
}

// An unresolved value is the ABSENCE of a classification. Grouping on it would
// correlate on our own ignorance, and it would fire hardest on the pools carrying
// the least data — the exact inversion of useful.
func TestDetectIncidentsNeverCorrelatesOnUnresolvedValues(t *testing.T) {
	for _, unresolved := range []string{"unknown", "", "  ", "UNKNOWN"} {
		t.Run(fmt.Sprintf("%q", unresolved), func(t *testing.T) {
			pool := clean(20)
			for i := 0; i < 5; i++ {
				pool = append(pool, IncidentInput{
					MailboxID: fmt.Sprintf("u%d", i), Email: fmt.Sprintf("u%d@u%d.test", i, i),
					Degraded: true, Route: unresolved, SigningDomain: unresolved, ReturnPathDomain: unresolved,
				})
			}

			for _, in := range DetectIncidents(pool) {
				t.Errorf("correlated on the unresolved value %q as dimension %s: %+v",
					unresolved, in.Dimension, in)
			}
		})
	}
}

// A zero-degradation outside is the STRONGEST signal available, not a divide-by-zero
// to special-case at every read. The continuity correction keeps it finite (no Inf
// to serialise) and monotone: more clean mailboxes outside means higher lift.
func TestDetectIncidentsScoresACleanOutsideAsTheStrongestSignal(t *testing.T) {
	build := func(outside int) Incident {
		pool := clean(outside)
		for i := 0; i < 3; i++ {
			pool = append(pool, p(fmt.Sprintf("x%d", i), true, "microsoft",
				fmt.Sprintf("s%d.test", i), fmt.Sprintf("r%d.test", i)))
		}
		return find(t, DetectIncidents(pool), DimensionRoute, "microsoft")
	}

	small, large := build(10), build(40)
	if !(large.Lift > small.Lift) {
		t.Errorf("lift did not rise with a larger clean outside: %v vs %v", small.Lift, large.Lift)
	}
	for _, in := range []Incident{small, large} {
		if math.IsInf(in.Lift, 0) || math.IsNaN(in.Lift) {
			t.Errorf("lift = %v, which cannot be serialised into JSON", in.Lift)
		}
	}
}

// Values are folded on case and surrounding whitespace, or one signing domain
// written two ways would split into two cohorts and neither would clear the bar —
// a correlation lost to formatting.
func TestDetectIncidentsFoldsValueFormatting(t *testing.T) {
	pool := clean(20)
	for i, v := range []string{"Mail.Acme.Test", "mail.acme.test ", " MAIL.acme.test"} {
		pool = append(pool, p(fmt.Sprintf("f%d", i), true, "google", v, fmt.Sprintf("r%d.test", i)))
	}

	got := find(t, DetectIncidents(pool), DimensionSigning, "mail.acme.test")
	if got.CohortSize != 3 || got.DegradedInside != 3 {
		t.Errorf("cohort = %d/%d, want 3/3 — the three spellings are one domain",
			got.DegradedInside, got.CohortSize)
	}
}

// The sender-domain dimension groups on the ORGANIZATIONAL domain, so sibling
// subdomains land together. This is the one dimension where folding to the eTLD+1 is
// right: it asks "whose reputation is this", which is the gate's question, not
// "which DNS name published this key".
func TestDetectIncidentsGroupsSenderSubdomainsByOrganizationalDomain(t *testing.T) {
	pool := clean(20)
	for i, email := range []string{"a@mail.acme.test", "b@send.acme.test", "c@acme.test"} {
		pool = append(pool, IncidentInput{
			MailboxID: fmt.Sprintf("s%d", i), Email: email, Degraded: true,
			Route: "google", SigningDomain: fmt.Sprintf("k%d.test", i), ReturnPathDomain: fmt.Sprintf("r%d.test", i),
		})
	}

	got := find(t, DetectIncidents(pool), DimensionSenderDomain, "acme.test")
	if got.CohortSize != 3 {
		t.Errorf("cohort = %d, want 3 — three subdomains of one organizational domain", got.CohortSize)
	}
}

// Strongest first, and a total order after that, so a UI and a test never disagree
// about which incident is the headline.
func TestDetectIncidentsIsSortedAndDeterministic(t *testing.T) {
	pool := clean(20)
	for i := 0; i < 5; i++ { // strong: 4/5 on one route
		pool = append(pool, p(fmt.Sprintf("ms-%d", i), i < 4, "microsoft", fmt.Sprintf("ms%d.test", i), fmt.Sprintf("r%d.test", i)))
	}
	for i := 0; i < 4; i++ { // weaker: 2/4 on one signing domain
		pool = append(pool, p(fmt.Sprintf("sg-%d", i), i < 2, "google", "shared.test", fmt.Sprintf("q%d.test", i)))
	}

	first := DetectIncidents(pool)
	if len(first) < 2 {
		t.Fatalf("expected at least two incidents, got %+v", first)
	}
	for i := 1; i < len(first); i++ {
		if first[i-1].Lift < first[i].Lift {
			t.Errorf("incident %d has higher lift than %d — not sorted strongest-first", i, i-1)
		}
	}
	for i := 0; i < 5; i++ {
		again := DetectIncidents(pool)
		if len(again) != len(first) {
			t.Fatalf("run %d produced %d incidents, first run produced %d", i, len(again), len(first))
		}
		for j := range again {
			// Compared field-by-field rather than with ==: Incident carries a slice,
			// so the struct is not comparable, and reflect.DeepEqual here would also
			// pass on two nil-vs-empty Members that a JSON reader would see
			// differently.
			a, b := again[j], first[j]
			if a.Dimension != b.Dimension || a.Value != b.Value || a.Lift != b.Lift ||
				a.CohortSize != b.CohortSize || a.DegradedInside != b.DegradedInside ||
				a.CohortOutside != b.CohortOutside || a.DegradedOutside != b.DegradedOutside ||
				len(a.Members) != len(b.Members) {
				t.Errorf("run %d differs at %d: %+v vs %+v", i, j, a, b)
				continue
			}
			for k := range a.Members {
				if a.Members[k] != b.Members[k] {
					t.Errorf("run %d, incident %d: member %d = %q, first run had %q", i, j, k, a.Members[k], b.Members[k])
				}
			}
		}
	}
}

func TestDetectIncidentsHandlesAnEmptyPool(t *testing.T) {
	if got := DetectIncidents(nil); len(got) != 0 {
		t.Errorf("DetectIncidents(nil) = %+v, want none", got)
	}
	if got := DetectIncidents([]IncidentInput{}); got == nil {
		t.Error("DetectIncidents returned a nil slice; callers marshal this straight to JSON, " +
			"where nil becomes null and an empty slice becomes [] — the contract says []")
	}
}

// MinIncidentPool is published to clients so a UI can say "this pool is too small
// to look" instead of "we looked and found nothing". That makes it a CLAIM about
// the detector, and nothing else here checks the claim: the detector enforces
// MinIncidentCohort and the empty-outside rule separately and never reads
// MinIncidentPool at all, so the constant could drift out of agreement with the
// behaviour it advertises and every other test would stay green.
//
// So this brute-forces the boundary. Below the floor, NO arrangement of
// participants — any assignment of values, any subset degraded — may produce an
// incident. At the floor, at least one must.
func TestMinIncidentPoolIsTheSmallestPoolThatCanReportAnything(t *testing.T) {
	// Every assignment of `n` participants to `values` groups, crossed with every
	// subset of them degraded.
	arrangements := func(n int, values []string) [][]IncidentInput {
		var out [][]IncidentInput
		total := 1
		for i := 0; i < n; i++ {
			total *= len(values)
		}
		for combo := 0; combo < total; combo++ {
			for mask := 0; mask < (1 << n); mask++ {
				pool := make([]IncidentInput, 0, n)
				c := combo
				for i := 0; i < n; i++ {
					v := values[c%len(values)]
					c /= len(values)
					pool = append(pool, IncidentInput{
						MailboxID: fmt.Sprintf("m%d", i),
						// One organizational domain per group, so the sender-domain
						// dimension is exercised alongside the observed three rather
						// than being accidentally unique per participant.
						Email:    fmt.Sprintf("m%d@%s", i, v),
						Degraded: mask&(1<<i) != 0,
						Route:    v, SigningDomain: v, ReturnPathDomain: v,
					})
				}
				out = append(out, pool)
			}
		}
		return out
	}

	values := []string{"a.test", "b.test", "c.test"}

	below := MinIncidentPool - 1
	for _, pool := range arrangements(below, values) {
		if got := DetectIncidents(pool); len(got) != 0 {
			t.Fatalf("a pool of %d produced %+v; MinIncidentPool claims %d is the smallest "+
				"pool that can report anything, so the published floor is too high",
				below, got, MinIncidentPool)
		}
	}

	var reported bool
	for _, pool := range arrangements(MinIncidentPool, values) {
		if len(DetectIncidents(pool)) != 0 {
			reported = true
			break
		}
	}
	if !reported {
		t.Errorf("no arrangement of %d participants produced an incident; MinIncidentPool "+
			"claims this size can, so the published floor is too low", MinIncidentPool)
	}
}

// The headline shape this feature exists for, and until now only a hand-authored
// frontend fixture rendered it: ONE cohort that correlates on several dimensions at
// once, because a single fault often has several names. A relay change shows up as a
// route AND as the signing domain that relay signs for.
//
// DetectIncidents' own doc comment promises both are reported and that "which
// dimension carries the correlation is the actionable part". Nothing verified it —
// the sort test uses two cohorts with DIFFERENT members, so it could not have caught
// a fold that silently kept only the first dimension to match.
func TestDetectIncidentsReportsOneCohortOnEveryDimensionItShares(t *testing.T) {
	pool := clean(20)
	// Three mailboxes sharing everything: route, signing domain, return path, and —
	// via their addresses — one organizational domain.
	for i := 0; i < 3; i++ {
		pool = append(pool, IncidentInput{
			MailboxID: fmt.Sprintf("shared-%d", i),
			Email:     fmt.Sprintf("shared-%d@acme.test", i),
			Degraded:  true,
			Route:     "microsoft", SigningDomain: "mail.acme.test", ReturnPathDomain: "bounce.acme.test",
		})
	}

	got := DetectIncidents(pool)

	byDimension := map[string]Incident{}
	for _, in := range got {
		byDimension[in.Dimension] = in
	}
	for _, want := range []string{DimensionRoute, DimensionSigning, DimensionReturnPath, DimensionSenderDomain} {
		if _, ok := byDimension[want]; !ok {
			t.Errorf("no %s incident; one fault with several names must be reported under each, "+
				"because which dimension carries it is what an operator acts on. got %+v", want, got)
		}
	}

	// Same three mailboxes every time. A dimension that reported a DIFFERENT
	// membership would mean the cohorts were built from different participants,
	// which is the bug this assertion exists to catch.
	for dimension, in := range byDimension {
		if len(in.Members) != 3 {
			t.Errorf("%s named %d members, want the same 3: %v", dimension, len(in.Members), in.Members)
			continue
		}
		for i, id := range in.Members {
			if want := fmt.Sprintf("shared-%d", i); id != want {
				t.Errorf("%s member %d = %q, want %q", dimension, i, id, want)
			}
		}
	}
}
