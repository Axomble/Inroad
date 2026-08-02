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
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// defaultDSN is the dev-machine fallback: the compose Postgres (host port 5433,
// chosen so it can coexist with a native install on 5432), but a database of its
// own rather than the one `make run-api` serves.
const defaultDSN = "postgres://inroad:inroad@localhost:5433/inroad_test?sslmode=disable"

// duplicateDatabase is Postgres's SQLSTATE for "database already exists". Two
// packages' tests can race to create it, and losing that race is success.
const duplicateDatabase = "42P04"

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
func DSN(t *testing.T) string {
	t.Helper()
	dsn := resolve()
	if err := ensureDatabase(dsn); err != nil {
		t.Fatalf("dbtest: preparing %s: %v", redact(dsn), err)
	}
	return dsn
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

// ensureDatabase creates the target database if it is missing, by connecting to
// the server's maintenance database. A duplicate error means a concurrent package
// won the race, which is fine.
func ensureDatabase(dsn string) error {
	cfg, err := pgconn.ParseConfig(dsn)
	if err != nil {
		return fmt.Errorf("parse dsn: %w", err)
	}
	target := cfg.Database
	if target == "" {
		return errors.New("dsn has no database name")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	adminDSN := replaceDatabase(dsn, "postgres")
	conn, err := pgx.Connect(ctx, adminDSN)
	if err != nil {
		// Nothing to create against — surface the real reason (server down,
		// wrong port) rather than a later, more confusing migration failure.
		return fmt.Errorf("connect to maintenance database: %w", err)
	}
	defer func() { _ = conn.Close(ctx) }()

	// The identifier cannot be parameterised; it is quoted instead, and it comes
	// from a parsed DSN rather than anything user-supplied at runtime.
	_, err = conn.Exec(ctx, `CREATE DATABASE "`+strings.ReplaceAll(target, `"`, `""`)+`"`)
	if err == nil {
		return nil
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == duplicateDatabase {
		return nil
	}
	return fmt.Errorf("create database %q: %w", target, err)
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
