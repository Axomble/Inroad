package dbtest

import (
	"fmt"
	"testing"
)

func TestWithPoolLimitsCapsTheDSN(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{
			name: "appends to an existing query string",
			in:   "postgres://inroad:inroad@localhost:5433/inroad_test?sslmode=disable",
			want: fmt.Sprintf("postgres://inroad:inroad@localhost:5433/inroad_test?sslmode=disable&pool_max_conns=%d&pool_min_conns=0", testPoolMaxConns),
		},
		{
			name: "starts a query string when there is none",
			in:   "postgres://u:p@h:5432/app_test",
			want: fmt.Sprintf("postgres://u:p@h:5432/app_test?pool_max_conns=%d&pool_min_conns=0", testPoolMaxConns),
		},
		{
			// Someone who pinned the size in INROAD_TEST_DATABASE_URL meant it;
			// two pool_max_conns keys would also be ambiguous to pgxpool.
			name: "an already-pinned DSN is left alone",
			in:   "postgres://u:p@h:5432/app_test?pool_max_conns=2",
			want: "postgres://u:p@h:5432/app_test?pool_max_conns=2",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := withPoolLimits(tc.in); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// The suite runs four packages at once (`go test -p 4`); the cap has to leave real
// room under a stock max_connections=100, not merely fit.
func TestTestPoolMaxConnsLeavesHeadroomUnderParallelPackages(t *testing.T) {
	const packagesInParallel = 4
	const stockMaxConnections = 100
	if total := testPoolMaxConns * packagesInParallel; total > stockMaxConnections/2 {
		t.Errorf("%d packages x %d conns = %d, more than half of a stock max_connections=%d",
			packagesInParallel, testPoolMaxConns, total, stockMaxConnections)
	}
}
