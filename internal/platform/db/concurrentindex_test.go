package db

import (
	"io/fs"
	"regexp"
	"strings"
	"testing"
)

var concurrently = regexp.MustCompile(`(?i)\bCONCURRENTLY\b`)

// A migration that builds or drops an index CONCURRENTLY must be exactly ONE
// statement. golang-migrate's pgx/v5 driver sends a file as a single Exec; with
// one statement that is not a transaction block, and the concurrent build is
// allowed. With two, Postgres runs the file as an implicit transaction and
// refuses the CONCURRENTLY — at deploy time, on the production database, with
// the schema left dirty. This fails the build instead.
//
// TestRetentionMigrationsRollBackAndForwardAgain (integration) proves the other
// half: that such a file really does build, and leaves a VALID index.
func TestConcurrentIndexMigrationsAreSingleStatements(t *testing.T) {
	names, err := fs.Glob(migrationsFS, "migrations/*.sql")
	if err != nil {
		t.Fatalf("glob migrations: %v", err)
	}
	var checked int
	for _, name := range names {
		raw, err := fs.ReadFile(migrationsFS, name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		sql := strings.TrimSpace(stripComments(string(raw)))
		if !concurrently.MatchString(sql) {
			continue
		}
		checked++
		if n := strings.Count(sql, ";"); n != 1 || !strings.HasSuffix(sql, ";") {
			t.Errorf("%s uses CONCURRENTLY but holds %d statements; it must be exactly one, or golang-migrate runs it "+
				"in an implicit transaction and Postgres refuses the concurrent build", name, n)
		}
	}
	if checked == 0 {
		t.Fatal("no CONCURRENTLY migration found — the scan is not reading the migrations it thinks it is")
	}
}
