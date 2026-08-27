// Package realtime is the workspace-scoped realtime fan-out hub: Redis
// pub/sub with monotonic per-workspace sequence numbers, a bounded replay log,
// gap-free reconnect, in-process fan-out to many readers over ONE Redis
// subscription, and a registry of live connections closed on shutdown.
//
// It is deliberately transport-agnostic. The hub moves Envelopes; it does not
// know what a WebSocket is, imports no socket library, and would serve SSE or
// long-poll unchanged. The Upgrade, the Origin check and the ping/pong
// keepalive belong to the HTTP layer (spec §3, §4), which registers its
// connection here so shutdown can close it — Go's http.Server explicitly does
// not track hijacked connections, so srv.Shutdown never will.
//
// The design mirrors internal/app/agentchat/stream.go, which is the working,
// tested precedent for every hard part of this (atomic seq+log+publish in one
// Lua evaluation, subscribe-before-replay, drop-the-slow-reader). Key names and
// scoping differ — workspace rather than thread — and the payload is a typed
// Envelope rather than a chunk. See docs/superpowers/specs/realtime-websocket.md.
//
// Security (spec §7): every entry point takes the workspace id as an explicit
// uuid.UUID parameter, which callers must source from a verified wsticket. There
// is no API here that discovers, enumerates or defaults a workspace, so a
// client cannot request one. Envelope payloads must carry ids and minimal
// display fields only — never full records, never recipient PII.
package realtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// replayTTL and replayEntries bound the replay window (spec §9): a client gone
// longer than replayTTL, or further behind than replayEntries events, does a
// FULL REFETCH through the normal authorized REST endpoints rather than a
// replay. That is the deliberate choice — the log exists to make a five-second
// tab-switch or a Wi-Fi blip invisible, not to be an event store. Postgres
// remains the source of truth, and a refetch is always correct, so the window
// only trades a little bandwidth on rare long absences for a small, bounded
// memory cost per workspace.
//
// A truncated replay is never a silent corruption: every entry carries its own
// sequence number, so a reader that receives seq 900 having last seen 200 can
// see the gap and refetch. StaleReplay on the reader's first frame says so
// explicitly.
const (
	replayTTL     = 5 * time.Minute
	replayEntries = 200
)

// subscriberBuffer is how many envelopes one reader may fall behind before it
// is dropped. Its channel is closed, its connection ends, and the client
// reconnects with its last sequence — replay is the designed recovery, and it
// is strictly better than one stalled tab blocking dispatch for every other
// connection on the node.
const subscriberBuffer = 256

// channelPrefix is the per-workspace pub/sub channel (spec §5). Channels are
// workspace-scoped with the subject as an envelope FIELD rather than its own
// channel, so a workspace with fifty open tabs costs one Redis subscription per
// node, not fifty: this process holds a single PSubscribe on channelPattern and
// fans out in-process.
const channelPrefix = "ws.ch:"

// channelPattern matches every workspace's channel. One PSubscribe per process.
const channelPattern = channelPrefix + "*"

// Subject identifies what an event is about. It is a field of the envelope
// rather than part of the channel key so that adding a subject kind needs no
// transport change; connections filter client-side (spec §9).
type Subject struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

// Envelope is one realtime event exactly as the wire carries it (spec §5).
//
// Type is a string and Data is opaque JSON on purpose: a new event type needs a
// new client handler and nothing else — no change to this package, no schema
// migration of the transport.
type Envelope struct {
	// Seq is monotonic per workspace and drives replay. It is assigned by the
	// hub, not the caller: Publish ignores whatever is here and the published
	// body carries the sequence the atomic script allocated.
	Seq int64 `json:"seq"`
	// Type is the dotted event name, e.g. "inbox.message.created".
	Type string `json:"type"`
	// Subject is what the event is about.
	Subject Subject `json:"subject"`
	// At is the event time, RFC3339 on the wire.
	At time.Time `json:"at"`
	// ActorID is the user who caused the event, empty for system-originated
	// events. The client DROPS events it originated (spec §6): without this an
	// optimistic RTK Query patch fights the realtime echo of the actor's own
	// action and the UI visibly snaps back and forth.
	//
	// snake_case, not the spec §5 sketch's `actorId`: every JSON boundary in this
	// repo is snake_case (CLAUDE.md), and the two camelCase tags that exist are
	// foreign API shapes (WebAuthn, Microsoft Graph). The client parses
	// `actor_id`, so a camelCase tag here would silently disable the self-echo
	// guard — the field would simply always be absent.
	ActorID string `json:"actor_id,omitempty"`
	// Data is type-specific and MINIMAL — ids and display fields only. A socket
	// event must not become an exfiltration path around the permission checks
	// the REST endpoints apply (spec §7.2), and carries no recipient PII (§7.3).
	Data json.RawMessage `json:"data,omitempty"`
}

// Frame is one envelope as a reader sees it: the sequence, so the connection
// can report its position on reconnect, and the encoded payload, so the
// transport writes bytes without re-marshalling per connection.
type Frame struct {
	Seq  int64
	Data []byte
	// StaleReplay is true on the FIRST frame a reader receives when the hub
	// could not honour its requested position — the log had been trimmed or had
	// expired past it. The client must do a full refetch instead of trusting
	// that it has seen everything in between. Silently delivering a gap is the
	// one failure this window must not cause.
	StaleReplay bool
}

// Publisher is the write half of the hub as an emitting domain sees it.
// Declared here, at the seam, so services and the coreapi client can be tested
// against an in-memory recorder with no Redis (dependency inversion — callers
// depend on this interface, never on *Hub).
type Publisher interface {
	// Publish appends ev to the workspace's replay log and fans it out live,
	// returning the sequence it was assigned. workspaceID must come from
	// server-side state, never from client input.
	Publish(ctx context.Context, workspaceID uuid.UUID, ev Envelope) (int64, error)
}

// publishEvent assigns the envelope's sequence, appends it to the replay log,
// trims it, arms the TTL on both keys, and fans the payload out on the
// workspace's channel — atomically, in one server-side evaluation.
//
// Atomicity is what makes the replay contract hold: the stored entry and the
// published message are the same "<seq>:<json>" string, so a reader splices an
// LRANGE replay onto the live feed with no gap (it subscribed first) and no
// duplicate (it drops anything at or below the last sequence it delivered).
//
// The script also splices the sequence INTO the JSON body, because the browser
// reads the body, not the log framing — a body claiming seq 0 while the frame
// says 41 would make the client's reconnect position wrong. ARGV[1] is the
// envelope's encoding with its `{"seq":0,` prefix already cut by the Go caller
// (encodeBody), so this is a string concatenation rather than JSON manipulation
// in Lua. Doing it here rather than in Go is what
// keeps "assign the sequence" and "emit the payload carrying it" one operation.
//
// The sequence counter shares the log's TTL. A workspace idle for the whole
// window loses both together, which is why Attach treats a requested position
// ahead of the counter as stale rather than filtering out everything after it.
var publishEvent = redis.NewScript(`
local seq = redis.call('INCR', KEYS[3])
redis.call('PEXPIRE', KEYS[3], ARGV[2])
local entry = seq .. ':{"seq":' .. seq .. ',' .. ARGV[1]
redis.call('RPUSH', KEYS[1], entry)
redis.call('LTRIM', KEYS[1], -tonumber(ARGV[3]), -1)
redis.call('PEXPIRE', KEYS[1], ARGV[2])
redis.call('PUBLISH', KEYS[2], entry)
return seq
`)

// Hub is the Redis-backed realtime fan-out for one process.
type Hub struct {
	rdb        *redis.Client
	ttl        time.Duration
	logEntries int

	// mu guards every map below plus the shared subscriber's lifecycle.
	mu sync.Mutex
	// readers maps a workspace to the connections attached to it on THIS node.
	// The shared subscriber fans one Redis message out to all of them.
	readers  map[uuid.UUID]map[*reader]struct{}
	pubsub   *redis.PubSub
	stopSubs context.CancelFunc
	closed   bool
}

// reader is one attached connection's slot in the in-process fan-out.
type reader struct {
	workspaceID uuid.UUID
	envelope    chan string
	closed      bool
}

// New wraps an EXISTING Redis client. The hub deliberately does not dial: the
// API process already has a client (the queue, the rate limiter and agentchat
// share the instance), and go-redis pools connections internally, so this is
// one more code path on one Redis dependency rather than a second one.
func New(rdb *redis.Client) *Hub {
	return &Hub{
		rdb:        rdb,
		ttl:        replayTTL,
		logEntries: replayEntries,
		readers:    map[uuid.UUID]map[*reader]struct{}{},
	}
}

// Close tears down the shared subscriber and releases every attached reader.
// It does NOT close the Redis client, which the hub borrowed and does not own.
//
// This is the registry half of spec §4: http.Server.Shutdown does not close
// hijacked connections, so the transport must be told to stop some other way.
// Closing a reader's channel ends its pump, which ends the write loop that owns
// the socket. Callers close the hub BEFORE Shutdown.
func (h *Hub) Close() error {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return nil
	}
	h.closed = true
	stop, sub := h.stopSubs, h.pubsub
	h.stopSubs, h.pubsub = nil, nil
	// With the subscriber gone nothing will ever arrive again, so every pump
	// must end rather than park forever on a dead channel.
	for workspaceID, set := range h.readers {
		for rd := range set {
			h.closeLocked(rd)
		}
		delete(h.readers, workspaceID)
	}
	h.mu.Unlock()

	if stop != nil {
		stop()
	}
	if sub != nil {
		if err := sub.Close(); err != nil {
			return fmt.Errorf("realtime: close subscriber: %w", err)
		}
	}
	return nil
}

// Connections reports how many readers are attached on this node. It backs the
// per-user / per-workspace connection caps (spec §7.5) and shutdown assertions.
func (h *Hub) Connections() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	total := 0
	for _, set := range h.readers {
		total += len(set)
	}
	return total
}

func logKey(workspaceID uuid.UUID) string     { return "ws.log:" + workspaceID.String() }
func channelKey(workspaceID uuid.UUID) string { return channelPrefix + workspaceID.String() }
func seqKey(workspaceID uuid.UUID) string     { return "ws.seq:" + workspaceID.String() }

// workspaceFromChannel recovers the workspace a pattern message arrived for.
// A channel whose suffix is not a UUID is ignored rather than guessed at: it is
// the ONLY place a workspace id is derived from a string, and delivering to the
// wrong tenant is the worst bug this package could have.
func workspaceFromChannel(channel string) (uuid.UUID, bool) {
	raw, ok := strings.CutPrefix(channel, channelPrefix)
	if !ok {
		return uuid.Nil, false
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		return uuid.Nil, false
	}
	return id, true
}

// Publish implements Publisher.
func (h *Hub) Publish(ctx context.Context, workspaceID uuid.UUID, ev Envelope) (int64, error) {
	if workspaceID == uuid.Nil {
		return 0, ErrNoWorkspace
	}
	if ev.Type == "" {
		return 0, ErrNoType
	}
	if ev.At.IsZero() {
		ev.At = time.Now().UTC()
	}
	payload, err := encodeBody(ev)
	if err != nil {
		return 0, err
	}
	seq, err := publishEvent.Run(ctx, h.rdb,
		[]string{logKey(workspaceID), channelKey(workspaceID), seqKey(workspaceID)},
		payload, h.ttl.Milliseconds(), h.logEntries,
	).Int64()
	if err != nil {
		return 0, fmt.Errorf("realtime: publish envelope: %w", err)
	}
	return seq, nil
}

// encodeBody returns the envelope's JSON encoding with its leading `{"seq":0,`
// removed, so publishEvent's Lua can prepend the sequence it actually assigns:
// `{"seq":41,` + body.
//
// It marshals the whole Envelope with Seq forced to zero and then cuts the
// resulting `{"seq":0,` prefix, rather than declaring a near-copy of the struct
// without the field: a second struct is how a wire form silently drifts from
// the type its emitters build. The cut is verified against seqPrefix and
// returns an error if the encoding ever stops starting that way — a field
// reorder must fail loudly here, not corrupt every payload.
func encodeBody(ev Envelope) (string, error) {
	ev.Seq = 0
	raw, err := json.Marshal(ev)
	if err != nil {
		return "", fmt.Errorf("realtime: encode envelope: %w", err)
	}
	body, ok := strings.CutPrefix(string(raw), seqPrefix)
	if !ok {
		return "", fmt.Errorf("realtime: encode envelope: encoding does not begin %s: %q", seqPrefix, raw)
	}
	return body, nil
}

// seqPrefix is how a zero-Seq envelope encodes: Seq is the first field of the
// struct, so encoding/json emits it first.
const seqPrefix = `{"seq":0,`

// Errors a caller can act on. A missing workspace is a programming error at the
// security boundary, so it is a distinct, loud value rather than a wrapped
// Redis failure.
var (
	ErrNoWorkspace = errors.New("realtime: workspace id required")
	ErrNoType      = errors.New("realtime: envelope type required")
	ErrClosed      = errors.New("realtime: hub closed")
)

// Attach registers a connection for workspaceID and returns every envelope
// after afterSeq: first what the replay log already holds, then the live feed,
// with no gap and no duplicate.
//
// The ordering is what makes that true. It joins the shared subscriber's
// fan-out BEFORE reading the log, so an event published during the handoff
// lands in this reader's buffer instead of being missed; then it replays the
// log; then it forwards live events, discarding any sequence it already
// delivered.
//
// afterSeq of 0 means "I have nothing" — send the whole window. A position
// AHEAD of the workspace's counter is treated as stale (the counter's TTL
// lapsed), and so is a position older than the trimmed log; both replay from
// the start of the window with StaleReplay set on the first frame so the client
// refetches rather than assuming continuity.
//
// The returned channel is closed when ctx is done, when the reader falls too
// far behind, or when the hub is closed.
func (h *Hub) Attach(ctx context.Context, workspaceID uuid.UUID, afterSeq int64) (<-chan Frame, error) {
	if workspaceID == uuid.Nil {
		return nil, ErrNoWorkspace
	}
	if afterSeq < 0 {
		afterSeq = 0
	}
	if err := h.ensureSubscriber(ctx); err != nil {
		return nil, err
	}
	rd, err := h.addReader(workspaceID)
	if err != nil {
		return nil, err
	}

	stale := false
	if afterSeq > 0 {
		current, err := h.rdb.Get(ctx, seqKey(workspaceID)).Int64()
		if err != nil && !errors.Is(err, redis.Nil) {
			h.removeReader(rd)
			return nil, fmt.Errorf("realtime: read workspace sequence: %w", err)
		}
		if errors.Is(err, redis.Nil) || current < afterSeq {
			afterSeq, stale = 0, true
		}
	}

	entries, err := h.rdb.LRange(ctx, logKey(workspaceID), 0, -1).Result()
	if err != nil {
		h.removeReader(rd)
		return nil, fmt.Errorf("realtime: replay log: %w", err)
	}

	out := make(chan Frame, 64)
	go h.pump(ctx, rd, out, afterSeq, stale, entries)
	return out, nil
}

// pump emits the replayed backlog and then bridges the live feed.
func (h *Hub) pump(ctx context.Context, rd *reader, out chan<- Frame, afterSeq int64, stale bool, backlog []string) {
	defer close(out)
	defer h.removeReader(rd)

	// high is the last sequence already delivered; a live event at or below it
	// was part of the backlog and would be a duplicate.
	high := afterSeq
	first := true
	for _, entry := range backlog {
		seq, payload, ok := splitEnvelope(entry)
		if !ok || seq <= high {
			continue
		}
		// The window has been trimmed past where the client was: the first frame
		// it gets is not the one after its last, so say so.
		if first && afterSeq > 0 && seq > afterSeq+1 {
			stale = true
		}
		if !send(ctx, out, Frame{Seq: seq, Data: payload, StaleReplay: stale && first}) {
			return
		}
		high, first = seq, false
	}

	for {
		select {
		case <-ctx.Done():
			return
		case entry, open := <-rd.envelope:
			if !open {
				return
			}
			seq, payload, ok := splitEnvelope(entry)
			if !ok || seq <= high {
				continue
			}
			if first && afterSeq > 0 && seq > afterSeq+1 {
				stale = true
			}
			if !send(ctx, out, Frame{Seq: seq, Data: payload, StaleReplay: stale && first}) {
				return
			}
			high, first = seq, false
		}
	}
}

// send delivers a frame unless the reader has gone away.
func send(ctx context.Context, out chan<- Frame, f Frame) bool {
	select {
	case out <- f:
		return true
	case <-ctx.Done():
		return false
	}
}

// splitEnvelope parses the "<seq>:<json>" wire form the Lua script writes. The
// JSON body may itself contain colons, so this splits on the FIRST one only.
func splitEnvelope(raw string) (int64, []byte, bool) {
	i := strings.IndexByte(raw, ':')
	if i <= 0 {
		return 0, nil, false
	}
	seq, err := strconv.ParseInt(raw[:i], 10, 64)
	if err != nil {
		return 0, nil, false
	}
	return seq, []byte(raw[i+1:]), true
}

// ---- shared subscriber -----------------------------------------------------

// ensureSubscriber starts the ONE pub/sub connection this process uses for
// realtime fan-out, if it is not already running. Idempotent: the first Attach
// pays for it and every later one reuses it.
//
// The subscriber outlives the attaching request's context on purpose — it is
// process-scoped infrastructure, torn down only by Close. A per-request context
// would kill the fan-out for every other connection when one tab closed.
func (h *Hub) ensureSubscriber(ctx context.Context) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return ErrClosed
	}
	if h.pubsub != nil {
		return nil
	}
	subCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	sub := h.rdb.PSubscribe(subCtx, channelPattern)
	// Wait for the subscription confirmation: that is what guarantees an event
	// published after Attach returns cannot be missed.
	if _, err := sub.Receive(subCtx); err != nil {
		cancel()
		_ = sub.Close()
		return fmt.Errorf("realtime: subscribe %s: %w", channelPattern, err)
	}
	h.pubsub, h.stopSubs = sub, cancel
	go h.dispatch(subCtx, sub)
	return nil
}

// dispatch is the shared subscriber's loop: one Redis message in, one
// in-process fan-out to that workspace's readers.
func (h *Hub) dispatch(ctx context.Context, sub *redis.PubSub) {
	messages := sub.Channel(redis.WithChannelSize(subscriberBuffer))
	for {
		select {
		case <-ctx.Done():
			return
		case msg, open := <-messages:
			if !open {
				return
			}
			workspaceID, ok := workspaceFromChannel(msg.Channel)
			if !ok {
				continue
			}
			h.deliver(workspaceID, msg.Payload)
		}
	}
}

func (h *Hub) addReader(workspaceID uuid.UUID) (*reader, error) {
	rd := &reader{workspaceID: workspaceID, envelope: make(chan string, subscriberBuffer)}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return nil, ErrClosed
	}
	if h.readers[workspaceID] == nil {
		h.readers[workspaceID] = map[*reader]struct{}{}
	}
	h.readers[workspaceID][rd] = struct{}{}
	return rd, nil
}

func (h *Hub) removeReader(rd *reader) {
	h.mu.Lock()
	defer h.mu.Unlock()
	set := h.readers[rd.workspaceID]
	if _, ok := set[rd]; !ok {
		return
	}
	delete(set, rd)
	if len(set) == 0 {
		delete(h.readers, rd.workspaceID)
	}
	h.closeLocked(rd)
}

// closeLocked closes a reader's envelope channel exactly once. Callers hold mu.
func (h *Hub) closeLocked(rd *reader) {
	if rd.closed {
		return
	}
	rd.closed = true
	close(rd.envelope)
}

// deliver hands one envelope to every reader attached to THAT workspace on this
// node — and to no other workspace's readers, which is the tenant boundary this
// package exists to hold. A reader whose buffer is full is dropped rather than
// waited on (see subscriberBuffer).
func (h *Hub) deliver(workspaceID uuid.UUID, envelope string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for rd := range h.readers[workspaceID] {
		select {
		case rd.envelope <- envelope:
		default:
			delete(h.readers[workspaceID], rd)
			h.closeLocked(rd)
		}
	}
	if len(h.readers[workspaceID]) == 0 {
		delete(h.readers, workspaceID)
	}
}

// Compile-time proof the Redis hub satisfies the emitting side's seam.
var _ Publisher = (*Hub)(nil)
