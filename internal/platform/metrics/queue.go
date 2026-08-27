package metrics

import (
	"log/slog"

	"github.com/prometheus/client_golang/prometheus"
)

// QueueDepth is one queue's backlog by task state, in the shape this package
// exports it. It is deliberately NOT asynq.QueueInfo: platform/metrics must
// not depend on the queue transport, so the adapter that owns asynq
// (platform/queue) narrows its Inspector down to this. Only the five states
// that describe backlog are carried — archived/completed/aggregating say
// nothing about whether workers are keeping up.
type QueueDepth struct {
	Name      string
	Pending   int
	Active    int
	Scheduled int
	Retry     int
	Dead      int
}

// QueueInspector reports the current backlog of every queue. Satisfied by the
// asynq-backed adapter in platform/queue. Errors are returned, never
// swallowed: a Redis outage must show up as a scrape-time log line rather than
// as a queue that silently reads zero (which looks exactly like "all caught
// up" on a dashboard).
type QueueInspector interface {
	QueueDepths() ([]QueueDepth, error)
}

// queueCollector reports asynq backlog per queue ON SCRAPE. Scrape-time
// reading (rather than a background poller) means the numbers a dashboard
// shows are the numbers at scrape instant, and a failing Redis surfaces as an
// absent series plus a log line instead of a stale one frozen at its last good
// value.
//
// The only label is the queue NAME. Queue names in this deployment are
// bounded: "default" plus one "w:<worker-id>" per worker replica (queue.go's
// per-IP routing), so the series count tracks replica count, not tenant count.
// No workspace/campaign/mailbox label appears here (spec §7 — unbounded
// cardinality).
type queueCollector struct {
	inspector QueueInspector
	logger    *slog.Logger
	depth     *prometheus.Desc
}

// queueStateLabel is the label distinguishing the five backlog states.
const queueStateLabel = "state"

func newQueueCollector(inspector QueueInspector, logger *slog.Logger) *queueCollector {
	return &queueCollector{
		inspector: inspector,
		logger:    logger,
		depth: prometheus.NewDesc(
			"inroad_queue_depth",
			`Tasks in an asynq queue, labeled by queue name and state ("pending"|"active"|"scheduled"|"retry"|"dead").`,
			[]string{"queue", queueStateLabel}, nil,
		),
	}
}

// Describe implements prometheus.Collector.
func (c *queueCollector) Describe(ch chan<- *prometheus.Desc) { ch <- c.depth }

// Collect implements prometheus.Collector.
func (c *queueCollector) Collect(ch chan<- prometheus.Metric) {
	depths, err := c.inspector.QueueDepths()
	if err != nil {
		// Emitting nothing is the honest answer: a series frozen at its last
		// good value would read as "backlog is fine" during exactly the
		// incident an operator needs to see. Prometheus renders the gap, and
		// the log line names the cause.
		c.logger.Error("queue depth scrape failed", "err", err)
		return
	}
	for _, d := range depths {
		for state, n := range map[string]int{
			"pending":   d.Pending,
			"active":    d.Active,
			"scheduled": d.Scheduled,
			"retry":     d.Retry,
			"dead":      d.Dead,
		} {
			ch <- prometheus.MustNewConstMetric(c.depth, prometheus.GaugeValue, float64(n), d.Name, state)
		}
	}
}

// RegisterQueue attaches asynq queue-depth gauges to this Metrics' registry,
// read on scrape through the given inspector. Call it once per process at the
// composition root (the worker), after the queue client exists. logger
// receives scrape-time inspection failures; a nil logger falls back to
// slog.Default so a caller can't accidentally make failures silent.
//
// A nil receiver (or nil inspector — the deployment has no Redis wired) is a
// no-op, matching every other method here.
func (m *Metrics) RegisterQueue(inspector QueueInspector, logger *slog.Logger) error {
	if m == nil || inspector == nil {
		return nil
	}
	if logger == nil {
		logger = slog.Default()
	}
	return m.registry.Register(newQueueCollector(inspector, logger))
}
