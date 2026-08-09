package contact

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// fakeStore is the whole persistence dependency of this domain, so the service
// is exercised without a database. searchRows/searchErr and countN/countErr are
// what the search tests script; importing only touches upserts.
type fakeStore struct {
	upserts int
	// upserted keeps every input the import produced, which is the only way to
	// assert what the custom-field column mapping actually wrote.
	upserted []UpsertInput

	searchRows []SearchRow
	searchErr  error
	lastSearch SearchParams

	countN     int64
	countErr   error
	lastFilter SearchFilter

	// record holds the record-page reads; its methods live in record_test.go.
	record recordFake
}

func (f *fakeStore) Upsert(_ context.Context, _ uuid.UUID, in UpsertInput) (uuid.UUID, bool, error) {
	f.upserts++
	f.upserted = append(f.upserted, in)
	return uuid.New(), true, nil
}
func (f *fakeStore) AddToList(context.Context, uuid.UUID, uuid.UUID) error { return nil }

func (f *fakeStore) Search(_ context.Context, _ uuid.UUID, p SearchParams) ([]SearchRow, error) {
	f.lastSearch = p
	return f.searchRows, f.searchErr
}

func (f *fakeStore) CountMatches(_ context.Context, _ uuid.UUID, filter SearchFilter, _ int) (int64, error) {
	f.lastFilter = filter
	return f.countN, f.countErr
}

func TestImportCSVParsesHeaderAndSkipsBadRows(t *testing.T) {
	svc := &Service{store: &fakeStore{}, fields: &fakeFieldStore{}}
	csv := "email,first_name\nalice@x.com,Alice\nnot-an-email,Bob\nbob@x.com,Bob\n"
	res, err := svc.importRows(context.Background(), uuid.New(), uuid.New(), strings.NewReader(csv))
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if res.Imported != 2 || res.Skipped != 1 {
		t.Fatalf("got %+v, want Imported=2 Skipped=1", res)
	}
}

func TestImportCSVRejectsMissingEmailColumn(t *testing.T) {
	svc := &Service{store: &fakeStore{}, fields: &fakeFieldStore{}}
	if _, err := svc.importRows(context.Background(), uuid.New(), uuid.New(), strings.NewReader("name\nAlice\n")); err == nil {
		t.Fatal("expected error for missing email column")
	}
}

// --- custom field column mapping -----------------------------------------

// importFixture wires an import over a fixed set of live definitions.
func importFixture(defs ...FieldDef) (*Service, *fakeStore) {
	store := &fakeStore{}
	return &Service{store: store, checker: &fakeChecker{exists: true}, fields: &fakeFieldStore{defs: defs}}, store
}

// The end-to-end claim of issue #62: a CSV column named after a custom field
// lands in the contact's stored values instead of being discarded.
func TestImportCSVMapsUnknownColumnsToCustomFields(t *testing.T) {
	svc, store := importFixture(
		FieldDef{ID: uuid.New(), Key: "industry", Label: "Industry", Type: FieldTypeText},
		FieldDef{ID: uuid.New(), Key: "renewal", Label: "Renewal", Type: FieldTypeDate},
	)
	csv := "email,first_name,Industry,renewal\nalice@x.com,Alice,fintech,2026-08-09\n"

	res, err := svc.importRows(context.Background(), uuid.New(), uuid.New(), strings.NewReader(csv))
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if len(store.upserted) != 1 {
		t.Fatalf("upserts = %d, want 1", len(store.upserted))
	}
	var got map[string]string
	if err := json.Unmarshal(store.upserted[0].CustomFields, &got); err != nil {
		t.Fatalf("decode custom fields: %v", err)
	}
	// The header is "Industry" but the key is "industry": operators export CSVs
	// from other tools and should not have to re-case their headers.
	if got["industry"] != "fintech" || got["renewal"] != "2026-08-09" {
		t.Errorf("custom fields = %v, want industry=fintech renewal=2026-08-09", got)
	}
	if len(res.MappedFields) != 2 {
		t.Errorf("MappedFields = %v, want both keys reported", res.MappedFields)
	}
}

// The old behaviour was to drop these silently, which is what made a mis-named
// column impossible to diagnose.
func TestImportCSVReportsColumnsItCouldNotMap(t *testing.T) {
	svc, _ := importFixture(FieldDef{ID: uuid.New(), Key: "industry", Label: "Industry", Type: FieldTypeText})
	csv := "email,industry,favourite_colour\nalice@x.com,fintech,blue\n"

	res, err := svc.importRows(context.Background(), uuid.New(), uuid.New(), strings.NewReader(csv))
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if len(res.IgnoredColumns) != 1 || res.IgnoredColumns[0] != "favourite_colour" {
		t.Errorf("IgnoredColumns = %v, want [favourite_colour]", res.IgnoredColumns)
	}
}

// One bad cell must not cost the contact: the row imports, the value is
// dropped, and the count says so.
func TestImportCSVCountsInvalidValuesButStillImportsTheRow(t *testing.T) {
	svc, store := importFixture(FieldDef{ID: uuid.New(), Key: "renewal", Label: "Renewal", Type: FieldTypeDate})
	csv := "email,renewal\nalice@x.com,next tuesday\n"

	res, err := svc.importRows(context.Background(), uuid.New(), uuid.New(), strings.NewReader(csv))
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if res.Imported != 1 || res.InvalidValues != 1 {
		t.Fatalf("got %+v, want Imported=1 InvalidValues=1", res)
	}
	var got map[string]string
	if err := json.Unmarshal(store.upserted[0].CustomFields, &got); err != nil {
		t.Fatalf("decode custom fields: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("custom fields = %v, want the rejected value omitted", got)
	}
}

// A blank cell is omitted rather than written as "". UpsertContact MERGES this
// object, so writing "" would let a partial CSV erase enrichment an earlier
// import supplied.
func TestImportCSVOmitsBlankCellsSoAMergeCannotErase(t *testing.T) {
	svc, store := importFixture(FieldDef{ID: uuid.New(), Key: "industry", Label: "Industry", Type: FieldTypeText})
	csv := "email,industry\nalice@x.com,\n"

	if _, err := svc.importRows(context.Background(), uuid.New(), uuid.New(), strings.NewReader(csv)); err != nil {
		t.Fatalf("import: %v", err)
	}
	var got map[string]string
	if err := json.Unmarshal(store.upserted[0].CustomFields, &got); err != nil {
		t.Fatalf("decode custom fields: %v", err)
	}
	if _, present := got["industry"]; present {
		t.Errorf("custom fields = %v, want the blank cell omitted entirely", got)
	}
}

// An archived field's key is not a mapping target: nothing new accumulates
// under a retired field.
func TestImportCSVDoesNotMapToArchivedFields(t *testing.T) {
	archived := FieldDef{ID: uuid.New(), Key: "legacy", Label: "Legacy", Type: FieldTypeText, ArchivedAt: ptr(fixedNow)}
	svc, store := importFixture(archived)
	csv := "email,legacy\nalice@x.com,value\n"

	res, err := svc.importRows(context.Background(), uuid.New(), uuid.New(), strings.NewReader(csv))
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	var got map[string]string
	if err := json.Unmarshal(store.upserted[0].CustomFields, &got); err != nil {
		t.Fatalf("decode custom fields: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("custom fields = %v, want nothing written to an archived key", got)
	}
	if len(res.IgnoredColumns) != 1 {
		t.Errorf("IgnoredColumns = %v, want the archived column reported as unmapped", res.IgnoredColumns)
	}
}

// A workspace field named "email" must not be able to take over the address
// column — the built-in names win.
func TestImportCSVBuiltinColumnsOutrankCustomFields(t *testing.T) {
	svc, store := importFixture(FieldDef{ID: uuid.New(), Key: "company", Label: "Company", Type: FieldTypeText})
	csv := "email,company\nalice@x.com,Acme\n"

	if _, err := svc.importRows(context.Background(), uuid.New(), uuid.New(), strings.NewReader(csv)); err != nil {
		t.Fatalf("import: %v", err)
	}
	in := store.upserted[0]
	if in.Company != "Acme" {
		t.Errorf("Company = %q, want the built-in column to win", in.Company)
	}
	var got map[string]string
	if err := json.Unmarshal(in.CustomFields, &got); err != nil {
		t.Fatalf("decode custom fields: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("custom fields = %v, want the built-in column not duplicated into them", got)
	}
}
