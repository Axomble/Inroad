package inbox

import (
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// NewPgStoreWithSearchTimeout is NewPgStore with the search statement_timeout
// overridden, so an integration test can drive the timeout path without
// seeding enough data to make a real search slow.
func NewPgStoreWithSearchTimeout(pool *pgxpool.Pool, timeout time.Duration) *PgStore {
	s := NewPgStore(pool)
	s.searchTimeout = timeout
	return s
}

// This file exposes internals to the package's external test package
// (inbox_test), the standard Go seam for testing an unexported unit without
// widening the real API surface. It exists only under `go test`.

// OverviewWindowAt is overviewWindowAt, exported for tests: the day/week
// boundary arithmetic, at a caller-chosen instant and UTC offset. Testing it
// directly is what makes an exact assertion possible ("this Sunday resolves to
// that Monday"), rather than the weak "it landed on some Monday" a
// clock-reading version forces.
func OverviewWindowAt(now time.Time, offsetMinutes int) OverviewWindow {
	return overviewWindowAt(now, offsetMinutes)
}
