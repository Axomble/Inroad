// Package db owns the Postgres connection pool, schema migrations, and sqlc output.
package db

import (
	"context"
	"fmt"
	"math"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	pgxuuid "github.com/vgarvardt/pgx-google-uuid/v5"
)

// DefaultPoolMaxConns is the floor Connect applies when neither the DSN nor the
// caller sizes the pool. pgxpool's own default (max(4, numCPU)) can starve the
// worker on pool.Acquire once WorkerConcurrency is raised toward the ≥50-mailbox
// NFR, so the floor must exceed the default worker concurrency (10) with headroom
// for the periodic sweepers and the API server's own handlers.
//
// It reasons about ONE process. The cluster-wide budget — replicas × max +
// headroom ≤ Postgres max_connections — is the operator's, which is why
// INROAD_DB_MAX_CONNS exists: four stock processes hit a default 100 exactly.
const DefaultPoolMaxConns = 25

// DefaultPoolMinConns keeps a few warm connections so a burst of advances doesn't
// pay full connection-establishment latency from cold.
const DefaultPoolMinConns = 4

// PoolSize is a caller's requested pool sizing, in practice from
// INROAD_DB_MAX_CONNS / INROAD_DB_MIN_CONNS. A zero value means "unspecified"
// and defers to the defaults above.
type PoolSize struct {
	Max int
	Min int
}

// poolConfig parses a DSN and applies pool sizing under one precedence rule:
//
//	DSN pin  >  caller-supplied PoolSize  >  DefaultPool{Max,Min}Conns
//
// A DSN that pins pool_max_conns / pool_min_conns therefore wins outright, in
// BOTH directions — that pin is how a caller which is not a server opts out, and
// the integration suite depends on it: it pins both so four packages under
// `go test -p 4` cannot add up to the server's max_connections. The two keys are
// resolved independently, so pinning only one leaves the other to the env/default.
//
// The defaults apply as a FLOOR (pgxpool has already filled in its own smaller
// default by this point, and we only ever raise it); an explicit PoolSize is
// applied exactly, because an operator lowering the pool to fit a connection
// budget must not be silently raised back up.
func poolConfig(url string, size PoolSize) (*pgxpool.Config, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("parse database url: %w", err)
	}
	if err := size.validate(); err != nil {
		return nil, err
	}
	if !pinsPoolParam(url, "pool_max_conns") {
		if size.Max > 0 {
			cfg.MaxConns = int32(size.Max)
		} else if cfg.MaxConns < DefaultPoolMaxConns {
			cfg.MaxConns = DefaultPoolMaxConns
		}
	}
	if !pinsPoolParam(url, "pool_min_conns") {
		if size.Max > 0 {
			// Min travels with Max: a caller that sized the pool owns both ends,
			// so an unset Min means zero warm connections, not the stock floor of
			// 4 — which could otherwise exceed a deliberately tiny Max.
			cfg.MinConns = int32(size.Min)
		} else if cfg.MinConns < DefaultPoolMinConns {
			cfg.MinConns = DefaultPoolMinConns
		}
	}
	return cfg, nil
}

// validate rejects a sizing that can only fail later, at the first Acquire.
// config.Load checks the same invariants at the env boundary; this is the
// belt-and-braces copy for any other caller of ConnectSized.
func (s PoolSize) validate() error {
	if s.Max < 0 || s.Min < 0 {
		return fmt.Errorf("pool size must not be negative (max=%d min=%d)", s.Max, s.Min)
	}
	if s.Max > 0 && s.Max < s.Min {
		return fmt.Errorf("pool max conns (%d) must be at least min conns (%d)", s.Max, s.Min)
	}
	// pgxpool sizes are int32; reject rather than silently wrap a nonsense value
	// into a small (or negative) pool.
	if s.Max > math.MaxInt32 || s.Min > math.MaxInt32 {
		return fmt.Errorf("pool size out of range (max=%d min=%d)", s.Max, s.Min)
	}
	return nil
}

// Connect opens a pgx connection pool at the default sizing. Callers that read
// operator configuration should use ConnectSized.
func Connect(ctx context.Context, url string) (*pgxpool.Pool, error) {
	return ConnectSized(ctx, url, PoolSize{})
}

// ConnectSized opens a pgx connection pool, registers the google/uuid codec so
// sqlc's uuid.UUID columns scan correctly, sizes the pool per poolConfig's
// precedence rule, and verifies connectivity.
func ConnectSized(ctx context.Context, url string, size PoolSize) (*pgxpool.Pool, error) {
	cfg, err := poolConfig(url, size)
	if err != nil {
		return nil, err
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
