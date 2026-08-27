//go:build integration

// This file carries the integration build tag despite its plain name: MigrateTo's
// whole contract is "what version is the schema on afterwards", and nothing but a
// real Postgres can answer that. `make test` therefore does not run it; it runs
// under `make test-integration` with the rest.
package db_test

import (
	"context"
	"os"
	"regexp"
	"sort"
	"strconv"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/inroad/inroad/internal/platform/db"
	"github.com/inroad/inroad/internal/platform/db/dbtest"
)

// MigrateTo was added so a rollback test could name the version it wants to land
// on, because MigrateDown steps back exactly ONE migration — meaning every such
// test silently retargets the previous migration's rollback the day a new one
// lands. Three rollback tests now depend on MigrateTo, but its own behaviour was
// only ever exercised through them, so a MigrateTo that stepped instead of
// targeting would fail those three with assertions about their own columns and
// nothing pointing here.
//
// The assertions read golang-migrate's own bookkeeping rather than any migration's
// columns, so this test does not retarget or go stale when a migration is added.
//
// The three phases run in sequence on ONE scratch database on purpose: each
// phase's precondition is the previous phase's postcondition, and giving them a
// database each would mean three full up-migrations of ~70 files to observe the
// same three transitions.
func TestMigrateToLandsOnTheVersionNamed(t *testing.T) {
	ctx := context.Background()
	dsn := dbtest.ScratchDSN(t, "migrate_to")

	if err := db.Migrate(dsn); err != nil {
		t.Fatalf("migrate up: %v", err)
	}
	head, dirty := schemaVersion(t, ctx, dsn)
	if dirty {
		t.Fatalf("schema is dirty at version %d after a clean up-migration", head)
	}
	// Two back, not one. Landing on the SECOND-highest version is what a step count
	// would do, so a target two away is the only way to tell targeting from stepping.
	//
	// Read from the embedded migration list rather than computed as head-2. Versions
	// are no longer contiguous: since the timestamp convention a version is a
	// YYYYMMDDHHMMSS instant, so head-2 is not a migration at all and MigrateTo would
	// fail on a version that never existed. That is exactly what happened when the
	// convention landed — this test went red on arithmetic, not on behaviour.
	applied := appliedVersionsAscending(t)
	if len(applied) < 3 {
		t.Fatalf("only %d migrations embedded; this test needs at least 3 to distinguish "+
			"targeting a version from stepping back one", len(applied))
	}
	if got := applied[len(applied)-1]; got != head {
		t.Fatalf("embedded head %d does not match the applied head %d", got, head)
	}
	target := applied[len(applied)-3]
	stepBack := applied[len(applied)-2]
	if err := db.MigrateTo(dsn, target); err != nil {
		t.Fatalf("MigrateTo(%d) from %d: %v", target, head, err)
	}
	if got, dirty := schemaVersion(t, ctx, dsn); got != target || dirty {
		t.Fatalf("after MigrateTo(%d): version = %d, dirty = %v; want %d and clean "+
			"(version %d would mean it stepped back once instead of targeting)",
			target, got, dirty, target, stepBack)
	}

	// Asking for the version already in place must succeed and change nothing.
	// golang-migrate reports ErrNoChange here, and MigrateTo has to absorb it —
	// surfacing it would make every caller special-case "already there", and the
	// rollback tests would fail on a second run against a crashed-run database.
	if err := db.MigrateTo(dsn, target); err != nil {
		t.Fatalf("MigrateTo(%d) when already at %d: %v (ErrNoChange must be absorbed)", target, target, err)
	}
	if got, dirty := schemaVersion(t, ctx, dsn); got != target || dirty {
		t.Fatalf("after a no-op MigrateTo(%d): version = %d, dirty = %v; want %d and clean", target, got, dirty, target)
	}

	// Forward again, which is the half the rollback tests rely on to leave the
	// scratch database usable: a down migration that cannot be re-applied is a
	// broken down migration, and this is where that shows up.
	if err := db.MigrateTo(dsn, head); err != nil {
		t.Fatalf("MigrateTo(%d) from %d: %v", head, target, err)
	}
	if got, dirty := schemaVersion(t, ctx, dsn); got != head || dirty {
		t.Fatalf("after MigrateTo(%d): version = %d, dirty = %v; want %d and clean", head, got, dirty, head)
	}
}

// A version that does not exist must fail, and must fail BEFORE touching the
// schema.
//
// The failure direction matters more than the error: a MigrateTo that applied
// what it could and then reported a problem would leave the database on some
// version nobody named — and, if it stopped mid-migration, marked dirty, which
// blocks every subsequent migration until a human clears it by hand.
func TestMigrateToRejectsAVersionThatDoesNotExist(t *testing.T) {
	ctx := context.Background()
	dsn := dbtest.ScratchDSN(t, "migrate_to_range")

	if err := db.Migrate(dsn); err != nil {
		t.Fatalf("migrate up: %v", err)
	}
	head, _ := schemaVersion(t, ctx, dsn)

	beyond := head + 1000
	if err := db.MigrateTo(dsn, beyond); err == nil {
		t.Fatalf("MigrateTo(%d) succeeded with only %d migrations available; an out-of-range "+
			"target must be reported, not silently treated as 'as far as possible'", beyond, head)
	}
	if got, dirty := schemaVersion(t, ctx, dsn); got != head || dirty {
		t.Errorf("after a failed MigrateTo(%d): version = %d, dirty = %v; want %d and clean — "+
			"the schema must be left exactly as it was", beyond, got, dirty, head)
	}
}

// schemaVersion reads golang-migrate's own bookkeeping table: the version the
// schema is on, and whether a migration died part-way and left it dirty.
//
// A short-lived connection per call, not a pool held for the test: the scratch
// database is DROPPed on cleanup, and an idle pooled connection is exactly what
// makes that drop need FORCE.
func schemaVersion(t *testing.T, ctx context.Context, dsn string) (uint, bool) {
	t.Helper()
	conn, err := pgx.Connect(ctx, db.WithoutPoolParams(dsn))
	if err != nil {
		t.Fatalf("connect to read schema_migrations: %v", err)
	}
	defer func() { _ = conn.Close(ctx) }()

	var version int64
	var dirty bool
	if err := conn.QueryRow(ctx, `SELECT version, dirty FROM schema_migrations`).Scan(&version, &dirty); err != nil {
		t.Fatalf("read schema_migrations: %v", err)
	}
	return uint(version), dirty
}

// appliedVersionsAscending is every migration version on disk, ascending.
//
// It does NOT assume versions are contiguous, which they stopped being when
// migrations moved to YYYYMMDDHHMMSS timestamps — head-2 is not a migration and
// MigrateTo would fail on a version that never existed.
//
// Reads the directory rather than the embedded FS purely because this file is an
// external test package and migrationsFS is unexported. migrationnames_test.go, which
// is internal, guards the embedded set; between them the two cover both views.
func appliedVersionsAscending(t *testing.T) []uint {
	t.Helper()
	const dir = "migrations"
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	version := regexp.MustCompile(`^(\d+)_[a-z0-9_]+\.up\.sql$`)
	out := make([]uint, 0, len(entries))
	for _, e := range entries {
		m := version.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		v, err := strconv.ParseUint(m[1], 10, 64)
		if err != nil {
			t.Fatalf("migration %q has an unparseable version: %v", e.Name(), err)
		}
		out = append(out, uint(v))
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
