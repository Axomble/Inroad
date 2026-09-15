package providersignal

import (
	"sync"
	"testing"
	"time"
)

// fixedClock returns a clock the test advances by hand, so window bounds are
// asserted exactly rather than approximately (CONTRIBUTING.md's third "tests
// that assert nothing" shape: a fixture and an assertion reading two different
// clocks make the result depend on how fast the machine ran).
func fixedClock(at *time.Time) func() time.Time {
	return func() time.Time { return *at }
}

func TestCollectorAccumulatesPerKey(t *testing.T) {
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	c := NewCollector(fixedClock(&now))

	c.Observe(ProviderSMTP, OpSend, ReasonOK)
	c.Observe(ProviderSMTP, OpSend, ReasonOK)
	c.Observe(ProviderSMTP, OpSend, ReasonRateLimited)
	c.Observe(ProviderGmail, OpPoll, ReasonAuthFailed)

	now = now.Add(5 * time.Minute)
	w := c.Drain()

	if w.Start != time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC) {
		t.Errorf("window start = %v, want the moment the collector was created", w.Start)
	}
	if w.End != now {
		t.Errorf("window end = %v, want %v", w.End, now)
	}
	want := []Count{
		{Key: Key{Provider: ProviderGmail, Operation: OpPoll, Reason: ReasonAuthFailed}, Events: 1},
		{Key: Key{Provider: ProviderSMTP, Operation: OpSend, Reason: ReasonOK}, Events: 2},
		{Key: Key{Provider: ProviderSMTP, Operation: OpSend, Reason: ReasonRateLimited}, Events: 1},
	}
	if len(w.Counts) != len(want) {
		t.Fatalf("drained %d counts, want %d: %+v", len(w.Counts), len(want), w.Counts)
	}
	for i, got := range w.Counts {
		if got != want[i] {
			t.Errorf("count %d = %+v, want %+v (order must be deterministic)", i, got, want[i])
		}
	}
}

// The counters are WINDOW DELTAS: a drain resets them, so aggregation is a plain
// SUM with no per-worker baseline to reconcile. A second drain with nothing
// observed in between must be empty, not a repeat of the first.
func TestDrainResetsCountsAndAdvancesTheWindow(t *testing.T) {
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	c := NewCollector(fixedClock(&now))
	c.Observe(ProviderSMTP, OpSend, ReasonOK)

	now = now.Add(time.Minute)
	first := c.Drain()
	if len(first.Counts) != 1 || first.Counts[0].Events != 1 {
		t.Fatalf("first drain = %+v, want one count of 1", first.Counts)
	}

	now = now.Add(time.Minute)
	second := c.Drain()
	if len(second.Counts) != 0 {
		t.Fatalf("second drain = %+v, want no counts — a delta reported twice double-counts", second.Counts)
	}
	// The next window starts where the last one ended, so consecutive windows
	// tile the timeline with no gap and no overlap.
	if second.Start != first.End {
		t.Errorf("second window starts at %v, want the first window's end %v", second.Start, first.End)
	}
}

// A drain whose downstream write fails must lose that window's counts and
// nothing else. Re-adding them on failure would double-count whenever the write
// actually landed but its acknowledgement did not, and the brief's rule is the
// other way round: lose a window of counts, never a send.
func TestDrainedCountsAreNotRestoredOnFailure(t *testing.T) {
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	c := NewCollector(fixedClock(&now))
	c.Observe(ProviderSMTP, OpSend, ReasonOK)
	_ = c.Drain() // caller's write fails here; nothing is handed back

	c.Observe(ProviderSMTP, OpSend, ReasonOK)
	w := c.Drain()
	if len(w.Counts) != 1 || w.Counts[0].Events != 1 {
		t.Fatalf("drain after a failed flush = %+v, want only the ONE event observed since", w.Counts)
	}
}

// Unknown vocabulary must never reach the window: the persisted CHECK mirrors
// the closed sets, and one stray value fails the batch insert for every key in
// the window, not just its own.
func TestObserveNormalizesUnknownVocabulary(t *testing.T) {
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	c := NewCollector(fixedClock(&now))
	c.Observe(Provider("carrier-pigeon"), Operation("telepathy"), Reason("vibes"))

	w := c.Drain()
	if len(w.Counts) != 1 {
		t.Fatalf("drain = %+v, want one normalized count", w.Counts)
	}
	got := w.Counts[0]
	if got.Provider != ProviderSMTP {
		t.Errorf("provider = %q, want %q (MultiSender's own default leg)", got.Provider, ProviderSMTP)
	}
	if got.Operation != OpSend {
		t.Errorf("operation = %q, want %q", got.Operation, OpSend)
	}
	if got.Reason != ReasonOther {
		t.Errorf("reason = %q, want %q", got.Reason, ReasonOther)
	}
}

// The collector is written from every asynq handler goroutine at once and
// drained from the flush loop. Run with -race.
func TestCollectorIsSafeForConcurrentUse(t *testing.T) {
	now := time.Now()
	c := NewCollector(fixedClock(&now))

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 100 {
				c.Observe(ProviderSMTP, OpSend, ReasonOK)
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for range 20 {
			_ = c.Drain()
		}
	}()
	wg.Wait()
}

// The key space is closed by construction (3 providers x 2 operations x 8
// reasons), which is why the collector needs no cap and cannot be grown
// unboundedly by a hostile provider response. Pin it, so a future dimension is a
// deliberate act rather than a memory leak discovered in production.
func TestKeySpaceIsBounded(t *testing.T) {
	now := time.Now()
	c := NewCollector(fixedClock(&now))
	for _, p := range []Provider{ProviderSMTP, ProviderGmail, ProviderM365, "unknown"} {
		for _, op := range []Operation{OpSend, OpPoll, "unknown"} {
			for _, r := range []Reason{ReasonOK, ReasonAuthFailed, ReasonRateLimited, ReasonThrottled,
				ReasonBlocked, ReasonRejected, ReasonUnreachable, ReasonOther, "unknown"} {
				c.Observe(p, op, r)
			}
		}
	}
	if got, want := len(c.Drain().Counts), 3*2*8; got != want {
		t.Fatalf("distinct keys = %d, want %d — the key space is no longer closed", got, want)
	}
}
