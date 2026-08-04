//go:build integration

package aisettings

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inroad/inroad/internal/platform/db"
	"github.com/inroad/inroad/internal/platform/db/dbtest"
	"github.com/inroad/inroad/internal/platform/db/gen"
)

// aiFixture bundles a per-test environment so the setup helper returns one
// value, mirroring the warmup integration tests.
type aiFixture struct {
	ctx   context.Context
	pool  *pgxpool.Pool
	q     *gen.Queries
	store *PgStore
	ws    gen.Workspace
}

func setupAI(t *testing.T) aiFixture {
	t.Helper()
	ctx := context.Background()
	if err := db.Migrate(dbtest.DSN(t)); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	pool, err := db.Connect(ctx, dbtest.DSN(t))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	q := gen.New(pool)
	w, err := q.CreateWorkspace(ctx, "AI "+uuid.NewString())
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	return aiFixture{ctx: ctx, pool: pool, q: q, store: NewPgStore(q), ws: w}
}

func (f aiFixture) insertProvider(t *testing.T, kind, config string) gen.InsertAIProviderRow {
	t.Helper()
	row, err := f.store.InsertProvider(f.ctx, gen.InsertAIProviderParams{
		WorkspaceID: f.ws.ID, Kind: kind, Config: []byte(config),
		SecretCiphertext: "ct", KeyPrefix: "sk-aaaaa", DisplayName: kind,
	})
	if err != nil {
		t.Fatalf("insert %s provider: %v", kind, err)
	}
	return row
}

// TestSettingsUpsertRoundTrip proves the singleton upsert: no row reads as
// pgx.ErrNoRows, a first upsert inserts, a second updates in place, and the
// text[] column round-trips.
func TestSettingsUpsertRoundTrip(t *testing.T) {
	f := setupAI(t)

	if _, err := f.store.GetSettings(f.ctx, f.ws.ID); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("fresh workspace must be ErrNoRows, got %v", err)
	}
	first, err := f.store.UpsertSettings(f.ctx, gen.UpsertAISettingsParams{
		WorkspaceID:       f.ws.ID,
		DefaultSmartModel: "default-smart-model",
		DefaultFastModel:  "default-fast-model",
		EnabledModelIds:   []string{"a/one", "b/two"},
	})
	if err != nil || len(first.EnabledModelIds) != 2 {
		t.Fatalf("first upsert: %+v, %v", first, err)
	}
	second, err := f.store.UpsertSettings(f.ctx, gen.UpsertAISettingsParams{
		WorkspaceID:            f.ws.ID,
		DefaultSmartModel:      "default-smart-model",
		DefaultFastModel:       "default-fast-model",
		EnabledModelIds:        []string{},
		AdditionalInstructions: "be brief",
	})
	if err != nil || second.AdditionalInstructions != "be brief" || len(second.EnabledModelIds) != 0 {
		t.Fatalf("second upsert: %+v, %v", second, err)
	}
}

// TestProviderTargetUniquenessAndTenancy proves the expression unique index:
// the same (kind, base_url) conflicts as ErrDuplicateTarget, a different
// base_url is a second door, the kind CHECK rejects unknown values, and rows
// are invisible/undeletable across workspaces.
func TestProviderTargetUniquenessAndTenancy(t *testing.T) {
	f := setupAI(t)

	first := f.insertProvider(t, "openai_compatible", `{"base_url":"https://a.example/v1"}`)
	if _, err := f.store.InsertProvider(f.ctx, gen.InsertAIProviderParams{
		WorkspaceID: f.ws.ID, Kind: "openai_compatible",
		Config: []byte(`{"base_url":"https://a.example/v1"}`), SecretCiphertext: "ct2",
	}); !errors.Is(err, ErrDuplicateTarget) {
		t.Fatalf("same target must be ErrDuplicateTarget, got %v", err)
	}
	f.insertProvider(t, "openai_compatible", `{"base_url":"https://b.example/v1"}`)
	f.insertProvider(t, "anthropic", `{}`)

	if _, err := f.store.InsertProvider(f.ctx, gen.InsertAIProviderParams{
		WorkspaceID: f.ws.ID, Kind: "mistral", Config: []byte(`{}`), SecretCiphertext: "ct",
	}); err == nil || errors.Is(err, ErrDuplicateTarget) {
		t.Fatalf("kind CHECK must reject unknown value, got %v", err)
	}

	rows, err := f.store.ListProviders(f.ctx, f.ws.ID)
	if err != nil || len(rows) != 3 {
		t.Fatalf("want 3 doors, got %d (%v)", len(rows), err)
	}

	other, err := f.q.CreateWorkspace(f.ctx, "Other "+uuid.NewString())
	if err != nil {
		t.Fatalf("other workspace: %v", err)
	}
	if _, err := f.store.GetProvider(f.ctx, other.ID, first.ID); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("foreign Get must be ErrNoRows, got %v", err)
	}
	if n, err := f.store.DeleteProvider(f.ctx, other.ID, first.ID); err != nil || n != 0 {
		t.Fatalf("foreign delete must affect zero rows: %d (%v)", n, err)
	}
	// The SAME target string in ANOTHER workspace does not conflict.
	if _, err := f.store.InsertProvider(f.ctx, gen.InsertAIProviderParams{
		WorkspaceID: other.ID, Kind: "openai_compatible",
		Config: []byte(`{"base_url":"https://a.example/v1"}`), SecretCiphertext: "ct",
	}); err != nil {
		t.Fatalf("same target in another workspace must insert: %v", err)
	}
}

// TestModelsSelfEnforcingTenancyAndCascade proves the model surface: the
// INSERT...SELECT rejects a foreign door (ErrProviderNotInWorkspace), the
// unique name is ErrDuplicateModel, the CHECK rejects non-positive windows,
// and deleting a door cascades its models.
func TestModelsSelfEnforcingTenancyAndCascade(t *testing.T) {
	f := setupAI(t)
	door := f.insertProvider(t, "openai_compatible", `{"base_url":"https://gw.example/v1"}`)

	valid := gen.InsertAIModelParams{
		WorkspaceID: f.ws.ID, ProviderID: door.ID, Name: "llama-3.3-70b", Label: "Llama",
		ContextWindowTokens: 131072, MaxOutputTokens: 8192,
	}
	created, err := f.store.InsertModel(f.ctx, valid)
	if err != nil {
		t.Fatalf("InsertModel: %v", err)
	}
	if _, err := f.store.InsertModel(f.ctx, valid); !errors.Is(err, ErrDuplicateModel) {
		t.Fatalf("duplicate name must be ErrDuplicateModel, got %v", err)
	}

	other, err := f.q.CreateWorkspace(f.ctx, "Other "+uuid.NewString())
	if err != nil {
		t.Fatalf("other workspace: %v", err)
	}
	foreign := valid
	foreign.WorkspaceID = other.ID
	foreign.Name = "other-model"
	if _, err := f.store.InsertModel(f.ctx, foreign); !errors.Is(err, ErrProviderNotInWorkspace) {
		t.Fatalf("foreign door must be ErrProviderNotInWorkspace, got %v", err)
	}

	bad := valid
	bad.Name = "zero-window"
	bad.ContextWindowTokens = 0
	if _, err := f.store.InsertModel(f.ctx, bad); err == nil {
		t.Fatalf("CHECK must reject non-positive context window")
	}

	if n, err := f.store.DeleteModel(f.ctx, other.ID, created.ID); err != nil || n != 0 {
		t.Fatalf("foreign model delete must affect zero rows: %d (%v)", n, err)
	}
	if n, err := f.store.DeleteProvider(f.ctx, f.ws.ID, door.ID); err != nil || n != 1 {
		t.Fatalf("DeleteProvider: %d (%v)", n, err)
	}
	models, err := f.store.ListModels(f.ctx, f.ws.ID)
	if err != nil || len(models) != 0 {
		t.Fatalf("models must cascade with the door: %+v (%v)", models, err)
	}
}

// TestCatalogCacheSingleRow proves the single-row upsert semantics of
// ai_catalog_cache.
func TestCatalogCacheSingleRow(t *testing.T) {
	f := setupAI(t)

	if _, err := f.q.GetAICatalogCache(f.ctx); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("fresh cache must be ErrNoRows, got %v", err)
	}
	if err := f.q.UpsertAICatalogCache(f.ctx, []byte(`{"a":1}`)); err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	if err := f.q.UpsertAICatalogCache(f.ctx, []byte(`{"b":2}`)); err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	row, err := f.q.GetAICatalogCache(f.ctx)
	if err != nil || string(row.Payload) != `{"b": 2}` && string(row.Payload) != `{"b":2}` {
		t.Fatalf("cache must hold the latest payload: %s (%v)", row.Payload, err)
	}
}
