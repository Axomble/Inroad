package inprocess

import (
	"strings"
	"testing"

	"github.com/inroad/inroad/internal/platform/fleetscore"
)

// placementReason is the one piece of the placement path that decides what the
// decision log SAYS, and the rule it enforces is the one the whole log rests on:
// an entry must never print a score comparison that was not actually made. These
// are unit tests because that rule is a property of the prose, not of Postgres.

func ranked(ids ...string) []fleetscore.Ranked {
	out := make([]fleetscore.Ranked, 0, len(ids))
	for i, id := range ids {
		out = append(out, fleetscore.Ranked{
			Candidate: fleetscore.Candidate{WorkerID: id},
			Score:     float64(len(ids) - i),
		})
	}
	return out
}

// A real comparison between two eligible workers is the ONLY case that may
// render one.
func TestPlacementReasonRendersAComparisonOnlyWhenOneHappened(t *testing.T) {
	got := placementReason(ranked("win", "second"), 3, "win").String()
	if !strings.Contains(got, "win") || !strings.Contains(got, "second") {
		t.Fatalf("reason = %q, want both candidates named", got)
	}
	if !strings.Contains(got, " over ") {
		t.Fatalf("reason = %q, want a comparison", got)
	}
	if !strings.Contains(got, "3 candidates") {
		t.Fatalf("reason = %q, want the number of candidates considered", got)
	}
}

// One eligible candidate was scored but compared against nothing. Naming a
// runner-up here would invent a worker that was never in the running, and an
// operator would tune against a number nothing computed.
func TestPlacementReasonWithASingleCandidateNamesNoRunnerUp(t *testing.T) {
	got := placementReason(ranked("only"), 4, "only").String()
	if strings.Contains(got, " over ") {
		t.Fatalf("reason = %q, want no comparison for a single eligible candidate", got)
	}
	if !strings.Contains(got, "only eligible candidate") || !strings.Contains(got, "4 candidates") {
		t.Fatalf("reason = %q, want it to say it was the only eligible candidate of four considered", got)
	}
}

// The scorer's winner is a REQUEST, not an outcome: a concurrent caller may have
// placed the mailbox first, and the upsert then keeps that live incumbent.
// Printing the comparison the scorer made would describe a decision that did not
// take effect.
func TestPlacementReasonReportsAConcurrentAdoptionInsteadOfTheScore(t *testing.T) {
	got := placementReason(ranked("wanted", "second"), 2, "someone-else").String()
	if !strings.HasPrefix(got, "forced:") {
		t.Fatalf("reason = %q, want a forced reason — the score did not decide this", got)
	}
	if strings.Contains(got, " over ") {
		t.Fatalf("reason = %q, want no comparison: the comparison did not decide the outcome", got)
	}
	if !strings.Contains(got, "someone-else") {
		t.Fatalf("reason = %q, want it to name the worker the mailbox actually landed on", got)
	}
}

// Every Reason this function returns has to be writable, or the entry is
// rejected three layers down at the table's CHECK.
func TestPlacementReasonIsAlwaysValid(t *testing.T) {
	for _, tc := range []struct {
		name     string
		ranked   []fleetscore.Ranked
		assigned string
	}{
		{"contested", ranked("a", "b"), "a"},
		{"uncontested", ranked("a"), "a"},
		{"adopted", ranked("a", "b"), "c"},
		{"nothing ranked", nil, ""},
	} {
		if r := placementReason(tc.ranked, 1, tc.assigned); !r.Valid() {
			t.Errorf("%s: reason is not writable", tc.name)
		}
	}
}

// adoptedReason's zero value is what lets callers say "nothing unusual
// happened"; returning a valid Reason for a placement that went where it was
// aimed would make every self-host entry claim a race that never occurred.
func TestAdoptedReasonIsZeroWhenThePlacementWentWhereItWasAimed(t *testing.T) {
	if r := adoptedReason("w1", "w1"); r.Valid() {
		t.Fatalf("adoptedReason for an unchanged placement = %q, want the zero Reason", r)
	}
	if r := adoptedReason("w1", "w2"); !r.Valid() {
		t.Fatal("adoptedReason for a changed placement must produce a reason")
	}
}
