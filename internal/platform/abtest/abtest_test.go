package abtest_test

import (
	"fmt"
	"math"
	"testing"

	"github.com/inroad/inroad/internal/platform/abtest"
)

var evenSplit = []abtest.Variant{{ID: "", Weight: 1}, {ID: "v-b", Weight: 1}}

// The property the whole design rests on: a retried send re-selects the same
// variant. Without it, one logical message could go out as A on the first
// attempt and B on the retry.
func TestSelectIsStableForTheSamePair(t *testing.T) {
	first, ok := abtest.Select("enr-1", "step-1", evenSplit)
	if !ok {
		t.Fatal("Select returned nothing for a weighted list")
	}
	for i := 0; i < 100; i++ {
		again, ok := abtest.Select("enr-1", "step-1", evenSplit)
		if !ok || again.ID != first.ID {
			t.Fatalf("attempt %d = %+v (ok=%v), want a stable %+v", i, again, ok, first)
		}
	}
}

// A contact must not be pinned to "always the first variant" across the whole
// sequence, or a two-step campaign compares two disjoint populations.
func TestSelectVariesAcrossStepsForOneEnrollment(t *testing.T) {
	seen := map[string]bool{}
	for step := 0; step < 40; step++ {
		v, ok := abtest.Select("enr-1", fmt.Sprintf("step-%d", step), evenSplit)
		if !ok {
			t.Fatal("Select returned nothing")
		}
		seen[v.ID] = true
	}
	if len(seen) < 2 {
		t.Fatalf("one enrollment drew only %v across 40 steps, want both variants", seen)
	}
}

func TestSelectRespectsWeightsAcrossAPopulation(t *testing.T) {
	candidates := []abtest.Variant{{ID: "", Weight: 3}, {ID: "v-b", Weight: 1}}
	const n = 20000
	base := 0
	for i := 0; i < n; i++ {
		v, ok := abtest.Select(fmt.Sprintf("enr-%d", i), "step-1", candidates)
		if !ok {
			t.Fatal("Select returned nothing")
		}
		if v.ID == "" {
			base++
		}
	}
	share := float64(base) / n
	if math.Abs(share-0.75) > 0.02 {
		t.Errorf("base share = %.3f, want ~0.75 for a 3:1 split", share)
	}
}

// Weight 0 is how a losing variant is retired without deleting the sends
// attributed to it, so it must never be selected.
func TestSelectNeverPicksAZeroWeightVariant(t *testing.T) {
	candidates := []abtest.Variant{{ID: "", Weight: 0}, {ID: "v-b", Weight: 1}}
	for i := 0; i < 500; i++ {
		v, ok := abtest.Select(fmt.Sprintf("enr-%d", i), "step-1", candidates)
		if !ok || v.ID != "v-b" {
			t.Fatalf("got %+v (ok=%v), want v-b every time", v, ok)
		}
	}
}

// All weights zero means nothing may send — the caller must treat this as an
// error rather than falling back to some default copy.
func TestSelectReportsNothingEligible(t *testing.T) {
	for _, candidates := range [][]abtest.Variant{
		nil,
		{},
		{{ID: "", Weight: 0}},
		{{ID: "", Weight: 0}, {ID: "v-b", Weight: 0}},
	} {
		if _, ok := abtest.Select("enr-1", "step-1", candidates); ok {
			t.Errorf("Select(%v) reported a selection, want none", candidates)
		}
	}
}

// A single eligible candidate is the overwhelmingly common case (every step
// with no variants at all), and must always resolve to the base content.
func TestSelectWithOnlyBaseAlwaysPicksBase(t *testing.T) {
	v, ok := abtest.Select("enr-1", "step-1", []abtest.Variant{{ID: "", Weight: 1}})
	if !ok || v.ID != "" {
		t.Fatalf("got %+v (ok=%v), want the base variant", v, ok)
	}
}
