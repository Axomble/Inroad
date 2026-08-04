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

// cancelChannel is the single pub/sub channel every API instance listens on for
// stop requests. ONE shared subscriber connection multiplexes every run, rather
// than a connection per run — a workspace with fifty live threads must not cost
// fifty Redis connections per node.
const cancelChannel = "agentcancel"

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

// publishChunk appends the payload, arms the log's TTL, and fans the payload
// out on the thread's pub/sub channel — atomically, in one server-side
// evaluation.
//
// Atomicity is what makes the replay contract hold. RPUSH's return value is the
// new length, i.e. a monotonic 1-based sequence number, and because the PUBLISH
// happens inside the same script, live subscribers observe events in exactly
// the order and numbering the log records. A reader can therefore splice a
// LRANGE replay onto the live feed with no gap (it subscribed first) and no
// duplicate (it drops anything at or below the last sequence it replayed).
var publishChunk = redis.NewScript(`
local n = redis.call('RPUSH', KEYS[1], ARGV[1])
redis.call('PEXPIRE', KEYS[1], ARGV[2])
redis.call('PUBLISH', KEYS[2], n .. ':' .. ARGV[1])
return n
`)

// RedisStream is the Redis-backed implementation of the chunk transport: the
// publisher the run loop writes to, the reader the SSE handler attaches to, and
// the shared cancel bus that turns a stop request into a context cancellation
// on whichever node is actually running the goroutine.
type RedisStream struct {
	rdb *redis.Client
	ttl time.Duration

	// cancels maps a live run to its cancel func. Guarded by mu; the shared
	// subscriber goroutine reads it, the run manager writes it.
	mu      sync.Mutex
	cancels map[uuid.UUID]context.CancelFunc
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
	return &RedisStream{rdb: rdb, ttl: streamTTL, cancels: map[uuid.UUID]context.CancelFunc{}}
}

func (s *RedisStream) Close() error { return s.rdb.Close() }

func logKey(threadID uuid.UUID) string     { return "agentstream:" + threadID.String() }
func channelKey(threadID uuid.UUID) string { return "agentstream.ch:" + threadID.String() }

// Publish implements StreamPublisher.
func (s *RedisStream) Publish(ctx context.Context, threadID uuid.UUID, ev Event) (int64, error) {
	payload, err := json.Marshal(ev)
	if err != nil {
		return 0, fmt.Errorf("agentchat: encode event: %w", err)
	}
	seq, err := publishChunk.Run(ctx, s.rdb,
		[]string{logKey(threadID), channelKey(threadID)},
		payload, s.ttl.Milliseconds(),
	).Int64()
	if err != nil {
		return 0, fmt.Errorf("agentchat: publish chunk: %w", err)
	}
	return seq, nil
}

// Clear implements StreamPublisher.
func (s *RedisStream) Clear(ctx context.Context, threadID uuid.UUID) error {
	if err := s.rdb.Del(ctx, logKey(threadID)).Err(); err != nil {
		return fmt.Errorf("agentchat: clear stream: %w", err)
	}
	return nil
}

// Attach returns a channel of every event after afterSeq: first the log entries
// already recorded, then the live feed, with no gap and no duplicate.
//
// The ordering is what makes that true. It subscribes and WAITS for the
// subscription to be confirmed before reading the log, so an event published
// during the handoff lands in the pub/sub buffer rather than being missed; then
// it replays the log; then it forwards live events, discarding any whose
// sequence it already replayed.
//
// The returned channel is closed when ctx is done or the subscription drops.
func (s *RedisStream) Attach(ctx context.Context, threadID uuid.UUID, afterSeq int64) (<-chan Frame, error) {
	if afterSeq < 0 {
		afterSeq = 0
	}
	sub := s.rdb.Subscribe(ctx, channelKey(threadID))
	// Receive blocks until Redis acknowledges the SUBSCRIBE. Without it the
	// LRANGE below could run before the subscription is live, opening exactly
	// the gap this design exists to close.
	if _, err := sub.Receive(ctx); err != nil {
		_ = sub.Close()
		return nil, fmt.Errorf("agentchat: subscribe: %w", err)
	}

	// Sequence numbers are 1-based, so the entry numbered afterSeq+1 sits at
	// zero-based index afterSeq.
	backlog, err := s.rdb.LRange(ctx, logKey(threadID), afterSeq, -1).Result()
	if err != nil {
		_ = sub.Close()
		return nil, fmt.Errorf("agentchat: replay log: %w", err)
	}

	out := make(chan Frame, 64)
	go s.pump(ctx, sub, out, afterSeq, backlog)
	return out, nil
}

// pump emits the replayed backlog and then bridges the live feed.
func (s *RedisStream) pump(ctx context.Context, sub *redis.PubSub, out chan<- Frame, afterSeq int64, backlog []string) {
	defer close(out)
	defer func() { _ = sub.Close() }()

	// high is the last sequence already delivered. A live event at or below it
	// was part of the backlog and would be a duplicate.
	high := afterSeq
	for i, payload := range backlog {
		seq := afterSeq + int64(i) + 1
		if !send(ctx, out, Frame{Seq: seq, Data: []byte(payload)}) {
			return
		}
		high = seq
		// A terminal event ends a run, and the run's log is deleted right
		// after — so the NEXT run on this thread starts numbering at 1 again.
		// Resetting the high-water mark here is what lets one long-lived
		// subscription survive across runs instead of silently filtering the
		// next run's entire output as "already seen".
		if isTerminal([]byte(payload)) {
			high = 0
		}
	}

	live := sub.Channel()
	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-live:
			if !ok {
				return
			}
			seq, payload, ok := splitEnvelope(msg.Payload)
			if !ok || seq <= high {
				continue
			}
			if !send(ctx, out, Frame{Seq: seq, Data: payload}) {
				return
			}
			high = seq
			if isTerminal(payload) {
				high = 0
			}
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

// splitEnvelope parses the "<seq>:<json>" wire form the Lua script publishes.
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

// isTerminal reports whether a payload ends a run. It decodes only the type
// field: an unparseable payload is treated as non-terminal, which at worst
// keeps the high-water mark one event too high rather than replaying a run.
func isTerminal(payload []byte) bool {
	var probe struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(payload, &probe); err != nil {
		return false
	}
	return terminalEvents[probe.Type]
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
	if err := s.rdb.Publish(ctx, cancelChannel, runID.String()).Err(); err != nil {
		return fmt.Errorf("agentchat: request cancel: %w", err)
	}
	return nil
}

// ListenCancellations runs the ONE shared subscriber for stop requests. It
// blocks until ctx is done and is started once per process.
func (s *RedisStream) ListenCancellations(ctx context.Context) error {
	sub := s.rdb.Subscribe(ctx, cancelChannel)
	defer func() { _ = sub.Close() }()
	if _, err := sub.Receive(ctx); err != nil {
		return fmt.Errorf("agentchat: subscribe cancel channel: %w", err)
	}
	ch := sub.Channel()
	for {
		select {
		case <-ctx.Done():
			return nil
		case msg, ok := <-ch:
			if !ok {
				return errors.New("agentchat: cancel subscription closed")
			}
			runID, err := uuid.Parse(msg.Payload)
			if err != nil {
				continue
			}
			s.cancelLocal(runID)
		}
	}
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
