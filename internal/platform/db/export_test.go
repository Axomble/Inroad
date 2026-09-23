package db

// Test-only handles on the migration lock's internals, for db_test (which has to
// be an external package: dbtest imports db).
const MigrationLockKey = migrationLockKey

var WithMigrationLockCtx = withMigrationLockCtx
