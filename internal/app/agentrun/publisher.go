package agentrun

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/app/agentchat"
)

// deltaWindow and deltaBytes bound how long a token fragment may sit in the
// coalescing buffer. Every delta used to cost its own round trip to Redis —
// roughly two thousand blocking EVALSHAs for one long answer, which on a
// managed Redis is seconds of pure latency added to a run. Batching a few tens
// of milliseconds of fragments is invisible to a reader (the panel commits the
// in-flight message on a 100 ms throttle anyway) and cuts the round trips by
// one to two orders of magnitude.
const (
	deltaWindow = 50 * time.Millisecond
	deltaBytes  = 512
)

// publishTimeout bounds the flush that happens after the run's context is
// already done.
const publishTimeout = 5 * time.Second

// runPublisher is the run loop's view of the stream: it coalesces consecutive
// text/reasoning/tool-input fragments and NEVER fails the run.
//
// Redis is the preview transport; Postgres is canonical. A run whose tool
// already paused a campaign must not be reported as failed because the
// progress chunk describing it could not be published — so a publish error is
// logged and the loop continues.
type runPublisher struct {
	inner    agentchat.StreamPublisher
	logger   *slog.Logger
	threadID uuid.UUID
	runID    string

	pending  agentchat.Event
	buffered bool
	since    time.Time
	now      func() time.Time
}

func newRunPublisher(inner agentchat.StreamPublisher, logger *slog.Logger, threadID uuid.UUID, runID string) *runPublisher {
	return &runPublisher{inner: inner, logger: logger, threadID: threadID, runID: runID, now: time.Now}
}

// isDelta reports whether an event is a fragment of something the client
// concatenates, and so may be merged with its neighbours.
func isDelta(eventType string) bool {
	switch eventType {
	case agentchat.EventTextDelta, agentchat.EventReasoningDelta, agentchat.EventToolInputDelta:
		return true
	default:
		return false
	}
}

// Publish buffers a delta or, for anything else, flushes what is buffered and
// sends immediately — so the sequence a reader observes is unchanged, and every
// structural event (tool start, tool output, approval, terminal) is still on
// the wire the instant it happens.
func (p *runPublisher) Publish(ctx context.Context, ev agentchat.Event) {
	if !isDelta(ev.Type) {
		p.Flush(ctx)
		p.send(ctx, ev)
		return
	}
	if p.buffered && p.pending.Type == ev.Type && p.pending.ToolCallID == ev.ToolCallID {
		p.pending.Text += ev.Text
	} else {
		p.Flush(ctx)
		p.pending, p.buffered, p.since = ev, true, p.now()
	}
	if len(p.pending.Text) >= deltaBytes || p.now().Sub(p.since) >= deltaWindow {
		p.Flush(ctx)
	}
}

// Flush emits whatever is buffered.
func (p *runPublisher) Flush(ctx context.Context) {
	if !p.buffered {
		return
	}
	ev := p.pending
	p.pending, p.buffered = agentchat.Event{}, false
	p.send(ctx, ev)
}

// Close flushes the tail of a run's output even when the run's context has
// already been cancelled — the fragments are still worth delivering, and the
// canonical transcript is written on the same detached basis.
func (p *runPublisher) Close(ctx context.Context) {
	if !p.buffered {
		return
	}
	flushCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), publishTimeout)
	defer cancel()
	p.Flush(flushCtx)
}

func (p *runPublisher) send(ctx context.Context, ev agentchat.Event) {
	if _, err := p.inner.Publish(ctx, p.threadID, ev); err != nil {
		p.logger.Warn("agent stream publish failed",
			"thread_id", p.threadID, "run_id", p.runID, "event", ev.Type, "err", err)
	}
}
