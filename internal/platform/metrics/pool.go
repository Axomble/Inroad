package metrics

import (
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
)

// PoolStatter is the narrow seam the pgxpool collector reads through: the one
// method *pgxpool.Pool already satisfies. Declaring it here (consumer side,
// one method) rather than taking a *pgxpool.Pool keeps the collector unit
// testable against a hand-built Stat and keeps metrics from depending on a
// live database.
type PoolStatter interface {
	Stat() *pgxpool.Stat
}

// poolMetric is one gauge/counter derived from a pgxpool.Stat snapshot.
type poolMetric struct {
	desc  *prometheus.Desc
	kind  prometheus.ValueType
	value func(*pgxpool.Stat) float64
}

// poolCollector reports pgxpool saturation ON SCRAPE rather than from a
// background poller: pgxpool.Stat() is a cheap snapshot of counters the pool
// already maintains, so sampling it at scrape time gives Prometheus the value
// as of the scrape instead of as of whenever a ticker last fired — and it
// needs no goroutine, no shutdown seam, and no staleness window.
//
// This is the collector that makes the DB connection budget observable: an
// operator watching inroad_db_pool_acquired_conns approach
// inroad_db_pool_max_conns, or inroad_db_pool_empty_acquire_total climbing,
// sees exhaustion BEFORE pool.Acquire begins blocking. No workspace/tenant
// label is attached anywhere here (nor could one be — the pool is per-process,
// not per-tenant); see the cardinality note on RegisterPool.
type poolCollector struct {
	pool    PoolStatter
	metrics []poolMetric
}

func newPoolCollector(pool PoolStatter) *poolCollector {
	gauge := func(name, help string, value func(*pgxpool.Stat) float64) poolMetric {
		return poolMetric{
			desc:  prometheus.NewDesc(name, help, nil, nil),
			kind:  prometheus.GaugeValue,
			value: value,
		}
	}
	counter := func(name, help string, value func(*pgxpool.Stat) float64) poolMetric {
		return poolMetric{
			desc:  prometheus.NewDesc(name, help, nil, nil),
			kind:  prometheus.CounterValue,
			value: value,
		}
	}
	return &poolCollector{pool: pool, metrics: []poolMetric{
		gauge("inroad_db_pool_acquired_conns", "Connections currently checked out of the pgx pool.",
			func(s *pgxpool.Stat) float64 { return float64(s.AcquiredConns()) }),
		gauge("inroad_db_pool_idle_conns", "Idle connections available in the pgx pool.",
			func(s *pgxpool.Stat) float64 { return float64(s.IdleConns()) }),
		gauge("inroad_db_pool_total_conns", "Total connections currently in the pgx pool (idle + acquired + constructing).",
			func(s *pgxpool.Stat) float64 { return float64(s.TotalConns()) }),
		gauge("inroad_db_pool_max_conns", "Configured maximum size of the pgx pool (INROAD_DB_MAX_CONNS).",
			func(s *pgxpool.Stat) float64 { return float64(s.MaxConns()) }),
		counter("inroad_db_pool_acquire_total", "Cumulative successful connection acquisitions from the pgx pool.",
			func(s *pgxpool.Stat) float64 { return float64(s.AcquireCount()) }),
		counter("inroad_db_pool_acquire_seconds_total", "Cumulative time spent waiting for connection acquisitions from the pgx pool.",
			func(s *pgxpool.Stat) float64 { return s.AcquireDuration().Seconds() }),
		counter("inroad_db_pool_empty_acquire_total", "Cumulative acquisitions that had to wait because the pgx pool was empty — the leading indicator of budget exhaustion.",
			func(s *pgxpool.Stat) float64 { return float64(s.EmptyAcquireCount()) }),
		counter("inroad_db_pool_canceled_acquire_total", "Cumulative acquisitions aborted by context cancellation while waiting for a pgx pool connection.",
			func(s *pgxpool.Stat) float64 { return float64(s.CanceledAcquireCount()) }),
	}}
}

// Describe implements prometheus.Collector.
func (c *poolCollector) Describe(ch chan<- *prometheus.Desc) {
	for _, m := range c.metrics {
		ch <- m.desc
	}
}

// Collect implements prometheus.Collector, taking ONE Stat snapshot and
// deriving every series from it so all the values a scrape reports are
// mutually consistent (acquired + idle summing against total, rather than
// straddling two snapshots taken microseconds apart).
func (c *poolCollector) Collect(ch chan<- prometheus.Metric) {
	stat := c.pool.Stat()
	if stat == nil {
		return
	}
	for _, m := range c.metrics {
		ch <- prometheus.MustNewConstMetric(m.desc, m.kind, m.value(stat))
	}
}

// RegisterPool attaches a pgx pool's saturation stats to this Metrics'
// registry, read on scrape. Call it once per process, at the composition root,
// right after the pool is built.
//
// Deliberately carries NO workspace/campaign/mailbox label: the pool is a
// process-wide resource, and per-tenant Prometheus labels are unbounded
// cardinality (spec §7). Per-workspace behaviour belongs in logs and the DB.
//
// A nil receiver is a no-op (metrics disabled), matching every other method
// here, so a caller needs no nil check. Registration errors are returned
// rather than panicking: this runs during startup where the caller can decide,
// and calling it twice on one registry is a programming error worth surfacing
// loudly rather than a reason to abort the process from inside a metrics
// helper.
func (m *Metrics) RegisterPool(pool PoolStatter) error {
	if m == nil || pool == nil {
		return nil
	}
	return m.registry.Register(newPoolCollector(pool))
}
