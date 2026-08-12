package list

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/inroad/inroad/internal/platform/db/gen"
)

// fakeStore is an in-memory Store: happy-path answers by default, with
// injectable errors so the service's translation branches are reachable
// without a database.
type fakeStore struct {
	created     gen.List
	renameErr   error
	deleteErr   error
	renameCalls int
	deleteCalls int
}

func (f *fakeStore) Create(_ context.Context, ws uuid.UUID, name string) (gen.List, error) {
	f.created = gen.List{ID: uuid.New(), WorkspaceID: ws, Name: name}
	return f.created, nil
}
func (f *fakeStore) List(context.Context, uuid.UUID) ([]gen.ListListsRow, error) {
	return []gen.ListListsRow{{
		ID: f.created.ID, WorkspaceID: f.created.WorkspaceID, Name: f.created.Name,
		CreatedAt: f.created.CreatedAt, ContactCount: 3,
	}}, nil
}
func (f *fakeStore) Get(context.Context, uuid.UUID, uuid.UUID) (gen.List, error) {
	return f.created, nil
}
func (f *fakeStore) CountMembers(context.Context, uuid.UUID) (int64, error) { return 3, nil }
func (f *fakeStore) Rename(_ context.Context, ws, id uuid.UUID, name string) (gen.List, error) {
	f.renameCalls++
	if f.renameErr != nil {
		return gen.List{}, f.renameErr
	}
	return gen.List{ID: id, WorkspaceID: ws, Name: name}, nil
}
func (f *fakeStore) Delete(context.Context, uuid.UUID, uuid.UUID) error {
	f.deleteCalls++
	return f.deleteErr
}

func TestCreateList(t *testing.T) {
	svc := NewService(&fakeStore{})
	l, err := svc.Create(context.Background(), uuid.New(), "Prospects")
	if err != nil || l.Name != "Prospects" {
		t.Fatalf("Create: %v %+v", err, l)
	}
}

func TestListLists(t *testing.T) {
	store := &fakeStore{}
	svc := NewService(store)
	ws := uuid.New()
	created, err := svc.Create(context.Background(), ws, "Prospects")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	ls, err := svc.List(context.Background(), ws)
	if err != nil || len(ls) != 1 || ls[0].ID != created.ID {
		t.Fatalf("List: %v %+v", err, ls)
	}
}

func TestGetList(t *testing.T) {
	store := &fakeStore{}
	svc := NewService(store)
	ws := uuid.New()
	created, err := svc.Create(context.Background(), ws, "Prospects")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	l, err := svc.Get(context.Background(), ws, created.ID)
	if err != nil || l.ID != created.ID {
		t.Fatalf("Get: %v %+v", err, l)
	}
}

func TestMemberCount(t *testing.T) {
	svc := NewService(&fakeStore{})
	n, err := svc.MemberCount(context.Background(), uuid.New())
	if err != nil || n != 3 {
		t.Fatalf("MemberCount: %v %d", err, n)
	}
}

func TestRename(t *testing.T) {
	boom := errors.New("boom")
	tests := map[string]struct {
		name           string
		storeErr       error
		wantErr        error
		wantStoreCalls int
	}{
		"happy path":           {name: "Renamed", wantStoreCalls: 1},
		"empty name":           {name: "", wantErr: ErrValidation},
		"over-long name":       {name: strings.Repeat("x", 201), wantErr: ErrValidation},
		"not found":            {name: "Renamed", storeErr: pgx.ErrNoRows, wantErr: ErrNotFound, wantStoreCalls: 1},
		"store error surfaces": {name: "Renamed", storeErr: boom, wantErr: boom, wantStoreCalls: 1},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			store := &fakeStore{renameErr: tc.storeErr}
			svc := NewService(store)
			l, err := svc.Rename(context.Background(), uuid.New(), uuid.New(), tc.name)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("Rename err = %v, want %v", err, tc.wantErr)
			}
			if store.renameCalls != tc.wantStoreCalls {
				t.Fatalf("store.Rename calls = %d, want %d", store.renameCalls, tc.wantStoreCalls)
			}
			if tc.wantErr == nil && l.Name != tc.name {
				t.Fatalf("renamed name = %q, want %q", l.Name, tc.name)
			}
		})
	}
}

func TestDelete(t *testing.T) {
	boom := errors.New("boom")
	tests := map[string]struct {
		storeErr error
		wantErr  error
	}{
		"happy path":           {},
		"not found":            {storeErr: pgx.ErrNoRows, wantErr: ErrNotFound},
		"in use (FK restrict)": {storeErr: &pgconn.PgError{Code: "23503"}, wantErr: ErrInUse},
		"store error surfaces": {storeErr: boom, wantErr: boom},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			store := &fakeStore{deleteErr: tc.storeErr}
			svc := NewService(store)
			err := svc.Delete(context.Background(), uuid.New(), uuid.New())
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("Delete err = %v, want %v", err, tc.wantErr)
			}
			if store.deleteCalls != 1 {
				t.Fatalf("store.Delete calls = %d, want 1", store.deleteCalls)
			}
		})
	}
}
