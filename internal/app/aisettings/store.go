// Package aisettings is the control-plane domain for workspace AI
// configuration: model defaults, multi-cloud provider credentials (sealed
// under the per-workspace DEK), user-defined models, and provider model
// discovery (agent-platform spec §2/§3, PR A1). Following the reference
// pattern in internal/app/mailbox, the domain defines its own repository
// interface (Store) and the service depends on that interface, never the
// concrete sqlc-backed struct.
package aisettings

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/inroad/inroad/internal/platform/db/gen"
)

// Store-level sentinels: constraint outcomes translated so the service can
// map them to HTTP statuses without importing pgx/pgconn.
var (
	// ErrDuplicateTarget is the uq_workspace_ai_providers_target violation: the
	// workspace already has a door with this (kind, base_url/endpoint/project).
	ErrDuplicateTarget = errors.New("aisettings: provider target already configured")
	// ErrDuplicateModel is the workspace_ai_models unique-name violation.
	ErrDuplicateModel = errors.New("aisettings: model already exists for this provider")
	// ErrProviderNotInWorkspace is returned when a model insert names a
	// provider row the workspace does not own (the self-enforcing INSERT ...
	// SELECT emitted zero rows).
	ErrProviderNotInWorkspace = errors.New("aisettings: provider not in workspace")
)

// Store is the repository interface this domain depends on. Every method is
// workspace-scoped; the workspace id always comes from the JWT at the
// handler, never from caller-controlled input. Provider LIST/INSERT/UPDATE
// reads return the MASKED sqlc row shapes (no secret_ciphertext column
// selected); only GetProvider returns the full row, and it never reaches a
// response DTO.
type Store interface {
	GetSettings(ctx context.Context, workspaceID uuid.UUID) (gen.WorkspaceAiSetting, error)
	UpsertSettings(ctx context.Context, arg gen.UpsertAISettingsParams) (gen.WorkspaceAiSetting, error)

	InsertProvider(ctx context.Context, arg gen.InsertAIProviderParams) (gen.InsertAIProviderRow, error)
	GetProvider(ctx context.Context, workspaceID, id uuid.UUID) (gen.WorkspaceAiProvider, error)
	ListProviders(ctx context.Context, workspaceID uuid.UUID) ([]gen.ListAIProvidersRow, error)
	UpdateProviderConfig(ctx context.Context, arg gen.UpdateAIProviderConfigParams) (gen.UpdateAIProviderConfigRow, error)
	UpdateProviderSecret(ctx context.Context, arg gen.UpdateAIProviderSecretParams) (int64, error)
	DeleteProvider(ctx context.Context, workspaceID, id uuid.UUID) (int64, error)

	InsertModel(ctx context.Context, arg gen.InsertAIModelParams) (gen.WorkspaceAiModel, error)
	ListModels(ctx context.Context, workspaceID uuid.UUID) ([]gen.ListAIModelsRow, error)
	DeleteModel(ctx context.Context, workspaceID, id uuid.UUID) (int64, error)
}

// PgStore implements Store by wrapping sqlc-generated queries. It is the only
// place in this domain that knows about gen.Queries or Postgres error codes.
type PgStore struct {
	q *gen.Queries
}

func NewPgStore(q *gen.Queries) *PgStore { return &PgStore{q: q} }

func (s *PgStore) GetSettings(ctx context.Context, workspaceID uuid.UUID) (gen.WorkspaceAiSetting, error) {
	return s.q.GetAISettings(ctx, workspaceID)
}

func (s *PgStore) UpsertSettings(ctx context.Context, arg gen.UpsertAISettingsParams) (gen.WorkspaceAiSetting, error) {
	return s.q.UpsertAISettings(ctx, arg)
}

func (s *PgStore) InsertProvider(ctx context.Context, arg gen.InsertAIProviderParams) (gen.InsertAIProviderRow, error) {
	row, err := s.q.InsertAIProvider(ctx, arg)
	if isUniqueViolation(err) {
		return gen.InsertAIProviderRow{}, ErrDuplicateTarget
	}
	return row, err
}

func (s *PgStore) GetProvider(ctx context.Context, workspaceID, id uuid.UUID) (gen.WorkspaceAiProvider, error) {
	return s.q.GetAIProvider(ctx, gen.GetAIProviderParams{ID: id, WorkspaceID: workspaceID})
}

func (s *PgStore) ListProviders(ctx context.Context, workspaceID uuid.UUID) ([]gen.ListAIProvidersRow, error) {
	return s.q.ListAIProviders(ctx, workspaceID)
}

func (s *PgStore) UpdateProviderConfig(ctx context.Context, arg gen.UpdateAIProviderConfigParams) (gen.UpdateAIProviderConfigRow, error) {
	row, err := s.q.UpdateAIProviderConfig(ctx, arg)
	if isUniqueViolation(err) {
		return gen.UpdateAIProviderConfigRow{}, ErrDuplicateTarget
	}
	return row, err
}

func (s *PgStore) UpdateProviderSecret(ctx context.Context, arg gen.UpdateAIProviderSecretParams) (int64, error) {
	return s.q.UpdateAIProviderSecret(ctx, arg)
}

func (s *PgStore) DeleteProvider(ctx context.Context, workspaceID, id uuid.UUID) (int64, error) {
	return s.q.DeleteAIProvider(ctx, gen.DeleteAIProviderParams{ID: id, WorkspaceID: workspaceID})
}

// InsertModel creates a user-defined model. The underlying INSERT ... SELECT
// is self-enforcing: a provider row not in the workspace emits zero rows
// (pgx.ErrNoRows → ErrProviderNotInWorkspace); a duplicate name under the
// same door is the unique violation (ErrDuplicateModel).
func (s *PgStore) InsertModel(ctx context.Context, arg gen.InsertAIModelParams) (gen.WorkspaceAiModel, error) {
	row, err := s.q.InsertAIModel(ctx, arg)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return gen.WorkspaceAiModel{}, ErrProviderNotInWorkspace
	case isUniqueViolation(err):
		return gen.WorkspaceAiModel{}, ErrDuplicateModel
	}
	return row, err
}

func (s *PgStore) ListModels(ctx context.Context, workspaceID uuid.UUID) ([]gen.ListAIModelsRow, error) {
	return s.q.ListAIModels(ctx, workspaceID)
}

func (s *PgStore) DeleteModel(ctx context.Context, workspaceID, id uuid.UUID) (int64, error) {
	return s.q.DeleteAIModel(ctx, gen.DeleteAIModelParams{ID: id, WorkspaceID: workspaceID})
}

// isUniqueViolation reports SQLSTATE 23505 (unique_violation).
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
