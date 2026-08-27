package realtime

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// newTestHub starts an in-memory Redis and returns a hub over it.
func newTestHub(t *testing.T) *Hub {
	t.Helper()
	server := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: server.Addr()})
	hub := New(rdb)
	t.Cleanup(func() {
		_ = hub.Close()
		_ = rdb.Close()
	})
	return hub
}

// readFrames drains n frames, FAILING the test rather than hanging when the hub
// delivers fewer. A hanging test in CI is worse than a failing one.
func readFrames(t *testing.T, frames <-chan Frame, n int) []Frame {
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

// expectClosed asserts the reader's channel closes, against a deadline.
func expectClosed(t *testing.T, frames <-chan Frame) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case _, open := <-frames:
			if !open {
				return
			}
		case <-deadline:
			t.Fatal("reader channel never closed")
		}
	}
}

// expectNothing asserts no frame arrives within a short grace period. Used for
// the cross-tenant assertion, where "nothing" is the whole point.
func expectNothing(t *testing.T, frames <-chan Frame, within time.Duration) {
	t.Helper()
	select {
	case frame, open := <-frames:
		if open {
			t.Fatalf("unexpected frame seq=%d data=%s", frame.Seq, frame.Data)
		}
		t.Fatal("reader closed unexpectedly")
	case <-time.After(within):
	}
}

func decode(t *testing.T, data []byte) Envelope {
	t.Helper()
	var ev Envelope
	if err := json.Unmarshal(data, &ev); err != nil {
		t.Fatalf("decode envelope %q: %v", data, err)
	}
	return ev
}

func testEnvelope(eventType string) Envelope {
	return Envelope{
		Type:    eventType,
		Subject: Subject{Kind: "thread", ID: "11111111-1111-1111-1111-111111111111"},
		ActorID: "22222222-2222-2222-2222-222222222222",
		Data:    json.RawMessage(`{"thread_id":"11111111-1111-1111-1111-111111111111"}`),
	}
}

func TestSplitEnvelopePreservesJSONPayload(t *testing.T) {
	seq, payload, ok := splitEnvelope(`12:{"type":"inbox.message.created","at":"2026-08-26T15:41:00Z"}`)
	if !ok || seq != 12 || string(payload) != `{"type":"inbox.message.created","at":"2026-08-26T15:41:00Z"}` {
		t.Fatalf("seq=%d payload=%q ok=%v", seq, payload, ok)
	}
}

// A published envelope round-trips with its type, subject, actor and data
// intact, and the sequence the publisher was told is the sequence the reader
// sees — in the frame and in the JSON body.
func TestPublishRoundTripsToReader(t *testing.T) {
	hub := newTestHub(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	workspaceID := uuid.New()

	frames, err := hub.Attach(ctx, workspaceID, 0)
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	seq, err := hub.Publish(ctx, workspaceID, testEnvelope("inbox.message.created"))
	if err != nil {
		t.Fatalf("publish: %v", err)
	}

	got := readFrames(t, frames, 1)[0]
	if got.Seq != seq {
		t.Fatalf("frame seq=%d, publish returned %d", got.Seq, seq)
	}
	ev := decode(t, got.Data)
	if ev.Seq != seq {
		t.Fatalf("envelope seq=%d, want %d — the wire body must carry it too", ev.Seq, seq)
	}
	if ev.Type != "inbox.message.created" {
		t.Fatalf("type=%q", ev.Type)
	}
	if ev.Subject.Kind != "thread" || ev.Subject.ID != "11111111-1111-1111-1111-111111111111" {
		t.Fatalf("subject=%+v", ev.Subject)
	}
	if ev.ActorID != "22222222-2222-2222-2222-222222222222" {
		t.Fatalf("actorId=%q — the client cannot drop its own echo without it", ev.ActorID)
	}
	if string(ev.Data) != `{"thread_id":"11111111-1111-1111-1111-111111111111"}` {
		t.Fatalf("data=%s", ev.Data)
	}
	if ev.At.IsZero() {
		t.Fatal("at is zero: Publish must stamp it")
	}
}

// The envelope's JSON field names are the contract slice 5's client is written
// against. A rename here silently breaks every handler on the other side, so it
// is asserted on the encoded form rather than the struct.
func TestEnvelopeWireFieldNames(t *testing.T) {
	body, err := encodeBody(Envelope{
		Seq:     999, // ignored: the sequence comes from the atomic script
		Type:    "inbox.message.created",
		Subject: Subject{Kind: "thread", ID: "t1"},
		At:      time.Date(2026, 8, 26, 15, 41, 0, 0, time.UTC),
		ActorID: "u1",
		Data:    json.RawMessage(`{"a":1}`),
	})
	if err != nil {
		t.Fatalf("encodeBody: %v", err)
	}
	// What the Lua script publishes, assembled the same way.
	got := `{"seq":41,` + body
	const want = `{"seq":41,"type":"inbox.message.created","subject":{"kind":"thread","id":"t1"},` +
		`"at":"2026-08-26T15:41:00Z","actor_id":"u1","data":{"a":1}}`
	if got != want {
		t.Fatalf("envelope wire form changed:\n got %s\nwant %s", got, want)
	}
	// And it is valid JSON with exactly one seq — a body that kept its own would
	// give the client a duplicate key whose winner depends on the parser.
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal([]byte(got), &decoded); err != nil {
		t.Fatalf("published body is not valid JSON: %v", err)
	}
	if string(decoded["seq"]) != "41" {
		t.Fatalf("seq=%s, want 41", decoded["seq"])
	}

	// Every key is snake_case, matching this repo's JSON boundary convention
	// (CLAUDE.md) and — specifically — what the client actually parses:
	// web/src/features/realtime/socket-events.ts reads `actor_id`.
	//
	// This is not pedantry about style. The spec §5 sketch wrote `actorId`, the
	// hub shipped that tag, and the guard that stops a client re-applying its own
	// optimistic update reads `actor_id`. The field was simply always absent, so
	// the self-echo guard silently never fired and a dragged deal would snap back
	// and forth. A string-equality assertion above pins the shape; this one names
	// the reason, so the next reader does not "correct" it back to camelCase.
	for key := range decoded {
		if strings.ToLower(key) != key {
			t.Errorf("envelope key %q is not snake_case — the client parses snake_case and would read undefined", key)
		}
	}
	if _, ok := decoded["actor_id"]; !ok {
		t.Error(`envelope has no "actor_id" key — the client's self-echo guard reads it and would never fire`)
	}
}

// Sequences are monotonic per workspace and independent BETWEEN workspaces: two
// tenants publishing concurrently must not consume each other's numbering, or a
// reader's replay position would mean something different than it thought.
func TestPublishSequenceIsMonotonicPerWorkspace(t *testing.T) {
	hub := newTestHub(t)
	ctx := context.Background()
	first, second := uuid.New(), uuid.New()

	for i := int64(1); i <= 5; i++ {
		seq, err := hub.Publish(ctx, first, testEnvelope("pulse.updated"))
		if err != nil {
			t.Fatalf("publish first %d: %v", i, err)
		}
		if seq != i {
			t.Fatalf("workspace A seq=%d, want %d", seq, i)
		}
		seq, err = hub.Publish(ctx, second, testEnvelope("pulse.updated"))
		if err != nil {
			t.Fatalf("publish second %d: %v", i, err)
		}
		if seq != i {
			t.Fatalf("workspace B seq=%d, want %d — counters must not be shared", seq, i)
		}
	}
}

// Publish refuses to run without a workspace, and refuses a typeless envelope.
// The workspace check is the security boundary: the caller must always supply
// one from a verified ticket (spec §7.1), and a uuid.Nil default would publish
// into a channel no tenant owns.
func TestPublishAndAttachRequireAWorkspace(t *testing.T) {
	hub := newTestHub(t)
	ctx := context.Background()

	if _, err := hub.Publish(ctx, uuid.Nil, testEnvelope("pulse.updated")); !errors.Is(err, ErrNoWorkspace) {
		t.Fatalf("publish with nil workspace: err=%v, want ErrNoWorkspace", err)
	}
	if _, err := hub.Publish(ctx, uuid.New(), Envelope{}); !errors.Is(err, ErrNoType) {
		t.Fatalf("publish typeless: err=%v, want ErrNoType", err)
	}
	if _, err := hub.Attach(ctx, uuid.Nil, 0); !errors.Is(err, ErrNoWorkspace) {
		t.Fatalf("attach with nil workspace: err=%v, want ErrNoWorkspace", err)
	}
}

// Attaching after a disconnect replays what the log holds and then bridges live
// events with no gap and no duplicate.
func TestAttachReplaysBacklogThenBridgesLive(t *testing.T) {
	hub := newTestHub(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	workspaceID := uuid.New()

	for range 3 {
		if _, err := hub.Publish(ctx, workspaceID, testEnvelope("mailbox.changed")); err != nil {
			t.Fatalf("publish backlog: %v", err)
		}
	}
	// The client saw seq 1 before it dropped off.
	frames, err := hub.Attach(ctx, workspaceID, 1)
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	if _, err := hub.Publish(ctx, workspaceID, testEnvelope("campaign.launched")); err != nil {
		t.Fatalf("publish live: %v", err)
	}

	got := readFrames(t, frames, 3)
	for i, want := range []int64{2, 3, 4} {
		if got[i].Seq != want {
			t.Fatalf("frame %d seq=%d, want %d", i, got[i].Seq, want)
		}
		if got[i].StaleReplay {
			t.Fatalf("frame %d flagged stale: the log covered the client's position", i)
		}
	}
}

// Attach joins the fan-out BEFORE reading the log, so an event published during
// the handoff is at worst delivered twice — once from the log, once live — never
// missed. This reproduces exactly that overlap: an entry that is already in the
// log arrives again on the live channel. It must be dropped, and the events
// after it must still flow.
//
// Reproducing the race by timing would be flaky, so the duplicate is injected
// on the channel directly. That is the same string the Lua script publishes, so
// the reader cannot tell the difference.
func TestAttachDropsALiveEventItAlreadyReplayed(t *testing.T) {
	hub := newTestHub(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	workspaceID := uuid.New()

	if _, err := hub.Publish(ctx, workspaceID, testEnvelope("mailbox.changed")); err != nil {
		t.Fatalf("publish: %v", err)
	}
	logged, err := hub.rdb.LRange(ctx, logKey(workspaceID), 0, -1).Result()
	if err != nil || len(logged) != 1 {
		t.Fatalf("read log: entries=%v err=%v", logged, err)
	}

	// The reader replays seq 1 from the log...
	frames, err := hub.Attach(ctx, workspaceID, 0)
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	if got := readFrames(t, frames, 1)[0]; got.Seq != 1 {
		t.Fatalf("replayed seq=%d, want 1", got.Seq)
	}
	// ...and then the same entry lands on the live feed, as it would when the
	// publish raced the handoff.
	if err := hub.rdb.Publish(ctx, channelKey(workspaceID), logged[0]).Err(); err != nil {
		t.Fatalf("republish: %v", err)
	}
	expectNothing(t, frames, 250*time.Millisecond)

	// And the reader is not wedged: the next real event still arrives.
	if _, err := hub.Publish(ctx, workspaceID, testEnvelope("campaign.launched")); err != nil {
		t.Fatalf("publish next: %v", err)
	}
	if got := readFrames(t, frames, 1)[0]; got.Seq != 2 {
		t.Fatalf("next seq=%d, want 2", got.Seq)
	}
}

// A client gone longer than the window gets what remains, flagged stale on the
// first frame so it does a full refetch instead of assuming continuity. A
// silent gap is the one failure a bounded replay window must not cause.
func TestAttachFlagsATrimmedReplayAsStale(t *testing.T) {
	hub := newTestHub(t)
	hub.logEntries = 4
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	workspaceID := uuid.New()

	for range 10 {
		if _, err := hub.Publish(ctx, workspaceID, testEnvelope("pulse.updated")); err != nil {
			t.Fatalf("publish: %v", err)
		}
	}
	// The client's last seen event (2) fell out of the window long ago.
	frames, err := hub.Attach(ctx, workspaceID, 2)
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	got := readFrames(t, frames, 4)
	if got[0].Seq != 7 || got[3].Seq != 10 {
		t.Fatalf("trimmed replay sequences=%d..%d, want 7..10", got[0].Seq, got[3].Seq)
	}
	if !got[0].StaleReplay {
		t.Fatal("first frame not flagged stale: the client would assume it saw 3..6")
	}
	for i, frame := range got[1:] {
		if frame.StaleReplay {
			t.Fatalf("frame %d flagged stale: only the first frame carries the flag", i+1)
		}
	}
}

// A position AHEAD of the workspace's counter (the counter's TTL lapsed while
// the client was away) replays the window from the start and is flagged stale,
// rather than filtering out everything that follows.
func TestAttachTreatsAPositionAheadOfTheCounterAsStale(t *testing.T) {
	hub := newTestHub(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	workspaceID := uuid.New()

	if _, err := hub.Publish(ctx, workspaceID, testEnvelope("pulse.updated")); err != nil {
		t.Fatalf("publish: %v", err)
	}
	frames, err := hub.Attach(ctx, workspaceID, 900)
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	got := readFrames(t, frames, 1)[0]
	if got.Seq != 1 {
		t.Fatalf("seq=%d, want 1", got.Seq)
	}
	if !got.StaleReplay {
		t.Fatal("first frame not flagged stale")
	}
}

// Many readers on one workspace share a SINGLE Redis subscription; every one
// still sees every event. Fifty tabs must cost one subscription per node.
func TestAttachFansOutToEveryReaderOverOneSubscription(t *testing.T) {
	hub := newTestHub(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	workspaceID := uuid.New()

	readers := make([]<-chan Frame, 0, 5)
	for i := range 5 {
		frames, err := hub.Attach(ctx, workspaceID, 0)
		if err != nil {
			t.Fatalf("attach %d: %v", i, err)
		}
		readers = append(readers, frames)
	}
	hub.mu.Lock()
	subs := 0
	if hub.pubsub != nil {
		subs = 1
	}
	attached := len(hub.readers[workspaceID])
	hub.mu.Unlock()
	if subs != 1 || attached != 5 {
		t.Fatalf("redis subscriptions=%d readers=%d, want 1 and 5", subs, attached)
	}
	if got := hub.Connections(); got != 5 {
		t.Fatalf("Connections()=%d, want 5", got)
	}

	if _, err := hub.Publish(ctx, workspaceID, testEnvelope("deal.moved")); err != nil {
		t.Fatalf("publish: %v", err)
	}
	for i, frames := range readers {
		got := readFrames(t, frames, 1)[0]
		if decode(t, got.Data).Type != "deal.moved" {
			t.Fatalf("reader %d frame=%s", i, got.Data)
		}
	}
}

// THE tenant boundary. A reader on workspace A must never see a workspace B
// event, no matter that both are served by the same process, the same Redis
// connection and the same PSubscribe pattern.
func TestReaderNeverSeesAnotherWorkspacesEvent(t *testing.T) {
	hub := newTestHub(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tenantA, tenantB := uuid.New(), uuid.New()

	framesA, err := hub.Attach(ctx, tenantA, 0)
	if err != nil {
		t.Fatalf("attach A: %v", err)
	}
	framesB, err := hub.Attach(ctx, tenantB, 0)
	if err != nil {
		t.Fatalf("attach B: %v", err)
	}

	if _, err := hub.Publish(ctx, tenantB, testEnvelope("inbox.message.created")); err != nil {
		t.Fatalf("publish B: %v", err)
	}
	// B receives it — which proves the publish actually happened and that A's
	// silence below is isolation rather than a dead hub.
	if got := readFrames(t, framesB, 1)[0]; got.Seq != 1 {
		t.Fatalf("B seq=%d, want 1", got.Seq)
	}
	expectNothing(t, framesA, 250*time.Millisecond)

	// And A's own replay log holds nothing of B's, either.
	replayA, err := hub.Attach(ctx, tenantA, 0)
	if err != nil {
		t.Fatalf("re-attach A: %v", err)
	}
	expectNothing(t, replayA, 250*time.Millisecond)
}

// A reader that stops draining is DROPPED once it is subscriberBuffer behind,
// rather than being waited on: one stalled tab must not stall dispatch for
// every other connection on the node. The client reconnects and replays.
func TestSlowReaderIsDroppedNotWaitedOn(t *testing.T) {
	hub := newTestHub(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	workspaceID := uuid.New()

	slow, err := hub.Attach(ctx, workspaceID, 0)
	if err != nil {
		t.Fatalf("attach slow: %v", err)
	}
	// Publish well past the reader's buffer plus the out channel, without
	// reading a single frame.
	for i := range subscriberBuffer + 128 {
		if _, err := hub.Publish(ctx, workspaceID, testEnvelope("pulse.updated")); err != nil {
			t.Fatalf("publish %d: %v", i, err)
		}
	}
	// The channel closes: the reader was dropped and its pump ended.
	expectClosed(t, slow)

	// The hub is still healthy — a fresh reader gets live events.
	healthy, err := hub.Attach(ctx, workspaceID, 0)
	if err != nil {
		t.Fatalf("attach healthy: %v", err)
	}
	// Drain the replay window it inherits, then confirm the live feed works.
	drained := readFrames(t, healthy, replayEntries)
	last := drained[len(drained)-1].Seq
	if _, err := hub.Publish(ctx, workspaceID, testEnvelope("campaign.launched")); err != nil {
		t.Fatalf("publish after drop: %v", err)
	}
	if got := readFrames(t, healthy, 1)[0]; got.Seq != last+1 {
		t.Fatalf("live seq after drop=%d, want %d", got.Seq, last+1)
	}
}

// Cancelling a reader's context ends only that reader. This is the per-
// connection teardown the transport relies on when one socket closes.
func TestReaderContextCancellationEndsThatReaderAlone(t *testing.T) {
	hub := newTestHub(t)
	shared, cancelShared := context.WithCancel(context.Background())
	defer cancelShared()
	doomedCtx, cancelDoomed := context.WithCancel(context.Background())
	workspaceID := uuid.New()

	survivor, err := hub.Attach(shared, workspaceID, 0)
	if err != nil {
		t.Fatalf("attach survivor: %v", err)
	}
	doomed, err := hub.Attach(doomedCtx, workspaceID, 0)
	if err != nil {
		t.Fatalf("attach doomed: %v", err)
	}

	cancelDoomed()
	expectClosed(t, doomed)

	if _, err := hub.Publish(shared, workspaceID, testEnvelope("send.bounced")); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if got := readFrames(t, survivor, 1)[0]; decode(t, got.Data).Type != "send.bounced" {
		t.Fatalf("survivor frame=%s", got.Data)
	}
}

// Close releases EVERY live connection. http.Server.Shutdown does not close
// hijacked connections (spec §4), so this registry is the only thing that ends
// them; without it graceful shutdown hangs until the deadline expires.
func TestCloseReleasesEveryRegisteredConnection(t *testing.T) {
	server := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	hub := New(rdb)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	first, second := uuid.New(), uuid.New()

	readers := make([]<-chan Frame, 0, 3)
	for _, workspaceID := range []uuid.UUID{first, first, second} {
		frames, err := hub.Attach(ctx, workspaceID, 0)
		if err != nil {
			t.Fatalf("attach: %v", err)
		}
		readers = append(readers, frames)
	}
	if got := hub.Connections(); got != 3 {
		t.Fatalf("Connections()=%d, want 3", got)
	}

	if err := hub.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	for _, frames := range readers {
		expectClosed(t, frames)
	}
	if got := hub.Connections(); got != 0 {
		t.Fatalf("Connections()=%d after Close, want 0", got)
	}
	// Close is idempotent, and a closed hub refuses new connections rather than
	// handing back a channel nothing will ever write to.
	if err := hub.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
	if _, err := hub.Attach(ctx, first, 0); !errors.Is(err, ErrClosed) {
		t.Fatalf("attach after close: err=%v, want ErrClosed", err)
	}
}
