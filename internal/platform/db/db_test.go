package db

import (
	"strings"
	"testing"
	"time"
)

// The default sizing exists so the worker does not starve on Acquire, but it is
// the wrong size for a test package: four of them under `go test -p 4` used to
// reach stock max_connections exactly. The precedence rule under test is
//
//	DSN pin > caller PoolSize > default
//
// so a DSN that pins the pool keys must win over both, in both directions, and an
// operator lowering the pool via PoolSize must not be raised back to the default.
func TestPoolConfigSizing(t *testing.T) {
	cases := []struct {
		name, dsn        string
		size             PoolSize
		wantMax, wantMin int32
	}{
		{
			name:    "an unpinned DSN with no caller sizing gets the default",
			dsn:     "postgres://u:p@h:5432/app?sslmode=disable",
			wantMax: DefaultPoolMaxConns,
			wantMin: DefaultPoolMinConns,
		},
		{
			name:    "an explicit pool_max_conns is respected",
			dsn:     "postgres://u:p@h:5432/app?sslmode=disable&pool_max_conns=8",
			wantMax: 8,
			wantMin: DefaultPoolMinConns,
		},
		{
			name:    "an explicit pool_min_conns of zero is respected",
			dsn:     "postgres://u:p@h:5432/app?sslmode=disable&pool_max_conns=8&pool_min_conns=0",
			wantMax: 8,
			wantMin: 0,
		},
		{
			name:    "a pinned max above the default is left alone",
			dsn:     "postgres://u:p@h:5432/app?pool_max_conns=64",
			wantMax: 64,
			wantMin: DefaultPoolMinConns,
		},
		{
			name:    "caller sizing beats the default",
			dsn:     "postgres://u:p@h:5432/app?sslmode=disable",
			size:    PoolSize{Max: 50, Min: 10},
			wantMax: 50,
			wantMin: 10,
		},
		{
			name:    "caller sizing BELOW the default is not raised back up",
			dsn:     "postgres://u:p@h:5432/app?sslmode=disable",
			size:    PoolSize{Max: 5, Min: 1},
			wantMax: 5,
			wantMin: 1,
		},
		{
			name:    "a caller max with no min means no warm connections, not the default floor",
			dsn:     "postgres://u:p@h:5432/app?sslmode=disable",
			size:    PoolSize{Max: 3},
			wantMax: 3,
			wantMin: 0,
		},
		{
			// The escape hatch the integration suite depends on: dbtest pins both
			// keys, and no env-driven caller sizing may override that.
			name:    "a DSN pin beats caller sizing on both keys",
			dsn:     "postgres://u:p@h:5432/app?pool_max_conns=4&pool_min_conns=0",
			size:    PoolSize{Max: 100, Min: 40},
			wantMax: 4,
			wantMin: 0,
		},
		{
			name:    "pinning only max leaves min to the caller",
			dsn:     "postgres://u:p@h:5432/app?pool_max_conns=4",
			size:    PoolSize{Max: 100, Min: 7},
			wantMax: 4,
			wantMin: 7,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := poolConfig(tc.dsn, tc.size)
			if err != nil {
				t.Fatalf("poolConfig: %v", err)
			}
			if cfg.MaxConns != tc.wantMax {
				t.Errorf("MaxConns = %d, want %d", cfg.MaxConns, tc.wantMax)
			}
			if cfg.MinConns != tc.wantMin {
				t.Errorf("MinConns = %d, want %d", cfg.MinConns, tc.wantMin)
			}
		})
	}
}

// A pool sizing that cannot work must be rejected while building the config, not
// discovered later as an Acquire that blocks forever.
func TestPoolConfigRejectsImpossibleSizing(t *testing.T) {
	const dsn = "postgres://u:p@h:5432/app?sslmode=disable"
	cases := []struct {
		name     string
		size     PoolSize
		wantText string
	}{
		{"max below min", PoolSize{Max: 2, Min: 10}, "at least min conns"},
		{"negative max", PoolSize{Max: -1}, "must not be negative"},
		{"negative min", PoolSize{Max: 10, Min: -1}, "must not be negative"},
		{"max beyond int32", PoolSize{Max: 1 << 40}, "out of range"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := poolConfig(dsn, tc.size)
			if err == nil {
				t.Fatalf("poolConfig(%+v) = nil error, want a rejection", tc.size)
			}
			if !strings.Contains(err.Error(), tc.wantText) {
				t.Fatalf("poolConfig error = %q, want it to mention %q", err, tc.wantText)
			}
		})
	}
}

// Connect's default sizing must stay exactly what it was before PoolSize existed,
// so callers that do not read operator config (cmd/seed, tests) are unaffected.
func TestConnectDefaultsMatchTheZeroPoolSize(t *testing.T) {
	const dsn = "postgres://u:p@h:5432/app?sslmode=disable"
	cfg, err := poolConfig(dsn, PoolSize{})
	if err != nil {
		t.Fatalf("poolConfig: %v", err)
	}
	if cfg.MaxConns != DefaultPoolMaxConns || cfg.MinConns != DefaultPoolMinConns {
		t.Fatalf("default sizing = (%d, %d), want (%d, %d)",
			cfg.MaxConns, cfg.MinConns, DefaultPoolMaxConns, DefaultPoolMinConns)
	}
}

// TestPoolConfigConnectionRecyclingDefaults proves poolConfig sets an explicit
// connection-recycling policy (MaxConnLifetime/MaxConnIdleTime/HealthCheckPeriod)
// rather than leaving pgxpool.ParseConfig's own built-in defaults (1h/30m/1m) in
// place unexamined — and that an operator's DSN pin on any one of the three wins,
// exactly like the pool_max_conns/pool_min_conns precedence above.
func TestPoolConfigConnectionRecyclingDefaults(t *testing.T) {
	cases := []struct {
		name                                     string
		dsn                                      string
		wantMaxConnLifetime, wantMaxConnIdleTime time.Duration
		wantHealthCheckPeriod                    time.Duration
	}{
		{
			name:                  "an unpinned DSN gets this package's own recycling defaults",
			dsn:                   "postgres://u:p@h:5432/app?sslmode=disable",
			wantMaxConnLifetime:   defaultMaxConnLifetime,
			wantMaxConnIdleTime:   defaultMaxConnIdleTime,
			wantHealthCheckPeriod: defaultHealthCheckPeriod,
		},
		{
			name:                  "a pinned pool_max_conn_lifetime is respected",
			dsn:                   "postgres://u:p@h:5432/app?sslmode=disable&pool_max_conn_lifetime=2h",
			wantMaxConnLifetime:   2 * time.Hour,
			wantMaxConnIdleTime:   defaultMaxConnIdleTime,
			wantHealthCheckPeriod: defaultHealthCheckPeriod,
		},
		{
			name:                  "a pinned pool_max_conn_idle_time is respected",
			dsn:                   "postgres://u:p@h:5432/app?sslmode=disable&pool_max_conn_idle_time=10m",
			wantMaxConnLifetime:   defaultMaxConnLifetime,
			wantMaxConnIdleTime:   10 * time.Minute,
			wantHealthCheckPeriod: defaultHealthCheckPeriod,
		},
		{
			name:                  "a pinned pool_health_check_period is respected",
			dsn:                   "postgres://u:p@h:5432/app?sslmode=disable&pool_health_check_period=30s",
			wantMaxConnLifetime:   defaultMaxConnLifetime,
			wantMaxConnIdleTime:   defaultMaxConnIdleTime,
			wantHealthCheckPeriod: 30 * time.Second,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := poolConfig(tc.dsn, PoolSize{})
			if err != nil {
				t.Fatalf("poolConfig: %v", err)
			}
			if cfg.MaxConnLifetime != tc.wantMaxConnLifetime {
				t.Errorf("MaxConnLifetime = %v, want %v", cfg.MaxConnLifetime, tc.wantMaxConnLifetime)
			}
			if cfg.MaxConnIdleTime != tc.wantMaxConnIdleTime {
				t.Errorf("MaxConnIdleTime = %v, want %v", cfg.MaxConnIdleTime, tc.wantMaxConnIdleTime)
			}
			if cfg.HealthCheckPeriod != tc.wantHealthCheckPeriod {
				t.Errorf("HealthCheckPeriod = %v, want %v", cfg.HealthCheckPeriod, tc.wantHealthCheckPeriod)
			}
		})
	}
}
