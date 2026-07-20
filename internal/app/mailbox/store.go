// Package mailbox manages SMTP/IMAP mailbox connections used to send and
// poll campaign email. This is the reference implementation of the domain
// pattern used across the app: the domain defines its own repository
// interface (Store) and the service depends on that interface, never on the
// concrete sqlc-backed struct (dependency inversion).
package mailbox

import (
	"context"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/platform/db/gen"
)

// Store is the repository interface this domain depends on. It is defined
// here (by the consumer), not by the persistence layer, so the service can
// be unit-tested against a fake without a database.
//
// Every method returns MailboxSafe (never gen.Mailbox) so SecretCiphertext
// can't leak out of this package. The one exception is the internal
// getWithSecret path (unexported) that the worker's coreapi in-process
// client uses via its own DB access — control-plane callers only ever
// touch the safe view.
type Store interface {
	Create(ctx context.Context, arg gen.CreateMailboxParams) (MailboxSafe, error)
	Get(ctx context.Context, workspaceID, id uuid.UUID) (MailboxSafe, error)
	List(ctx context.Context, workspaceID uuid.UUID) ([]MailboxSafe, error)
	CountByEmail(ctx context.Context, workspaceID uuid.UUID, email string) (int64, error)
	UpdateStatus(ctx context.Context, workspaceID, id uuid.UUID, status, lastErr string) (MailboxSafe, error)
	Delete(ctx context.Context, workspaceID, id uuid.UUID) (int64, error)
}

// PgStore implements Store by wrapping sqlc-generated queries. It is the
// only place in this domain that knows about gen.Queries or its param
// structs (aside from Create, which takes gen.CreateMailboxParams directly
// to avoid a 17-field wrapper).
type PgStore struct {
	q *gen.Queries
}

func NewPgStore(q *gen.Queries) *PgStore { return &PgStore{q: q} }

func (s *PgStore) Create(ctx context.Context, arg gen.CreateMailboxParams) (MailboxSafe, error) {
	m, err := s.q.CreateMailbox(ctx, arg)
	if err != nil {
		return MailboxSafe{}, err
	}
	return safeFromGen(m), nil
}

func (s *PgStore) Get(ctx context.Context, workspaceID, id uuid.UUID) (MailboxSafe, error) {
	m, err := s.q.GetMailbox(ctx, gen.GetMailboxParams{ID: id, WorkspaceID: workspaceID})
	if err != nil {
		return MailboxSafe{}, err
	}
	return safeFromGen(m), nil
}

func (s *PgStore) List(ctx context.Context, workspaceID uuid.UUID) ([]MailboxSafe, error) {
	rows, err := s.q.ListMailboxes(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	out := make([]MailboxSafe, len(rows))
	for i, m := range rows {
		out[i] = safeFromGen(m)
	}
	return out, nil
}

func (s *PgStore) CountByEmail(ctx context.Context, workspaceID uuid.UUID, email string) (int64, error) {
	return s.q.CountMailboxByEmail(ctx, gen.CountMailboxByEmailParams{WorkspaceID: workspaceID, Email: email})
}

func (s *PgStore) UpdateStatus(ctx context.Context, workspaceID, id uuid.UUID, status, lastErr string) (MailboxSafe, error) {
	m, err := s.q.UpdateMailboxStatus(ctx, gen.UpdateMailboxStatusParams{
		ID:          id,
		WorkspaceID: workspaceID,
		Status:      status,
		LastError:   lastErr,
	})
	if err != nil {
		return MailboxSafe{}, err
	}
	return safeFromGen(m), nil
}

func (s *PgStore) Delete(ctx context.Context, workspaceID, id uuid.UUID) (int64, error) {
	return s.q.DeleteMailbox(ctx, gen.DeleteMailboxParams{ID: id, WorkspaceID: workspaceID})
}
