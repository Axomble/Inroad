package db

import (
	"io/fs"
	"regexp"
	"sort"
	"testing"
	"time"
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
// Two eras, deliberately, and the boundary is frozen.
//
// SEQUENTIAL (6 digits) is every migration up to legacyVersionCeiling. Those are
// applied in deployed databases and recorded by version in schema_migrations, so
// renaming one would strand every installation that already ran it. They stay.
//
// TIMESTAMP (14 digits, YYYYMMDDHHMMSS) is every migration from here on, and the
// reason is that sequential numbering plus parallel branches is a collision machine.
// Each branch picks max+1 when it is CREATED; merge order decides who was actually
// right. Both PRs are individually valid and the collision exists only in the union,
// which is why a guard running on a branch cannot see it — it has no way to know what
// another open PR is about to claim.
//
// That is not hypothetical here. It has taken main down five times: the 000057
// outage this file was written for, and four more in one week (000069 twice, then
// 000071 twice — the second of those was a renumbering FIX that collided in turn).
// golang-migrate refuses to initialise at all when two files claim a version, so each
// one stops every migration, every database-backed test, and every fresh deploy.
//
// Timestamps make merge order irrelevant: two branches created a second apart already
// hold different versions, and no coordination is required between them.
var migrationName = regexp.MustCompile(`^(\d{6}|\d{14})_([a-z0-9_]+)\.(up|down)\.sql$`)

// legacyVersionCeiling is the last sequential migration. Nothing at or below it may
// be renamed; nothing new may be added below it.
const legacyVersionCeiling = "000071"

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

// The sequential era must stay contiguous, and nothing new may join it.
//
// A gap inside the legacy block almost always means a migration was deleted after
// being applied somewhere, which strands that installation on a version this build no
// longer contains. That check is worth keeping for the block where it still applies.
//
// It deliberately does NOT extend to timestamps: gaps there are the normal state of
// affairs, since two developers working in the same week produce versions minutes or
// days apart. Losing gap-detection for new migrations is the price of collision
// immunity, and it is the right trade — a deleted-after-applied migration is rare and
// recoverable, while a version collision takes main down for everyone and has done so
// five times.
func TestTheSequentialEraIsContiguousAndClosed(t *testing.T) {
	legacy := map[string]bool{}
	for _, f := range readMigrationFiles(t) {
		if len(f.version) != 6 {
			continue
		}
		if f.version > legacyVersionCeiling {
			t.Errorf("%s_%s is a NEW sequential migration above the frozen ceiling %s — "+
				"sequential numbering is what collides; use a YYYYMMDDHHMMSS version",
				f.version, f.name, legacyVersionCeiling)
		}
		legacy[f.version] = true
	}

	ordered := make([]string, 0, len(legacy))
	for v := range legacy {
		ordered = append(ordered, v)
	}
	sort.Strings(ordered)
	for i := 1; i < len(ordered); i++ {
		prev, cur := ordered[i-1], ordered[i]
		if next := increment(prev); next != cur {
			t.Errorf("version %s follows %s — expected %s; a gap in the sequential era "+
				"usually means a migration was deleted after being applied, stranding "+
				"installations on a version this build no longer has", cur, prev, next)
		}
	}
}

// A timestamp version must be a real instant, and must sort above every sequential
// one so the two eras compose into a single increasing order.
//
// The plausibility check is not pedantry: a fat-fingered 20261301... would sort fine
// and parse fine, and would then be a permanent lie about when the migration was
// written — which is the only thing a timestamp version carries that a counter does
// not.
func TestTimestampVersionsAreRealInstantsAboveTheLegacyCeiling(t *testing.T) {
	for _, f := range readMigrationFiles(t) {
		if len(f.version) != 14 {
			continue
		}
		if f.version <= legacyVersionCeiling {
			t.Errorf("%s_%s sorts at or below the sequential ceiling %s, so migrate would run "+
				"it before migrations written years earlier", f.version, f.name, legacyVersionCeiling)
		}
		if _, err := time.Parse("20060102150405", f.version); err != nil {
			t.Errorf("%s_%s is not a YYYYMMDDHHMMSS instant: %v", f.version, f.name, err)
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
