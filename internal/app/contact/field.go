package contact

import (
	"context"
	"errors"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/platform/db/gen"
)

// FieldType is the generated enum, aliased rather than redeclared so there is
// exactly one definition of the set (CLAUDE.md: derive from the owning
// definition, never hand-copy a shape that already exists). A new variant added
// to the migration therefore cannot drift from the Go side.
type FieldType = gen.ContactFieldType

// The variants, re-exported under domain names so callers in this package read
// as domain code rather than as persistence code.
const (
	FieldTypeText   = gen.ContactFieldTypeText
	FieldTypeNumber = gen.ContactFieldTypeNumber
	FieldTypeDate   = gen.ContactFieldTypeDate
	FieldTypeSelect = gen.ContactFieldTypeSelect
)

// dateLayout is the only accepted date form. Dates are stored as text in a
// JSONB object and rendered verbatim into emails, so a single unambiguous
// layout is the point: 03/04/2026 means two different days depending on who
// reads it, and an email is read by someone who never saw the import.
const dateLayout = "2006-01-02"

// maxValueBytes bounds one field's stored value. Values are substituted into
// outbound email bodies, so an unbounded value is an unbounded message.
const maxValueBytes = 2000

// MaxLiveFields caps how many live definitions a workspace may hold. Every
// contact form, import mapping and preflight pass walks the whole set, and the
// per-contact JSONB carries one entry per populated field on every row.
const MaxLiveFields = 100

// keyRE mirrors the CHECK constraint in migration 000052 exactly. It is
// deliberately narrower than personalize.customRE ([a-zA-Z0-9_]+): that regex
// is case-sensitive and resolves an unknown key to the empty string, so
// allowing `Industry` and `industry` to coexist would let a casing slip send a
// blank where a value was meant.
var keyRE = regexp.MustCompile(`^[a-z][a-z0-9_]{0,39}$`)

// Field-definition errors. Each names a condition a caller can act on; anything
// else propagates as an unclassified error rather than being flattened into one
// of these.
var (
	// ErrFieldNotFound is an id that is not a live field of this workspace.
	ErrFieldNotFound = errors.New("custom field not found")
	// ErrFieldKeyTaken covers an ARCHIVED key too — keys are retired
	// permanently, because stored values are addressed by key.
	ErrFieldKeyTaken = errors.New("custom field key already used")
	// ErrFieldArchived is an attempt to edit a retired definition.
	ErrFieldArchived = errors.New("custom field is archived")
	// ErrTooManyFields is the MaxLiveFields ceiling.
	ErrTooManyFields = errors.New("workspace has too many custom fields")
)

// InvalidFieldError reports a value or definition the caller can fix, carrying
// the offending field key so a bulk write can say which one failed. It is a
// distinct type rather than a sentinel because the message is per-case.
type InvalidFieldError struct {
	Key    string
	Reason string
}

func (e *InvalidFieldError) Error() string {
	if e.Key == "" {
		return e.Reason
	}
	return fmt.Sprintf("%s: %s", e.Key, e.Reason)
}

// FieldDef is one workspace-defined custom field. Options is non-nil only for
// FieldTypeSelect; ArchivedAt non-nil means retired.
type FieldDef struct {
	ID         uuid.UUID
	Key        string
	Label      string
	Type       FieldType
	Options    []string
	CreatedAt  time.Time
	ArchivedAt *time.Time
}

// Live reports whether the definition still accepts writes and appears in
// forms, import mapping and token completion.
func (f FieldDef) Live() bool { return f.ArchivedAt == nil }

// FieldDefInput is a create request, pre-validation.
type FieldDefInput struct {
	Key     string
	Label   string
	Type    FieldType
	Options []string
}

// FieldStore is the repository seam for definitions and for the per-contact
// values they describe. Separate from Store because it is a distinct
// responsibility with distinct callers (settings CRUD, import mapping,
// preflight), and keeping it small is what lets the import path be unit-tested
// against a fake set of definitions with no database.
type FieldStore interface {
	// ListFieldDefs returns every definition in the workspace, archived
	// included — callers filter, because recognising a retired key is itself a
	// thing several of them need to do.
	ListFieldDefs(ctx context.Context, workspaceID uuid.UUID) ([]FieldDef, error)
	CreateFieldDef(ctx context.Context, workspaceID uuid.UUID, in FieldDefInput) (FieldDef, error)
	// UpdateFieldDef edits the two mutable attributes. Key and Type are fixed at
	// creation: both are load-bearing on data already written (key addresses
	// stored values and appears in tokens operators have typed into live
	// sequences; type is the promise every existing value was validated under).
	UpdateFieldDef(ctx context.Context, workspaceID, id uuid.UUID, label string, options []string) (FieldDef, error)
	ArchiveFieldDef(ctx context.Context, workspaceID, id uuid.UUID) (FieldDef, error)

	// GetCustomFields returns the contact's stored values, or ErrNotFound when
	// the contact is not in this workspace.
	GetCustomFields(ctx context.Context, workspaceID, contactID uuid.UUID) (map[string]string, error)
	// SetCustomFields replaces the contact's whole value object.
	SetCustomFields(ctx context.Context, workspaceID, contactID uuid.UUID, values map[string]string) error
}

// normalizeKey lower-cases and trims a proposed key, then holds it to the same
// shape the database CHECK enforces, so a bad key is a 400 with a reason rather
// than a 23514 the caller cannot read.
func normalizeKey(raw string) (string, error) {
	key := strings.ToLower(strings.TrimSpace(raw))
	if !keyRE.MatchString(key) {
		return "", &InvalidFieldError{Key: raw, Reason: "key must start with a letter and contain only lower-case letters, digits and underscores (max 40)"}
	}
	return key, nil
}

// validate normalizes and checks a create request as a whole: the type decides
// whether options are required or forbidden, mirroring the biconditional CHECK
// in the migration so the two cannot disagree.
func (in FieldDefInput) validate() (FieldDefInput, error) {
	key, err := normalizeKey(in.Key)
	if err != nil {
		return FieldDefInput{}, err
	}
	label := strings.TrimSpace(in.Label)
	if label == "" || len(label) > 80 {
		return FieldDefInput{}, &InvalidFieldError{Key: key, Reason: "label must be 1-80 characters"}
	}
	switch in.Type {
	case FieldTypeText, FieldTypeNumber, FieldTypeDate, FieldTypeSelect:
	default:
		return FieldDefInput{}, &InvalidFieldError{Key: key, Reason: "type must be one of text, number, date, select"}
	}
	options, err := validateOptions(key, in.Type, in.Options)
	if err != nil {
		return FieldDefInput{}, err
	}
	return FieldDefInput{Key: key, Label: label, Type: in.Type, Options: options}, nil
}

// validateOptions returns the cleaned choice list for a select, or nil for
// every other type. A non-select carrying options is rejected rather than
// silently ignored: accepting it would let the UI believe choices were saved.
func validateOptions(key string, t FieldType, raw []string) ([]string, error) {
	if t != FieldTypeSelect {
		if len(raw) > 0 {
			return nil, &InvalidFieldError{Key: key, Reason: "only a select field may have options"}
		}
		return nil, nil
	}
	seen := make(map[string]struct{}, len(raw))
	out := make([]string, 0, len(raw))
	for _, o := range raw {
		o = strings.TrimSpace(o)
		if o == "" {
			continue
		}
		if len(o) > maxValueBytes {
			return nil, &InvalidFieldError{Key: key, Reason: "an option is too long"}
		}
		if _, dup := seen[o]; dup {
			continue
		}
		seen[o] = struct{}{}
		out = append(out, o)
	}
	if len(out) == 0 {
		return nil, &InvalidFieldError{Key: key, Reason: "a select field needs at least one option"}
	}
	if len(out) > 100 {
		return nil, &InvalidFieldError{Key: key, Reason: "a select field may have at most 100 options"}
	}
	return out, nil
}

// CoerceValue validates raw against the field's type and returns what should be
// stored. Every value is stored as a JSON STRING regardless of type, because
// that string is what gets substituted into an email verbatim: storing a JSON
// number would hand rendering to fmt's %v, where 15000000 becomes 1.5e+07 in a
// customer's subject line. Validating on the way in and rendering the validated
// text on the way out keeps the two ends honest.
//
// An empty raw is always allowed and means "no value" — the caller decides
// whether that clears the key or skips it.
func (f FieldDef) CoerceValue(raw string) (string, error) {
	v := strings.TrimSpace(raw)
	if v == "" {
		return "", nil
	}
	if len(v) > maxValueBytes {
		return "", &InvalidFieldError{Key: f.Key, Reason: fmt.Sprintf("value exceeds %d characters", maxValueBytes)}
	}
	switch f.Type {
	case FieldTypeNumber:
		// ParseFloat accepts "Inf" and "NaN", which are not numbers an operator
		// meant to type and would render as literal "NaN" in an email.
		n, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return "", &InvalidFieldError{Key: f.Key, Reason: "value must be a number"}
		}
		if math.IsNaN(n) || math.IsInf(n, 0) {
			return "", &InvalidFieldError{Key: f.Key, Reason: "value must be a finite number"}
		}
	case FieldTypeDate:
		if _, err := time.Parse(dateLayout, v); err != nil {
			return "", &InvalidFieldError{Key: f.Key, Reason: "value must be a date in YYYY-MM-DD form"}
		}
	case FieldTypeSelect:
		if !slices.Contains(f.Options, v) {
			return "", &InvalidFieldError{Key: f.Key, Reason: "value must be one of the field's options"}
		}
	case FieldTypeText:
		// Any text within the length cap. Escaping for HTML bodies happens at
		// substitution time (personalize.HTML), not here — a value is stored as
		// the operator typed it so the plain-text body is not double-escaped.
	}
	return v, nil
}

// liveByKey indexes the live definitions by key. Archived fields are excluded
// because every caller of this (write validation, import mapping, preflight)
// is asking "may this key be written or referenced now".
func liveByKey(defs []FieldDef) map[string]FieldDef {
	out := make(map[string]FieldDef, len(defs))
	for _, d := range defs {
		if d.Live() {
			out[d.Key] = d
		}
	}
	return out
}
