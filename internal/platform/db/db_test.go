package db

import "testing"

// The production floor exists so the worker does not starve on Acquire, but it is
// the wrong size for a test package: four of them under `go test -p 4` used to
// reach stock max_connections exactly. A DSN that pins the pool keys must
// therefore win over the floor in both directions.
func TestPoolConfigSizing(t *testing.T) {
	cases := []struct {
		name, dsn        string
		wantMax, wantMin int32
	}{
		{
			name:    "an unpinned DSN gets the server-sized floor",
			dsn:     "postgres://u:p@h:5432/app?sslmode=disable",
			wantMax: poolMaxConnsFloor,
			wantMin: poolMinConns,
		},
		{
			name:    "an explicit pool_max_conns is respected",
			dsn:     "postgres://u:p@h:5432/app?sslmode=disable&pool_max_conns=8",
			wantMax: 8,
			wantMin: poolMinConns,
		},
		{
			name:    "an explicit pool_min_conns of zero is respected",
			dsn:     "postgres://u:p@h:5432/app?sslmode=disable&pool_max_conns=8&pool_min_conns=0",
			wantMax: 8,
			wantMin: 0,
		},
		{
			name:    "a pinned max above the floor is left alone",
			dsn:     "postgres://u:p@h:5432/app?pool_max_conns=64",
			wantMax: 64,
			wantMin: poolMinConns,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := poolConfig(tc.dsn)
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
