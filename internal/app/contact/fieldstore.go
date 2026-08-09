package contact

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/inroad/inroad/internal/platform/db/gen"
)

// uniqueViolation is Postgres 23505. The unique index on (workspace_id, key) is
// what actually decides whether a key is free, so a pre-check SELECT would only
// widen the race, not close it — the constraint is the source of truth and this
// translates it.
const uniqueViolation = "23505"

// PgFieldStore implements FieldStore over the sqlc-generated queries. It is a
// separate type from PgStore because FieldStore is a separate seam; both wrap
// the same *gen.Queries.
type PgFieldStore struct{ q *gen.Queries }

// NewPgFieldStore builds the field store. It takes the same pool as PgStore;
// the definitions path needs no composed SQL, so gen.Queries is enough.
func NewPgFieldStore(q *gen.Queries) *PgFieldStore { return &PgFieldStore{q: q} }

func (s *PgFieldStore) ListFieldDefs(ctx context.Context, ws uuid.UUID) ([]FieldDef, error) {
	rows, err := s.q.ListContactFieldDefs(ctx, ws)
	if err != nil {
		return nil, fmt.Errorf("list custom fields: %w", err)
	}
	out := make([]FieldDef, 0, len(rows))
	for _, r := range rows {
		out = append(out, FieldDef{
			ID: r.ID, Key: r.Key, Label: r.Label, Type: r.FieldType, Options: r.Options,
			CreatedAt: r.CreatedAt.Time, ArchivedAt: timePtr(r.ArchivedAt.Valid, r.ArchivedAt.Time),
		})
	}
	return out, nil
}

func (s *PgFieldStore) CreateFieldDef(ctx context.Context, ws uuid.UUID, in FieldDefInput) (FieldDef, error) {
	row, err := s.q.CreateContactFieldDef(ctx, gen.CreateContactFieldDefParams{
		WorkspaceID: ws, Key: in.Key, Label: in.Label, FieldType: in.Type, Options: in.Options,
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
			return FieldDef{}, ErrFieldKeyTaken
		}
		return FieldDef{}, fmt.Errorf("create custom field: %w", err)
	}
	return FieldDef{
		ID: row.ID, Key: row.Key, Label: row.Label, Type: row.FieldType, Options: row.Options,
		CreatedAt: row.CreatedAt.Time, ArchivedAt: timePtr(row.ArchivedAt.Valid, row.ArchivedAt.Time),
	}, nil
}

// UpdateFieldDef distinguishes "no such field here" from "archived", because
// the two mean different things to the caller: one is a wrong id, the other is
// a field that exists and deliberately refuses edits. The UPDATE itself filters
// on archived_at IS NULL, so a zero-row result is disambiguated by a follow-up
// read rather than by trusting the caller's view of the field's state.
func (s *PgFieldStore) UpdateFieldDef(ctx context.Context, ws, id uuid.UUID, label string, options []string) (FieldDef, error) {
	row, err := s.q.UpdateContactFieldDef(ctx, gen.UpdateContactFieldDefParams{
		WorkspaceID: ws, ID: id, Label: label, Options: options,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return FieldDef{}, s.classifyMiss(ctx, ws, id)
	}
	if err != nil {
		return FieldDef{}, fmt.Errorf("update custom field: %w", err)
	}
	return FieldDef{
		ID: row.ID, Key: row.Key, Label: row.Label, Type: row.FieldType, Options: row.Options,
		CreatedAt: row.CreatedAt.Time, ArchivedAt: timePtr(row.ArchivedAt.Valid, row.ArchivedAt.Time),
	}, nil
}

func (s *PgFieldStore) ArchiveFieldDef(ctx context.Context, ws, id uuid.UUID) (FieldDef, error) {
	row, err := s.q.ArchiveContactFieldDef(ctx, gen.ArchiveContactFieldDefParams{WorkspaceID: ws, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return FieldDef{}, ErrFieldNotFound
	}
	if err != nil {
		return FieldDef{}, fmt.Errorf("archive custom field: %w", err)
	}
	return FieldDef{
		ID: row.ID, Key: row.Key, Label: row.Label, Type: row.FieldType, Options: row.Options,
		CreatedAt: row.CreatedAt.Time, ArchivedAt: timePtr(row.ArchivedAt.Valid, row.ArchivedAt.Time),
	}, nil
}

// classifyMiss answers why a workspace-pinned, archived-filtered UPDATE matched
// nothing. A field that is genuinely absent and one that is merely retired both
// produce zero rows, and telling the operator "not found" for a field they can
// see in the list would be a lie.
func (s *PgFieldStore) classifyMiss(ctx context.Context, ws, id uuid.UUID) error {
	if _, err := s.q.GetContactFieldDef(ctx, gen.GetContactFieldDefParams{WorkspaceID: ws, ID: id}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrFieldNotFound
		}
		return fmt.Errorf("read custom field: %w", err)
	}
	return ErrFieldArchived
}

// GetCustomFields decodes the contact's JSONB value object. Values are written
// exclusively as JSON strings (see FieldDef.CoerceValue), but this decodes
// permissively via decodeStringMap because the column predates the definitions
// table and may already hold objects written before any of this existed.
func (s *PgFieldStore) GetCustomFields(ctx context.Context, ws, contactID uuid.UUID) (map[string]string, error) {
	raw, err := s.q.GetContactCustomFields(ctx, gen.GetContactCustomFieldsParams{WorkspaceID: ws, ID: contactID})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read custom fields: %w", err)
	}
	return decodeStringMap(raw)
}

func (s *PgFieldStore) SetCustomFields(ctx context.Context, ws, contactID uuid.UUID, values map[string]string) error {
	// A nil map marshals to "null", which is a legal JSONB value but not an
	// object — every reader here expects an object, so normalise before encoding.
	if values == nil {
		values = map[string]string{}
	}
	encoded, err := json.Marshal(values)
	if err != nil {
		return fmt.Errorf("encode custom fields: %w", err)
	}
	n, err := s.q.SetContactCustomFields(ctx, gen.SetContactCustomFieldsParams{
		WorkspaceID: ws, ID: contactID, CustomFields: encoded,
	})
	if err != nil {
		return fmt.Errorf("write custom fields: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// timePtr lifts a pgtype.Timestamptz's two fields into the nullable time the
// domain type uses, so "not archived" is one representation (nil) rather than
// a zero time a caller might compare against.
func timePtr(valid bool, t time.Time) *time.Time {
	if !valid {
		return nil
	}
	return &t
}

// decodeStringMap turns a custom_fields document into the string map the rest of
// the domain works in. It mirrors coreapi/inprocess.decodeCustom's tolerance of
// non-string values, with one deliberate difference: a malformed document is an
// ERROR here rather than an empty map. That path renders an email and prefers
// sending with a blank token to not sending at all; this path is a UI read and
// an edit form, where silently showing "no custom fields" invites the operator
// to overwrite data that is actually still there.
func decodeStringMap(raw []byte) (map[string]string, error) {
	if len(raw) == 0 {
		return map[string]string{}, nil
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("decode custom fields: %w", err)
	}
	out := make(map[string]string, len(doc))
	for k, v := range doc {
		switch t := v.(type) {
		case string:
			out[k] = t
		case nil:
			out[k] = ""
		default:
			out[k] = fmt.Sprintf("%v", t)
		}
	}
	return out, nil
}
