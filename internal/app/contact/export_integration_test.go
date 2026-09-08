//go:build integration

package contact

import (
	"context"
	"encoding/csv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// exportSeed is a contact row for these tests, written directly (like
// search_integration_test.go's seed/insert) so custom_fields is under the
// test's control — the sqlc upsert path defaults it to '{}', which a test
// asserting on stored values cannot use.
type exportSeed struct {
	email, first, last, company string
	// customFields is raw JSON; "" means '{}'.
	customFields string
}

func (f fixture) insertForExport(t *testing.T, ctx context.Context, ws uuid.UUID, seeds []exportSeed) {
	t.Helper()
	for _, s := range seeds {
		cf := s.customFields
		if cf == "" {
			cf = "{}"
		}
		if _, err := f.pool.Exec(ctx,
			`INSERT INTO contacts (workspace_id, email, first_name, last_name, company, custom_fields)
			 VALUES ($1, $2, $3, $4, $5, $6::jsonb)`,
			ws, s.email, s.first, s.last, s.company, cf); err != nil {
			t.Fatalf("insert %s: %v", s.email, err)
		}
	}
}

// drainCSV runs the exact PrepareExport/StreamExport sequence
// exportContactsCSV runs, minus the HTTP framing, and returns the parsed CSV
// records (the header at index 0) — proving the real query path (search.go's
// composed SQL, the real Postgres jsonb column) end to end.
func drainCSV(t *testing.T, ctx context.Context, svc *Service, ws uuid.UUID, req SearchRequest) [][]string {
	t.Helper()
	plan, err := svc.PrepareExport(ctx, ws, req)
	if err != nil {
		t.Fatalf("PrepareExport: %v", err)
	}
	var buf strings.Builder
	w := csv.NewWriter(&buf)
	if err := w.Write(plan.Header()); err != nil {
		t.Fatalf("write header: %v", err)
	}
	if err := svc.StreamExport(ctx, ws, plan, func(r []string) error { return w.Write(r) }); err != nil {
		t.Fatalf("StreamExport: %v", err)
	}
	w.Flush()
	records, err := csv.NewReader(strings.NewReader(buf.String())).ReadAll()
	if err != nil {
		t.Fatalf("parse CSV: %v", err)
	}
	return records
}

// The header row carries the workspace's LIVE custom-field columns and
// nothing else — an archived field's key must not appear — and an empty
// workspace still produces a header-only file rather than nothing at all.
func TestExportContactsHeaderIncludesLiveCustomFieldColumns(t *testing.T) {
	ctx := context.Background()
	f := setup(t, ctx)
	if _, err := f.svc.CreateFieldDef(ctx, f.ws, FieldDefInput{Key: "industry", Label: "Industry", Type: FieldTypeText}); err != nil {
		t.Fatalf("create field: %v", err)
	}
	legacy, err := f.svc.CreateFieldDef(ctx, f.ws, FieldDefInput{Key: "legacy", Label: "Legacy", Type: FieldTypeText})
	if err != nil {
		t.Fatalf("create field: %v", err)
	}
	if _, err := f.svc.ArchiveFieldDef(ctx, f.ws, legacy.ID); err != nil {
		t.Fatalf("archive field: %v", err)
	}

	records := drainCSV(t, ctx, f.svc, f.ws, SearchRequest{})
	if len(records) != 1 {
		t.Fatalf("records = %v, want just the header — this workspace has no contacts", records)
	}
	want := []string{"email", "first_name", "last_name", "company", "industry"}
	if len(records[0]) != len(want) {
		t.Fatalf("header = %v, want %v", records[0], want)
	}
	for i := range want {
		if records[0][i] != want[i] {
			t.Fatalf("header = %v, want %v", records[0], want)
		}
	}
}

// q and list must filter the export exactly as they filter GET /contacts.
func TestExportContactsHonoursQueryAndListFilter(t *testing.T) {
	ctx := context.Background()
	f := setup(t, ctx)
	f.insert(t, ctx, f.ws, []seed{
		{email: "in1@x.test", company: "Acme", createdAt: base(), inList: true},
		{email: "in2@x.test", company: "Acme", createdAt: base().Add(time.Minute), inList: true},
		{email: "out@x.test", company: "Widgets", createdAt: base().Add(2 * time.Minute)},
	})

	all := drainCSV(t, ctx, f.svc, f.ws, SearchRequest{})
	if len(all) != 4 { // header + 3 contacts
		t.Fatalf("records = %v, want a header and all 3 contacts", all)
	}

	byList := drainCSV(t, ctx, f.svc, f.ws, SearchRequest{ListID: &f.list})
	if len(byList) != 3 { // header + 2 list members
		t.Fatalf("list-filtered records = %v, want a header and the 2 list members", byList)
	}
	for _, row := range byList[1:] {
		if row[0] == "out@x.test" {
			t.Fatal("a contact outside the list appeared in the list-filtered export")
		}
	}

	byQuery := drainCSV(t, ctx, f.svc, f.ws, SearchRequest{Q: "widgets"})
	if len(byQuery) != 2 || byQuery[1][0] != "out@x.test" {
		t.Fatalf("q-filtered records = %v, want just out@x.test", byQuery)
	}
}

// TestExportContactsNeverLeaksAnotherWorkspace is the tenant-isolation case —
// the most important test in this file. Both workspaces define a custom field
// under the SAME key and hold a contact sharing the SAME email, so a missing
// workspace_id filter anywhere in the export path (the row query OR the
// custom-field decode) shows up as either a leaked contact or a leaked value.
func TestExportContactsNeverLeaksAnotherWorkspace(t *testing.T) {
	ctx := context.Background()
	f := setup(t, ctx)

	if _, err := f.svc.CreateFieldDef(ctx, f.ws, FieldDefInput{Key: "industry", Label: "Industry", Type: FieldTypeText}); err != nil {
		t.Fatalf("create field (mine): %v", err)
	}
	if _, err := f.svc.CreateFieldDef(ctx, f.other, FieldDefInput{Key: "industry", Label: "Industry", Type: FieldTypeText}); err != nil {
		t.Fatalf("create field (theirs): %v", err)
	}

	f.insertForExport(t, ctx, f.ws, []exportSeed{
		{email: "shared1@acme.com", first: "Sam", last: "Shared", company: "Acme", customFields: `{"industry":"mine-fintech"}`},
		{email: "shared2@acme.com", first: "Sam", last: "Shared", company: "Acme"},
	})
	f.insertForExport(t, ctx, f.other, []exportSeed{
		{email: "shared1@acme.com", first: "Sam", last: "Shared", company: "Acme", customFields: `{"industry":"theirs-secret"}`},
		{email: "theirs@acme.com", first: "Sam", last: "Shared", company: "Acme"},
	})

	records := drainCSV(t, ctx, f.svc, f.ws, SearchRequest{})
	if len(records) != 3 { // header + this workspace's 2 contacts
		t.Fatalf("records = %v, want a header and exactly this workspace's 2 contacts", records)
	}
	industryCol := -1
	for i, h := range records[0] {
		if h == "industry" {
			industryCol = i
		}
	}
	if industryCol == -1 {
		t.Fatalf("header %v is missing the industry column", records[0])
	}
	for _, row := range records[1:] {
		if row[0] == "theirs@acme.com" {
			t.Fatal("another workspace's contact leaked into the export")
		}
		if row[industryCol] == "theirs-secret" {
			t.Fatalf("another workspace's custom field value leaked into the export: %v", row)
		}
		if row[0] == "shared1@acme.com" && row[industryCol] != "mine-fintech" {
			t.Fatalf("shared1's industry = %q, want this workspace's own value mine-fintech", row[industryCol])
		}
	}
}
