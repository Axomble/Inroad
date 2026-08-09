package contact

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func textField(key string) FieldDef {
	return FieldDef{ID: uuid.New(), Key: key, Label: strings.ToUpper(key), Type: FieldTypeText}
}

func fieldSvc(store *fakeFieldStore) *Service {
	return &Service{store: &fakeStore{}, checker: &fakeChecker{exists: true}, fields: store}
}

// --- value coercion -------------------------------------------------------
//
// Every type's accept AND reject branch, because coercion is the only thing
// standing between an operator's spreadsheet and a customer's inbox.

func TestCoerceValueByType(t *testing.T) {
	cases := []struct {
		name  string
		def   FieldDef
		raw   string
		want  string
		valid bool
	}{
		{"text passes through", textField("note"), "  hello  ", "hello", true},
		{"number accepts decimal", FieldDef{Key: "score", Type: FieldTypeNumber}, "12.5", "12.5", true},
		{"number accepts negative", FieldDef{Key: "score", Type: FieldTypeNumber}, "-3", "-3", true},
		{"number rejects words", FieldDef{Key: "score", Type: FieldTypeNumber}, "twelve", "", false},
		// ParseFloat accepts these, so they need an explicit guard: "NaN" would
		// otherwise be substituted into an email literally.
		{"number rejects NaN", FieldDef{Key: "score", Type: FieldTypeNumber}, "NaN", "", false},
		{"number rejects Inf", FieldDef{Key: "score", Type: FieldTypeNumber}, "Inf", "", false},
		{"date accepts ISO", FieldDef{Key: "renewal", Type: FieldTypeDate}, "2026-08-09", "2026-08-09", true},
		{"date rejects ambiguous slashes", FieldDef{Key: "renewal", Type: FieldTypeDate}, "03/04/2026", "", false},
		{"date rejects impossible day", FieldDef{Key: "renewal", Type: FieldTypeDate}, "2026-02-31", "", false},
		{"select accepts a listed option", FieldDef{Key: "tier", Type: FieldTypeSelect, Options: []string{"A", "B"}}, "A", "A", true},
		{"select rejects an unlisted option", FieldDef{Key: "tier", Type: FieldTypeSelect, Options: []string{"A", "B"}}, "C", "", false},
		{"empty is always allowed", FieldDef{Key: "tier", Type: FieldTypeSelect, Options: []string{"A"}}, "   ", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.def.CoerceValue(tc.raw)
			if tc.valid && err != nil {
				t.Fatalf("CoerceValue(%q) = error %v, want %q", tc.raw, err, tc.want)
			}
			if !tc.valid {
				var invalid *InvalidFieldError
				if !errors.As(err, &invalid) {
					t.Fatalf("CoerceValue(%q) error = %v, want *InvalidFieldError", tc.raw, err)
				}
				return
			}
			if got != tc.want {
				t.Errorf("CoerceValue(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}

func TestCoerceValueRejectsOverlongValue(t *testing.T) {
	def := textField("note")
	if _, err := def.CoerceValue(strings.Repeat("x", maxValueBytes+1)); err == nil {
		t.Fatal("expected an error for a value past the length cap")
	}
}

// --- definition validation ------------------------------------------------

func TestCreateFieldDefNormalizesKeyAndRejectsBadOnes(t *testing.T) {
	store := &fakeFieldStore{}
	def, err := fieldSvc(store).CreateFieldDef(context.Background(), uuid.New(), FieldDefInput{
		Key: "  Industry  ", Label: " Industry ", Type: FieldTypeText,
	})
	if err != nil {
		t.Fatalf("CreateFieldDef: %v", err)
	}
	if def.Key != "industry" || def.Label != "Industry" {
		t.Fatalf("got key=%q label=%q, want key=industry label=Industry", def.Key, def.Label)
	}

	for _, bad := range []string{"", "9lives", "has space", "has-hyphen", strings.Repeat("a", 41)} {
		if _, err := fieldSvc(&fakeFieldStore{}).CreateFieldDef(context.Background(), uuid.New(),
			FieldDefInput{Key: bad, Label: "L", Type: FieldTypeText}); err == nil {
			t.Errorf("key %q was accepted, want rejection", bad)
		}
	}
}

func TestCreateFieldDefEnforcesOptionsMatchType(t *testing.T) {
	ctx, ws := context.Background(), uuid.New()

	if _, err := fieldSvc(&fakeFieldStore{}).CreateFieldDef(ctx, ws, FieldDefInput{
		Key: "tier", Label: "Tier", Type: FieldTypeSelect,
	}); err == nil {
		t.Error("a select with no options was accepted, want rejection")
	}
	if _, err := fieldSvc(&fakeFieldStore{}).CreateFieldDef(ctx, ws, FieldDefInput{
		Key: "note", Label: "Note", Type: FieldTypeText, Options: []string{"A"},
	}); err == nil {
		t.Error("a text field carrying options was accepted, want rejection")
	}
}

func TestCreateFieldDefRefusesPastTheLiveCeiling(t *testing.T) {
	store := &fakeFieldStore{}
	for i := 0; i < MaxLiveFields; i++ {
		store.defs = append(store.defs, textField("f"))
	}
	_, err := fieldSvc(store).CreateFieldDef(context.Background(), uuid.New(),
		FieldDefInput{Key: "one_more", Label: "One more", Type: FieldTypeText})
	if !errors.Is(err, ErrTooManyFields) {
		t.Fatalf("err = %v, want ErrTooManyFields", err)
	}
}

// Archived fields do not count toward the ceiling: they cost nothing to read
// and a workspace that has tidied up should not look full.
func TestCreateFieldDefIgnoresArchivedFieldsInTheCeiling(t *testing.T) {
	store := &fakeFieldStore{}
	for i := 0; i < MaxLiveFields; i++ {
		d := textField("f")
		d.ArchivedAt = ptr(fixedNow)
		store.defs = append(store.defs, d)
	}
	if _, err := fieldSvc(store).CreateFieldDef(context.Background(), uuid.New(),
		FieldDefInput{Key: "fresh", Label: "Fresh", Type: FieldTypeText}); err != nil {
		t.Fatalf("CreateFieldDef: %v", err)
	}
}

func TestUpdateFieldDefRefusesAnArchivedField(t *testing.T) {
	archived := textField("industry")
	archived.ArchivedAt = ptr(fixedNow)
	store := &fakeFieldStore{defs: []FieldDef{archived}}

	_, err := fieldSvc(store).UpdateFieldDef(context.Background(), uuid.New(), archived.ID, "New label", nil)
	if !errors.Is(err, ErrFieldArchived) {
		t.Fatalf("err = %v, want ErrFieldArchived", err)
	}
}

// --- per-contact values ---------------------------------------------------

func TestSetContactFieldsValidatesAgainstTheFieldType(t *testing.T) {
	def := FieldDef{ID: uuid.New(), Key: "renewal", Label: "Renewal", Type: FieldTypeDate}
	store := &fakeFieldStore{defs: []FieldDef{def}, values: map[string]string{}}

	_, err := fieldSvc(store).SetContactFields(context.Background(), uuid.New(), uuid.New(),
		map[string]string{"renewal": "next tuesday"})
	var invalid *InvalidFieldError
	if !errors.As(err, &invalid) {
		t.Fatalf("err = %v, want *InvalidFieldError", err)
	}
	if invalid.Key != "renewal" {
		t.Errorf("error key = %q, want renewal", invalid.Key)
	}
	if store.lastSet != nil {
		t.Error("a rejected value must not reach the store")
	}
}

func TestSetContactFieldsRefusesAnUnknownKey(t *testing.T) {
	store := &fakeFieldStore{defs: []FieldDef{textField("industry")}, values: map[string]string{}}
	_, err := fieldSvc(store).SetContactFields(context.Background(), uuid.New(), uuid.New(),
		map[string]string{"nonexistent": "x"})
	if err == nil {
		t.Fatal("expected a rejection for a key with no live definition")
	}
}

// An archived key's stored value survives a save that never mentioned it: the
// form could not show the field, so its silence is not an instruction to clear.
func TestSetContactFieldsPreservesValuesUnderArchivedKeys(t *testing.T) {
	live := textField("industry")
	archived := textField("legacy")
	archived.ArchivedAt = ptr(fixedNow)
	store := &fakeFieldStore{
		defs:   []FieldDef{live, archived},
		values: map[string]string{"industry": "fintech", "legacy": "keep me"},
	}

	_, err := fieldSvc(store).SetContactFields(context.Background(), uuid.New(), uuid.New(),
		map[string]string{"industry": "healthcare"})
	if err != nil {
		t.Fatalf("SetContactFields: %v", err)
	}
	if store.lastSet["legacy"] != "keep me" {
		t.Errorf("archived value = %q, want it preserved", store.lastSet["legacy"])
	}
	if store.lastSet["industry"] != "healthcare" {
		t.Errorf("industry = %q, want healthcare", store.lastSet["industry"])
	}
}

// A live key the form omitted IS cleared — that is the difference between this
// replace and the import path's merge.
func TestSetContactFieldsClearsOmittedLiveKeys(t *testing.T) {
	store := &fakeFieldStore{
		defs:   []FieldDef{textField("industry"), textField("tier")},
		values: map[string]string{"industry": "fintech", "tier": "A"},
	}
	if _, err := fieldSvc(store).SetContactFields(context.Background(), uuid.New(), uuid.New(),
		map[string]string{"industry": "fintech"}); err != nil {
		t.Fatalf("SetContactFields: %v", err)
	}
	if _, present := store.lastSet["tier"]; present {
		t.Errorf("tier = %q, want it cleared by an omitting save", store.lastSet["tier"])
	}
}

// An explicitly-empty value clears the key rather than storing "": a stored
// empty string and an absent key render identically in an email.
func TestSetContactFieldsStoresNoEmptyStrings(t *testing.T) {
	store := &fakeFieldStore{defs: []FieldDef{textField("industry")}, values: map[string]string{"industry": "fintech"}}
	if _, err := fieldSvc(store).SetContactFields(context.Background(), uuid.New(), uuid.New(),
		map[string]string{"industry": "  "}); err != nil {
		t.Fatalf("SetContactFields: %v", err)
	}
	if _, present := store.lastSet["industry"]; present {
		t.Errorf("industry = %q, want the key absent", store.lastSet["industry"])
	}
}

// The contact read includes live fields with no value (so a form can render the
// whole set) and orphaned values (so nothing silently disappears).
func TestContactFieldsIncludesEmptyLiveFieldsAndOrphans(t *testing.T) {
	store := &fakeFieldStore{
		defs:   []FieldDef{textField("industry"), textField("tier")},
		values: map[string]string{"industry": "fintech", "gone": "orphan"},
	}
	values, err := fieldSvc(store).ContactFields(context.Background(), uuid.New(), uuid.New())
	if err != nil {
		t.Fatalf("ContactFields: %v", err)
	}
	byKey := map[string]FieldValue{}
	for _, v := range values {
		byKey[v.Key] = v
	}
	if v, ok := byKey["tier"]; !ok || v.Value != "" || v.Def == nil {
		t.Errorf("tier = %+v, want a defined field with an empty value", v)
	}
	if v, ok := byKey["gone"]; !ok || v.Def != nil || v.Value != "orphan" {
		t.Errorf("gone = %+v, want an orphan value with no definition", v)
	}
}
