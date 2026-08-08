package db

import (
	"embed"
	"errors"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Migrate applies all up migrations. It is a no-op if the schema is current.
func Migrate(url string) (err error) {
	m, err := newMigrator(url)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, closeMigrator(m)) }()
	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return err
	}
	return nil
}

// MigrateDown rolls back a single migration.
func MigrateDown(url string) (err error) {
	m, err := newMigrator(url)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, closeMigrator(m)) }()
	if err := m.Steps(-1); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return err
	}
	return nil
}

func newMigrator(url string) (*migrate.Migrate, error) {
	src, err := iofs.New(migrationsFS, "migrations")
	if err != nil {
		return nil, err
	}
	return migrate.NewWithSourceInstance("iofs", src, "pgx5://"+trimScheme(WithoutPoolParams(url)))
}

// closeMigrator releases the connection migrate's driver opened for itself. It has
// to be called explicitly: the driver holds its own *sql.DB, so without a Close
// each Migrate call leaks a connection for the life of the process. The migrate
// binary exits and would never notice, but the integration suite calls Migrate once
// per test — those leaks accumulated until Postgres refused new clients, and the
// failure surfaced in whichever unrelated package asked last.
func closeMigrator(m *migrate.Migrate) error {
	sourceErr, dbErr := m.Close()
	return errors.Join(sourceErr, dbErr)
}

// trimScheme converts a postgres:// URL into the driver-prefixed form migrate
// expects. Callers pass the URL through WithoutPoolParams first: migrate's driver
// is not pgxpool and would forward pgxpool's own keys to the server as unknown
// configuration parameters.
func trimScheme(url string) string {
	for _, p := range []string{"postgres://", "postgresql://"} {
		if len(url) >= len(p) && url[:len(p)] == p {
			return url[len(p):]
		}
	}
	return url
}
