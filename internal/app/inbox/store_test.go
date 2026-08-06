package inbox_test

import (
	"testing"

	"github.com/inroad/inroad/internal/app/inbox"
)

// NormalizeLimit is the one pure, DB-independent piece of store-layer logic in
// this domain: it is what stands between a caller's requested page size and
// the LIMIT Postgres actually runs, so it is worth pinning without a
// database. Everything else PgStore does is a thin sqlc wrapper, proven by
// store_integration_test.go against real Postgres instead.
func TestNormalizeLimit(t *testing.T) {
	cases := []struct {
		name      string
		requested int32
		want      int32
	}{
		{"zero defaults", 0, inbox.DefaultThreadPageLimit},
		{"negative defaults", -5, inbox.DefaultThreadPageLimit},
		{"within bounds is unchanged", 40, 40},
		{"exactly the max is unchanged", inbox.MaxThreadPageLimit, inbox.MaxThreadPageLimit},
		{"over the max is clamped", inbox.MaxThreadPageLimit + 1000, inbox.MaxThreadPageLimit},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := inbox.NormalizeLimit(c.requested); got != c.want {
				t.Errorf("NormalizeLimit(%d) = %d, want %d", c.requested, got, c.want)
			}
		})
	}
}
