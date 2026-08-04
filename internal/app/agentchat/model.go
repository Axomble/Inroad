package agentchat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/inroad/inroad/internal/platform/ai"
	"github.com/inroad/inroad/internal/platform/crypto"
	"github.com/inroad/inroad/internal/platform/db/gen"
)

// ResolvedModel is a model the run loop can actually dial: which door it comes
// through, what the provider calls it, its limits, and a ready ChatStreamer
// built from that door's unsealed credentials.
//
// The credentials themselves are NOT a field here. They are unsealed inside
// Resolve, handed to the streamer factory, and go out of scope — nothing above
// this type ever holds a provider key, so no caller can log one by accident
// (security invariant 1).
type ResolvedModel struct {
	// ID is the composite "<provider_row_uuid>/<name>" recorded on the run.
	ID string
	// Name is the BARE model name the provider SDK expects.
	Name                string
	Kind                string
	ContextWindowTokens int
	MaxOutputTokens     int
	Streamer            ai.ChatStreamer
}

// ModelResolver is the run loop's window onto workspace AI configuration: what
// model a selector means, and what extra instructions the workspace appends to
// the system prompt.
//
// It is an interface so the loop can be tested against a fake provider with no
// database, no keyring, and no network.
type ModelResolver interface {
	// Resolve turns a selector — an explicit model id, or one of the
	// 'default-smart-model' / 'default-fast-model' sentinels — into a dialable
	// model. Every failure wraps ai.ErrNoModel.
	Resolve(ctx context.Context, workspaceID uuid.UUID, selector string) (ResolvedModel, error)
	// Instructions returns the workspace's additional_instructions, appended
	// verbatim to the end of the stable system prompt.
	Instructions(ctx context.Context, workspaceID uuid.UUID) (string, error)
}

// NativeCatalog serves models.dev metadata for native provider kinds
// (*ai.CatalogSource in production).
type NativeCatalog interface {
	NativeModels(ctx context.Context, providerKey string) ([]ai.NativeModel, error)
}

// Sealers unseals a workspace's credential blobs (*crypto.Keyring in
// production). Narrowed to the one method this package needs.
type Sealers interface {
	SealerFor(ctx context.Context, workspaceID uuid.UUID) (*crypto.Sealer, error)
}

// PgModelResolver resolves models against the workspace_ai_* tables PR A1
// owns. It READS those tables directly rather than importing app/aisettings:
// app packages do not import each other, and the alternative — routing a
// runtime concern through another domain's HTTP-shaped service — would be a
// worse coupling than sharing the schema through sqlc.
type PgModelResolver struct {
	q       *gen.Queries
	keyring Sealers
	catalog NativeCatalog
	factory ai.StreamerFactory
}

func NewPgModelResolver(q *gen.Queries, keyring Sealers, catalog NativeCatalog, factory ai.StreamerFactory) *PgModelResolver {
	return &PgModelResolver{q: q, keyring: keyring, catalog: catalog, factory: factory}
}

func (r *PgModelResolver) Instructions(ctx context.Context, workspaceID uuid.UUID) (string, error) {
	row, err := r.q.GetAISettings(ctx, workspaceID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return row.AdditionalInstructions, nil
}

func (r *PgModelResolver) Resolve(ctx context.Context, workspaceID uuid.UUID, selector string) (ResolvedModel, error) {
	settings, err := r.q.GetAISettings(ctx, workspaceID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return ResolvedModel{}, err
	}
	defaultSmart, defaultFast := ai.SentinelSmartModel, ai.SentinelFastModel
	if settings.DefaultSmartModel != "" {
		defaultSmart, defaultFast = settings.DefaultSmartModel, settings.DefaultFastModel
	}

	available, err := r.availableModels(ctx, workspaceID, settings.EnabledModelIds)
	if err != nil {
		return ResolvedModel{}, err
	}
	model, err := ai.ResolveModel(selector, defaultSmart, defaultFast, available)
	if err != nil {
		return ResolvedModel{}, err
	}

	provider, err := r.q.GetAIProvider(ctx, gen.GetAIProviderParams{ID: model.ProviderID, WorkspaceID: workspaceID})
	if errors.Is(err, pgx.ErrNoRows) {
		// The model list was built from this workspace's own provider rows, so
		// this only happens if a door was deleted mid-flight.
		return ResolvedModel{}, errors.Join(ai.ErrNoModel, errors.New("provider door for "+model.ID+" is no longer configured"))
	}
	if err != nil {
		return ResolvedModel{}, err
	}

	creds, err := r.openCredentials(ctx, workspaceID, provider.SecretCiphertext)
	if err != nil {
		return ResolvedModel{}, err
	}
	cfg, err := decodeConfig(provider.Config)
	if err != nil {
		return ResolvedModel{}, err
	}
	streamer, err := r.factory.Streamer(provider.Kind, creds, cfg)
	if err != nil {
		return ResolvedModel{}, fmt.Errorf("agentchat: build streamer for %s: %w", provider.Kind, err)
	}
	return ResolvedModel{
		ID:                  model.ID,
		Name:                model.Name,
		Kind:                provider.Kind,
		ContextWindowTokens: model.ContextWindowTokens,
		MaxOutputTokens:     model.MaxOutputTokens,
		Streamer:            streamer,
	}, nil
}

// availableModels builds the workspace's merged model set — models.dev entries
// for native doors plus user-defined models — filtered to the enabled list. An
// EMPTY enabled list means "everything a configured door can serve", matching
// the settings semantics in PR A1.
func (r *PgModelResolver) availableModels(ctx context.Context, workspaceID uuid.UUID, enabled []string) ([]ai.CatalogModel, error) {
	providers, err := r.q.ListAIProviders(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	var out []ai.CatalogModel
	for _, p := range providers {
		key, ok := ai.NativeCatalogKey(p.Kind)
		if !ok {
			continue
		}
		native, err := r.catalog.NativeModels(ctx, key)
		// An unreachable catalog with no cached snapshot degrades to "no
		// native entries": custom models still resolve, so a self-host that
		// cannot reach models.dev can still run the agent.
		if errors.Is(err, ai.ErrCatalogUnavailable) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, n := range native {
			out = append(out, ai.CatalogModel{
				ID: ai.ModelID(p.ID, n.Name), ProviderID: p.ID, Kind: p.Kind,
				Name: n.Name, Label: n.Label,
				ContextWindowTokens: n.ContextWindowTokens, MaxOutputTokens: n.MaxOutputTokens,
				SupportsReasoning: n.SupportsReasoning, Source: ai.SourceCatalog,
			})
		}
	}
	custom, err := r.q.ListAIModels(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	for _, m := range custom {
		out = append(out, ai.CatalogModel{
			ID: ai.ModelID(m.ProviderID, m.Name), ProviderID: m.ProviderID, Kind: m.Kind,
			Name: m.Name, Label: m.Label,
			ContextWindowTokens: int(m.ContextWindowTokens), MaxOutputTokens: int(m.MaxOutputTokens),
			SupportsReasoning: m.SupportsReasoning, Source: ai.SourceCustom, CustomID: m.ID,
		})
	}
	if len(enabled) == 0 {
		return out, nil
	}
	allowed := make(map[string]bool, len(enabled))
	for _, id := range enabled {
		allowed[id] = true
	}
	filtered := out[:0]
	for _, m := range out {
		if allowed[m.ID] {
			filtered = append(filtered, m)
		}
	}
	return filtered, nil
}

// openCredentials unseals a door's credential blob. The plaintext exists only
// in this frame and in the streamer the factory builds from it.
func (r *PgModelResolver) openCredentials(ctx context.Context, workspaceID uuid.UUID, ciphertext string) (ai.Credentials, error) {
	sealer, err := r.keyring.SealerFor(ctx, workspaceID)
	if err != nil {
		return ai.Credentials{}, err
	}
	blob, err := sealer.Open(ciphertext)
	if err != nil {
		return ai.Credentials{}, fmt.Errorf("agentchat: open provider credentials: %w", err)
	}
	var c ai.Credentials
	if err := json.Unmarshal(blob, &c); err != nil {
		return ai.Credentials{}, fmt.Errorf("agentchat: decode credential blob: %w", err)
	}
	return c, nil
}

// decodeConfig fails loud on a corrupt persisted config rather than treating it
// as empty — an empty config would silently dial a DIFFERENT endpoint than the
// operator configured.
func decodeConfig(raw []byte) (map[string]string, error) {
	out := map[string]string{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &out); err != nil {
			return nil, fmt.Errorf("agentchat: decode provider config: %w", err)
		}
	}
	return out, nil
}

// Compile-time proof the Postgres-backed resolver satisfies the loop's seam.
var _ ModelResolver = (*PgModelResolver)(nil)
