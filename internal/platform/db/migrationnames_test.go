package db

import (
	"io/fs"
	"regexp"
	"sort"
	"testing"
)

// Migration filenames are a correctness surface, not a naming convention.
//
// golang-migrate refuses to initialise at all when two files claim the same
// version — not the offending migration, the WHOLE migrator. So a duplicate does
// not break one feature, it stops every migration and every database-backed test,
// and a fresh deploy cannot come up. It happened: two PRs branched from the same
// highest version, both added 000057, and main could not migrate from the moment
// they merged until someone renumbered.
//
// Nothing catches it before merge, because each PR is individually valid — the
// collision only exists in the union. A test over the EMBEDDED filesystem is the
// cheapest guard: it needs no database, it runs in `go test ./...` like everything
// else, and it reads exactly the bytes that ship (migrate.go's migrationsFS), not a
// directory listing that could drift from the embed pattern.
var migrationName = regexp.MustCompile(`^(\d{6})_([a-z0-9_]+)\.(up|down)\.sql$`)

type migrationFile struct {
	version   string
	name      string
	direction string
}

func readMigrationFiles(t *testing.T) []migrationFile {
	t.Helper()
	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		t.Fatalf("read embedded migrations: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("no embedded migrations found — the embed pattern is not matching")
	}
	out := make([]migrationFile, 0, len(entries))
	for _, e := range entries {
		m := migrationName.FindStringSubmatch(e.Name())
		if m == nil {
			t.Errorf("migration %q does not match NNNNNN_snake_name.(up|down).sql; "+
				"golang-migrate parses the version out of the filename, so a typo here "+
				"is silently a different migration", e.Name())
			continue
		}
		out = append(out, migrationFile{version: m[1], name: m[2], direction: m[3]})
	}
	return out
}

// The guard that would have prevented the outage.
func TestNoTwoMigrationsClaimTheSameVersion(t *testing.T) {
	namesByVersion := map[string]map[string]bool{}
	for _, f := range readMigrationFiles(t) {
		if namesByVersion[f.version] == nil {
			namesByVersion[f.version] = map[string]bool{}
		}
		namesByVersion[f.version][f.name] = true
	}
	for version, names := range namesByVersion {
		if len(names) > 1 {
			distinct := make([]string, 0, len(names))
			for n := range names {
				distinct = append(distinct, n)
			}
			sort.Strings(distinct)
			t.Errorf("version %s is claimed by %d different migrations (%v) — golang-migrate "+
				"will refuse to initialise AT ALL, so every migration and every "+
				"database-backed test fails until one is renumbered", version, len(distinct), distinct)
		}
	}
}

// A version with only one direction is the other way a migration silently misbehaves:
// an up with no down cannot be rolled back, and a down with no up never runs.
func TestEveryMigrationHasBothDirections(t *testing.T) {
	seen := map[string]map[string]bool{}
	for _, f := range readMigrationFiles(t) {
		key := f.version + "_" + f.name
		if seen[key] == nil {
			seen[key] = map[string]bool{}
		}
		if seen[key][f.direction] {
			t.Errorf("%s has two %s files", key, f.direction)
		}
		seen[key][f.direction] = true
	}
	for key, dirs := range seen {
		if !dirs["up"] {
			t.Errorf("%s has a down migration with no up — it can never run", key)
		}
		if !dirs["down"] {
			t.Errorf("%s has an up migration with no down — it cannot be rolled back", key)
		}
	}
}

// Versions should advance without gaps. A gap is not fatal to golang-migrate, but it
// almost always means a migration was deleted after being applied somewhere, which
// leaves that installation on a version this build no longer contains.
func TestMigrationVersionsAreContiguous(t *testing.T) {
	versions := map[string]bool{}
	for _, f := range readMigrationFiles(t) {
		versions[f.version] = true
	}
	ordered := make([]string, 0, len(versions))
	for v := range versions {
		ordered = append(ordered, v)
	}
	sort.Strings(ordered)

	for i := 1; i < len(ordered); i++ {
		prev, cur := ordered[i-1], ordered[i]
		if next := increment(prev); next != cur {
			t.Errorf("version %s follows %s — expected %s; a gap usually means a migration "+
				"was deleted after being applied, stranding installations on a version this "+
				"build no longer has", cur, prev, next)
		}
	}
}

// increment adds one to a zero-padded six-digit version, preserving the width.
func increment(version string) string {
	digits := []byte(version)
	for i := len(digits) - 1; i >= 0; i-- {
		if digits[i] != '9' {
			digits[i]++
			return string(digits)
		}
		digits[i] = '0'
	}
	return string(digits)
}
