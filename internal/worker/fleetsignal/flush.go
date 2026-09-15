package fleetsignal

import (
	"context"
	"log/slog"
	"time"

	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/platform/providersignal"
)

// shutdownFlushTimeout bounds the FINAL flush, which deliberately runs outside
// the cancelled run context (see Run). It is short because a stopping worker
// must not be held open by a database that is also going away — the partial
// window is worth a couple of seconds, never a hung shutdown.
const shutdownFlushTimeout = 5 * time.Second

// Recorder is the one-method slice of coreapi this package needs. Declared here,
// by the consumer, so a test can satisfy it without implementing
// coreapi.Client's ~40 methods — the same reason cmd/worker declares
// heartbeatClient.
type Recorder interface {
	RecordWorkerProviderSignals(ctx context.Context, in coreapi.WorkerProviderSignals) error
}

// Flusher periodically drains a Collector into the control plane.
//
// It is the ONLY thing in this package that does I/O, and it is deliberately off
// the send path: handlers write to the in-memory collector and never wait on
// anything, so a database outage degrades observability and cannot degrade
// delivery.
type Flusher struct {
	recorder  Recorder
	collector *providersignal.Collector
	// workerID is the identity the counts are attributed to
	// (internal/platform/workerid — an explicit pin, the host's public IP, or
	// its hostname). Empty means this process has no stable identity, and see
	// Flush for why that means recording nothing.
	workerID string
}

// NewFlusher builds the flush loop over a collector and the coreapi seam.
func NewFlusher(recorder Recorder, collector *providersignal.Collector, workerID string) *Flusher {
	return &Flusher{recorder: recorder, collector: collector, workerID: workerID}
}

// Flush drains one window and reports it.
//
// Three cases produce no write at all, and none is an error:
//
//   - no worker id: this process owns no "w:<id>" affinity queue and cannot be
//     attributed to an egress identity, so its counts have nowhere honest to go.
//     Recording them under "" would pool every unidentified worker in a
//     deployment into one fictional host, which is worse than not measuring.
//   - no recorder or no collector: signals are not wired.
//   - an empty window: the normal state of a healthy, idle worker.
//
// A failed write returns the error for the caller to LOG, and the drained counts
// are gone — the collector already reset. That is the stated trade: losing a
// flush loses a window of counts, never a send. Restoring them would double-count
// whenever the write landed but its acknowledgement did not, which is precisely
// the situation where the numbers are about to be read in anger.
func (f *Flusher) Flush(ctx context.Context) error {
	if f == nil || f.recorder == nil || f.collector == nil || f.workerID == "" {
		return nil
	}
	window := f.collector.Drain()
	if len(window.Counts) == 0 {
		return nil
	}

	counts := make([]coreapi.WorkerProviderSignalCount, 0, len(window.Counts))
	for _, c := range window.Counts {
		counts = append(counts, coreapi.WorkerProviderSignalCount{
			Provider:  string(c.Provider),
			Operation: string(c.Operation),
			Reason:    string(c.Reason),
			Events:    c.Events,
		})
	}
	return f.recorder.RecordWorkerProviderSignals(ctx, coreapi.WorkerProviderSignals{
		WorkerID:    f.workerID,
		WindowStart: window.Start,
		WindowEnd:   window.End,
		Counts:      counts,
	})
}

// Run flushes every interval until ctx is cancelled, then flushes ONCE MORE on
// the way out so a graceful stop does not discard the partial window this worker
// just finished accumulating.
//
// That final flush runs under a FRESH bounded context, not ctx. ctx is the thing
// that was just cancelled; reusing it would cancel the shutdown write before it
// could reach the database, and the last window of every worker's life would be
// lost on every single deploy. This is one of the three places
// context.Background() is correct — a shutdown path that must outlive the context
// being cancelled — and it carries its own deadline so it can never hang the
// stop.
//
// Failures are logged and never returned: this is a goroutine, there is nobody to
// return to, and a flush that failed is the one thing that must not escalate.
func (f *Flusher) Run(ctx context.Context, interval time.Duration) {
	if f == nil {
		return
	}
	if interval > 0 {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for running := true; running; {
			select {
			case <-ctx.Done():
				running = false
			case <-ticker.C:
				if err := f.Flush(ctx); err != nil {
					slog.ErrorContext(ctx, "fleet provider signal flush failed",
						"worker_id", f.workerID, "err", err,
						"note", "this window's counts are lost; sends are unaffected")
				}
			}
		}
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownFlushTimeout)
	defer cancel()
	if err := f.Flush(shutdownCtx); err != nil {
		slog.Error("final fleet provider signal flush failed on shutdown",
			"worker_id", f.workerID, "err", err,
			"note", "the last partial window is lost; sends are unaffected")
	}
}
