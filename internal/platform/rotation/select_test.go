package rotation

import (
	"errors"
	"testing"
	"time"
)

// day is a fixed reference instant; LRU assertions are relative to it so the
// tests never read the clock (the package doesn't either).
var day = time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)

func TestSelectRoundRobinPicksTheLeastAssigned(t *testing.T) {
	got, err := Select(ModeRoundRobin, []Candidate{
		{MailboxID: "a", Weight: 100, RemainingToday: 100, AssignedCount: 9},
		{MailboxID: "b", Weight: 1, RemainingToday: 1, AssignedCount: 2},
		{MailboxID: "c", Weight: 50, RemainingToday: 50, AssignedCount: 5},
	})
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	// b wins on count alone: round_robin deliberately ignores weight and capacity.
	if got.MailboxID != "b" {
		t.Errorf("mailbox = %q, want b (lowest assigned_count)", got.MailboxID)
	}
}

func TestSelectLRUPicksTheOldestAndPrefersNeverUsed(t *testing.T) {
	oldest := Candidate{MailboxID: "old", LastAssignedAt: day.Add(-48 * time.Hour)}
	recent := Candidate{MailboxID: "recent", LastAssignedAt: day}
	never := Candidate{MailboxID: "never"} // zero LastAssignedAt

	got, err := Select(ModeLRU, []Candidate{recent, oldest})
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if got.MailboxID != "old" {
		t.Errorf("mailbox = %q, want old", got.MailboxID)
	}

	got, err = Select(ModeLRU, []Candidate{recent, oldest, never})
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if got.MailboxID != "never" {
		t.Errorf("mailbox = %q, want never (a never-used mailbox goes first)", got.MailboxID)
	}
}

// Capacity is what makes 'weighted' the default: an equally-weighted mailbox with
// more room left today takes the contact.
func TestSelectWeightedPrefersRemainingCapacity(t *testing.T) {
	got, err := Select(ModeWeighted, []Candidate{
		{MailboxID: "nearly-full", Weight: 10, RemainingToday: 2},
		{MailboxID: "roomy", Weight: 10, RemainingToday: 40},
	})
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if got.MailboxID != "roomy" {
		t.Errorf("mailbox = %q, want roomy", got.MailboxID)
	}
}

// A higher operator weight must be able to outrank more remaining capacity —
// otherwise the weight setting does nothing.
func TestSelectWeightedRespectsOperatorWeight(t *testing.T) {
	got, err := Select(ModeWeighted, []Candidate{
		{MailboxID: "heavy", Weight: 100, RemainingToday: 10},
		{MailboxID: "light", Weight: 1, RemainingToday: 40},
	})
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if got.MailboxID != "heavy" {
		t.Errorf("mailbox = %q, want heavy", got.MailboxID)
	}
}

func TestSelectWeightedDeprioritizesUnhealthyMailboxes(t *testing.T) {
	for _, tc := range []struct{ state, want string }{
		{"healthy", "candidate"},
		{healthWatch, "healthy-peer"},
		{healthThrottled, "healthy-peer"},
		{healthPaused, "healthy-peer"},
	} {
		t.Run(tc.state, func(t *testing.T) {
			// Same weight and capacity on both sides, so only health can decide.
			got, err := Select(ModeWeighted, []Candidate{
				{MailboxID: "candidate", Weight: 10, RemainingToday: 20, HealthState: tc.state},
				{MailboxID: "healthy-peer", Weight: 10, RemainingToday: 20, HealthState: "healthy"},
			})
			if err != nil {
				t.Fatalf("Select: %v", err)
			}
			if got.MailboxID != tc.want {
				t.Errorf("mailbox = %q, want %q for health %q", got.MailboxID, tc.want, tc.state)
			}
		})
	}
}

// Unhealthy is deprioritized, never excluded: a pool where every mailbox is
// paused must still send.
func TestSelectWeightedStillPicksWhenEveryMailboxIsPaused(t *testing.T) {
	got, err := Select(ModeWeighted, []Candidate{
		{MailboxID: "a", Weight: 1, RemainingToday: 5, HealthState: healthPaused},
		{MailboxID: "b", Weight: 1, RemainingToday: 50, HealthState: healthPaused},
	})
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if got.MailboxID != "b" {
		t.Errorf("mailbox = %q, want b (still ranked on capacity)", got.MailboxID)
	}
}

// An older mailbox absorbs more volume than a fresh one at equal weight and
// capacity — the log2 age term.
func TestSelectWeightedFavoursTheOlderMailbox(t *testing.T) {
	got, err := Select(ModeWeighted, []Candidate{
		{MailboxID: "fresh", Weight: 10, RemainingToday: 20, WarmupAgeDays: 0},
		{MailboxID: "seasoned", Weight: 10, RemainingToday: 20, WarmupAgeDays: 30},
	})
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if got.MailboxID != "seasoned" {
		t.Errorf("mailbox = %q, want seasoned", got.MailboxID)
	}
	// The age term must not dominate: a much roomier fresh mailbox still wins.
	got, err = Select(ModeWeighted, []Candidate{
		{MailboxID: "fresh", Weight: 10, RemainingToday: 400, WarmupAgeDays: 0},
		{MailboxID: "seasoned", Weight: 10, RemainingToday: 20, WarmupAgeDays: 30},
	})
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if got.MailboxID != "fresh" {
		t.Errorf("mailbox = %q, want fresh (capacity outweighs the age term)", got.MailboxID)
	}
}

// The single-factor tests above each vary one term with the others held equal, so
// they would all still pass if the RELATIVE magnitude of the terms drifted — a
// health factor that swamped remaining capacity, or an age term that swamped an
// explicit operator weight, would go unnoticed. These cases put two factors in
// direct opposition and pin the trade-off, including from both sides of each
// break-even so a change in either direction fails.
//
// Scores for reference (weight × remaining × health × (1 + log2(age+1))):
// health is 1.0 / 0.7 / 0.4 / 0.1 and the age term spans 1.0 at day 0 to ~9.5 at
// a year, so age is a heavier term than its "tie-breaker" appearance suggests.
func TestSelectWeightedResolvesConflictingFactors(t *testing.T) {
	for _, tc := range []struct {
		name string
		why  string
		a, b Candidate
		want string
	}{
		{
			name: "operator weight outranks a capacity edge",
			// 500 vs 40. A 100:1 weight is the operator saying "send from this one";
			// an 8x capacity edge must not overrule an instruction that explicit.
			// Concentration stays bounded anyway — A drops out of the eligible set
			// after its remaining 5 sends.
			a:    Candidate{MailboxID: "heavy", Weight: 100, RemainingToday: 5},
			b:    Candidate{MailboxID: "roomy", Weight: 1, RemainingToday: 40},
			want: "heavy",
		},
		{
			name: "a large enough capacity edge outranks a modest weight",
			// 50 vs 60. The other side of the same break-even: weight is a
			// preference, not a lexicographic priority, so 12x the capacity beats a
			// 10x weight. Without this the first case could pass on a formula that
			// ignored capacity entirely.
			a:    Candidate{MailboxID: "preferred", Weight: 10, RemainingToday: 5},
			b:    Candidate{MailboxID: "spacious", Weight: 1, RemainingToday: 60},
			want: "spacious",
		},
		{
			name: "an aged watch-listed mailbox beats a brand-new healthy one",
			// 20 vs 125. 'watch' is a mild degradation; a mailbox connected today has
			// no sending reputation at all. The one with a month of history and more
			// room is genuinely better able to carry cold volume.
			a:    Candidate{MailboxID: "fresh-healthy", Weight: 1, RemainingToday: 20, HealthState: "healthy"},
			b:    Candidate{MailboxID: "aged-watch", Weight: 1, RemainingToday: 30, HealthState: healthWatch, WarmupAgeDays: 30},
			want: "aged-watch",
		},
		{
			name: "health outranks a 5x capacity edge",
			// 5 vs 10. The regression guard that matters most: a paused mailbox must
			// not win on volume alone. health 0.1 puts the break-even at 10x capacity,
			// so 5x is not enough.
			a:    Candidate{MailboxID: "paused-roomy", Weight: 1, RemainingToday: 50, HealthState: healthPaused},
			b:    Candidate{MailboxID: "healthy-tight", Weight: 1, RemainingToday: 10, HealthState: "healthy"},
			want: "healthy-tight",
		},
		{
			name: "a paused mailbox past the 10x break-even still wins",
			// 20 vs 3. Deliberate: health WEIGHTS selection, it does not gate it
			// (gating is out of scope), and a pool must still send when its healthy
			// member has 3 sends left. See the report note — whether 10x is the right
			// threshold is a product question, not a formula bug.
			a:    Candidate{MailboxID: "paused-roomy", Weight: 1, RemainingToday: 200, HealthState: healthPaused},
			b:    Candidate{MailboxID: "healthy-nearly-full", Weight: 1, RemainingToday: 3, HealthState: "healthy"},
			want: "paused-roomy",
		},
		{
			name: "an explicit weight outranks a year of age",
			// 500 vs 95. The specific inversion to guard: the age term maxes out
			// around 9.5x, so it must never overrule a 50:1 operator weight.
			a:    Candidate{MailboxID: "weighted", Weight: 50, RemainingToday: 10},
			b:    Candidate{MailboxID: "veteran", Weight: 1, RemainingToday: 10, WarmupAgeDays: 365},
			want: "weighted",
		},
		{
			name: "age outranks a token weight bump",
			// 20 vs 95. The other side: nudging a weight from 1 to 2 does not
			// out-rank a year of sending history, so age stays meaningful.
			a:    Candidate{MailboxID: "nudged", Weight: 2, RemainingToday: 10},
			b:    Candidate{MailboxID: "veteran", Weight: 1, RemainingToday: 10, WarmupAgeDays: 365},
			want: "veteran",
		},
		{
			name: "healthy and new beats paused and aged at equal capacity",
			// 20 vs 11.9. Age must not rehabilitate a paused mailbox: at equal
			// capacity the healthy one wins even with no history at all.
			a:    Candidate{MailboxID: "fresh-healthy", Weight: 1, RemainingToday: 20, HealthState: "healthy"},
			b:    Candidate{MailboxID: "aged-paused", Weight: 1, RemainingToday: 20, HealthState: healthPaused, WarmupAgeDays: 30},
			want: "fresh-healthy",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Select(ModeWeighted, []Candidate{tc.a, tc.b})
			if err != nil {
				t.Fatalf("Select: %v", err)
			}
			if got.MailboxID != tc.want {
				t.Errorf("winner = %q, want %q (scores: %s=%.2f %s=%.2f)",
					got.MailboxID, tc.want, tc.a.MailboxID, score(tc.a), tc.b.MailboxID, score(tc.b))
			}
		})
	}
}

// Two identical candidates must resolve the same way every time and regardless of
// slice order, or two concurrent assigners would disagree before the write-once
// claim ever arbitrates.
func TestSelectTieBreaksDeterministicallyOnMailboxID(t *testing.T) {
	for _, mode := range []string{ModeRoundRobin, ModeLRU, ModeWeighted} {
		t.Run(mode, func(t *testing.T) {
			forward := []Candidate{
				{MailboxID: "aaa", Weight: 5, RemainingToday: 10, AssignedCount: 3, LastAssignedAt: day},
				{MailboxID: "bbb", Weight: 5, RemainingToday: 10, AssignedCount: 3, LastAssignedAt: day},
			}
			reversed := []Candidate{forward[1], forward[0]}
			for _, candidates := range [][]Candidate{forward, reversed} {
				got, err := Select(mode, candidates)
				if err != nil {
					t.Fatalf("Select: %v", err)
				}
				if got.MailboxID != "aaa" {
					t.Errorf("mailbox = %q, want aaa (lowest id breaks the tie)", got.MailboxID)
				}
			}
		})
	}
}

func TestSelectEmptyPoolReturnsErrNoEligibleSender(t *testing.T) {
	for _, mode := range []string{ModeRoundRobin, ModeLRU, ModeWeighted, "nonsense"} {
		if _, err := Select(mode, nil); !errors.Is(err, ErrNoEligibleSender) {
			t.Errorf("mode %q: err = %v, want ErrNoEligibleSender", mode, err)
		}
	}
}

func TestSelectSingleCandidateWinsInEveryMode(t *testing.T) {
	only := Candidate{MailboxID: "only", Weight: 1, RemainingToday: 1}
	for _, mode := range []string{ModeRoundRobin, ModeLRU, ModeWeighted} {
		got, err := Select(mode, []Candidate{only})
		if err != nil {
			t.Fatalf("mode %q: %v", mode, err)
		}
		if got.MailboxID != "only" {
			t.Errorf("mode %q: mailbox = %q, want only", mode, got.MailboxID)
		}
	}
}

// An unrecognized stored mode must behave as 'weighted' (the column default)
// rather than fail the send.
func TestSelectUnknownModeFallsBackToWeighted(t *testing.T) {
	candidates := []Candidate{
		{MailboxID: "small", Weight: 1, RemainingToday: 1},
		{MailboxID: "large", Weight: 1, RemainingToday: 99},
	}
	got, err := Select("something_new", candidates)
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if got.MailboxID != "large" {
		t.Errorf("mailbox = %q, want large (weighted behaviour)", got.MailboxID)
	}
}

// Degenerate inputs must not produce a NaN score or a panic: a zero-capacity or
// negative-weight row can only reach here from corrupted data, and it must lose
// rather than poison the comparison.
func TestSelectWeightedHandlesDegenerateRows(t *testing.T) {
	got, err := Select(ModeWeighted, []Candidate{
		{MailboxID: "broken", Weight: -5, RemainingToday: -1, WarmupAgeDays: -30},
		{MailboxID: "sane", Weight: 1, RemainingToday: 1},
	})
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if got.MailboxID != "sane" {
		t.Errorf("mailbox = %q, want sane", got.MailboxID)
	}
}
