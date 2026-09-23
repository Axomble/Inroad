package db

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// Reading tracking_events DIRECTLY silently loses every event older than the
// tracking retention window, because retention rolls those rows up into
// tracking_event_rollups and deletes them (migration 20260923110315). A reader
// that should see them — anything that counts, ranks, or decides on opens and
// clicks — must read the tracking_engagement view instead.
//
// The failure mode is the reason for a test rather than a comment. A query that
// reads the raw table passes every test in the repo, because no fixture has ever
// been rolled up; it only goes wrong in production, months after the operator
// enabled retention, as an open rate that fell or — for a sequence branch on
// "opened within N days" — as enrollments quietly taking the wrong branch and
// being sent the wrong email.
//
// So every reference to tracking_events in a query file or in Go SQL outside
// tests is listed here with its reason. A new one fails this test until someone
// either moves it onto tracking_engagement or adds it with a reason that
// explains why the rolled-up rows genuinely do not matter to it.
var rawTrackingReaders = map[string]string{
	// queries/*.sql, keyed file:Query.
	"tracking.sql:InsertTrackingEvent": "the writer.",
	"tracking.sql:CountRecentSendOpensFromSubnet": "the bot classifier's burst rule: needs client_ip (the rollup drops it) and looks back " +
		"botfilter.BurstWindow (10 minutes), far inside the shortest tracking window retention accepts (30 days).",
	"retention.sql:RollupTrackingEvents": "retention itself: the statement that moves raw rows into the rollup.",
	"retention.sql:PurgeSends":           "retention itself: deletes a deleted send's raw events.",
	// Go SQL outside queries/, keyed by repo-relative path.
	"internal/app/crm/integration_store.go": "the CRM deal activity feed (ListEvents) lists individual open/click EVENTS; a rollup cannot " +
		"preserve an event log, so rolled-up events leave the feed by design (documented in the deploy docs).",
	"internal/sandbox/store.go": "the sandbox seeder WRITES fixture events, and its NOT EXISTS guards its own re-run; it reads nothing reported.",
}

var rawTrackingRef = regexp.MustCompile(`(?i)\b(?:FROM|JOIN|INTO|UPDATE)\s+(?:ONLY\s+)?tracking_events\b`)

func TestNothingReadsRawTrackingEventsOutsideTheAllowlist(t *testing.T) {
	found := map[string]bool{}

	for _, q := range namedQueries(t) {
		if rawTrackingRef.MatchString(stripComments(q.body)) {
			found[q.file+":"+q.name] = true
		}
	}

	root := filepath.Join("..", "..", "..")
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case "gen", "web", "node_modules", ".git", "docs", ".claude":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if rawTrackingRef.MatchString(stripGoComments(string(src))) {
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			found[filepath.ToSlash(rel)] = true
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk source tree: %v", err)
	}
	if len(found) == 0 {
		t.Fatal("found no reference to tracking_events at all — the scan is not reading what it thinks it is, so it would pass vacuously")
	}

	var unexpected, stale []string
	for ref := range found {
		if _, ok := rawTrackingReaders[ref]; !ok {
			unexpected = append(unexpected, ref)
		}
	}
	for ref := range rawTrackingReaders {
		if !found[ref] {
			stale = append(stale, ref)
		}
	}
	sort.Strings(unexpected)
	sort.Strings(stale)
	for _, ref := range unexpected {
		t.Errorf("%s reads tracking_events directly. Once tracking retention is enabled that table no longer holds "+
			"events older than the window — read tracking_engagement (raw + rolled up) instead, or, if the rolled-up "+
			"rows genuinely do not matter here, add it to rawTrackingReaders with the reason.", ref)
	}
	for _, ref := range stale {
		t.Errorf("rawTrackingReaders lists %s, which no longer reads tracking_events — remove the entry", ref)
	}
}

// stripGoComments drops // line comments, so prose that mentions the table (as
// this repository's comments often do) is not mistaken for a query. SQL lives in
// string literals, which this leaves alone.
func stripGoComments(src string) string {
	var b strings.Builder
	for _, line := range strings.Split(src, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "//") {
			continue
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}
