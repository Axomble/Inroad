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
