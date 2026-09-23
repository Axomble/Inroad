//go:build integration

package db_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inroad/inroad/internal/platform/db"
	"github.com/inroad/inroad/internal/platform/db/dbtest"
)

// The failure this lock exists for, reproduced: several processes migrating the
// SAME fresh database at once. Without the lock, the migrators waiting in
// golang-migrate's blocking pg_advisory_lock hold snapshots that the CREATE
// INDEX CONCURRENTLY in 20260923144758 must wait out, the two waits form a
// cycle, Postgres aborts the index build, and the schema is left dirty. This is
// what the integration suite does on a fresh database with -p 4, and what
// replicas that migrate on boot do on every deploy.
func TestConcurrentMigratorsOnAFreshDatabaseAllSucceed(t *testing.T) {
	dsn := dbtest.ScratchDSN(t, "concurrent_migrate")
	const migrators = 6

	var wg sync.WaitGroup
	errs := make([]error, migrators)
	start := make(chan struct{})
	for i := range migrators {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs[i] = db.Migrate(dsn)
		}()
	}
	close(start)
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("migrator %d: %v", i, err)
		}
	}

	version, dirty, err := db.Version(dsn)
	if err != nil || dirty || version == 0 {
		t.Fatalf("after concurrent migrate: version %d dirty %v err %v", version, dirty, err)
	}

	ctx := context.Background()
	conn, err := pgx.Connect(ctx, db.WithoutPoolParams(dsn))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	var valid bool
	if err := conn.QueryRow(ctx, `
		SELECT i.indisvalid FROM pg_index i JOIN pg_class c ON c.oid = i.indexrelid
		WHERE c.relname = 'idx_inbox_threads_campaign_contact'`).Scan(&valid); err != nil || !valid {
		t.Fatalf("CONCURRENTLY index after concurrent migrate: valid=%v err=%v", valid, err)
	}
}

// A waiter gives up with db.ErrMigrationLockTimeout rather than hanging or, worse,
// proceeding without the lock — and while it waits, it holds no running
// statement for a concurrent index build to deadlock against.
func TestMigrationLockWaitIsBoundedAndHoldsNoSnapshot(t *testing.T) {
	dsn := dbtest.ScratchDSN(t, "migrate_lock_wait")
	ctx := context.Background()

	holder, err := pgx.Connect(ctx, db.WithoutPoolParams(dsn))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = holder.Close(ctx) }()
	if _, err := holder.Exec(ctx, `SELECT pg_advisory_lock($1)`, db.MigrationLockKey); err != nil {
		t.Fatal(err)
	}

	waitCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	done := make(chan error, 1)
	ran := false
	go func() {
		done <- db.WithMigrationLockCtx(waitCtx, dsn, 50*time.Millisecond, func() error { ran = true; return nil })
	}()

	// Mid-wait, no other session on this database is running a statement: the
	// waiter is idle between polls, which is the whole point.
	time.Sleep(500 * time.Millisecond)
	var active int
	if err := holder.QueryRow(ctx, `
		SELECT count(*) FROM pg_stat_activity
		WHERE datname = current_database() AND pid <> pg_backend_pid()
		  AND state = 'active' AND query ILIKE '%advisory_lock%'`).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if active != 0 {
		t.Errorf("%d session(s) are waiting INSIDE an advisory-lock statement", active)
	}

	err = <-done
	if !errors.Is(err, db.ErrMigrationLockTimeout) {
		t.Fatalf("want db.ErrMigrationLockTimeout, got %v", err)
	}
	if ran {
		t.Fatal("fn ran without the lock")
	}

	// Once released, the next caller gets it.
	if _, err := holder.Exec(ctx, `SELECT pg_advisory_unlock($1)`, db.MigrationLockKey); err != nil {
		t.Fatal(err)
	}
	if err := db.WithMigrationLockCtx(ctx, dsn, 50*time.Millisecond, func() error { ran = true; return nil }); err != nil || !ran {
		t.Fatalf("after release: ran=%v err=%v", ran, err)
	}
}
