package queue

import (
	"errors"
	"fmt"

	"github.com/hibiken/asynq"

	"github.com/inroad/inroad/internal/platform/metrics"
)

// Inspector reads queue backlog out of Redis. It is the ONLY use of asynq's
// Inspector in this codebase, and it is deliberately READ-ONLY: nothing here
// deletes, archives or cancels a task. That restraint is load-bearing, not
// stylistic — the deferred-send design rests on "undo is a row status flip the
// handler re-reads", NOT a queue mutation, and an Inspector able to delete
// tasks is precisely the shortcut that would quietly break it. Depth is the
// only thing worth reading out of the queue.
//
// It exists so platform/metrics can report queue depth without importing
// asynq: metrics declares the one-method metrics.QueueInspector seam, and this
// adapter is what satisfies it.
type Inspector struct {
	inner *asynq.Inspector
}

// NewInspector opens a read-only inspector against the same Redis the worker
// consumes from.
func NewInspector(redisAddr string) *Inspector {
	return &Inspector{inner: asynq.NewInspector(asynq.RedisClientOpt{Addr: redisAddr})}
}

// Close releases the inspector's Redis connection.
func (i *Inspector) Close() error { return i.inner.Close() }

// QueueDepths implements metrics.QueueInspector: one QueueDepth per queue
// asynq currently knows about, discovered rather than configured so a
// per-worker "w:<id>" queue (spec §15 per-IP routing) shows up without the
// scraping process having to be told the replica list.
//
// "Dead" maps to asynq's Archived — the terminal state a task lands in after
// exhausting its retries. Retrieval is per-queue, so one queue disappearing
// between Queues() and GetQueueInfo() (an operator deleting it) would
// otherwise fail the whole scrape; asynq reports that as ErrQueueNotFound and
// it is skipped rather than propagated, since a queue that no longer exists
// has no depth to report and is not an inspection failure. Every other error
// IS propagated: metrics renders a gap and logs, rather than reporting a
// misleading partial backlog.
func (i *Inspector) QueueDepths() ([]metrics.QueueDepth, error) {
	names, err := i.inner.Queues()
	if err != nil {
		return nil, fmt.Errorf("list queues: %w", err)
	}
	depths := make([]metrics.QueueDepth, 0, len(names))
	for _, name := range names {
		info, err := i.inner.GetQueueInfo(name)
		if err != nil {
			if errors.Is(err, asynq.ErrQueueNotFound) {
				continue
			}
			return nil, fmt.Errorf("queue %q info: %w", name, err)
		}
		depths = append(depths, queueDepthOf(info))
	}
	return depths, nil
}

// queueDepthOf narrows one asynq.QueueInfo to the backlog states metrics
// exports. Split out from QueueDepths so the mapping — the part that can be
// silently wrong, e.g. Retry and Scheduled transposed — is unit-testable
// without a live Redis.
func queueDepthOf(info *asynq.QueueInfo) metrics.QueueDepth {
	return metrics.QueueDepth{
		Name:      info.Queue,
		Pending:   info.Pending,
		Active:    info.Active,
		Scheduled: info.Scheduled,
		Retry:     info.Retry,
		// asynq's terminal post-retry-exhaustion state is Archived; "dead"
		// is the label operators know it by (and what DeadLetterErrorHandler
		// already calls it here).
		Dead: info.Archived,
	}
}
