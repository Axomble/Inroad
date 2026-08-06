package agentchat

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

func TestSplitEnvelopePreservesJSONPayload(t *testing.T) {
	seq, payload, ok := splitEnvelope(`12:{"type":"text_delta","text":"a:b"}`)
	if !ok || seq != 12 || string(payload) != `{"type":"text_delta","text":"a:b"}` {
		t.Fatalf("seq=%d payload=%q ok=%v", seq, payload, ok)
	}
}

// newTestStream starts an in-memory Redis and returns a stream over it.
func newTestStream(t *testing.T) *RedisStream {
	t.Helper()
	server := miniredis.RunT(t)
	stream := NewRedisStreamWithClient(redis.NewClient(&redis.Options{Addr: server.Addr()}))
	t.Cleanup(func() { _ = stream.Close() })
	return stream
}

// readEvents drains n frames, failing the test rather than hanging when the
// stream delivers fewer.
func readEvents(t *testing.T, frames <-chan Frame, n int) []Frame {
	t.Helper()
	out := make([]Frame, 0, n)
	deadline := time.After(3 * time.Second)
	for len(out) < n {
		select {
		case frame, open := <-frames:
			if !open {
				t.Fatalf("stream closed after %d of %d frames: %+v", len(out), n, out)
			}
			out = append(out, frame)
		case <-deadline:
			t.Fatalf("timed out after %d of %d frames: %+v", len(out), n, out)
		}
	}
	return out
}

func eventType(t *testing.T, data []byte) string {
	t.Helper()
	var ev Event
	if err := json.Unmarshal(data, &ev); err != nil {
		t.Fatalf("decode frame %q: %v", data, err)
	}
	return ev.Type
}

// A client that reconnects with the LAST run's Last-Event-ID must still receive
// the NEXT run's opening events. The run's log is deleted on completion, so a
// per-log sequence would restart at 1 and every one of those events would look
// like a duplicate of something the client already saw — a short second run
// would render as nothing at all.
func TestAttachAfterRunDeliversNextRunFromItsFirstEvent(t *testing.T) {
	stream := newTestStream(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	threadID := uuid.New()

	// Run 1: three chunks and a terminal event, then the log is cleared.
	for _, ev := range []Event{
		{Type: EventTextDelta, Text: "one"},
		{Type: EventTextDelta, Text: "two"},
		{Type: EventMessagePersisted},
		{Type: EventDone},
	} {
		if _, err := stream.Publish(ctx, threadID, ev); err != nil {
			t.Fatalf("publish run 1: %v", err)
		}
	}
	if err := stream.Clear(ctx, threadID); err != nil {
		t.Fatalf("clear: %v", err)
	}

	// The client's last seen id is run 1's terminal event.
	frames, err := stream.Attach(ctx, threadID, 4)
	if err != nil {
		t.Fatalf("attach: %v", err)
	}

	// Run 2 is short: two events total.
	for _, ev := range []Event{{Type: EventTextDelta, Text: "next run"}, {Type: EventDone}} {
		if _, err := stream.Publish(ctx, threadID, ev); err != nil {
			t.Fatalf("publish run 2: %v", err)
		}
	}

	got := readEvents(t, frames, 2)
	if eventType(t, got[0].Data) != EventTextDelta || eventType(t, got[1].Data) != EventDone {
		t.Fatalf("frames=%s / %s", got[0].Data, got[1].Data)
	}
	if got[0].Seq != 5 || got[1].Seq != 6 {
		t.Fatalf("sequences=%d,%d — run 2 must keep counting up from run 1", got[0].Seq, got[1].Seq)
	}

	// And nothing from run 1 is replayed: the log was cleared, so the two
	// frames above are the whole delivery.
	select {
	case extra, open := <-frames:
		if open {
			t.Fatalf("unexpected extra frame seq=%d data=%s", extra.Seq, extra.Data)
		}
	case <-time.After(100 * time.Millisecond):
	}
}

// Attaching mid-run replays what the log already holds and then bridges live
// events with no gap and no duplicate.
func TestAttachReplaysBacklogThenBridgesLive(t *testing.T) {
	stream := newTestStream(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	threadID := uuid.New()

	for _, text := range []string{"a", "b", "c"} {
		if _, err := stream.Publish(ctx, threadID, Event{Type: EventTextDelta, Text: text}); err != nil {
			t.Fatalf("publish: %v", err)
		}
	}
	frames, err := stream.Attach(ctx, threadID, 1)
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	if _, err := stream.Publish(ctx, threadID, Event{Type: EventDone}); err != nil {
		t.Fatalf("publish live: %v", err)
	}

	got := readEvents(t, frames, 3)
	for i, want := range []int64{2, 3, 4} {
		if got[i].Seq != want {
			t.Fatalf("frame %d seq=%d, want %d", i, got[i].Seq, want)
		}
	}
}

// Two readers on one thread share a single Redis subscription; both still see
// every event.
func TestAttachFansOutToEveryReader(t *testing.T) {
	stream := newTestStream(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	threadID := uuid.New()

	first, err := stream.Attach(ctx, threadID, 0)
	if err != nil {
		t.Fatalf("attach first: %v", err)
	}
	second, err := stream.Attach(ctx, threadID, 0)
	if err != nil {
		t.Fatalf("attach second: %v", err)
	}
	if _, err := stream.Publish(ctx, threadID, Event{Type: EventDone}); err != nil {
		t.Fatalf("publish: %v", err)
	}
	for _, frames := range []<-chan Frame{first, second} {
		got := readEvents(t, frames, 1)
		if eventType(t, got[0].Data) != EventDone {
			t.Fatalf("frame=%s", got[0].Data)
		}
	}
}

// The replay buffer is bounded: a run that outruns the cap keeps its most
// recent chunks, and their sequence numbers stay truthful.
func TestPublishTrimsTheReplayLog(t *testing.T) {
	stream := newTestStream(t)
	stream.logEntries = 4
	ctx := context.Background()
	threadID := uuid.New()

	for i := 0; i < 10; i++ {
		if _, err := stream.Publish(ctx, threadID, Event{Type: EventTextDelta, Text: "x"}); err != nil {
			t.Fatalf("publish %d: %v", i, err)
		}
	}
	frames, err := stream.Attach(ctx, threadID, 0)
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	got := readEvents(t, frames, 4)
	if got[0].Seq != 7 || got[3].Seq != 10 {
		t.Fatalf("trimmed replay sequences=%d..%d, want 7..10", got[0].Seq, got[3].Seq)
	}
}
