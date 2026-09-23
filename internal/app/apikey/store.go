package apikey

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inroad/inroad/internal/platform/audit"
	"github.com/inroad/inroad/internal/platform/db/gen"
)

// CreateParams is the persistence input for a new key, in clean Go types so the
// service never handles pgtype values. The store maps it to the sqlc params.
type CreateParams struct {
	WorkspaceID     uuid.UUID
	CreatedBy       uuid.UUID
	Name            string
	Prefix          string
	SecretHash      []byte
	Scopes          []string
	IPAllowlist     []string // canonical CIDR strings; nil/empty = no restriction
	RateLimitPerMin *int32   // nil = unlimited
	ExpiresAt       *time.Time
}

// Store is the persistence seam the service and verifier depend on (dependency
// inversion): exactly the methods they use, so unit tests inject an in-memory
// fake with no DB. *PgStore satisfies it.
type Store interface {
	// Create persists the key and ev (an apikey.created audit event, completed
	// with the new key's id) in ONE transaction: a key without its audit row
	// cannot exist (security.md invariant 82, in-transaction class).
	Create(ctx context.Context, p CreateParams, ev audit.Event) (gen.ApiKey, error)
	// GetByPrefix resolves a presented token's public prefix to its stored row
	// (pgx.ErrNoRows when unknown). It is the ONLY verify-path lookup; the prefix
	// is globally unique, so it also resolves the workspace.
	GetByPrefix(ctx context.Context, prefix string) (gen.ApiKey, error)
	// ListByWorkspace returns the workspace's keys WITHOUT their secret hash (the
	// projection omits it by construction).
	ListByWorkspace(ctx context.Context, ws uuid.UUID) ([]gen.ListApiKeysByWorkspaceRow, error)
	// Revoke marks (ws, id) revoked, tenant-pinned and idempotent. Returns the
	// number of rows affected: 1 when the key exists in ws (revoked or already
	// revoked), 0 when it is unknown or belongs to another workspace. ev is
	// written in the same transaction when a row matched, and not at all when
	// none did (nothing happened, so there is nothing to record).
	Revoke(ctx context.Context, ws, id uuid.UUID, ev audit.Event) (int64, error)
	// TouchLastUsed stamps last_used_at; best-effort, called off the request path.
	TouchLastUsed(ctx context.Context, id uuid.UUID) error
}

// PgStore is the sqlc-backed persistence for the apikey domain.
type PgStore struct {
	pool *pgxpool.Pool
	q    *gen.Queries
}

// NewPgStore builds a PgStore over the given pool.
func NewPgStore(pool *pgxpool.Pool) *PgStore {
	return &PgStore{pool: pool, q: gen.New(pool)}
}

func (s *PgStore) Create(ctx context.Context, p CreateParams, ev audit.Event) (gen.ApiKey, error) {
	var key gen.ApiKey
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		qtx := s.q.WithTx(tx)
		var err error
		if key, err = createKey(ctx, qtx, p); err != nil {
			return err
		}
		ev.TargetID = key.ID.String()
		return audit.Insert(ctx, qtx, ev)
	})
	return key, err
}

func createKey(ctx context.Context, q *gen.Queries, p CreateParams) (gen.ApiKey, error) {
	return q.CreateApiKey(ctx, gen.CreateApiKeyParams{
		WorkspaceID:     p.WorkspaceID,
		CreatedByUserID: pgUUID(p.CreatedBy),
		Name:            p.Name,
		Prefix:          p.Prefix,
		SecretHash:      p.SecretHash,
		Scopes:          p.Scopes,
		IpAllowlist:     p.IPAllowlist,
		RateLimitPerMin: p.RateLimitPerMin,
		ExpiresAt:       pgTimePtr(p.ExpiresAt),
	})
}

func (s *PgStore) GetByPrefix(ctx context.Context, prefix string) (gen.ApiKey, error) {
	return s.q.GetApiKeyByPrefix(ctx, prefix)
}

func (s *PgStore) ListByWorkspace(ctx context.Context, ws uuid.UUID) ([]gen.ListApiKeysByWorkspaceRow, error) {
	return s.q.ListApiKeysByWorkspace(ctx, ws)
}

func (s *PgStore) Revoke(ctx context.Context, ws, id uuid.UUID, ev audit.Event) (int64, error) {
	var n int64
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		qtx := s.q.WithTx(tx)
		var err error
		if n, err = qtx.RevokeApiKey(ctx, gen.RevokeApiKeyParams{ID: id, WorkspaceID: ws}); err != nil || n == 0 {
			return err
		}
		return audit.Insert(ctx, qtx, ev)
	})
	return n, err
}

func (s *PgStore) TouchLastUsed(ctx context.Context, id uuid.UUID) error {
	return s.q.TouchApiKeyLastUsed(ctx, id)
}
