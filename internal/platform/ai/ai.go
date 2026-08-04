// Package ai holds the LLM platform seam for the agent platform: provider
// kinds, the runtime models.dev catalog source, model-list discovery over
// HTTP, and sentinel model resolution. The ChatStreamer runtime arrives in
// phase A2 — this package deliberately carries NO provider SDK dependency.
package ai

import (
	"errors"

	"github.com/google/uuid"
)

// Provider kinds — the same values the workspace_ai_providers CHECK
// constraint accepts. One kind per DOOR to a vendor: the same Anthropic model
// may be reachable natively, via Bedrock, and via Vertex (three kinds).
const (
	KindAnthropic        = "anthropic"
	KindBedrock          = "bedrock"
	KindVertexAnthropic  = "vertex_anthropic"
	KindOpenAI           = "openai"
	KindAzureOpenAI      = "azure_openai"
	KindOpenAICompatible = "openai_compatible"
	KindGoogle           = "google"
	KindVertexGoogle     = "vertex_google"
)

// Kinds is the closed set of valid provider kinds.
var Kinds = map[string]bool{
	KindAnthropic: true, KindBedrock: true, KindVertexAnthropic: true,
	KindOpenAI: true, KindAzureOpenAI: true, KindOpenAICompatible: true,
	KindGoogle: true, KindVertexGoogle: true,
}

// Model-selector sentinels. Workspace settings store one of these (the
// default) or an explicit model id; ResolveModel walks a preference list for
// a sentinel.
const (
	SentinelSmartModel = "default-smart-model"
	SentinelFastModel  = "default-fast-model"
)

// Model sources for the merged model list.
const (
	SourceCatalog = "catalog" // native metadata from models.dev
	SourceCustom  = "custom"  // user-defined workspace_ai_models row
)

// Credentials is the kind-variant secret object sealed WHOLE into
// workspace_ai_providers.secret_ciphertext (JSON-encoded, then sealed under
// the per-workspace DEK). Only the fields a kind uses are set.
type Credentials struct {
	APIKey             string `json:"api_key,omitempty"`
	AccessKeyID        string `json:"access_key_id,omitempty"`
	SecretAccessKey    string `json:"secret_access_key,omitempty"`
	ServiceAccountJSON string `json:"service_account_json,omitempty"`
}

// CatalogModel is one entry of the workspace's merged model list: a
// models.dev native model or a user-defined custom model, both scoped to the
// provider ROW they are reachable through. Identity everywhere is
// ID = "<provider_row_uuid>/<name>" — the same model name via two doors never
// collides, and the runtime can go from id straight to the door's sealed
// credentials.
type CatalogModel struct {
	ID                  string
	ProviderID          uuid.UUID
	Kind                string
	Name                string
	Label               string
	ContextWindowTokens int
	MaxOutputTokens     int
	SupportsReasoning   bool
	Source              string
	// CustomID is the workspace_ai_models row id for Source == SourceCustom
	// (the DELETE /ai/models/{id} address); uuid.Nil for catalog entries.
	CustomID uuid.UUID
	// Informational display costs (USD per million tokens); nil when the
	// source doesn't report pricing.
	InputCostPerMTok  *float64
	OutputCostPerMTok *float64
}

// ModelID builds the composite provider-row-scoped model id.
func ModelID(providerID uuid.UUID, name string) string {
	return providerID.String() + "/" + name
}

// ErrNoModel is wrapped by every ResolveModel failure so callers can map the
// whole class to one status without matching message text.
var ErrNoModel = errors.New("ai: no usable model")

// Ordered sentinel preferences, matched against a model's bare NAME (ids are
// per-workspace provider-row uuids, so a static id list cannot work). Smart
// runs the agent loop; fast runs cheap one-shots (thread titles). A workspace
// serving none of these names (pure gateway setup) falls back to the first
// available model, so a sentinel still resolves.
var (
	smartPreferenceNames = []string{"claude-sonnet-5", "gpt-5.2", "claude-opus-5"}
	fastPreferenceNames  = []string{"claude-haiku-4-5", "gpt-5.2-mini", "claude-sonnet-5"}
)

// ResolveModel resolves a selector against the workspace's available
// (enabled) model list:
//
//   - an explicit id must be present in available;
//   - a sentinel first defers to the workspace's stored default for that
//     sentinel (when it is an explicit id), then walks the sentinel's name
//     preference list, then falls back to the first available model.
func ResolveModel(selector, defaultSmart, defaultFast string, available []CatalogModel) (CatalogModel, error) {
	if selector == SentinelSmartModel && defaultSmart != "" && defaultSmart != SentinelSmartModel {
		selector = defaultSmart
	}
	if selector == SentinelFastModel && defaultFast != "" && defaultFast != SentinelFastModel {
		selector = defaultFast
	}

	var preference []string
	switch selector {
	case SentinelSmartModel:
		preference = smartPreferenceNames
	case SentinelFastModel:
		preference = fastPreferenceNames
	default:
		for _, m := range available {
			if m.ID == selector {
				return m, nil
			}
		}
		return CatalogModel{}, errors.Join(ErrNoModel, errors.New("model "+selector+" is not available in this workspace"))
	}

	for _, name := range preference {
		for _, m := range available {
			if m.Name == name {
				return m, nil
			}
		}
	}
	if len(available) > 0 {
		return available[0], nil
	}
	return CatalogModel{}, errors.Join(ErrNoModel, errors.New("no models available for "+selector))
}
