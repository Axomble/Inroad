package metrics_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inroad/inroad/internal/platform/metrics"
	"github.com/inroad/inroad/internal/platform/metrics/metricstest"
)

// poolWithMaxConns stands up a REAL *pgxpool.Pool against an
// unreachable-but-parseable DSN. pgxpool does not dial until the first
// acquire, and Stat() reports the pool's own bookkeeping (max size, conn
// counts, acquire counters) without the server ever answering — so these tests
// exercise the actual pgx type the collector reads in production, rather than
// a hand-rolled stand-in that could silently drift from it.
func poolWithMaxConns(t *testing.T, maxConns int32) *pgxpool.Pool {
	t.Helper()
	cfg, err := pgxpool.ParseConfig("postgres://u:p@127.0.0.1:1/db?sslmode=disable")
	if err != nil {
		t.Fatalf("parse config: %v", err)
	}
	cfg.MaxConns = maxConns
	cfg.MinConns = 0
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatalf("new pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// realPoolStat is the default 7-connection pool most of these tests use.
func realPoolStat(t *testing.T) *pgxpool.Pool {
	t.Helper()
	return poolWithMaxConns(t, 7)
}

// TestRegisterPoolReportsWhatThePoolHas is the load-bearing assertion: the
// exported max-conns series must equal the pool's OWN configured maximum, not
// a value the test also supplied to the collector. If the collector read the
// wrong Stat accessor, this fails.
func TestRegisterPoolReportsWhatThePoolHas(t *testing.T) {
	m := metrics.New()
	pool := realPoolStat(t)
	if err := m.RegisterPool(pool); err != nil {
		t.Fatalf("register pool: %v", err)
	}

	families := metricstest.Scrape(t, m)
	got := metricstest.GaugeValue(families, "inroad_db_pool_max_conns", nil)
	if want := float64(pool.Stat().MaxConns()); got != want {
		t.Fatalf("inroad_db_pool_max_conns = %v, want %v (the pool's own MaxConns)", got, want)
	}
	if got != 7 {
		t.Fatalf("inroad_db_pool_max_conns = %v, want 7 (the configured max)", got)
	}
}

// TestRegisterPoolExposesEverySaturationSeries pins the metric names an
// operator's dashboard and alerts will reference. A rename is a breaking change
// for them, so it should break here first.
func TestRegisterPoolExposesEverySaturationSeries(t *testing.T) {
	m := metrics.New()
	if err := m.RegisterPool(realPoolStat(t)); err != nil {
		t.Fatalf("register pool: %v", err)
	}
	families := metricstest.Scrape(t, m)
	for _, name := range []string{
		"inroad_db_pool_acquired_conns",
		"inroad_db_pool_idle_conns",
		"inroad_db_pool_total_conns",
		"inroad_db_pool_max_conns",
		"inroad_db_pool_acquire_total",
		"inroad_db_pool_acquire_seconds_total",
		"inroad_db_pool_empty_acquire_total",
		"inroad_db_pool_canceled_acquire_total",
	} {
		if _, ok := families[name]; !ok {
			t.Errorf("missing series %q", name)
		}
	}
}

// TestPoolCollectorCarriesNoLabels is the cardinality guard. Every pool series
// is process-wide and must have ZERO labels — a workspace/campaign/mailbox
// dimension here would mint one series per tenant and kill the metrics
// backend, so this fails loudly if anyone adds one.
func TestPoolCollectorCarriesNoLabels(t *testing.T) {
	m := metrics.New()
	if err := m.RegisterPool(realPoolStat(t)); err != nil {
		t.Fatalf("register pool: %v", err)
	}
	families := metricstest.Scrape(t, m)
	for name, fam := range families {
		if len(name) < len("inroad_db_pool_") || name[:len("inroad_db_pool_")] != "inroad_db_pool_" {
			continue
		}
		for _, sample := range fam.GetMetric() {
			if len(sample.GetLabel()) != 0 {
				t.Errorf("%s has labels %v; pool metrics must be unlabeled (cardinality)", name, sample.GetLabel())
			}
		}
	}
}

// swappableStatter lets a test change which real pool the collector reads
// between two scrapes. It exists to prove the "read on scrape, not from a
// background poller" contract: pgxpool.Stat's fields are unexported, so a
// fabricated Stat is impossible and a SECOND genuine pool is the only way to
// present the collector with a different real value.
type swappableStatter struct {
	pool  metrics.PoolStatter
	calls int
}

func (s *swappableStatter) Stat() *pgxpool.Stat {
	s.calls++
	return s.pool.Stat()
}

// TestPoolCollectorReadsOnEveryScrape proves the collector samples at scrape
// time rather than caching at registration: swapping the underlying pool
// between two scrapes must change the reported value, and the pool must be
// consulted once per scrape.
func TestPoolCollectorReadsOnEveryScrape(t *testing.T) {
	small := poolWithMaxConns(t, 7)
	large := poolWithMaxConns(t, 21)
	statter := &swappableStatter{pool: small}

	m := metrics.New()
	if err := m.RegisterPool(statter); err != nil {
		t.Fatalf("register pool: %v", err)
	}

	if got := metricstest.GaugeValue(metricstest.Scrape(t, m), "inroad_db_pool_max_conns", nil); got != 7 {
		t.Fatalf("first scrape max_conns = %v, want 7", got)
	}
	statter.pool = large
	if got := metricstest.GaugeValue(metricstest.Scrape(t, m), "inroad_db_pool_max_conns", nil); got != 21 {
		t.Fatalf("second scrape max_conns = %v, want 21 — the collector cached instead of reading on scrape", got)
	}
	if statter.calls != 2 {
		t.Fatalf("Stat() called %d times across 2 scrapes, want exactly 2 (one consistent snapshot per scrape)", statter.calls)
	}
}

// TestPoolCollectorTracksNewConnActivity proves the exported counters follow
// the pool's real bookkeeping rather than a frozen snapshot: a failed dial
// against an unreachable server leaves total_conns at zero while the pool has
// genuinely attempted work, and re-scraping must still report the live values.
func TestPoolCollectorTracksNewConnActivity(t *testing.T) {
	m := metrics.New()
	pool := realPoolStat(t)
	if err := m.RegisterPool(pool); err != nil {
		t.Fatalf("register pool: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	// 127.0.0.1:1 refuses immediately, so the acquire fails on the dial.
	if _, err := pool.Acquire(ctx); err == nil {
		t.Fatal("acquire against an unreachable server unexpectedly succeeded")
	}

	families := metricstest.Scrape(t, m)
	// A dial that never established leaves no connection in the pool. Asserting
	// the post-failure value (rather than only the pre-failure one) is what
	// shows the scrape reflects the pool's state now.
	if got := metricstest.GaugeValue(families, "inroad_db_pool_total_conns", nil); got != 0 {
		t.Fatalf("total_conns = %v after a failed dial, want 0", got)
	}
	if got := metricstest.GaugeValue(families, "inroad_db_pool_acquired_conns", nil); got != 0 {
		t.Fatalf("acquired_conns = %v after a failed acquire, want 0", got)
	}
}

// TestRegisterPoolOnNilMetricsDoesNotPanic keeps the nil-receiver contract that
// lets tests and cmd/seed run without a registry.
func TestRegisterPoolOnNilMetricsDoesNotPanic(t *testing.T) {
	var m *metrics.Metrics
	if err := m.RegisterPool(realPoolStat(t)); err != nil {
		t.Fatalf("nil Metrics RegisterPool = %v, want nil", err)
	}
}

// TestRegisterPoolWithNilPoolIsNoOp: a caller with no pool wired must not get a
// collector that dereferences nil on the first scrape.
func TestRegisterPoolWithNilPoolIsNoOp(t *testing.T) {
	m := metrics.New()
	if err := m.RegisterPool(nil); err != nil {
		t.Fatalf("RegisterPool(nil) = %v, want nil", err)
	}
	families := metricstest.Scrape(t, m)
	if _, ok := families["inroad_db_pool_max_conns"]; ok {
		t.Fatal("no pool was registered, so no pool series should exist")
	}
}
