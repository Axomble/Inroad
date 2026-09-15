package fleetsignal

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/platform/mail"
	"github.com/inroad/inroad/internal/platform/providersignal"
)

type fakeRecorder struct {
	mu    sync.Mutex
	calls []coreapi.WorkerProviderSignals
	err   error
}

func (f *fakeRecorder) RecordWorkerProviderSignals(_ context.Context, in coreapi.WorkerProviderSignals) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, in)
	return f.err
}

func (f *fakeRecorder) snapshot() []coreapi.WorkerProviderSignals {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]coreapi.WorkerProviderSignals(nil), f.calls...)
}

func TestFlushReportsTheWindowWithTheWorkerIdentity(t *testing.T) {
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	c := providersignal.NewCollector(func() time.Time { return now })
	rec := &fakeRecorder{}
	f := NewFlusher(rec, c, "w-alpha")

	c.Observe(providersignal.ProviderSMTP, providersignal.OpSend, providersignal.ReasonOK)
	c.Observe(providersignal.ProviderSMTP, providersignal.OpSend, providersignal.ReasonRateLimited)
	c.Observe(providersignal.ProviderSMTP, providersignal.OpSend, providersignal.ReasonRateLimited)

	if err := f.Flush(context.Background()); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	calls := rec.snapshot()
	if len(calls) != 1 {
		t.Fatalf("recorder called %d times, want 1", len(calls))
	}
	got := calls[0]
	if got.WorkerID != "w-alpha" {
		t.Errorf("worker id = %q, want %q", got.WorkerID, "w-alpha")
	}
	if got.WindowStart != now || got.WindowEnd != now {
		t.Errorf("window = [%v, %v], want both at the injected clock %v", got.WindowStart, got.WindowEnd, now)
	}
	want := []coreapi.WorkerProviderSignalCount{
		{Provider: "smtp", Operation: "send", Reason: "ok", Events: 1},
		{Provider: "smtp", Operation: "send", Reason: "rate_limited", Events: 2},
	}
	if len(got.Counts) != len(want) {
		t.Fatalf("counts = %+v, want %+v", got.Counts, want)
	}
	for i := range want {
		if got.Counts[i] != want[i] {
			t.Errorf("count %d = %+v, want %+v", i, got.Counts[i], want[i])
		}
	}
}

// A quiet window is the normal state of a healthy worker. Reporting it would
// write a round trip every tick and a log line for nothing.
func TestFlushSkipsAnEmptyWindow(t *testing.T) {
	now := time.Now()
	rec := &fakeRecorder{}
	f := NewFlusher(rec, providersignal.NewCollector(func() time.Time { return now }), "w-alpha")

	if err := f.Flush(context.Background()); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if calls := rec.snapshot(); len(calls) != 0 {
		t.Fatalf("recorder called %d times for an empty window, want 0", len(calls))
	}
}

// The stated rule: losing a flush loses a WINDOW OF COUNTS and nothing else.
// The counts are not restored, because a failed write may still have landed and
// restoring them would double-count exactly when the system is already unhealthy.
func TestAFailedFlushLosesOnlyThatWindow(t *testing.T) {
	now := time.Now()
	c := providersignal.NewCollector(func() time.Time { return now })
	rec := &fakeRecorder{err: errors.New("database is down")}
	f := NewFlusher(rec, c, "w-alpha")

	c.Observe(providersignal.ProviderSMTP, providersignal.OpSend, providersignal.ReasonOK)
	if err := f.Flush(context.Background()); err == nil {
		t.Fatal("Flush swallowed the recorder's error; the caller must be able to log it")
	}

	rec.err = nil
	c.Observe(providersignal.ProviderGmail, providersignal.OpSend, providersignal.ReasonBlocked)
	if err := f.Flush(context.Background()); err != nil {
		t.Fatalf("second Flush: %v", err)
	}

	calls := rec.snapshot()
	if len(calls) != 2 {
		t.Fatalf("recorder called %d times, want 2", len(calls))
	}
	second := calls[1].Counts
	if len(second) != 1 || second[0].Reason != "blocked" {
		t.Fatalf("second window = %+v, want ONLY the event observed after the failure", second)
	}
}

// A worker with no stable id owns no per-IP affinity queue and cannot be
// attributed, so its counts have nowhere honest to go. Recording them under ""
// would pool every unidentified worker's signals into one fictional host.
func TestFlusherWithoutAWorkerIDRecordsNothing(t *testing.T) {
	now := time.Now()
	c := providersignal.NewCollector(func() time.Time { return now })
	rec := &fakeRecorder{}
	f := NewFlusher(rec, c, "")

	c.Observe(providersignal.ProviderSMTP, providersignal.OpSend, providersignal.ReasonOK)
	if err := f.Flush(context.Background()); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if calls := rec.snapshot(); len(calls) != 0 {
		t.Fatalf("recorder called %d times for an unidentified worker, want 0", len(calls))
	}
}

// Run must stop when its context is cancelled, and must flush ONE last time on
// the way out so a graceful shutdown does not discard the partial window the
// worker just finished accumulating.
func TestRunFlushesOnShutdown(t *testing.T) {
	now := time.Now()
	c := providersignal.NewCollector(func() time.Time { return now })
	rec := &fakeRecorder{}
	f := NewFlusher(rec, c, "w-alpha")
	c.Observe(providersignal.ProviderSMTP, providersignal.OpSend, providersignal.ReasonOK)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		// An interval far longer than the test, so the ONLY flush that can
		// happen is the shutdown one.
		f.Run(ctx, time.Hour)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after its context was cancelled")
	}

	calls := rec.snapshot()
	if len(calls) != 1 {
		t.Fatalf("recorder called %d times, want exactly the shutdown flush", len(calls))
	}
	if len(calls[0].Counts) != 1 || calls[0].Counts[0].Reason != "ok" {
		t.Errorf("shutdown flush = %+v, want the partial window", calls[0].Counts)
	}
}

// The shutdown flush must not run under the context that was just cancelled, or
// it would be cancelled before it could write and the final window would be lost
// every single time.
func TestShutdownFlushSurvivesTheCancelledContext(t *testing.T) {
	now := time.Now()
	c := providersignal.NewCollector(func() time.Time { return now })
	rec := &ctxCheckingRecorder{}
	f := NewFlusher(rec, c, "w-alpha")
	c.Observe(providersignal.ProviderSMTP, providersignal.OpSend, providersignal.ReasonOK)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f.Run(ctx, time.Hour)

	if rec.calls == 0 {
		t.Fatal("no shutdown flush happened at all")
	}
	if rec.sawCancelled {
		t.Error("the shutdown flush ran under the cancelled context, so it would never complete in production")
	}
}

type ctxCheckingRecorder struct {
	calls        int
	sawCancelled bool
}

func (r *ctxCheckingRecorder) RecordWorkerProviderSignals(ctx context.Context, _ coreapi.WorkerProviderSignals) error {
	r.calls++
	if ctx.Err() != nil {
		r.sawCancelled = true
	}
	return nil
}

// End to end through the decorator: a provider throttling this worker on a real
// send reaches the recorder as a typed, per-worker rate_limited delta.
func TestAThrottledSendReachesTheRecorderAsATypedDelta(t *testing.T) {
	now := time.Now()
	c := providersignal.NewCollector(func() time.Time { return now })
	rec := &fakeRecorder{}
	s := NewSender(&fakeSender{err: &mail.APIError{Provider: "m365", Op: "send", Status: 429}}, c)

	if _, err := s.Send(context.Background(), mail.OutboundJob{Provider: "m365"}, mail.Message{}); err == nil {
		t.Fatal("the decorator swallowed the send error")
	}
	if err := NewFlusher(rec, c, "w-alpha").Flush(context.Background()); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	calls := rec.snapshot()
	if len(calls) != 1 || len(calls[0].Counts) != 1 {
		t.Fatalf("recorder saw %+v, want one window with one count", calls)
	}
	want := coreapi.WorkerProviderSignalCount{Provider: "m365", Operation: "send", Reason: "rate_limited", Events: 1}
	if calls[0].Counts[0] != want {
		t.Errorf("count = %+v, want %+v", calls[0].Counts[0], want)
	}
}
