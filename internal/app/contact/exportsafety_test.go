package contact

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// --- CSV formula injection (both directions) --------------------------------

// Every cell this endpoint writes is attacker-influenceable: email, first/last
// name, company and every custom-field value all arrive through import or the
// API. Excel and Google Sheets execute a cell whose first character is one of
// = + - @ TAB CR, so the export prefixes those with a single apostrophe — the
// marker both applications read as "this is text".
//
// The escape is undone on IMPORT, because the OpenAPI description promises a
// file this endpoint produces re-imports unchanged, and naive escaping would
// otherwise corrupt real data: a phone number in a custom field genuinely
// starts with "+".
func TestExportEscapesEveryFormulaTriggerAndImportUndoesIt(t *testing.T) {
	// One case per trigger character, plus the shapes that must NOT change.
	cases := map[string]struct {
		value        string
		wantExported string
	}{
		"equals":                {"=1+1", "'=1+1"},
		"plus (a phone number)": {"+1-555-0100", "'+1-555-0100"},
		"minus (a negative)":    {"-5", "'-5"},
		"at (a DDE payload)":    {"@SUM(A1)", "'@SUM(A1)"},
		"tab":                   {"\t=1+1", "'\t=1+1"},
		"carriage return":       {"\r=1+1", "'\r=1+1"},
		"ordinary text":         {"Acme", "Acme"},
		"empty":                 {"", ""},
		// A genuine leading apostrophe is DATA, and must survive the round trip.
		// PRECEDENCE: the escape marker wins, so a value that would be read back
		// as an escape gets one more apostrophe than it started with.
		"apostrophe then text":    {"'tis", "'tis"},
		"apostrophe then equals":  {"'=1+1", "''=1+1"},
		"two apostrophes, equals": {"''=1+1", "'''=1+1"},
		"equals mid-value":        {"a=1+1", "a=1+1"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := escapeFormula(tc.value); got != tc.wantExported {
				t.Fatalf("escapeFormula(%q) = %q, want %q", tc.value, got, tc.wantExported)
			}
			// The round trip is the whole point: import must return exactly what
			// export was given, for every one of these.
			if got := unescapeFormula(tc.wantExported); got != tc.value {
				t.Errorf("unescapeFormula(%q) = %q, want %q", tc.wantExported, got, tc.value)
			}
		})
	}
}

// A file from ANOTHER system is not one of ours, so import must not invent an
// escape that was never applied: a bare formula stays a bare formula (we store
// text, not spreadsheet cells), and a leading apostrophe not guarding a trigger
// is data.
func TestImportOnlyUndoesAnApostropheThatGuardsATrigger(t *testing.T) {
	cases := map[string]string{
		"=1+1":  "=1+1", // never escaped, so nothing to undo
		"'tis":  "'tis", // an apostrophe over ordinary text is data
		"'":     "'",    // nothing behind it
		"''":    "''",   // still nothing behind it
		"'=1+1": "=1+1", // exactly one marker stripped
		"Acme":  "Acme",
	}
	for in, want := range cases {
		if got := unescapeFormula(in); got != want {
			t.Errorf("unescapeFormula(%q) = %q, want %q", in, got, want)
		}
	}
}

// The escape must reach every column, not just the built-ins — a custom-field
// value is the easiest of the five for an attacker to control.
func TestStreamExportEscapesFormulasInBuiltinAndCustomColumns(t *testing.T) {
	fields := &fakeFieldStore{defs: []FieldDef{{ID: uuid.New(), Key: "note", Label: "Note", Type: FieldTypeText}}}
	store := &fakeStore{exportPages: [][]SearchRow{{
		exportRow("=cmd@x.test", "+Alice", "-A", "@Acme", []byte(`{"note":"=HYPERLINK(\"http://evil\")"}`)),
	}}}
	svc := NewService(store, &fakeChecker{exists: true}, fields)
	plan, err := svc.PrepareExport(context.Background(), testWS, SearchRequest{})
	if err != nil {
		t.Fatalf("PrepareExport: %v", err)
	}

	var got []string
	if err := svc.StreamExport(context.Background(), testWS, plan, func(r []string) error { got = r; return nil }); err != nil {
		t.Fatalf("StreamExport: %v", err)
	}
	want := []string{"'=cmd@x.test", "'+Alice", "'-A", "'@Acme", `'=HYPERLINK("http://evil")`}
	if len(got) != len(want) {
		t.Fatalf("record = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("record[%d] = %q, want %q (whole record %q)", i, got[i], want[i], got)
		}
	}
}

// The round trip end to end, through the real CSV writer and the real importer:
// what comes out of the export goes back in unchanged. This is the assertion
// that would break if either half of the escaping were added without the other.
func TestExportedFormulaValuesReImportUnchanged(t *testing.T) {
	const (
		phone   = "+1-555-0100"
		company = "=cmd|'/c calc'!A1"
	)
	fields := &fakeFieldStore{defs: []FieldDef{{ID: uuid.New(), Key: "phone", Label: "Phone", Type: FieldTypeText}}}
	store := &fakeStore{searchRows: []SearchRow{{
		ID: uuid.New(), Email: "alice@x.test", FirstName: "Alice", LastName: "A",
		Company: company, CustomFields: []byte(`{"phone":"` + phone + `"}`),
	}}}
	h := NewHandler(NewService(store, &fakeChecker{exists: true}, fields))

	w := serveExport(t, h, "")
	if w.Code != 200 {
		t.Fatalf("status = %d, want 200 (%s)", w.Code, w.Body.String())
	}
	// The bytes on the wire really are defused.
	records, err := csv.NewReader(strings.NewReader(w.Body.String())).ReadAll()
	if err != nil {
		t.Fatalf("export is not valid CSV: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("records = %v, want a header and one row", records)
	}
	if records[1][3] != "'"+company || records[1][4] != "'"+phone {
		t.Fatalf("row = %q, want company/phone apostrophe-prefixed", records[1])
	}

	// And feeding them straight back gives the original values.
	svc2, store2 := importFixture(FieldDef{ID: uuid.New(), Key: "phone", Label: "Phone", Type: FieldTypeText})
	if _, err := svc2.importRows(context.Background(), testWS, testList, bytes.NewReader(w.Body.Bytes())); err != nil {
		t.Fatalf("re-import: %v", err)
	}
	if len(store2.upserted) != 1 {
		t.Fatalf("upserted %d contacts, want 1", len(store2.upserted))
	}
	in := store2.upserted[0]
	if in.Company != company {
		t.Errorf("Company = %q, want %q — the round trip the contract promises", in.Company, company)
	}
	if !strings.Contains(string(in.CustomFields), phone) {
		t.Errorf("custom fields = %s, want the phone number restored to %q", in.CustomFields, phone)
	}
}

// --- a truncated stream ------------------------------------------------------

// A mid-stream failure cannot change the status: the header row is on the wire
// so the response is committed to 200. It CAN be recorded, and it must be.
//
// This is not campaign.ResultsCSV's dead-connection case — that one materialises
// its whole result set before writing, so a write error there really is a gone
// client. StreamExport queries Postgres DURING the response, so a pool
// exhaustion or statement timeout produces a file short by an arbitrary number
// of contacts that is byte-for-byte indistinguishable from a complete one. The
// operator's next move is to import it somewhere, and nothing anywhere said the
// download was incomplete.
func TestExportContactsCSVRecordsAMidStreamTruncation(t *testing.T) {
	var logs bytes.Buffer
	restore := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(restore) })

	page := make([]SearchRow, exportPageSize)
	for i := range page {
		page[i] = exportRow("p@x.test", "", "", "", nil)
	}
	store := &fakeStore{
		exportPages:    [][]SearchRow{page},
		searchErr:      errors.New("timeout: canceling statement due to statement timeout"),
		searchErrAfter: 1, // the first page succeeds; the second query dies
	}

	w := serveExport(t, newHandler(store, true), "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 — the status is committed once the header row is written", w.Code)
	}

	logged := logs.String()
	if !strings.Contains(logged, "contact_export_truncated") {
		t.Fatalf("a truncated export must be recorded, got: %s", logged)
	}
	if !strings.Contains(logged, testWS.String()) {
		t.Errorf("the log must name the workspace, got: %s", logged)
	}
	// The row count is what tells an operator (and the person holding the file)
	// how much of it is real.
	if !strings.Contains(logged, `"rows_emitted":500`) {
		t.Errorf("the log must say how many rows the client got, got: %s", logged)
	}

	// Flushed on the way out: the rows the client was already told about must not
	// be dropped by the buffer on an error path.
	records, err := csv.NewReader(strings.NewReader(w.Body.String())).ReadAll()
	if err != nil {
		t.Fatalf("the truncated body must still be valid CSV: %v", err)
	}
	if len(records) != exportPageSize+1 {
		t.Errorf("records = %d, want %d (header + every row already emitted)", len(records), exportPageSize+1)
	}
}

// --- a custom field named after a built-in column ---------------------------

// normalizeKey does not reserve the built-in column names, so a workspace
// created before this guard can hold a custom field keyed "email". Header()
// would then emit "email" twice and import's `col[h] = i` (last wins) would map
// the CUSTOM column over the address column — every exported file would
// re-import with the wrong addresses. The export skips the collision instead,
// which is exactly what import already does (mapCustomColumns).
func TestExportSkipsACustomFieldNamedAfterABuiltinColumn(t *testing.T) {
	fields := &fakeFieldStore{defs: []FieldDef{
		{ID: uuid.New(), Key: "email", Label: "Email (legacy)", Type: FieldTypeText},
		{ID: uuid.New(), Key: "company", Label: "Company (legacy)", Type: FieldTypeText},
		{ID: uuid.New(), Key: "industry", Label: "Industry", Type: FieldTypeText},
	}}
	store := &fakeStore{exportPages: [][]SearchRow{{
		exportRow("a@x.test", "Alice", "A", "Acme", []byte(`{"email":"hijack@evil.test","industry":"fintech"}`)),
	}}}
	svc := NewService(store, &fakeChecker{exists: true}, fields)
	plan, err := svc.PrepareExport(context.Background(), testWS, SearchRequest{})
	if err != nil {
		t.Fatalf("PrepareExport: %v", err)
	}

	want := []string{"email", "first_name", "last_name", "company", "industry"}
	got := plan.Header()
	if len(got) != len(want) {
		t.Fatalf("header = %v, want %v (no built-in name repeated)", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("header = %v, want %v", got, want)
		}
	}

	var rec []string
	if err := svc.StreamExport(context.Background(), testWS, plan, func(r []string) error { rec = r; return nil }); err != nil {
		t.Fatalf("StreamExport: %v", err)
	}
	if len(rec) != len(want) {
		t.Fatalf("record = %v, want %d cells to match the header", rec, len(want))
	}
	if rec[0] != "a@x.test" {
		t.Errorf("record[0] = %q, want the contact's own address column", rec[0])
	}
}

// The other half: stop new ones being created at all, so the skip above only
// ever has to cover workspaces that predate this.
func TestCreateFieldDefRefusesABuiltinColumnName(t *testing.T) {
	svc := fieldSvc(&fakeFieldStore{})
	for _, key := range builtinColumns {
		t.Run(key, func(t *testing.T) {
			_, err := svc.CreateFieldDef(context.Background(), testWS, FieldDefInput{
				Key: key, Label: "X", Type: FieldTypeText,
			})
			var invalid *InvalidFieldError
			if !errors.As(err, &invalid) {
				t.Fatalf("err = %v, want an *InvalidFieldError naming the reserved key", err)
			}
			if invalid.Key != key {
				t.Errorf("error key = %q, want %q", invalid.Key, key)
			}
		})
	}
	// A key that merely CONTAINS a built-in name is fine — the reservation is
	// the exact set of header names, not a substring rule.
	if _, err := svc.CreateFieldDef(context.Background(), testWS, FieldDefInput{
		Key: "email_verified", Label: "Verified", Type: FieldTypeText,
	}); err != nil {
		t.Fatalf("email_verified must still be allowed, got %v", err)
	}
}
