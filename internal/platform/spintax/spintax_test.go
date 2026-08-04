package spintax_test

import (
	"strings"
	"testing"
	"time"

	"github.com/inroad/inroad/internal/platform/spintax"
)

// TestExpandPicksEachOptionDeterministically proves the two halves of the
// contract: the SAME seed always yields the SAME output (so a retried send
// regenerates the identical variant), and DIFFERENT seeds are not all funneled
// onto one option (spinning actually varies content across sends).
func TestExpandPicksEachOptionDeterministically(t *testing.T) {
	const s = "{a|b|c}"

	for seed := uint64(0); seed < 100; seed++ {
		first := spintax.Expand(s, seed)
		second := spintax.Expand(s, seed)
		if first != second {
			t.Fatalf("seed %d: Expand not deterministic: %q vs %q", seed, first, second)
		}
	}

	seen := map[string]bool{}
	for seed := uint64(0); seed < 100; seed++ {
		seen[spintax.Expand(s, seed)] = true
	}
	for _, want := range []string{"a", "b", "c"} {
		if !seen[want] {
			t.Errorf("option %q never chosen across 100 seeds: %v", want, seen)
		}
	}
}

// TestExpandNested proves the innermost group resolves first: "{Hey|Yo}"
// inside "{Hi|{Hey|Yo}}" must collapse to one concrete word before the outer
// group is itself resolved, so the final result is always one of the three
// leaf options — never a literal brace or pipe left over.
func TestExpandNested(t *testing.T) {
	const s = "{Hi|{Hey|Yo}} there"
	want := map[string]bool{"Hi there": true, "Hey there": true, "Yo there": true}

	for seed := uint64(0); seed < 50; seed++ {
		got := spintax.Expand(s, seed)
		if !want[got] {
			t.Fatalf("seed %d: Expand(%q) = %q, want one of %v", seed, s, got, want)
		}
	}
}

// TestExpandLeavesMergeFieldsAlone proves a doubled merge field like
// {{first_name}} is inert to Expand: its inner brace span ("{first_name}")
// has no '|', so it is not spin syntax and must survive byte-for-byte,
// including both braces, even though a sibling spin group in the same string
// IS resolved.
func TestExpandLeavesMergeFieldsAlone(t *testing.T) {
	const s = "Hi {{first_name}}, {a|b}"

	for seed := uint64(0); seed < 20; seed++ {
		got := spintax.Expand(s, seed)
		if !strings.Contains(got, "{{first_name}}") {
			t.Fatalf("seed %d: merge field mangled: %q", seed, got)
		}
		if got != "Hi {{first_name}}, a" && got != "Hi {{first_name}}, b" {
			t.Fatalf("seed %d: unexpected result: %q", seed, got)
		}
	}
}

// TestExpandNoPipesUntouched proves a brace group with no '|' is left
// completely unchanged — it is literal text (or a single-brace placeholder),
// not a spin group, so Expand must not touch it at all.
func TestExpandNoPipesUntouched(t *testing.T) {
	const s = "{literal}"
	for seed := uint64(0); seed < 5; seed++ {
		if got := spintax.Expand(s, seed); got != s {
			t.Fatalf("seed %d: Expand(%q) = %q, want unchanged", seed, s, got)
		}
	}
}

// TestExpandPathologicalInputTerminates proves the 10,000-iteration cap
// actually bounds pathological input: 20,000 levels of nested spin groups
// (more than the cap can fully resolve) must still return promptly rather
// than hang, looping forever trying to fully collapse every level.
func TestExpandPathologicalInputTerminates(t *testing.T) {
	s := "z"
	for i := 0; i < 20000; i++ {
		s = "{" + s + "|x}"
	}

	done := make(chan string, 1)
	go func() { done <- spintax.Expand(s, spintax.Seed("pathological")) }()

	select {
	case <-done:
		// Terminated within the iteration cap — the property under test.
	case <-time.After(5 * time.Second):
		t.Fatal("Expand did not terminate on pathological input — the iteration cap did not bound the work")
	}
}
