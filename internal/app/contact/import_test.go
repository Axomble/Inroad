package contact

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/inroad/inroad/internal/platform/db/gen"
)

type fakeStore struct{ upserts int }

func (f *fakeStore) Upsert(_ context.Context, _ uuid.UUID, _ UpsertInput) (uuid.UUID, bool, error) {
	f.upserts++
	return uuid.New(), true, nil
}
func (f *fakeStore) AddToList(context.Context, uuid.UUID, uuid.UUID) error { return nil }
func (f *fakeStore) ListByList(context.Context, uuid.UUID, uuid.UUID, int32, int32) ([]gen.Contact, error) {
	return nil, nil
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
