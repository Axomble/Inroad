package replylabel

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/inroad/inroad/internal/platform/db/gen"
)

// fakeStore is an in-memory Store. The service is pure policy over this seam,
// so every test here runs without a database.
type fakeStore struct {
	labels  []gen.ReplyLabel
	listErr error
	// deleted records the ids Delete was asked for, so a test can prove the
	// service refused BEFORE reaching the store.
	deleted []uuid.UUID
	// reordered records the last Reorder argument.
	reordered []uuid.UUID
}

func (f *fakeStore) List(context.Context, uuid.UUID) ([]gen.ReplyLabel, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	out := make([]gen.ReplyLabel, len(f.labels))
	copy(out, f.labels)
	return out, nil
}

func (f *fakeStore) Get(_ context.Context, _, id uuid.UUID) (gen.ReplyLabel, error) {
	for _, l := range f.labels {
		if l.ID == id {
			return l, nil
		}
	}
	return gen.ReplyLabel{}, pgx.ErrNoRows
}

func (f *fakeStore) GetByKey(_ context.Context, _ uuid.UUID, key string) (gen.ReplyLabel, bool, error) {
	for _, l := range f.labels {
		if l.Key == key {
			return l, true, nil
		}
	}
	return gen.ReplyLabel{}, false, nil
}

func (f *fakeStore) Create(_ context.Context, ws uuid.UUID, key string, in Input) (gen.ReplyLabel, error) {
	label := gen.ReplyLabel{
		ID: uuid.New(), WorkspaceID: ws, Key: key, Label: in.Label, Color: in.Color,
		Position: int32(len(f.labels)), StopsEnrollment: in.StopsEnrollment,
		IsAutomated: in.IsAutomated, SuppressesContact: in.SuppressesContact,
		CapturesDeal: in.CapturesDeal, DefersEnrollment: in.DefersEnrollment,
	}
	f.labels = append(f.labels, label)
	return label, nil
}

func (f *fakeStore) Update(_ context.Context, _, id uuid.UUID, in Input) (gen.ReplyLabel, error) {
	for i, l := range f.labels {
		if l.ID != id {
			continue
		}
		f.labels[i].Label, f.labels[i].Color = in.Label, in.Color
		f.labels[i].StopsEnrollment = in.StopsEnrollment
		f.labels[i].IsAutomated = in.IsAutomated
		f.labels[i].SuppressesContact = in.SuppressesContact
		f.labels[i].CapturesDeal = in.CapturesDeal
		f.labels[i].DefersEnrollment = in.DefersEnrollment
		return f.labels[i], nil
	}
	return gen.ReplyLabel{}, pgx.ErrNoRows
}

func (f *fakeStore) Reorder(_ context.Context, _ uuid.UUID, ids []uuid.UUID) error {
	f.reordered = ids
	return nil
}

func (f *fakeStore) Delete(_ context.Context, _, id uuid.UUID) (bool, error) {
	f.deleted = append(f.deleted, id)
	for i, l := range f.labels {
		if l.ID == id && !l.IsBuiltin {
			f.labels = append(f.labels[:i], f.labels[i+1:]...)
			return true, nil
		}
	}
	return false, nil
}

var _ Store = (*fakeStore)(nil)

func label(key string, builtin bool) gen.ReplyLabel {
	return gen.ReplyLabel{ID: uuid.New(), Key: key, Label: key, Color: "#112233", IsBuiltin: builtin}
}

func newService(labels ...gen.ReplyLabel) (*Service, *fakeStore) {
	store := &fakeStore{labels: labels}
	return NewService(store), store
}

// TestDeleteRefusesBuiltin is the taxonomy's load-bearing guard: a seeded label
// is named by the classifier AND by historical enrollment rows, so it may be
// renamed or have its flags turned off but never removed. The refusal happens
// in the service, ahead of the DB, so the caller gets a 409 explaining why
// rather than the 404 a guarded DELETE matching zero rows would produce.
func TestDeleteRefusesBuiltin(t *testing.T) {
	builtin := label("positive", true)
	svc, store := newService(builtin)
	err := svc.Delete(context.Background(), uuid.New(), builtin.ID)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict, got %v", err)
	}
	if len(store.deleted) != 0 {
		t.Fatalf("the store must not be reached for a builtin, got %v", store.deleted)
	}
	if len(store.labels) != 1 {
		t.Fatal("the builtin label was removed")
	}
}

// TestDeleteAllowsCustom: a custom label goes, and nothing else is touched —
// historical rows keep the now-orphaned key on purpose (a recorded
// classification is a fact, not a foreign key).
func TestDeleteAllowsCustom(t *testing.T) {
	custom := label("demo_requested", false)
	svc, store := newService(label("positive", true), custom)
	if err := svc.Delete(context.Background(), uuid.New(), custom.ID); err != nil {
		t.Fatal(err)
	}
	if len(store.labels) != 1 || store.labels[0].Key != "positive" {
		t.Fatalf("expected only the builtin to remain, got %+v", store.labels)
	}
}

func TestDeleteUnknownIsNotFound(t *testing.T) {
	svc, _ := newService()
	if err := svc.Delete(context.Background(), uuid.New(), uuid.New()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

// TestCreateDerivesAnImmutableKey: the human types a label, the machine key is
// slugified from it once and never changes, because historical
// sequence_enrollments.reply_class rows name it as free text.
func TestCreateDerivesAnImmutableKey(t *testing.T) {
	svc, _ := newService()
	created, err := svc.Create(context.Background(), uuid.New(), Input{
		Label: "Demo Requested!", Color: "#22C55E", StopsEnrollment: true, CapturesDeal: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.Key != "demo_requested" {
		t.Fatalf("key = %q, want demo_requested", created.Key)
	}
	if !created.CapturesDeal {
		t.Fatal("captures_deal did not survive the create")
	}
}

func TestValidation(t *testing.T) {
	cases := []struct {
		name string
		in   Input
	}{
		{"empty label", Input{Label: "  ", Color: "#22C55E"}},
		{"label with no key characters", Input{Label: "!!!", Color: "#22C55E"}},
		{"bad colour", Input{Label: "Warm", Color: "green"}},
		// An automated label is machine-generated mail by definition; stopping
		// the sequence on it would defeat the whole automated family.
		{"automated label that stops", Input{Label: "Bot", Color: "#22C55E", IsAutomated: true, StopsEnrollment: true}},
		// Deferral only makes sense on a label that keeps the enrollment alive.
		{"deferring label that is not automated", Input{Label: "Away", Color: "#22C55E", DefersEnrollment: true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, _ := newService()
			if _, err := svc.Create(context.Background(), uuid.New(), tc.in); !errors.Is(err, ErrValidation) {
				t.Fatalf("expected ErrValidation, got %v", err)
			}
		})
	}
}

// TestReorderRequiresTheCompleteSet: a stale client list must not silently drop
// a label, so anything but a permutation of the workspace's labels is refused
// before the store is touched.
func TestReorderRequiresTheCompleteSet(t *testing.T) {
	a, b := label("positive", true), label("negative", true)
	cases := []struct {
		name string
		ids  []uuid.UUID
	}{
		{"missing a label", []uuid.UUID{a.ID}},
		{"duplicate id", []uuid.UUID{a.ID, a.ID}},
		{"unknown id", []uuid.UUID{a.ID, uuid.New()}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, store := newService(a, b)
			if _, err := svc.Reorder(context.Background(), uuid.New(), tc.ids); !errors.Is(err, ErrValidation) {
				t.Fatalf("expected ErrValidation, got %v", err)
			}
			if store.reordered != nil {
				t.Fatalf("the store must not be reached, got %v", store.reordered)
			}
		})
	}
}

func TestReorderAppliesAPermutation(t *testing.T) {
	a, b := label("positive", true), label("negative", true)
	svc, store := newService(a, b)
	if _, err := svc.Reorder(context.Background(), uuid.New(), []uuid.UUID{b.ID, a.ID}); err != nil {
		t.Fatal(err)
	}
	if len(store.reordered) != 2 || store.reordered[0] != b.ID {
		t.Fatalf("reorder was not applied in order, got %v", store.reordered)
	}
}

// TestResolveDegradesToNotFound: a key no label claims is NOT an error — it is
// the documented "the custom label was deleted, the key lives on" case, and the
// caller falls back to pre-taxonomy behaviour.
func TestResolveDegradesToNotFound(t *testing.T) {
	svc, _ := newService(label("positive", true))
	for _, key := range []string{"", "  ", "gone_label"} {
		got, ok, err := svc.Resolve(context.Background(), uuid.New(), key)
		if err != nil {
			t.Fatalf("Resolve(%q) errored: %v", key, err)
		}
		if ok {
			t.Fatalf("Resolve(%q) unexpectedly resolved to %+v", key, got)
		}
	}
	if _, ok, err := svc.Resolve(context.Background(), uuid.New(), "positive"); err != nil || !ok {
		t.Fatalf("Resolve(positive) = ok %v, err %v", ok, err)
	}
}
