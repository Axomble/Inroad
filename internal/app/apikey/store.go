package apikey

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

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
	Create(ctx context.Context, p CreateParams) (gen.ApiKey, error)
	// GetByPrefix resolves a presented token's public prefix to its stored row
	// (pgx.ErrNoRows when unknown). It is the ONLY verify-path lookup; the prefix
	// is globally unique, so it also resolves the workspace.
	GetByPrefix(ctx context.Context, prefix string) (gen.ApiKey, error)
	// ListByWorkspace returns the workspace's keys WITHOUT their secret hash (the
	// projection omits it by construction).
	ListByWorkspace(ctx context.Context, ws uuid.UUID) ([]gen.ListApiKeysByWorkspaceRow, error)
	// Revoke marks (ws, id) revoked, tenant-pinned and idempotent. Returns the
	// number of rows affected: 1 when the key exists in ws (revoked or already
	// revoked), 0 when it is unknown or belongs to another workspace.
	Revoke(ctx context.Context, ws, id uuid.UUID) (int64, error)
	// TouchLastUsed stamps last_used_at; best-effort, called off the request path.
	TouchLastUsed(ctx context.Context, id uuid.UUID) error
}

// PgStore is the sqlc-backed persistence for the apikey domain.
type PgStore struct {
	q *gen.Queries
}

// NewPgStore builds a PgStore over the given pool.
func NewPgStore(pool *pgxpool.Pool) *PgStore {
	return &PgStore{q: gen.New(pool)}
}

func (s *PgStore) Create(ctx context.Context, p CreateParams) (gen.ApiKey, error) {
	return s.q.CreateApiKey(ctx, gen.CreateApiKeyParams{
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

func (s *PgStore) Revoke(ctx context.Context, ws, id uuid.UUID) (int64, error) {
	return s.q.RevokeApiKey(ctx, gen.RevokeApiKeyParams{ID: id, WorkspaceID: ws})
}

func (s *PgStore) TouchLastUsed(ctx context.Context, id uuid.UUID) error {
	return s.q.TouchApiKeyLastUsed(ctx, id)
}
