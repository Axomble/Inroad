// Package bus is the transport seam: a small interface expressing delivery
// INTENT (what guarantee the domain wants), not the mechanics of any one broker.
//
// Domain and worker code depends on these interfaces, never on asynq directly,
// so a future Kafka/NATS transport can provide the same guarantees its own way
// without touching callers. The only shipped implementation today is
// internal/platform/bus/redisbus (asynq over Redis).
//
// Honest limitation: delayed delivery (At/In), dedup (Key), and bounded retries
// (MaxRetry) are native to asynq/Redis. A non-Redis transport must supply them
// itself (delay queues, a dedup store, a scheduler topic). The seam names the
// guarantee; each transport earns it.
package bus

import (
	"context"
	"time"
)

// Job is a transport-neutral unit of work.
type Job struct {
	Kind    string // task type, e.g. "warmup:tick"
	Payload []byte // opaque, JSON-encoded domain payload
	Key     string // dedup / idempotency key ("" = none)
	Dest    string // routing destination; "" = default/shared queue
}

// Options are the delivery guarantees the domain asks for. All fields are
// optional; a zero Options publishes for immediate delivery with the transport's
// default retry policy.
type Options struct {
	At       time.Time     // deliver at (zero = now); takes precedence over In
	In       time.Duration // or deliver after (0 = now)
	MaxRetry int           // 0 = transport default
	// Timeout bounds ONE handler attempt: past it the transport cancels the
	// handler's context and the job becomes eligible for retry. 0 = transport
	// default. It is a ceiling on an attempt, not on the work overall — a job
	// that legitimately needs longer should be split, not given a wider ceiling.
	Timeout time.Duration
}

// Dispatcher publishes jobs. redisbus (asynq) is the only impl in v1.
type Dispatcher interface {
	Publish(ctx context.Context, j Job, o Options) error
}

// PeriodicScheduler registers recurring jobs (transport-specific under the hood).
type PeriodicScheduler interface {
	RegisterPeriodic(spec, kind string) error
}
