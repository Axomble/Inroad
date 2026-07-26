// Package db owns the Postgres connection pool, schema migrations, and sqlc output.
package db

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	pgxuuid "github.com/vgarvardt/pgx-google-uuid/v5"
)

// poolMaxConnsFloor is the minimum pool size Connect enforces when the DSN does
// not pin pool_max_conns explicitly. pgxpool's own default (max(4, numCPU)) can
// starve the worker on pool.Acquire once WorkerConcurrency is raised toward the
// ≥50-mailbox NFR, so the floor must exceed the default worker concurrency (10)
// with headroom for the periodic sweepers and the API server's own handlers.
const poolMaxConnsFloor = 25

// poolMinConns keeps a few warm connections so a burst of advances doesn't pay
// full connection-establishment latency from cold.
const poolMinConns = 4

// Connect opens a pgx connection pool, registers the google/uuid codec so
// sqlc's uuid.UUID columns scan correctly, sizes the pool for worker
// concurrency, and verifies connectivity.
func Connect(ctx context.Context, url string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, err
	}
	// Respect an explicit pool_max_conns in the DSN; otherwise raise a sane floor
	// so workers don't starve on Acquire under concurrency.
	if !strings.Contains(url, "pool_max_conns") && cfg.MaxConns < poolMaxConnsFloor {
		cfg.MaxConns = poolMaxConnsFloor
	}
	if cfg.MinConns < poolMinConns {
		cfg.MinConns = poolMinConns
	}
	cfg.AfterConnect = func(_ context.Context, conn *pgx.Conn) error {
		pgxuuid.Register(conn.TypeMap())
		return nil
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return pool, nil
}
