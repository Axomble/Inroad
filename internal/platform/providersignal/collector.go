package providersignal

import (
	"cmp"
	"slices"
	"sync"
	"time"
)

// Key identifies one counter: which transport leg ran, what it was doing, and
// what the provider said about it.
type Key struct {
	Provider  Provider
	Operation Operation
	Reason    Reason
}

// Count is one key's DELTA for a window — the number of events observed since
// the previous drain, never a running total. That choice is what makes
// aggregation a plain SUM over a time range with no per-worker baseline to
// reconcile, and it is why a worker restart costs at most one partial window
// rather than corrupting a cumulative series.
type Count struct {
	Key
	Events int64
}

// Window is one drained accumulation interval. Start is the previous drain (or
// the collector's construction), End is this one, so consecutive windows tile
// the timeline with no gap and no overlap.
type Window struct {
	Start  time.Time
	End    time.Time
	Counts []Count
}

// Collector accumulates classified verdicts in memory, per worker process.
//
// Memory is bounded by construction, not by a cap: Observe normalizes all three
// dimensions onto closed vocabularies, so the map can hold at most
// len(Provider) x len(Operation) x len(Reason) = 48 entries however hostile or
// novel a provider's responses become (TestKeySpaceIsBounded).
//
// The clock is injected because the window bounds are asserted exactly in tests,
// and because a fixture and the code under test reading two different clocks is
// how a time-windowed test starts depending on how fast the machine ran.
type Collector struct {
	mu     sync.Mutex
	counts map[Key]int64
	since  time.Time
	now    func() time.Time
}

// NewCollector returns a Collector whose first window opens now.
func NewCollector(now func() time.Time) *Collector {
	return &Collector{counts: make(map[Key]int64), since: now(), now: now}
}

// Observe records one classified outcome. It is called on the send and poll hot
// paths from every handler goroutine, so it does exactly one map write under a
// mutex and can fail in no way at all — telemetry must never be able to fail a
// send.
//
// All three dimensions are normalized here rather than at the call site: the
// persisted CHECK constraints mirror these closed sets, and ONE stray value
// fails the batch insert for every key in the window, not just its own.
func (c *Collector) Observe(p Provider, op Operation, r Reason) {
	key := Key{Provider: NormalizeProvider(string(p)), Operation: op, Reason: r}
	if !key.Operation.Known() {
		key.Operation = OpSend
	}
	if !key.Reason.Known() {
		key.Reason = ReasonOther
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.counts[key]++
}

// Drain returns the deltas accumulated since the last drain and RESETS them,
// opening the next window at this instant.
//
// The reset is unconditional and the counts are NOT handed back if the caller's
// write then fails. That is deliberate: a flush that failed may still have
// landed (a write whose acknowledgement was lost), so restoring the counts would
// double-count exactly when the system is already unhealthy. Losing a window of
// counts is the accepted cost; losing a send is not, which is why nothing on the
// send path ever waits on or is failed by this.
//
// The returned slice is ordered by key so a window is reproducible — the same
// observations always produce the same batch, whatever order the map iterated.
func (c *Collector) Drain() Window {
	end := c.now()

	c.mu.Lock()
	counts := c.counts
	start := c.since
	c.counts = make(map[Key]int64, len(counts))
	c.since = end
	c.mu.Unlock()

	out := make([]Count, 0, len(counts))
	for key, events := range counts {
		out = append(out, Count{Key: key, Events: events})
	}
	slices.SortFunc(out, func(a, b Count) int {
		return cmp.Or(
			cmp.Compare(a.Provider, b.Provider),
			cmp.Compare(a.Operation, b.Operation),
			cmp.Compare(a.Reason, b.Reason),
		)
	})
	return Window{Start: start, End: end, Counts: out}
}
