package contact

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// fakeStore is the whole persistence dependency of this domain, so the service
// is exercised without a database. searchRows/searchErr and countN/countErr are
// what the search tests script; importing only touches upserts.
type fakeStore struct {
	upserts int

	searchRows []SearchRow
	searchErr  error
	lastSearch SearchParams

	countN     int64
	countErr   error
	lastFilter SearchFilter
}

func (f *fakeStore) Upsert(_ context.Context, _ uuid.UUID, _ UpsertInput) (uuid.UUID, bool, error) {
	f.upserts++
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
	svc := &Service{store: &fakeStore{}}
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
	svc := &Service{store: &fakeStore{}}
	if _, err := svc.importRows(context.Background(), uuid.New(), uuid.New(), strings.NewReader("name\nAlice\n")); err == nil {
		t.Fatal("expected error for missing email column")
	}
}
