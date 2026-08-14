// Package dbtest resolves the database integration tests run against.
//
// It exists because every integration test used to carry its own copy of a
// `dsn()` helper defaulting to the DEV database. Running the suite on a
// developer's machine therefore wrote thousands of fixture workspaces, users and
// contacts into the database they were clicking around in — the demo data ended
// up buried under 5,712 test workspaces, and "is this row real?" became a
// question you had to answer before trusting anything on screen.
//
// The default is now a SEPARATE database on the same server, created on demand.
// A dev database is never touched unless someone explicitly points the suite at
// one.
package dbtest

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/inroad/inroad/internal/platform/db"
)

// defaultDSN is the dev-machine fallback: the compose Postgres (host port 5433,
// chosen so it can coexist with a native install on 5432), but a database of its
// own rather than the one `make run-api` serves.
const defaultDSN = "postgres://inroad:inroad@localhost:5433/inroad_test?sslmode=disable"

// duplicateDatabase is Postgres's SQLSTATE for "database already exists". Two
// packages' tests can race to create it, and losing that race is success.
const duplicateDatabase = "42P04"

// testPoolMaxConns caps the pool each integration package opens.
//
// db.Connect raises an unpinned pool to a server-sized floor (25 connections: the
// worker's concurrency plus its sweepers). Four test packages at once — what both
// CI and `make test-integration` run — made that 100, exactly the stock
// max_connections of postgres:16, so the suite sat on the ceiling and tipped over
// at random with "sorry, too many clients already" in whichever package asked
// last. It read as a defect in code nobody had touched.
//
// A test package runs one case at a time. The busiest cases fire up to 20
// goroutines at a single row to prove a race is handled atomically; 8 connections
// keeps that contention real at the database (where the test is looking) rather
// than queueing it in the pool, while four packages together stay well under any
// server's limit.
const testPoolMaxConns = 8

// DSN returns the connection string for integration tests, in precedence order:
//
//  1. INROAD_TEST_DATABASE_URL — an explicit override, used verbatim.
//  2. INROAD_DATABASE_URL with its database name suffixed `_test`. CI sets that
//     variable for the app; deriving keeps the suite on the same server without
//     it ever pointing at the app's own database.
//  3. defaultDSN.
//
// The database is created if it does not exist, so there is no setup step to
// forget and no failure that reads as "the suite is broken" when it means "you
// haven't made the database yet".
//
// The returned DSN pins the pgxpool sizing keys, so db.Connect leaves its
// production floor out of the suite — see testPoolMaxConns.
func DSN(t *testing.T) string {
	t.Helper()
	dsn := resolve()
	if err := ensureDatabaseOnce(dsn); err != nil {
		t.Fatalf("dbtest: preparing %s: %v", redact(dsn), err)
	}
	return withPoolLimits(dsn)
}

// ScratchDSN returns a DSN for a database of the calling test's own — created
// empty here and DROPPED when the test finishes.
//
// It exists for the one thing the shared test database cannot serve: rolling a
// migration BACK. Every package's setup migrates the shared database to the newest
// version and `make test-integration` runs four packages at once, so a down
// migration there drops a column another package is selecting at that moment. A
// down path is still worth exercising — a broken one is otherwise discovered
// during an incident, at the worst possible time — so it gets its own database.
//
// The name is suffixed rather than random so a crashed run leaves one
// recognisable database behind instead of accumulating them; the DROP up front
// makes the next run's schema clean regardless.
func ScratchDSN(t *testing.T, name string) string {
	t.Helper()
	scratch := replaceDatabase(resolve(), "inroad_scratch_"+name)
	drop := func() {
		// FORCE terminates any leftover backend: a pool the test forgot to close
		// would otherwise make DROP fail and strand the database.
		if err := adminExec(scratch, `DROP DATABASE IF EXISTS %s WITH (FORCE)`); err != nil {
			t.Errorf("dbtest: dropping scratch database %s: %v", redact(scratch), err)
		}
	}
	drop() // a previous crashed run may have left it behind, with a half-rolled-back schema
	if err := adminExec(scratch, `CREATE DATABASE %s`); err != nil {
		t.Fatalf("dbtest: creating scratch database %s: %v", redact(scratch), err)
	}
	t.Cleanup(drop)
	return withPoolLimits(scratch)
}

// withPoolLimits pins the pool size in the DSN so db.Connect's server-sized floor
// does not apply to a test process. pool_min_conns=0 on top of the cap means a
// package holds only the connections it is actually using, instead of keeping
// warm ones for a latency budget no test has.
//
// A DSN that already pins the size is returned untouched: someone who set it in
// INROAD_TEST_DATABASE_URL meant it, and a duplicated key would be ambiguous.
func withPoolLimits(dsn string) string {
	if strings.Contains(dsn, "pool_max_conns") {
		return dsn
	}
	separator := "?"
	if strings.Contains(dsn, "?") {
		separator = "&"
	}
	return fmt.Sprintf("%s%spool_max_conns=%d&pool_min_conns=0", dsn, separator, testPoolMaxConns)
}

// ensured memoises ensureDatabase per DSN. Every test in a package calls DSN, and
// each call otherwise opened a maintenance connection to retry a CREATE DATABASE
// that can only succeed once — short-lived connections, but they still count
// against max_connections while four packages run at once.
var ensured struct {
	mu sync.Mutex
	by map[string]error
}

func ensureDatabaseOnce(dsn string) error {
	ensured.mu.Lock()
	defer ensured.mu.Unlock()
	if err, done := ensured.by[dsn]; done {
		return err
	}
	err := ensureDatabase(dsn)
	if ensured.by == nil {
		ensured.by = make(map[string]error)
	}
	ensured.by[dsn] = err
	return err
}

func resolve() string {
	if v := os.Getenv("INROAD_TEST_DATABASE_URL"); v != "" {
		return v
	}
	if v := os.Getenv("INROAD_DATABASE_URL"); v != "" {
		if derived, ok := withTestSuffix(v); ok {
			return derived
		}
	}
	return defaultDSN
}

// withTestSuffix rewrites the database name in a postgres URL to name+"_test",
// leaving an already-suffixed name alone so repeated derivation is stable.
func withTestSuffix(dsn string) (string, bool) {
	cfg, err := pgconn.ParseConfig(dsn)
	if err != nil || cfg.Database == "" {
		return "", false
	}
	if strings.HasSuffix(cfg.Database, "_test") {
		return dsn, true
	}
	// Rewrite only the path segment: query parameters (sslmode, search_path)
	// and credentials must survive untouched.
	scheme, rest, ok := strings.Cut(dsn, "://")
	if !ok {
		return "", false
	}
	hostAndPath, query, hasQuery := strings.Cut(rest, "?")
	base, _, ok := strings.Cut(hostAndPath, "/")
	if !ok {
		return "", false
	}
	out := scheme + "://" + base + "/" + cfg.Database + "_test"
	if hasQuery {
		out += "?" + query
	}
	return out, true
}

// ensureDatabase creates the target database if it is missing. A duplicate error
// means a concurrent package won the race, which is fine.
func ensureDatabase(dsn string) error {
	err := adminExec(dsn, `CREATE DATABASE %s`)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == duplicateDatabase {
		return nil
	}
	return err
}

// adminExec runs one statement that cannot run inside the target database itself
// (CREATE/DROP DATABASE), from a short-lived connection to the server's
// maintenance database. stmt is a format string with a single %s for the target
// database identifier: an identifier cannot be a bind parameter, so it is
// double-quoted here, and it comes from a parsed DSN rather than from anything
// user-supplied at runtime.
func adminExec(dsn, stmt string) error {
	cfg, err := pgconn.ParseConfig(dsn)
	if err != nil {
		return fmt.Errorf("parse dsn: %w", err)
	}
	if cfg.Database == "" {
		return errors.New("dsn has no database name")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// A single connection, not a pool, and WithoutPoolParams because pgx.Connect
	// would forward pgxpool's keys to the server as unknown configuration
	// parameters. It is closed before this function returns, so it never overlaps
	// with the package's own pool.
	adminDSN := db.WithoutPoolParams(replaceDatabase(dsn, "postgres"))
	conn, err := pgx.Connect(ctx, adminDSN)
	if err != nil {
		// Nothing to act against — surface the real reason (server down, wrong
		// port) rather than a later, more confusing migration failure.
		return fmt.Errorf("connect to maintenance database: %w", err)
	}
	defer func() { _ = conn.Close(ctx) }()

	quoted := `"` + strings.ReplaceAll(cfg.Database, `"`, `""`) + `"`
	if _, err := conn.Exec(ctx, fmt.Sprintf(stmt, quoted)); err != nil {
		return fmt.Errorf("%q on database %q: %w", stmt, cfg.Database, err)
	}
	return nil
}

// replaceDatabase swaps the database name in a postgres URL, preserving
// credentials, host, and query parameters.
func replaceDatabase(dsn, name string) string {
	scheme, rest, ok := strings.Cut(dsn, "://")
	if !ok {
		return dsn
	}
	hostAndPath, query, hasQuery := strings.Cut(rest, "?")
	base, _, ok := strings.Cut(hostAndPath, "/")
	if !ok {
		base = hostAndPath
	}
	out := scheme + "://" + base + "/" + name
	if hasQuery {
		out += "?" + query
	}
	return out
}

// redact hides the password when a DSN reaches a failure message.
func redact(dsn string) string {
	scheme, rest, ok := strings.Cut(dsn, "://")
	if !ok {
		return dsn
	}
	creds, tail, ok := strings.Cut(rest, "@")
	if !ok {
		return dsn
	}
	user, _, hasPass := strings.Cut(creds, ":")
	if !hasPass {
		return dsn
	}
	return scheme + "://" + user + ":***@" + tail
}
