package contact

import (
	"context"
	"maps"
	"time"

	"github.com/google/uuid"
)

// fixedNow stands in for the archive timestamp. Tests assert that a field IS
// archived, never when, so a constant keeps the fake deterministic.
var fixedNow = time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)

// fakeFieldStore is the FieldStore half of this domain's persistence, kept in
// its own file because it is scripted by three unrelated suites (definition
// CRUD, import mapping, contact value writes) and threading it through
// fakeStore would couple them.
//
// defs is the workspace's definitions; values is one contact's stored object.
// Neither is keyed by workspace or contact id: the service pins both in every
// call, and a fake that re-implemented tenancy would be asserting its own
// filtering rather than the service's.
type fakeFieldStore struct {
	defs   []FieldDef
	values map[string]string

	listErr   error
	createErr error
	updateErr error
	getErr    error
	setErr    error

	// lastSet captures what SetCustomFields was asked to persist, which is the
	// only way to assert the merge/replace semantics from outside.
	lastSet map[string]string
	created FieldDefInput
}

func (f *fakeFieldStore) ListFieldDefs(context.Context, uuid.UUID) ([]FieldDef, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.defs, nil
}

func (f *fakeFieldStore) CreateFieldDef(_ context.Context, _ uuid.UUID, in FieldDefInput) (FieldDef, error) {
	f.created = in
	if f.createErr != nil {
		return FieldDef{}, f.createErr
	}
	def := FieldDef{ID: uuid.New(), Key: in.Key, Label: in.Label, Type: in.Type, Options: in.Options}
	f.defs = append(f.defs, def)
	return def, nil
}

func (f *fakeFieldStore) UpdateFieldDef(_ context.Context, _, id uuid.UUID, label string, options []string) (FieldDef, error) {
	if f.updateErr != nil {
		return FieldDef{}, f.updateErr
	}
	for i, d := range f.defs {
		if d.ID == id {
			f.defs[i].Label = label
			f.defs[i].Options = options
			return f.defs[i], nil
		}
	}
	return FieldDef{}, ErrFieldNotFound
}

func (f *fakeFieldStore) ArchiveFieldDef(_ context.Context, _, id uuid.UUID) (FieldDef, error) {
	for i, d := range f.defs {
		if d.ID == id {
			if f.defs[i].ArchivedAt == nil {
				f.defs[i].ArchivedAt = ptr(fixedNow)
			}
			return f.defs[i], nil
		}
	}
	return FieldDef{}, ErrFieldNotFound
}

func (f *fakeFieldStore) GetCustomFields(context.Context, uuid.UUID, uuid.UUID) (map[string]string, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	return maps.Clone(f.values), nil
}

func (f *fakeFieldStore) SetCustomFields(_ context.Context, _, _ uuid.UUID, values map[string]string) error {
	f.lastSet = maps.Clone(values)
	if f.setErr != nil {
		return f.setErr
	}
	f.values = maps.Clone(values)
	return nil
}
