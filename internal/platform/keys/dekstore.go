// Package keys adapts the sqlc-backed persistence to the crypto.DEKStore seam
// so the composition roots (cmd/inroad, cmd/worker) can build a crypto.Keyring
// without the crypto package importing db/gen. It sits in platform so both the
// API and worker binaries can wire it, and it imports only other platform
// packages (crypto, db/gen) — never app/*.
package keys

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/inroad/inroad/internal/platform/config"
	"github.com/inroad/inroad/internal/platform/crypto"
	"github.com/inroad/inroad/internal/platform/db/gen"
)

// BuildKeyring assembles the two-level key hierarchy from config: it selects the
// KeyProvider (KEK) by cfg.KeyProvider — only "local" is implemented today, and
// an unknown value fails closed with an error rather than silently degrading —
// then builds the Keyring over a PgDEKStore for the given queries plus the
// legacy master-key Sealer that opens pre-DEK v1 blobs (which re-seal to v2 on
// the next write). Both binary composition roots (cmd/inroad, cmd/worker) call
// this so the fail-closed guard lives in exactly one place.
func BuildKeyring(cfg *config.Config, q *gen.Queries) (*crypto.Keyring, error) {
	if cfg.KeyProvider != "local" {
		return nil, fmt.Errorf("unsupported INROAD_KEY_PROVIDER %q (only \"local\" is implemented)", cfg.KeyProvider)
	}
	kp, err := crypto.NewLocalKeyProvider(cfg.MasterKey)
	if err != nil {
		return nil, fmt.Errorf("key provider: %w", err)
	}
	legacy, err := crypto.NewSealer(cfg.MasterKey)
	if err != nil {
		return nil, fmt.Errorf("legacy sealer: %w", err)
	}
	return crypto.NewKeyring(kp, NewPgDEKStore(q), legacy), nil
}

// PgDEKStore is the sqlc-backed crypto.DEKStore: a thin adapter over the
// generated queries. It carries no crypto policy — it only reads and writes the
// wrapped DEK bytes; the plaintext DEK is never handed to it.
type PgDEKStore struct{ q *gen.Queries }

// compile-time guarantee the adapter satisfies the seam the Keyring depends on.
var _ crypto.DEKStore = (*PgDEKStore)(nil)

// NewPgDEKStore builds the adapter over the pool-bound *gen.Queries.
func NewPgDEKStore(q *gen.Queries) *PgDEKStore { return &PgDEKStore{q: q} }

// GetWrappedDEK returns the wrapped DEK and the wrapping provider's name. A
// missing row (pgx.ErrNoRows) maps to crypto.ErrDEKNotFound so the Keyring can
// distinguish a first-use miss (create a DEK) from a genuine backend error
// (surface it).
func (s *PgDEKStore) GetWrappedDEK(ctx context.Context, ws uuid.UUID) ([]byte, string, error) {
	row, err := s.q.GetWorkspaceDEK(ctx, ws)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, "", crypto.ErrDEKNotFound
		}
		return nil, "", err
	}
	return row.WrappedDek, row.KeyProvider, nil
}

// PutWrappedDEK persists a wrapped DEK fail-if-exists: the workspace_deks
// primary key rejects an overwrite, so a race returns the pg unique-violation
// error (the Keyring then re-Gets the winner). A DEK is never replaced in place.
func (s *PgDEKStore) PutWrappedDEK(ctx context.Context, ws uuid.UUID, wrapped []byte, provider string) error {
	return s.q.CreateWorkspaceDEK(ctx, gen.CreateWorkspaceDEKParams{
		WorkspaceID: ws,
		WrappedDek:  wrapped,
		KeyProvider: provider,
	})
}
