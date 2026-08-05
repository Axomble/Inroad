package agentrun

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/app/agentchat"
)

func newTestPublisher(inner agentchat.StreamPublisher, clock func() time.Time) *runPublisher {
	p := newRunPublisher(inner, slog.New(slog.DiscardHandler), uuid.New(), uuid.NewString())
	p.now = clock
	return p
}

// Deltas inside the window merge into one publish; a non-delta event flushes
// what is buffered FIRST, so the order a reader sees is the order the run
// produced.
func TestRunPublisherCoalescesDeltasAndFlushesBeforeStructuralEvents(t *testing.T) {
	inner := &fakePublisher{}
	frozen := time.Now()
	p := newTestPublisher(inner, func() time.Time { return frozen })
	ctx := context.Background()

	for _, text := range []string{"Hel", "lo ", "there"} {
		p.Publish(ctx, agentchat.Event{Type: agentchat.EventTextDelta, Text: text})
	}
	p.Publish(ctx, agentchat.Event{Type: agentchat.EventToolOutput, ToolCallID: "call-1"})

	if len(inner.events) != 2 {
		t.Fatalf("published %d events, want 2: %+v", len(inner.events), inner.events)
	}
	if inner.events[0].Type != agentchat.EventTextDelta || inner.events[0].Text != "Hello there" {
		t.Fatalf("coalesced delta=%+v", inner.events[0])
	}
	if inner.events[1].Type != agentchat.EventToolOutput {
		t.Fatalf("second event=%+v", inner.events[1])
	}
}

// Two kinds of fragment must never be concatenated into each other, and the
// buffer must not grow without bound while the model streams.
func TestRunPublisherSplitsOnKindAndSize(t *testing.T) {
	inner := &fakePublisher{}
	frozen := time.Now()
	p := newTestPublisher(inner, func() time.Time { return frozen })
	ctx := context.Background()

	p.Publish(ctx, agentchat.Event{Type: agentchat.EventReasoningDelta, Text: "thinking"})
	p.Publish(ctx, agentchat.Event{Type: agentchat.EventTextDelta, Text: "answering"})
	p.Publish(ctx, agentchat.Event{Type: agentchat.EventTextDelta, Text: strings.Repeat("x", deltaBytes)})

	// The reasoning fragment flushed when the kind changed; the text fragments
	// flushed when they crossed the size bound.
	if len(inner.events) != 2 {
		t.Fatalf("published %d events, want 2: %+v", len(inner.events), inner.events)
	}
	if inner.events[0].Type != agentchat.EventReasoningDelta || inner.events[0].Text != "thinking" {
		t.Fatalf("first event=%+v", inner.events[0])
	}
	if inner.events[1].Type != agentchat.EventTextDelta || len(inner.events[1].Text) != deltaBytes+len("answering") {
		t.Fatalf("second event type=%s len=%d", inner.events[1].Type, len(inner.events[1].Text))
	}
	if p.buffered {
		t.Fatal("buffer still holds a fragment after crossing the size bound")
	}
}

// A stall longer than the window must not leave the last fragment sitting in
// the buffer once the next one arrives.
func TestRunPublisherFlushesOnTheTimeWindow(t *testing.T) {
	inner := &fakePublisher{}
	now := time.Now()
	p := newTestPublisher(inner, func() time.Time { return now })
	ctx := context.Background()

	p.Publish(ctx, agentchat.Event{Type: agentchat.EventTextDelta, Text: "a"})
	if len(inner.events) != 0 {
		t.Fatalf("published early: %+v", inner.events)
	}
	now = now.Add(deltaWindow)
	p.Publish(ctx, agentchat.Event{Type: agentchat.EventTextDelta, Text: "b"})
	if len(inner.events) != 1 || inner.events[0].Text != "ab" {
		t.Fatalf("events=%+v", inner.events)
	}
}
