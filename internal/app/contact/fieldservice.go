package contact

import (
	"context"
	"slices"

	"github.com/google/uuid"
)

// ListFieldDefs returns the workspace's custom field definitions, archived ones
// included. The archived rows are returned rather than hidden because they are
// the explanation for values a contact still carries under a key no form offers
// any more — dropping them here would make that data look like corruption.
func (s *Service) ListFieldDefs(ctx context.Context, ws uuid.UUID) ([]FieldDef, error) {
	return s.fields.ListFieldDefs(ctx, ws)
}

// CreateFieldDef validates the request, enforces the live-field ceiling, and
// creates the definition. The ceiling is checked against live fields only:
// archived ones cost nothing at read time, and counting them would make a
// workspace that has tidied up look full.
//
// The check is not transactional with the insert, so two concurrent creates can
// both pass at the boundary and take the workspace one over MaxLiveFields. That
// is deliberate: the cap exists to stop unbounded growth, not to be exact, and
// the alternative (locking the workspace's definitions on every create) buys
// precision nobody can observe.
func (s *Service) CreateFieldDef(ctx context.Context, ws uuid.UUID, in FieldDefInput) (FieldDef, error) {
	valid, err := in.validate()
	if err != nil {
		return FieldDef{}, err
	}
	defs, err := s.fields.ListFieldDefs(ctx, ws)
	if err != nil {
		return FieldDef{}, err
	}
	live := 0
	for _, d := range defs {
		if d.Live() {
			live++
		}
	}
	if live >= MaxLiveFields {
		return FieldDef{}, ErrTooManyFields
	}
	return s.fields.CreateFieldDef(ctx, ws, valid)
}

// UpdateFieldDef edits the label and, for a select, its options. The field's
// existing type decides whether options are legal at all, so the type is read
// from the stored definition rather than taken from the request — a caller
// cannot change what a field is by describing it differently.
//
// Removing an option that contacts already hold does NOT rewrite those
// contacts: their stored value stays and keeps rendering in emails, it simply
// stops being offered in the form. Silently blanking live personalization
// because someone tidied a dropdown would be the worse surprise.
func (s *Service) UpdateFieldDef(ctx context.Context, ws, id uuid.UUID, label string, options []string) (FieldDef, error) {
	defs, err := s.fields.ListFieldDefs(ctx, ws)
	if err != nil {
		return FieldDef{}, err
	}
	current, ok := findByID(defs, id)
	if !ok {
		return FieldDef{}, ErrFieldNotFound
	}
	if !current.Live() {
		return FieldDef{}, ErrFieldArchived
	}
	trimmed, err := FieldDefInput{Key: current.Key, Label: label, Type: current.Type, Options: options}.validate()
	if err != nil {
		return FieldDef{}, err
	}
	return s.fields.UpdateFieldDef(ctx, ws, id, trimmed.Label, trimmed.Options)
}

// ArchiveFieldDef retires a definition. Stored values are untouched (see the
// migration): archiving is about what forms, imports and token completion
// offer, not about deleting contact data.
func (s *Service) ArchiveFieldDef(ctx context.Context, ws, id uuid.UUID) (FieldDef, error) {
	return s.fields.ArchiveFieldDef(ctx, ws, id)
}

// ContactFields returns one contact's custom values paired with the definitions
// that describe them, so a caller can render labels and types without a second
// round trip and without re-deriving the join itself.
//
// A value whose key has no live definition is still returned, marked by a nil
// Def. That is how an archived-key value stays visible instead of vanishing
// from a page that is meant to show what the record actually holds.
func (s *Service) ContactFields(ctx context.Context, ws, contactID uuid.UUID) ([]FieldValue, error) {
	values, err := s.fields.GetCustomFields(ctx, ws, contactID)
	if err != nil {
		return nil, err
	}
	defs, err := s.fields.ListFieldDefs(ctx, ws)
	if err != nil {
		return nil, err
	}
	return joinValues(defs, values), nil
}

// SetContactFields replaces a contact's custom values with the submitted set.
//
// It is a whole-object replace, not a merge: the caller is an edit form that
// rendered every live field, so a key it omitted was cleared on purpose. The
// import path is the merge case and goes through UpsertContact's `||` instead.
//
// Every submitted key must name a LIVE definition and every value must satisfy
// its type. Writing to an archived key is refused rather than accepted, because
// the field is retired precisely so nothing new accumulates under it. Values
// already stored under archived or unknown keys are preserved untouched — the
// form never showed them, so its silence about them is not an instruction.
func (s *Service) SetContactFields(ctx context.Context, ws, contactID uuid.UUID, submitted map[string]string) ([]FieldValue, error) {
	defs, err := s.fields.ListFieldDefs(ctx, ws)
	if err != nil {
		return nil, err
	}
	live := liveByKey(defs)

	// Read first so values under keys the form could not show survive the
	// replace. ErrNotFound here is also what proves the contact is ours before
	// anything is written.
	existing, err := s.fields.GetCustomFields(ctx, ws, contactID)
	if err != nil {
		return nil, err
	}
	next := make(map[string]string, len(existing)+len(submitted))
	for k, v := range existing {
		if _, isLive := live[k]; !isLive {
			next[k] = v
		}
	}

	for key, raw := range submitted {
		def, ok := live[key]
		if !ok {
			return nil, &InvalidFieldError{Key: key, Reason: "no live custom field with this key"}
		}
		value, err := def.CoerceValue(raw)
		if err != nil {
			return nil, err
		}
		// An empty value clears the key rather than storing "". A stored empty
		// string and an absent key render identically in an email, so keeping
		// both would be two representations of one state.
		if value == "" {
			continue
		}
		next[key] = value
	}

	if err := s.fields.SetCustomFields(ctx, ws, contactID, next); err != nil {
		return nil, err
	}
	return joinValues(defs, next), nil
}

// FieldValue is one stored value with the definition that describes it. Def is
// nil when the key has no live definition (archived, or written before the
// definitions table existed).
type FieldValue struct {
	Key   string
	Value string
	Def   *FieldDef
}

// joinValues pairs stored values with live definitions, in a stable order:
// defined fields first in their listed order (label, then key — see the query),
// then orphaned keys sorted by key. Live fields with no value are included with
// an empty Value so a form can render the full set from this one result.
func joinValues(defs []FieldDef, values map[string]string) []FieldValue {
	out := make([]FieldValue, 0, len(defs)+len(values))
	claimed := make(map[string]struct{}, len(defs))
	for i, d := range defs {
		if !d.Live() {
			continue
		}
		claimed[d.Key] = struct{}{}
		// Index rather than range copy: taking &d would alias the loop variable
		// for every entry. (Go 1.22+ scopes it per-iteration so this is safe
		// either way; indexing makes it obviously safe.)
		out = append(out, FieldValue{Key: d.Key, Value: values[d.Key], Def: &defs[i]})
	}
	orphans := make([]string, 0, len(values))
	for k := range values {
		if _, ok := claimed[k]; !ok {
			orphans = append(orphans, k)
		}
	}
	slices.Sort(orphans)
	for _, k := range orphans {
		out = append(out, FieldValue{Key: k, Value: values[k]})
	}
	return out
}

func findByID(defs []FieldDef, id uuid.UUID) (FieldDef, bool) {
	for _, d := range defs {
		if d.ID == id {
			return d, true
		}
	}
	return FieldDef{}, false
}
