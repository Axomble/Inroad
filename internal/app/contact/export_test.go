package contact

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/platform/cursor"
)

// --- ExportPlan / PrepareExport --------------------------------------------

// The header row must include the workspace's own custom-field columns — the
// property that makes the export more than a fixed built-in dump.
func TestExportPlanHeaderIncludesLiveCustomFields(t *testing.T) {
	fields := &fakeFieldStore{defs: []FieldDef{
		{ID: uuid.New(), Key: "industry", Label: "Industry", Type: FieldTypeText},
		{ID: uuid.New(), Key: "renewal", Label: "Renewal", Type: FieldTypeDate},
	}}
	svc := NewService(&fakeStore{}, &fakeChecker{exists: true}, fields)
	plan, err := svc.PrepareExport(context.Background(), testWS, SearchRequest{})
	if err != nil {
		t.Fatalf("PrepareExport: %v", err)
	}
	want := []string{"email", "first_name", "last_name", "company", "industry", "renewal"}
	got := plan.Header()
	if len(got) != len(want) {
		t.Fatalf("header = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("header = %v, want %v", got, want)
		}
	}
}

// An archived field's key must not become a column: import stops matching it
// (mapCustomColumns keys off liveByKey), so a mirror-image export must too.
func TestExportPlanHeaderExcludesArchivedFields(t *testing.T) {
	fields := &fakeFieldStore{defs: []FieldDef{
		{ID: uuid.New(), Key: "legacy", Label: "Legacy", Type: FieldTypeText, ArchivedAt: ptr(fixedNow)},
	}}
	svc := NewService(&fakeStore{}, &fakeChecker{exists: true}, fields)
	plan, err := svc.PrepareExport(context.Background(), testWS, SearchRequest{})
	if err != nil {
		t.Fatalf("PrepareExport: %v", err)
	}
	for _, h := range plan.Header() {
		if h == "legacy" {
			t.Fatalf("header = %v, want the archived field excluded", plan.Header())
		}
	}
}

// PrepareExport must reject the same things Search rejects, the same way, so
// the handler can reuse Search's status mapping.
func TestPrepareExportValidatesLikeSearch(t *testing.T) {
	svc := NewService(&fakeStore{}, &fakeChecker{exists: true}, &fakeFieldStore{})
	if _, err := svc.PrepareExport(context.Background(), testWS, SearchRequest{Q: "a"}); !errors.Is(err, ErrQueryTooShort) {
		t.Fatalf("short query err = %v, want ErrQueryTooShort", err)
	}
	if _, err := svc.PrepareExport(context.Background(), testWS, SearchRequest{Sort: "sideways"}); !errors.Is(err, cursor.ErrUnknownSort) {
		t.Fatalf("bad sort err = %v, want ErrUnknownSort", err)
	}
}

func TestPrepareExportUnknownListIsNotFound(t *testing.T) {
	svc := NewService(&fakeStore{}, &fakeChecker{exists: false}, &fakeFieldStore{})
	_, err := svc.PrepareExport(context.Background(), testWS, SearchRequest{ListID: &testList})
	if !errors.Is(err, ErrListNotFound) {
		t.Fatalf("err = %v, want ErrListNotFound", err)
	}
}

// Cursor/Limit on the request must have no bearing on what gets prepared —
// PrepareExport never reads either field.
func TestPrepareExportIgnoresCursorAndLimit(t *testing.T) {
	svc := NewService(&fakeStore{}, &fakeChecker{exists: true}, &fakeFieldStore{})
	n := 3
	_, err := svc.PrepareExport(context.Background(), testWS, SearchRequest{Cursor: "!!!not-a-cursor", Limit: &n})
	if err != nil {
		t.Fatalf("PrepareExport: %v, want cursor/limit to be irrelevant here", err)
	}
}

// --- StreamExport ------------------------------------------------------------

func exportRow(email, first, last, company string, customFields []byte) SearchRow {
	return SearchRow{ID: uuid.New(), Email: email, FirstName: first, LastName: last, Company: company, CustomFields: customFields}
}

func TestStreamExportEmitsOneRecordPerRowInHeaderOrder(t *testing.T) {
	fields := &fakeFieldStore{defs: []FieldDef{{ID: uuid.New(), Key: "industry", Label: "Industry", Type: FieldTypeText}}}
	store := &fakeStore{exportPages: [][]SearchRow{
		{exportRow("a@x.test", "Alice", "A", "Acme", []byte(`{"industry":"fintech"}`))},
	}}
	svc := NewService(store, &fakeChecker{exists: true}, fields)
	plan, err := svc.PrepareExport(context.Background(), testWS, SearchRequest{})
	if err != nil {
		t.Fatalf("PrepareExport: %v", err)
	}

	var got [][]string
	if err := svc.StreamExport(context.Background(), testWS, plan, func(r []string) error {
		got = append(got, r)
		return nil
	}); err != nil {
		t.Fatalf("StreamExport: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("emitted %d records, want 1", len(got))
	}
	want := []string{"a@x.test", "Alice", "A", "Acme", "fintech"}
	for i := range want {
		if got[0][i] != want[i] {
			t.Fatalf("record = %v, want %v", got[0], want)
		}
	}
}

// A live field with no stored value for a contact renders as an empty cell,
// not an omitted column — every row must have the same shape as the header.
func TestStreamExportRendersMissingCustomValueAsEmptyCell(t *testing.T) {
	fields := &fakeFieldStore{defs: []FieldDef{{ID: uuid.New(), Key: "industry", Label: "Industry", Type: FieldTypeText}}}
	store := &fakeStore{exportPages: [][]SearchRow{{exportRow("a@x.test", "", "", "", nil)}}}
	svc := NewService(store, &fakeChecker{exists: true}, fields)
	plan, err := svc.PrepareExport(context.Background(), testWS, SearchRequest{})
	if err != nil {
		t.Fatalf("PrepareExport: %v", err)
	}
	var got []string
	if err := svc.StreamExport(context.Background(), testWS, plan, func(r []string) error { got = r; return nil }); err != nil {
		t.Fatalf("StreamExport: %v", err)
	}
	if len(got) != 5 || got[4] != "" {
		t.Fatalf("record = %v, want a trailing empty cell for the unset field", got)
	}
}

// The core pagination guarantee: a full page followed by a short one must
// read as two Search calls, the second seeking from a cursor built off the
// first page's LAST row — exactly what Search's own NextCursor does.
func TestStreamExportPagesUntilAShortPage(t *testing.T) {
	page1 := make([]SearchRow, exportPageSize)
	for i := range page1 {
		page1[i] = exportRow("p@x.test", "", "", "", nil)
	}
	page2 := []SearchRow{exportRow("last@x.test", "", "", "", nil)}
	store := &fakeStore{exportPages: [][]SearchRow{page1, page2}}
	svc := NewService(store, &fakeChecker{exists: true}, &fakeFieldStore{})
	plan, err := svc.PrepareExport(context.Background(), testWS, SearchRequest{})
	if err != nil {
		t.Fatalf("PrepareExport: %v", err)
	}

	n := 0
	if err := svc.StreamExport(context.Background(), testWS, plan, func([]string) error { n++; return nil }); err != nil {
		t.Fatalf("StreamExport: %v", err)
	}
	if n != exportPageSize+1 {
		t.Fatalf("emitted %d records, want %d", n, exportPageSize+1)
	}
	if len(store.searchCalls) != 2 {
		t.Fatalf("store.Search called %d times, want 2 (a full page, then the short one)", len(store.searchCalls))
	}
	if store.searchCalls[0].Cur != nil {
		t.Fatal("the first page must not carry a cursor")
	}
	if store.searchCalls[1].Cur == nil {
		t.Fatal("the second page must carry the cursor the first page's last row produced")
	}
}

// A store error mid-export must stop the stream rather than being silently
// discarded or looped past.
func TestStreamExportPropagatesStoreError(t *testing.T) {
	boom := errors.New("boom")
	store := &fakeStore{searchErr: boom, exportPages: [][]SearchRow{{}}}
	svc := NewService(store, &fakeChecker{exists: true}, &fakeFieldStore{})
	plan, err := svc.PrepareExport(context.Background(), testWS, SearchRequest{})
	if err != nil {
		t.Fatalf("PrepareExport: %v", err)
	}
	if err := svc.StreamExport(context.Background(), testWS, plan, func([]string) error { return nil }); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want boom", err)
	}
}

// emit's own error (standing in for a broken HTTP connection) must stop the
// stream immediately rather than continuing to render further rows.
func TestStreamExportStopsOnEmitError(t *testing.T) {
	store := &fakeStore{exportPages: [][]SearchRow{
		{exportRow("a@x.test", "", "", "", nil), exportRow("b@x.test", "", "", "", nil)},
	}}
	svc := NewService(store, &fakeChecker{exists: true}, &fakeFieldStore{})
	plan, err := svc.PrepareExport(context.Background(), testWS, SearchRequest{})
	if err != nil {
		t.Fatalf("PrepareExport: %v", err)
	}
	boom := errors.New("write failed")
	n := 0
	err = svc.StreamExport(context.Background(), testWS, plan, func([]string) error {
		n++
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want boom", err)
	}
	if n != 1 {
		t.Fatalf("emit called %d times, want exactly 1 (stop at the first failure)", n)
	}
}

// Empty result: StreamExport must simply emit nothing (the header row is the
// handler's job, done unconditionally before StreamExport ever runs).
func TestStreamExportEmptyResultEmitsNothing(t *testing.T) {
	store := &fakeStore{exportPages: [][]SearchRow{{}}}
	svc := NewService(store, &fakeChecker{exists: true}, &fakeFieldStore{})
	plan, err := svc.PrepareExport(context.Background(), testWS, SearchRequest{})
	if err != nil {
		t.Fatalf("PrepareExport: %v", err)
	}
	n := 0
	if err := svc.StreamExport(context.Background(), testWS, plan, func([]string) error { n++; return nil }); err != nil {
		t.Fatalf("StreamExport: %v", err)
	}
	if n != 0 {
		t.Fatalf("emitted %d records, want 0", n)
	}
}
