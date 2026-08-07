//go:build integration

package dbtest

import (
	"context"
	"testing"

	"github.com/inroad/inroad/internal/platform/db"
)

// Every integration package starts the same two ways: db.Migrate(dbtest.DSN(t))
// and db.Connect(ctx, dbtest.DSN(t)). This asserts the one DSN serves both — the
// pool is capped to a test-sized pool_max_conns, AND migrate (a non-pool pgx
// consumer, which forwards unknown DSN keys to the server as configuration
// parameters) still accepts it.
func TestDSNCapsThePoolAndStaysMigratable(t *testing.T) {
	dsn := DSN(t)

	if err := db.Migrate(dsn); err != nil {
		t.Fatalf("migrate with a pool-tuned DSN: %v", err)
	}

	pool, err := db.Connect(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)

	if got := pool.Config().MaxConns; got != testPoolMaxConns {
		t.Errorf("pool MaxConns = %d, want the test cap %d (the production floor leaked into the suite)", got, testPoolMaxConns)
	}
	if got := pool.Config().MinConns; got != 0 {
		t.Errorf("pool MinConns = %d, want 0 so a package holds only what it uses", got)
	}
}
