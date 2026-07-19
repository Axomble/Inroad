package list

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/inroad/inroad/internal/platform/db/gen"
)

type fakeStore struct{ created gen.List }

func (f *fakeStore) Create(_ context.Context, ws uuid.UUID, name string) (gen.List, error) {
	f.created = gen.List{ID: uuid.New(), WorkspaceID: ws, Name: name}
	return f.created, nil
}
func (f *fakeStore) List(context.Context, uuid.UUID) ([]gen.List, error) { return []gen.List{f.created}, nil }
func (f *fakeStore) Get(context.Context, uuid.UUID, uuid.UUID) (gen.List, error) { return f.created, nil }
func (f *fakeStore) CountMembers(context.Context, uuid.UUID) (int64, error) { return 0, nil }

func TestCreateList(t *testing.T) {
	svc := NewService(&fakeStore{})
	l, err := svc.Create(context.Background(), uuid.New(), "Prospects")
	if err != nil || l.Name != "Prospects" {
		t.Fatalf("Create: %v %+v", err, l)
	}
}
