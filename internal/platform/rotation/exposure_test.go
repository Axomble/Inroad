package rotation

import (
	"fmt"
	"math"
	"testing"
)

// cand builds a candidate carrying only what the budget reads.
func cand(id string, assigned int64) Candidate {
	return Candidate{MailboxID: id, AssignedCount: assigned}
}

// byPrefix groups candidates by the first character of their id, which keeps the
// fixtures readable: "a1", "a2" share a fault domain, "b1" is another.
func byPrefix(c Candidate) string { return c.MailboxID[:1] }

func ids(candidates []Candidate) []string {
	out := make([]string, 0, len(candidates))
	for _, c := range candidates {
		out = append(out, c.MailboxID)
	}
	return out
}

func kept(t *testing.T, candidates []Candidate, domainOf FaultDomainOf) map[string]bool {
	t.Helper()
	got := map[string]bool{}
	for _, c := range WithinExposureBudget(candidates, domainOf) {
		got[c.MailboxID] = true
	}
	return got
}

// The finding the slice exists for: one domain carrying most of the campaign is
// narrowed out while an alternative exists, so the next contact goes elsewhere.
func TestWithinExposureBudgetNarrowsTheDominantFaultDomain(t *testing.T) {
	in := []Candidate{cand("a1", 70), cand("b1", 30)}

	got := kept(t, in, byPrefix)

	if got["a1"] {
		t.Errorf("the dominant domain at 70%% was kept: %v", ids(WithinExposureBudget(in, byPrefix)))
	}
	if !got["b1"] {
		t.Error("the under-exposed domain was dropped")
	}
}

// THE safety property, and the one most likely to ship a regression: a
// single-domain workspace is the ordinary case for this product, every candidate is
// over budget, and the budget must hand the whole set back rather than stop sending.
// A concentration limit that could withhold mail would be a worse bug than the
// concentration it prevents.
func TestWithinExposureBudgetNeverEmptiesTheSet(t *testing.T) {
	single := []Candidate{cand("a1", 60), cand("a2", 40)}

	got := WithinExposureBudget(single, byPrefix)

	if len(got) != len(single) {
		t.Fatalf("kept %v of a single-domain pool; every candidate is over budget and the "+
			"budget must be unsatisfiable, not fatal", ids(got))
	}
}

func TestWithinExposureBudgetLeavesTooLittleToActOnAlone(t *testing.T) {
	tests := []struct {
		name string
		in   []Candidate
		dom  FaultDomainOf
	}{
		// Nothing sent yet: no distribution can be lopsided.
		{"a fresh campaign", []Candidate{cand("a1", 0), cand("b1", 0)}, byPrefix},
		// One candidate cannot be narrowed to fewer than one.
		{"a single candidate", []Candidate{cand("a1", 100)}, byPrefix},
		// No classifier means no fault domains to compare.
		{"no domain function", []Candidate{cand("a1", 90), cand("b1", 10)}, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := WithinExposureBudget(tc.in, tc.dom); len(got) != len(tc.in) {
				t.Errorf("narrowed %v to %v", ids(tc.in), ids(got))
			}
		})
	}
}

// An unknown domain is not known to share a failure with anything, so it is never
// grouped and never narrowed. Bucketing the unclassified together would invent a
// shared fault domain out of our own ignorance and then act on it — the mistake the
// incident fold and the observer detector each had to be corrected for.
func TestWithinExposureBudgetNeverGroupsUnknownDomains(t *testing.T) {
	unknown := func(c Candidate) string {
		if c.MailboxID[:1] == "u" {
			return ""
		}
		return byPrefix(c)
	}
	// Two unclassified mailboxes carrying most of the volume, plus a real domain, so
	// the budget stays satisfiable and the fallback cannot mask the result.
	in := []Candidate{cand("u1", 45), cand("u2", 45), cand("b1", 10)}

	got := kept(t, in, unknown)

	if !got["u1"] || !got["u2"] {
		t.Errorf("unclassified mailboxes were grouped and narrowed: %v",
			ids(WithinExposureBudget(in, unknown)))
	}
}

// The reactive half: a degrading domain is narrowed at a lower share than a healthy
// one, which is the step between "fine" and the breaker cutting it off entirely.
func TestWithinExposureBudgetForAppliesAPerDomainCeiling(t *testing.T) {
	// 45% is comfortably inside the flat 0.6 cap and outside a 0.35 watch ceiling.
	in := []Candidate{cand("a1", 45), cand("b1", 55)}

	if got := kept(t, in, byPrefix); !got["a1"] {
		t.Fatal("45% was narrowed by the FLAT cap; this fixture must be within it, or the " +
			"ceiling test below proves nothing")
	}

	watch := func(domain string) float64 {
		if domain == "a" {
			return 0.35
		}
		return 0
	}
	got := WithinExposureBudgetFor(in, byPrefix, watch)
	for _, c := range got {
		if c.MailboxID == "a1" {
			t.Errorf("the degrading domain kept its 45%% against a 35%% ceiling: %v", ids(got))
		}
	}
}

// A ceiling of zero means "no opinion", never "may not send". Containment is
// LaneMaySend's decision, and a second implementation of it here is the shape every
// repeated defect in this subsystem has taken.
func TestWithinExposureBudgetForTreatsAnOutOfRangeCeilingAsNoOpinion(t *testing.T) {
	in := []Candidate{cand("a1", 45), cand("b1", 55)}

	// Asserted on the PUBLISHED ceiling rather than on the narrowing. When every
	// domain gets the bad value every candidate is dropped, the never-empty fallback
	// hands the whole set back, and the test passes whether or not the rule exists —
	// which is exactly how this assertion proved nothing on its first draft.
	for _, ceiling := range []float64{0, -1, 1.5, math.NaN()} {
		t.Run(fmt.Sprintf("%v", ceiling), func(t *testing.T) {
			shares := FaultDomainSharesFor(in, byPrefix, func(string) float64 { return ceiling })
			if len(shares) == 0 {
				t.Fatal("no shares reported")
			}
			for _, sh := range shares {
				if sh.Ceiling != MaxFaultDomainShare {
					t.Errorf("domain %s judged against %v for an out-of-range ceiling %v; "+
						"it must fall back to the flat cap", sh.Domain, sh.Ceiling, ceiling)
				}
			}
		})
	}
}

// The published usage must be judged against the SAME ceiling the selector used.
// Reporting a degrading domain against the flat cap showed a domain the selector was
// actively routing away from as comfortably within budget.
func TestFaultDomainSharesForReportsTheCeilingItJudgedAgainst(t *testing.T) {
	in := []Candidate{cand("a1", 45), cand("b1", 55)}
	watch := func(domain string) float64 {
		if domain == "a" {
			return 0.35
		}
		return 0
	}

	shares := FaultDomainSharesFor(in, byPrefix, watch)
	byDomain := map[string]FaultDomainShare{}
	for _, s := range shares {
		byDomain[s.Domain] = s
	}

	a, b := byDomain["a"], byDomain["b"]
	if a.Ceiling != 0.35 || !a.OverBudget {
		t.Errorf("domain a = %+v, want ceiling 0.35 and over budget", a)
	}
	if b.Ceiling != MaxFaultDomainShare || b.OverBudget {
		t.Errorf("domain b = %+v, want the flat ceiling and within budget", b)
	}
	// The two rows must be readable side by side: a at 45%% is over, b at 55%% is not,
	// and only the published ceiling explains that.
	if !(a.Share < b.Share) {
		t.Fatalf("fixture no longer has the smaller share over budget: a=%v b=%v", a.Share, b.Share)
	}
}

func TestFaultDomainSharesAreSortedWorstFirstAndNeverNil(t *testing.T) {
	in := []Candidate{cand("a1", 10), cand("b1", 60), cand("c1", 30)}

	got := FaultDomainShares(in, byPrefix)
	if len(got) != 3 {
		t.Fatalf("got %+v, want one row per domain", got)
	}
	for i := 1; i < len(got); i++ {
		if got[i-1].Share < got[i].Share {
			t.Errorf("row %d has a larger share than %d — not sorted worst-first", i, i-1)
		}
	}
	if FaultDomainShares(nil, byPrefix) == nil {
		t.Error("nil candidates returned a nil slice; this marshals to null, and the contract says []")
	}
	if FaultDomainShares(in, nil) == nil {
		t.Error("nil classifier returned a nil slice")
	}
}
