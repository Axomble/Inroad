package warmup

import (
	"context"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func TestStaticLibraryStableSelection(t *testing.T) {
	lib := NewStaticLibrary()
	ctx := context.Background()

	first, err := lib.Thread(ctx, "seed-42")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	second, err := lib.Thread(ctx, "seed-42")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("same seed produced different threads:\n%+v\n%+v", first, second)
	}
}

// TestStaticLibraryFullyExpanded pins the generator's output contract: whatever the
// seed, a returned Thread is ready to send — every spin group resolved, no spintax
// syntax and no bare brace left anywhere in the subject or any turn — and the opener
// still begins with one of the shared greeting options.
func TestStaticLibraryFullyExpanded(t *testing.T) {
	lib := NewStaticLibrary()
	// Sweep many seeds so we exercise every thread and a wide spread of variants.
	for i := 0; i < 500; i++ {
		th, err := lib.Thread(context.Background(), string(rune('a'+i%26))+"-seed-"+strconv.Itoa(i))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(th.Turns) == 0 {
			t.Fatal("thread has no turns")
		}
		for _, field := range append([]string{th.Subject}, th.Turns...) {
			if strings.ContainsAny(field, "{}|") {
				t.Fatalf("unexpanded spintax left in output: %q", field)
			}
		}
		if !startsWithGreeting(th.Turns[0]) {
			t.Fatalf("opener does not start with a known greeting: %q", th.Turns[0])
		}
	}
}

// TestStaticLibraryVariesWithinAThread proves the corpus is genuinely combinatorial
// and not one draw per conversation: seeds that land on the SAME template must still
// produce many distinct subjects and many distinct openers, and the subject's variant
// must not be locked to the opener's (independent per-field spintax seeds). Without
// this, adding spin groups would look right while every thread still shipped a single
// fixed body.
func TestStaticLibraryVariesWithinAThread(t *testing.T) {
	lib := NewStaticLibrary()
	const target = 0 // the template index we collect variants for

	subjects := map[string]bool{}
	openers := map[string]bool{}
	pairs := map[string]bool{}
	for i := 0; i < 4000; i++ {
		seed := "vary-" + strconv.Itoa(i)
		if int(hashU64("thread", seed)%uint64(len(curatedThreads()))) != target {
			continue
		}
		th, err := lib.Thread(context.Background(), seed)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		subjects[th.Subject] = true
		openers[th.Turns[0]] = true
		pairs[th.Subject+"\x00"+th.Turns[0]] = true
	}

	if len(subjects) < 3 {
		t.Errorf("only %d distinct subjects for one template — subjects are not spun", len(subjects))
	}
	if len(openers) < 20 {
		t.Errorf("only %d distinct openers for one template — bodies barely vary", len(openers))
	}
	// Independent draws ⇒ subject and opener combine freely, so the pair count must
	// exceed either alone. Equality would mean one field determines the other.
	if len(pairs) <= len(subjects) || len(pairs) <= len(openers) {
		t.Errorf("subject/opener not independent: %d pairs from %d subjects and %d openers",
			len(pairs), len(subjects), len(openers))
	}
}

// TestCuratedThreadsEditorialConstraints guards the deliberate register of the
// corpus against a future addition that drifts toward marketing mail: warmup bodies
// carry no links and no bulk-mail vocabulary, since either would make the traffic
// look like the cold email it exists to protect.
func TestCuratedThreadsEditorialConstraints(t *testing.T) {
	banned := []string{
		"http://", "https://", "www.", "unsubscribe", "click here", "free trial",
		"limited time", "book a demo", "special offer", "act now", "%off",
	}
	for i, th := range curatedThreads() {
		for _, field := range append([]string{th.Subject}, th.Turns...) {
			lower := strings.ToLower(field)
			for _, b := range banned {
				if strings.Contains(lower, b) {
					t.Errorf("thread %d contains banned marketing pattern %q: %q", i, b, field)
				}
			}
		}
		if len(th.Turns) < 2 {
			t.Errorf("thread %d has %d turns, want at least an opener + one reply", i, len(th.Turns))
		}
	}
}

func TestReplyExhaustion(t *testing.T) {
	th, err := NewStaticLibrary().Thread(context.Background(), "seed-reply")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for i := range th.Turns {
		body, ok := Reply(th, i)
		if !ok {
			t.Fatalf("expected turn %d to exist", i)
		}
		if body != th.Turns[i] {
			t.Fatalf("turn %d body mismatch", i)
		}
	}
	if _, ok := Reply(th, len(th.Turns)); ok {
		t.Fatal("expected exhaustion past the last turn")
	}
	if _, ok := Reply(th, -1); ok {
		t.Fatal("expected negative turn to be rejected")
	}
}

func TestEmptyLibraryReturnsError(t *testing.T) {
	var empty StaticLibrary // zero value: no threads
	if _, err := empty.Thread(context.Background(), "seed"); !errors.Is(err, ErrNoContent) {
		t.Fatalf("expected ErrNoContent, got %v", err)
	}
}

// TestMaxContentTurns proves the coarse repliable-thread bound equals the real
// maximum turn count across the library (so SelectWarmupReplyPartner never
// excludes a thread that could still yield a reply), and is derived, not guessed:
// it recomputes the max independently and compares.
func TestMaxContentTurns(t *testing.T) {
	want := 0
	for _, th := range curatedThreads() {
		if len(th.Turns) > want {
			want = len(th.Turns)
		}
	}
	if want < 2 {
		t.Fatalf("library max turns = %d, want >= 2 (a thread needs an opener + at least one reply)", want)
	}
	if got := MaxContentTurns(); got != want {
		t.Fatalf("MaxContentTurns() = %d, want %d (the actual library maximum)", got, want)
	}
}

// startsWithGreeting checks the opener against the greeting spin group itself, so the
// test stays honest when an opener is added or reworded — there is no second hard-coded
// list to drift out of sync with content.go.
func startsWithGreeting(s string) bool {
	for _, g := range strings.Split(strings.Trim(greeting, "{}"), "|") {
		if strings.HasPrefix(s, g) {
			return true
		}
	}
	return false
}
