// Package contact manages contacts and CSV import into lists.
package contact

import (
	"context"

	"github.com/google/uuid"
	"github.com/inroad/inroad/internal/platform/db/gen"
)

// UpsertInput carries the fields required to create or update a contact.
type UpsertInput struct{ Email, FirstName, LastName, Company string }

// Store is the repository interface this domain depends on. It is defined
// here (by the consumer), not by the persistence layer, so the service can
// be unit-tested against a fake without a database.
type Store interface {
	// Upsert returns the contact id and whether it was newly inserted.
	Upsert(ctx context.Context, workspaceID uuid.UUID, in UpsertInput) (uuid.UUID, bool, error)
	AddToList(ctx context.Context, listID, contactID uuid.UUID) error
	ListByList(ctx context.Context, workspaceID, listID uuid.UUID, limit, offset int32) ([]gen.Contact, error)
}

// PgStore implements Store by wrapping sqlc-generated queries.
type PgStore struct{ q *gen.Queries }

func NewPgStore(q *gen.Queries) *PgStore { return &PgStore{q: q} }

func (s *PgStore) Upsert(ctx context.Context, ws uuid.UUID, in UpsertInput) (uuid.UUID, bool, error) {
	row, err := s.q.UpsertContact(ctx, gen.UpsertContactParams{
		WorkspaceID: ws, Email: in.Email, FirstName: in.FirstName, LastName: in.LastName, Company: in.Company,
	})
	if err != nil {
		return uuid.Nil, false, err
	}
	return row.ID, row.Inserted, nil
}
func (s *PgStore) AddToList(ctx context.Context, listID, contactID uuid.UUID) error {
	return s.q.AddListMember(ctx, gen.AddListMemberParams{ListID: listID, ContactID: contactID})
}
func (s *PgStore) ListByList(ctx context.Context, ws, listID uuid.UUID, limit, offset int32) ([]gen.Contact, error) {
	return s.q.ListContactsByList(ctx, gen.ListContactsByListParams{ListID: listID, WorkspaceID: ws, Limit: limit, Offset: offset})
}
