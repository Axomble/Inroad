//go:build integration

package db_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/inroad/inroad/internal/platform/db"
	"github.com/inroad/inroad/internal/platform/db/dbtest"
)

// golang-migrate's driver opens a *sql.DB of its own and releases it only on
// Close. Every integration test starts with db.Migrate, so a Migrate that does not
// close leaks a connection per test that lives until the package's process exits —
// which is how the suite reached "sorry, too many clients already" in whichever
// unrelated package happened to ask last.
//
// The probe tags its connections with a unique application_name, so the count is
// unaffected by the other packages running against the same server under -p 4.
func TestMigrateReleasesItsConnection(t *testing.T) {
	ctx := context.Background()
	dsn := dbtest.DSN(t)

	appName := "migrate-leak-probe-" + t.Name()
	probeDSN := fmt.Sprintf("%s&application_name=%s", dsn, appName)

	observer, err := pgx.Connect(ctx, db.WithoutPoolParams(dsn))
	if err != nil {
		t.Fatalf("observer connect: %v", err)
	}
	t.Cleanup(func() { _ = observer.Close(ctx) })

	const migrations = 5
	for i := range migrations {
		if err := db.Migrate(probeDSN); err != nil {
			t.Fatalf("migrate %d: %v", i, err)
		}
	}

	var leaked int
	if err := observer.QueryRow(ctx,
		`SELECT count(*) FROM pg_stat_activity WHERE application_name = $1`, appName,
	).Scan(&leaked); err != nil {
		t.Fatalf("count backends: %v", err)
	}
	if leaked != 0 {
		t.Errorf("%d connections still open after %d Migrate calls, want 0", leaked, migrations)
	}
}

// TestVersionReportsCurrentSchemaVersion confirms Version (`inroadctl
// status`'s migration-version probe) returns a real, non-dirty version once
// the schema is migrated — proving it reads the ACTUAL applied version rather
// than, say, the highest embedded migration file.
func TestVersionReportsCurrentSchemaVersion(t *testing.T) {
	dsn := dbtest.DSN(t)
	if err := db.Migrate(dsn); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	version, dirty, err := db.Version(dsn)
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	if dirty {
		t.Fatal("expected a freshly migrated schema to be clean, got dirty=true")
	}
	if version == 0 {
		t.Fatal("expected a non-zero version after migrating, got 0")
	}
}
