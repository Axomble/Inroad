package agentchat

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

// streamTTL bounds how long a run's chunk log survives. It is a REPLAY buffer,
// not storage: the canonical transcript is Postgres, and the log is deleted the
// moment a run's parts are persisted. The hour is only for the pathological
// case of a run whose process died before it could clean up.
const streamTTL = time.Hour

// streamLogEntries caps the replay buffer. A single long run can emit tens of
// thousands of token deltas; without a bound the list grows for the whole TTL
// on memory that exists only to serve a reconnect. Trimming costs the oldest
// chunks of a very long answer — a reconnecting client sees the tail plus the
// canonical transcript it refetches on message_persisted, never a corrupted
// sequence, because every entry carries its own sequence number.
const streamLogEntries = 5000

// cancelChannel is the single pub/sub channel every API instance listens on for
// stop requests. ONE shared subscriber connection multiplexes every run, rather
// than a connection per run — a workspace with fifty live threads must not cost
// fifty Redis connections per node.
const cancelChannel = "agentcancel"

// streamPattern matches every thread's chunk channel. The same shared
// subscriber that carries cancellations carries chunks: SSE readers attach to
// an in-process fan-out, so a hundred open panels cost one Redis connection,
// not a hundred.
const streamPattern = "agentstream.ch:*"

// subscriberBuffer is how many envelopes a single SSE reader may fall behind
// before it is dropped. A dropped reader's channel is closed, its request ends,
// and the browser reconnects with Last-Event-ID — replaying from the log is the
// designed recovery, and it is strictly better than letting one stalled client
// block the shared dispatch loop for every other reader on the node.
const subscriberBuffer = 256

// StreamPublisher is the write half of the stream, as the run loop sees it.
// Defined here (by the consumer) so the runtime can be tested against an
// in-memory recorder with no Redis.
type StreamPublisher interface {
	// Publish appends an event to the thread's log and fans it out live. The
	// returned sequence number is the event's position in the log.
	Publish(ctx context.Context, threadID uuid.UUID, ev Event) (int64, error)
	// Clear deletes a finished run's log. Called AFTER the terminal event has
	// been published and the parts are in Postgres.
	Clear(ctx context.Context, threadID uuid.UUID) error
}

// Frame is one event as a reader sees it: the payload plus the sequence number
// the SSE handler puts in the `id:` line.
type Frame struct {
	Seq  int64
	Data []byte
}

// publishChunk assigns the event's sequence, appends it to the log, trims and
// arms the log's TTL, and fans the payload out on the thread's pub/sub
// channel — atomically, in one server-side evaluation.
//
// Atomicity is what makes the replay contract hold. The sequence comes from a
// per-thread counter that OUTLIVES the log: Clear deletes a finished run's
// chunks but not the counter, so the next run on the thread keeps counting up
// instead of restarting at 1. That is what stops a client holding
// Last-Event-ID: 20 from silently discarding the next run's first twenty
// events. The stored entry and the published payload are the same
// "<seq>:<json>" envelope, so a reader splices an LRANGE replay onto the live
// feed with no gap (it subscribed first) and no duplicate (it drops anything at
// or below the last sequence it delivered).
var publishChunk = redis.NewScript(`
local seq = redis.call('INCR', KEYS[3])
redis.call('PEXPIRE', KEYS[3], ARGV[2])
local entry = seq .. ':' .. ARGV[1]
redis.call('RPUSH', KEYS[1], entry)
redis.call('LTRIM', KEYS[1], -tonumber(ARGV[3]), -1)
redis.call('PEXPIRE', KEYS[1], ARGV[2])
redis.call('PUBLISH', KEYS[2], entry)
return seq
`)

// RedisStream is the Redis-backed implementation of the chunk transport: the
// publisher the run loop writes to, the reader the SSE handler attaches to, and
// the shared cancel bus that turns a stop request into a context cancellation
// on whichever node is actually running the goroutine.
type RedisStream struct {
	rdb        *redis.Client
	ttl        time.Duration
	logEntries int

	// mu guards every map below plus the shared subscriber's lifecycle.
	mu sync.Mutex
	// cancels maps a live run to its cancel func. The shared subscriber
	// goroutine reads it, the run manager writes it.
	cancels map[uuid.UUID]context.CancelFunc
	// readers maps a thread to the SSE readers currently attached to it on
	// THIS node. The shared subscriber fans one Redis message out to all of
	// them in-process.
	readers  map[uuid.UUID]map[*reader]struct{}
	pubsub   *redis.PubSub
	stopSubs context.CancelFunc
}

// reader is one attached SSE client's slot in the in-process fan-out.
type reader struct {
	threadID uuid.UUID
	envelope chan string
	closed   bool
}

// NewRedisStream dials Redis at addr — the same instance the queue and rate
// limiter use; go-redis pools connections internally, so this is one more
// client on one more code path, not a second Redis dependency.
func NewRedisStream(addr string) *RedisStream {
	return NewRedisStreamWithClient(redis.NewClient(&redis.Options{Addr: addr}))
}

// NewRedisStreamWithClient wraps an existing client (integration tests supply
// one pointed at the test instance).
func NewRedisStreamWithClient(rdb *redis.Client) *RedisStream {
	return &RedisStream{
		rdb: rdb, ttl: streamTTL, logEntries: streamLogEntries,
		cancels: map[uuid.UUID]context.CancelFunc{},
		readers: map[uuid.UUID]map[*reader]struct{}{},
	}
}

func (s *RedisStream) Close() error {
	s.mu.Lock()
	stop, sub := s.stopSubs, s.pubsub
	s.stopSubs, s.pubsub = nil, nil
	// Release every attached reader: with the subscriber gone nothing will ever
	// arrive, so their pumps must end rather than park on a dead channel.
	for threadID, set := range s.readers {
		for rd := range set {
			s.closeLocked(rd)
		}
		delete(s.readers, threadID)
	}
	s.mu.Unlock()
	if stop != nil {
		stop()
	}
	if sub != nil {
		_ = sub.Close()
	}
	return s.rdb.Close()
}

func logKey(threadID uuid.UUID) string     { return "agentstream:" + threadID.String() }
func channelKey(threadID uuid.UUID) string { return "agentstream.ch:" + threadID.String() }
func seqKey(threadID uuid.UUID) string     { return "agentstream.seq:" + threadID.String() }

// threadFromChannel recovers the thread id a pattern message arrived for.
func threadFromChannel(channel string) (uuid.UUID, bool) {
	raw, ok := strings.CutPrefix(channel, "agentstream.ch:")
	if !ok {
		return uuid.Nil, false
	}
	id, err := uuid.Parse(raw)
	return id, err == nil
}

// Publish implements StreamPublisher.
func (s *RedisStream) Publish(ctx context.Context, threadID uuid.UUID, ev Event) (int64, error) {
	payload, err := json.Marshal(ev)
	if err != nil {
		return 0, fmt.Errorf("agentchat: encode event: %w", err)
	}
	seq, err := publishChunk.Run(ctx, s.rdb,
		[]string{logKey(threadID), channelKey(threadID), seqKey(threadID)},
		payload, s.ttl.Milliseconds(), s.logEntries,
	).Int64()
	if err != nil {
		return 0, fmt.Errorf("agentchat: publish chunk: %w", err)
	}
	return seq, nil
}

// Clear implements StreamPublisher. The sequence counter deliberately survives:
// it is the thread's numbering epoch, and resetting it is exactly the bug that
// makes a reconnecting client miss the next run.
func (s *RedisStream) Clear(ctx context.Context, threadID uuid.UUID) error {
	if err := s.rdb.Del(ctx, logKey(threadID)).Err(); err != nil {
		return fmt.Errorf("agentchat: clear stream: %w", err)
	}
	return nil
}

// Attach returns a channel of every event after afterSeq: first the log entries
// already recorded, then the live feed, with no gap and no duplicate.
//
// The ordering is what makes that true. It joins the shared subscriber's
// fan-out BEFORE reading the log, so an event published during the handoff
// lands in this reader's buffer rather than being missed; then it replays the
// log; then it forwards live events, discarding any whose sequence it already
// replayed.
//
// afterSeq is treated as stale — and replay starts from the beginning of the
// log — when it is ahead of the thread's current counter. That happens when the
// counter's TTL lapsed between runs; without the check the client would filter
// out the whole of the next run.
//
// The returned channel is closed when ctx is done, or when this reader falls
// too far behind and is dropped.
func (s *RedisStream) Attach(ctx context.Context, threadID uuid.UUID, afterSeq int64) (<-chan Frame, error) {
	if afterSeq < 0 {
		afterSeq = 0
	}
	if err := s.ensureSubscriber(ctx); err != nil {
		return nil, err
	}
	rd := s.addReader(threadID)

	if afterSeq > 0 {
		current, err := s.rdb.Get(ctx, seqKey(threadID)).Int64()
		if err != nil && !errors.Is(err, redis.Nil) {
			s.removeReader(rd)
			return nil, fmt.Errorf("agentchat: read stream sequence: %w", err)
		}
		if errors.Is(err, redis.Nil) || current < afterSeq {
			afterSeq = 0
		}
	}

	entries, err := s.rdb.LRange(ctx, logKey(threadID), 0, -1).Result()
	if err != nil {
		s.removeReader(rd)
		return nil, fmt.Errorf("agentchat: replay log: %w", err)
	}

	out := make(chan Frame, 64)
	go s.pump(ctx, rd, out, afterSeq, entries)
	return out, nil
}

// pump emits the replayed backlog and then bridges the live feed.
func (s *RedisStream) pump(ctx context.Context, rd *reader, out chan<- Frame, afterSeq int64, backlog []string) {
	defer close(out)
	defer s.removeReader(rd)

	// high is the last sequence already delivered. A live event at or below it
	// was part of the backlog and would be a duplicate.
	high := afterSeq
	for _, entry := range backlog {
		seq, payload, ok := splitEnvelope(entry)
		if !ok || seq <= high {
			continue
		}
		if !send(ctx, out, Frame{Seq: seq, Data: payload}) {
			return
		}
		high = seq
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
			if !send(ctx, out, Frame{Seq: seq, Data: payload}) {
				return
			}
			high = seq
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

// splitEnvelope parses the "<seq>:<json>" wire form the Lua script writes.
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

// ensureSubscriber starts the ONE pub/sub connection this process uses for both
// cancellations and chunk fan-out, if it is not already running. It is
// idempotent, so whichever comes first — the cancellation listener at startup
// or the first SSE attach — pays for it, and every later caller reuses it.
//
// The subscriber outlives the caller's request context on purpose: it is
// process-scoped infrastructure, torn down by Close.
func (s *RedisStream) ensureSubscriber(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pubsub != nil {
		return nil
	}
	subCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	sub := s.rdb.Subscribe(subCtx, cancelChannel)
	if err := sub.PSubscribe(subCtx, streamPattern); err != nil {
		cancel()
		_ = sub.Close()
		return fmt.Errorf("agentchat: subscribe stream pattern: %w", err)
	}
	// One confirmation per subscription. Waiting for both is what guarantees a
	// message published after Attach returns cannot be missed.
	for range 2 {
		if _, err := sub.Receive(subCtx); err != nil {
			cancel()
			_ = sub.Close()
			return fmt.Errorf("agentchat: subscribe: %w", err)
		}
	}
	s.pubsub, s.stopSubs = sub, cancel
	go s.dispatch(subCtx, sub)
	return nil
}

// dispatch is the shared subscriber's loop: one Redis message in, either a
// local run cancellation or an in-process fan-out to that thread's readers.
func (s *RedisStream) dispatch(ctx context.Context, sub *redis.PubSub) {
	messages := sub.Channel(redis.WithChannelSize(subscriberBuffer))
	for {
		select {
		case <-ctx.Done():
			return
		case msg, open := <-messages:
			if !open {
				return
			}
			if msg.Channel == cancelChannel {
				if runID, err := uuid.Parse(msg.Payload); err == nil {
					s.cancelLocal(runID)
				}
				continue
			}
			threadID, ok := threadFromChannel(msg.Channel)
			if !ok {
				continue
			}
			s.deliver(threadID, msg.Payload)
		}
	}
}

func (s *RedisStream) addReader(threadID uuid.UUID) *reader {
	rd := &reader{threadID: threadID, envelope: make(chan string, subscriberBuffer)}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.readers[threadID] == nil {
		s.readers[threadID] = map[*reader]struct{}{}
	}
	s.readers[threadID][rd] = struct{}{}
	return rd
}

func (s *RedisStream) removeReader(rd *reader) {
	s.mu.Lock()
	defer s.mu.Unlock()
	set := s.readers[rd.threadID]
	if _, ok := set[rd]; !ok {
		return
	}
	delete(set, rd)
	if len(set) == 0 {
		delete(s.readers, rd.threadID)
	}
	s.closeLocked(rd)
}

// closeLocked closes a reader's envelope channel exactly once. Callers hold mu.
func (s *RedisStream) closeLocked(rd *reader) {
	if rd.closed {
		return
	}
	rd.closed = true
	close(rd.envelope)
}

// deliver hands one envelope to every reader attached to the thread on this
// node. A reader whose buffer is full is dropped rather than waited on — see
// subscriberBuffer.
func (s *RedisStream) deliver(threadID uuid.UUID, envelope string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for rd := range s.readers[threadID] {
		select {
		case rd.envelope <- envelope:
		default:
			delete(s.readers[threadID], rd)
			s.closeLocked(rd)
		}
	}
	if len(s.readers[threadID]) == 0 {
		delete(s.readers, threadID)
	}
}

// ---- cancellation ----------------------------------------------------------

// RegisterCancel binds a live run to its cancel func and returns the
// deregistration. The map is what a cancel request resolves against; a run id
// that is not in it belongs to another node (or has already finished), which is
// why the request travels over pub/sub rather than being handled locally.
func (s *RedisStream) RegisterCancel(runID uuid.UUID, cancel context.CancelFunc) func() {
	s.mu.Lock()
	s.cancels[runID] = cancel
	s.mu.Unlock()
	return func() {
		s.mu.Lock()
		delete(s.cancels, runID)
		s.mu.Unlock()
	}
}

// RequestCancel broadcasts a stop for runID. Every API instance receives it;
// the one actually running the goroutine cancels its context, the rest ignore
// it. This is why stopping a run works from any node behind a load balancer.
func (s *RedisStream) RequestCancel(ctx context.Context, runID uuid.UUID) error {
	// Cancel immediately when this node owns the run. Redis still broadcasts
	// the request so a different API node can cancel it when ownership is remote.
	s.cancelLocal(runID)
	if err := s.rdb.Publish(ctx, cancelChannel, runID.String()).Err(); err != nil {
		return fmt.Errorf("agentchat: request cancel: %w", err)
	}
	return nil
}

// ListenCancellations starts the shared subscriber (if the first SSE attach has
// not already) and blocks until ctx is done. It is started once per process.
func (s *RedisStream) ListenCancellations(ctx context.Context) error {
	if err := s.ensureSubscriber(ctx); err != nil {
		return err
	}
	<-ctx.Done()
	return nil
}

// cancelLocal fires the cancel func for runID if this process owns it.
func (s *RedisStream) cancelLocal(runID uuid.UUID) {
	s.mu.Lock()
	cancel, ok := s.cancels[runID]
	s.mu.Unlock()
	if ok {
		cancel()
	}
}

// Compile-time proof the Redis transport satisfies the run loop's seam.
var _ StreamPublisher = (*RedisStream)(nil)
