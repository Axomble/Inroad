package aisettings

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/inroad/inroad/internal/platform/ai"
	"github.com/inroad/inroad/internal/platform/crypto"
	"github.com/inroad/inroad/internal/platform/db/gen"
)

// Sentinel errors the handler layer maps to HTTP status codes.
var (
	// ErrValidation covers a malformed request: unknown kind, missing
	// kind-required credentials/config, a base_url/endpoint that violates the
	// scheme/SSRF policy, or an unknown model reference in settings.
	ErrValidation = errors.New("aisettings: invalid input")
	// ErrNotFound is returned when an id-addressed provider or model does not
	// exist in the caller's workspace.
	ErrNotFound = errors.New("aisettings: not found")
	// ErrDuplicate is returned when a create/update collides with an existing
	// row (same provider target, or same model name under a door) — 409.
	ErrDuplicate = errors.New("aisettings: already exists")
	// ErrDiscoveryFailed wraps a provider endpoint that could not be listed
	// (dial refused, SSRF-blocked, auth rejected, not a model list) — 502.
	ErrDiscoveryFailed = errors.New("aisettings: discovery failed")
)

// keyPrefixLen is how many leading characters of an api key / access key id
// are stored and echoed back for display ("sk-ant-a…"). minKeyLen keeps the
// stored prefix a strict prefix — a shorter secret stores an empty prefix
// rather than revealing itself.
const (
	keyPrefixLen = 8
	minKeyLen    = 12
)

// HostClassifier is the SSRF-vetting seam for user-supplied base-URL/endpoint
// hosts at WRITE time (mail.ClassifyHost in production; discovery re-vets at
// dial time through the guarded transport). It reports whether the host is
// private (loopback/RFC1918/ULA) and errors on unconditionally hostile
// targets (link-local incl. cloud metadata, multicast, unspecified) or a
// failed resolution.
type HostClassifier func(ctx context.Context, host string) (private bool, err error)

// NativeCatalog serves models.dev metadata for native provider kinds.
// Production is *ai.CatalogSource; tests fake it.
type NativeCatalog interface {
	NativeModels(ctx context.Context, providerKey string) ([]ai.NativeModel, error)
}

// Discoverer lists the models a provider door can serve. Production is
// *ai.HTTPDiscoverer (SSRF-guarded transport); tests fake it.
type Discoverer interface {
	Discover(ctx context.Context, req ai.DiscoverRequest) (ai.DiscoveryResult, error)
}

// ServiceDeps wires the service's collaborators (options struct — the list
// outgrew positional parameters).
type ServiceDeps struct {
	Store   Store
	Keyring *crypto.Keyring
	Catalog NativeCatalog
	// Discoverer performs provider model-list discovery over the guarded
	// transport.
	Discoverer Discoverer
	// ClassifyHost vets user-supplied hosts at write time;
	// AllowPrivateBaseURL is the operator opt-in
	// (INROAD_AI_ALLOW_PRIVATE_BASE_URL) for private/loopback endpoints.
	ClassifyHost        HostClassifier
	AllowPrivateBaseURL bool
}

// Service implements the AI-settings use cases: settings, provider doors
// (sealed credentials), user-defined models, and discovery.
type Service struct {
	store      Store
	keyring    *crypto.Keyring
	catalog    NativeCatalog
	discoverer Discoverer

	classify            HostClassifier
	allowPrivateBaseURL bool
}

func NewService(d ServiceDeps) *Service {
	return &Service{
		store: d.Store, keyring: d.Keyring, catalog: d.Catalog, discoverer: d.Discoverer,
		classify: d.ClassifyHost, allowPrivateBaseURL: d.AllowPrivateBaseURL,
	}
}

// ---- wire shapes -----------------------------------------------------------

// SettingsDTO is the wire shape of GET/PUT /ai/settings.
type SettingsDTO struct {
	DefaultSmartModel      string   `json:"default_smart_model"`
	DefaultFastModel       string   `json:"default_fast_model"`
	EnabledModelIDs        []string `json:"enabled_model_ids"`
	AdditionalInstructions string   `json:"additional_instructions"`
}

// ProviderDTO is the masked wire shape of a provider door. There is no
// secret/credential field on this type — omission by construction (security
// invariant 2); config carries only non-secret connection settings.
type ProviderDTO struct {
	ID          string            `json:"id"`
	Kind        string            `json:"kind"`
	DisplayName string            `json:"display_name"`
	Config      map[string]string `json:"config"`
	Configured  bool              `json:"configured"`
	KeyPrefix   string            `json:"key_prefix"`
	CreatedAt   string            `json:"created_at"`
	UpdatedAt   string            `json:"updated_at"`
}

// ModelDTO is one row of GET /ai/models: a models.dev native entry or a
// user-defined model, both scoped to the provider row they are reachable
// through. ID is the stable composite "<provider_row_uuid>/<name>" — settings
// store exactly these ids.
type ModelDTO struct {
	ID                  string `json:"id"`
	ProviderID          string `json:"provider_id"`
	Kind                string `json:"kind"`
	Name                string `json:"name"`
	Label               string `json:"label"`
	ContextWindowTokens int    `json:"context_window_tokens"`
	MaxOutputTokens     int    `json:"max_output_tokens"`
	SupportsReasoning   bool   `json:"supports_reasoning"`
	Source              string `json:"source"`
	// CustomModelID is the deletable row id behind a source=custom entry
	// (the {id} DELETE /ai/models/{id} takes); null for catalog entries.
	CustomModelID     *string  `json:"custom_model_id"`
	InputCostPerMTok  *float64 `json:"input_cost_per_mtok"`
	OutputCostPerMTok *float64 `json:"output_cost_per_mtok"`
	Enabled           bool     `json:"enabled"`
}

// DiscoveryDTO is the wire shape of POST /ai/providers/{id}/discover.
// Supported=false means the kind has no A1 discovery path (manual entry
// works); candidates are RETURNED, never persisted here.
type DiscoveryDTO struct {
	Supported bool                 `json:"supported"`
	Models    []ai.DiscoveredModel `json:"models"`
}

// ---- settings --------------------------------------------------------------

// defaultSettings is the response for a workspace with no settings row yet.
func defaultSettings() SettingsDTO {
	return SettingsDTO{
		DefaultSmartModel: ai.SentinelSmartModel,
		DefaultFastModel:  ai.SentinelFastModel,
		EnabledModelIDs:   []string{},
	}
}

// GetSettings returns the workspace's AI settings, or the defaults when no
// row exists yet (the row is created lazily on first PUT).
func (s *Service) GetSettings(ctx context.Context, ws uuid.UUID) (SettingsDTO, error) {
	row, err := s.store.GetSettings(ctx, ws)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return defaultSettings(), nil
	case err != nil:
		return SettingsDTO{}, err
	}
	return settingsDTO(row), nil
}

// SettingsUpdate is the PUT /ai/settings request after JSON decoding. Every
// field is a pointer so an omitted key keeps the current (or default) value.
type SettingsUpdate struct {
	DefaultSmartModel      *string
	DefaultFastModel       *string
	EnabledModelIDs        *[]string
	AdditionalInstructions *string
}

// UpdateSettings merges the request over current-or-default values, validates
// every model reference against the workspace's available model set (the
// same merged set GET /ai/models returns), and upserts the full row.
func (s *Service) UpdateSettings(ctx context.Context, ws uuid.UUID, req SettingsUpdate) (SettingsDTO, error) {
	base, err := s.GetSettings(ctx, ws)
	if err != nil {
		return SettingsDTO{}, fmt.Errorf("aisettings: read current settings: %w", err)
	}
	if req.DefaultSmartModel != nil {
		base.DefaultSmartModel = *req.DefaultSmartModel
	}
	if req.DefaultFastModel != nil {
		base.DefaultFastModel = *req.DefaultFastModel
	}
	if req.EnabledModelIDs != nil {
		base.EnabledModelIDs = *req.EnabledModelIDs
	}
	if req.AdditionalInstructions != nil {
		base.AdditionalInstructions = *req.AdditionalInstructions
	}
	if base.EnabledModelIDs == nil {
		base.EnabledModelIDs = []string{}
	}

	available, err := s.availableModels(ctx, ws)
	if err != nil {
		return SettingsDTO{}, err
	}
	availableIDs := make(map[string]bool, len(available))
	for _, m := range available {
		availableIDs[m.ID] = true
	}
	if err := validateModelRef(base.DefaultSmartModel, ai.SentinelSmartModel, "default_smart_model", availableIDs); err != nil {
		return SettingsDTO{}, err
	}
	if err := validateModelRef(base.DefaultFastModel, ai.SentinelFastModel, "default_fast_model", availableIDs); err != nil {
		return SettingsDTO{}, err
	}
	for _, id := range base.EnabledModelIDs {
		if !availableIDs[id] {
			return SettingsDTO{}, fmt.Errorf("%w: enabled_model_ids contains unavailable model %q", ErrValidation, id)
		}
	}

	row, err := s.store.UpsertSettings(ctx, gen.UpsertAISettingsParams{
		WorkspaceID:            ws,
		DefaultSmartModel:      base.DefaultSmartModel,
		DefaultFastModel:       base.DefaultFastModel,
		EnabledModelIds:        base.EnabledModelIDs,
		AdditionalInstructions: base.AdditionalInstructions,
	})
	if err != nil {
		return SettingsDTO{}, err
	}
	return settingsDTO(row), nil
}

// validateModelRef accepts the field's own sentinel or an id from the
// available set. The OTHER sentinel is rejected — "default-fast-model" is not
// a meaningful value for default_smart_model.
func validateModelRef(value, sentinel, field string, available map[string]bool) error {
	if value == sentinel || available[value] {
		return nil
	}
	return fmt.Errorf("%w: %s must be %q or an available model id, got %q", ErrValidation, field, sentinel, value)
}

// ---- providers -------------------------------------------------------------

// ProviderCreateInput is the POST /ai/providers request.
type ProviderCreateInput struct {
	Kind        string
	DisplayName string
	Credentials ai.Credentials
	Config      map[string]string
}

// CreateProvider validates the kind-specific credential/config shape, seals
// the credential object WHOLE under the workspace DEK, and inserts the door.
// The raw credentials exist only within this call; only the ciphertext and a
// display prefix are persisted, and the returned DTO is masked by
// construction.
func (s *Service) CreateProvider(ctx context.Context, ws uuid.UUID, in ProviderCreateInput) (ProviderDTO, error) {
	if !ai.Kinds[in.Kind] {
		return ProviderDTO{}, fmt.Errorf("%w: unknown kind %q", ErrValidation, in.Kind)
	}
	cfg := normalizeConfig(in.Config)
	if err := s.validateConfig(ctx, in.Kind, cfg); err != nil {
		return ProviderDTO{}, err
	}
	if err := validateCredentials(in.Kind, in.Credentials); err != nil {
		return ProviderDTO{}, err
	}

	ciphertext, prefix, err := s.sealCredentials(ctx, ws, in.Kind, in.Credentials)
	if err != nil {
		return ProviderDTO{}, err
	}
	cfgJSON, err := json.Marshal(cfg)
	if err != nil {
		return ProviderDTO{}, err
	}
	row, err := s.store.InsertProvider(ctx, gen.InsertAIProviderParams{
		WorkspaceID:      ws,
		Kind:             in.Kind,
		Config:           cfgJSON,
		SecretCiphertext: ciphertext,
		KeyPrefix:        prefix,
		DisplayName:      in.DisplayName,
	})
	switch {
	case errors.Is(err, ErrDuplicateTarget):
		return ProviderDTO{}, fmt.Errorf("%w: a %s provider with this target is already configured", ErrDuplicate, in.Kind)
	case err != nil:
		return ProviderDTO{}, err
	}
	return providerDTO(row.ID, row.Kind, row.DisplayName, row.Config, row.KeyPrefix, row.CreatedAt, row.UpdatedAt), nil
}

// ProviderUpdateInput is the PUT /ai/providers/{id} request. Credentials nil
// = keep the sealed blob; Config nil = keep; DisplayName nil = keep. Kind is
// immutable (it is not part of this input).
type ProviderUpdateInput struct {
	DisplayName *string
	Credentials *ai.Credentials
	Config      map[string]string
}

// UpdateProvider updates a door's non-secret half and, when credentials are
// supplied, replaces the sealed blob.
func (s *Service) UpdateProvider(ctx context.Context, ws, id uuid.UUID, in ProviderUpdateInput) (ProviderDTO, error) {
	existing, err := s.store.GetProvider(ctx, ws, id)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return ProviderDTO{}, ErrNotFound
	case err != nil:
		return ProviderDTO{}, err
	}

	cfg := configMap(existing.Config)
	if in.Config != nil {
		cfg = normalizeConfig(in.Config)
	}
	if err := s.validateConfig(ctx, existing.Kind, cfg); err != nil {
		return ProviderDTO{}, err
	}
	displayName := existing.DisplayName
	if in.DisplayName != nil {
		displayName = *in.DisplayName
	}

	// Replace the credential blob FIRST so the masked row the config update
	// returns already carries the new key_prefix.
	if in.Credentials != nil {
		if err := validateCredentials(existing.Kind, *in.Credentials); err != nil {
			return ProviderDTO{}, err
		}
		ciphertext, prefix, err := s.sealCredentials(ctx, ws, existing.Kind, *in.Credentials)
		if err != nil {
			return ProviderDTO{}, err
		}
		if _, err := s.store.UpdateProviderSecret(ctx, gen.UpdateAIProviderSecretParams{
			ID: id, WorkspaceID: ws, SecretCiphertext: ciphertext, KeyPrefix: prefix,
		}); err != nil {
			return ProviderDTO{}, err
		}
	}

	cfgJSON, err := json.Marshal(cfg)
	if err != nil {
		return ProviderDTO{}, err
	}
	row, err := s.store.UpdateProviderConfig(ctx, gen.UpdateAIProviderConfigParams{
		ID: id, WorkspaceID: ws, DisplayName: displayName, Config: cfgJSON,
	})
	switch {
	case errors.Is(err, ErrDuplicateTarget):
		return ProviderDTO{}, fmt.Errorf("%w: a %s provider with this target is already configured", ErrDuplicate, existing.Kind)
	case err != nil:
		return ProviderDTO{}, err
	}
	return providerDTO(row.ID, row.Kind, row.DisplayName, row.Config, row.KeyPrefix, row.CreatedAt, row.UpdatedAt), nil
}

// ListProviders returns the workspace's provider doors, masked.
func (s *Service) ListProviders(ctx context.Context, ws uuid.UUID) ([]ProviderDTO, error) {
	rows, err := s.store.ListProviders(ctx, ws)
	if err != nil {
		return nil, err
	}
	out := make([]ProviderDTO, len(rows))
	for i, r := range rows {
		out[i] = providerDTO(r.ID, r.Kind, r.DisplayName, r.Config, r.KeyPrefix, r.CreatedAt, r.UpdatedAt)
	}
	return out, nil
}

// DeleteProvider removes a door (its models cascade). Zero rows is
// ErrNotFound — id-addressed resources 404 like mailboxes do.
func (s *Service) DeleteProvider(ctx context.Context, ws, id uuid.UUID) error {
	rows, err := s.store.DeleteProvider(ctx, ws, id)
	if err != nil {
		return err
	}
	if rows == 0 {
		return ErrNotFound
	}
	return nil
}

// Discover lists the models a door can serve, unsealing its credentials only
// for the duration of the outbound call. Candidates are returned, never
// persisted — the frontend creates chosen ones via POST /ai/models.
func (s *Service) Discover(ctx context.Context, ws, id uuid.UUID) (DiscoveryDTO, error) {
	row, err := s.store.GetProvider(ctx, ws, id)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return DiscoveryDTO{}, ErrNotFound
	case err != nil:
		return DiscoveryDTO{}, err
	}
	creds, err := s.openCredentials(ctx, ws, row.SecretCiphertext)
	if err != nil {
		return DiscoveryDTO{}, err
	}
	res, err := s.discoverer.Discover(ctx, ai.DiscoverRequest{
		Kind:        row.Kind,
		Config:      configMap(row.Config),
		Credentials: creds,
	})
	if err != nil {
		// The upstream failure class (refused dial, SSRF-blocked, bad key,
		// not-a-model-list) is the caller's diagnosis; ours is "the provider
		// endpoint didn't answer usefully" — 502, never 500.
		return DiscoveryDTO{}, fmt.Errorf("%w: %w", ErrDiscoveryFailed, err)
	}
	if res.Models == nil {
		res.Models = []ai.DiscoveredModel{}
	}
	return DiscoveryDTO{Supported: res.Supported, Models: res.Models}, nil
}

// ---- models ----------------------------------------------------------------

// ModelCreateInput is the POST /ai/models request.
type ModelCreateInput struct {
	ProviderID          uuid.UUID
	Name                string
	Label               string
	ContextWindowTokens int
	MaxOutputTokens     int
	SupportsReasoning   bool
	InputCostPerMTok    *float64
	OutputCostPerMTok   *float64
}

// CreateModel registers a user-defined model under a provider door. The id it
// will surface as ("<provider_row_id>/<name>") must not collide with a native
// catalog entry of the same door — two entries answering to one id would make
// settings ambiguous.
func (s *Service) CreateModel(ctx context.Context, ws uuid.UUID, in ModelCreateInput) (ModelDTO, error) {
	switch {
	case in.Name == "":
		return ModelDTO{}, fmt.Errorf("%w: name is required", ErrValidation)
	case in.Label == "":
		return ModelDTO{}, fmt.Errorf("%w: label is required", ErrValidation)
	case in.ContextWindowTokens <= 0:
		return ModelDTO{}, fmt.Errorf("%w: context_window_tokens must be positive", ErrValidation)
	case in.MaxOutputTokens <= 0:
		return ModelDTO{}, fmt.Errorf("%w: max_output_tokens must be positive", ErrValidation)
	}

	available, err := s.availableModels(ctx, ws)
	if err != nil {
		return ModelDTO{}, err
	}
	newID := ai.ModelID(in.ProviderID, in.Name)
	for _, m := range available {
		if m.ID == newID {
			return ModelDTO{}, fmt.Errorf("%w: model %q already exists for this provider", ErrDuplicate, in.Name)
		}
	}

	row, err := s.store.InsertModel(ctx, gen.InsertAIModelParams{
		WorkspaceID:         ws,
		ProviderID:          in.ProviderID,
		Name:                in.Name,
		Label:               in.Label,
		ContextWindowTokens: int32(in.ContextWindowTokens),
		MaxOutputTokens:     int32(in.MaxOutputTokens),
		SupportsReasoning:   in.SupportsReasoning,
		InputCostPerMtok:    in.InputCostPerMTok,
		OutputCostPerMtok:   in.OutputCostPerMTok,
	})
	switch {
	case errors.Is(err, ErrProviderNotInWorkspace):
		return ModelDTO{}, ErrNotFound
	case errors.Is(err, ErrDuplicateModel):
		return ModelDTO{}, fmt.Errorf("%w: model %q already exists for this provider", ErrDuplicate, in.Name)
	case err != nil:
		return ModelDTO{}, err
	}

	// The door's kind, for the DTO. The insert just proved the row exists in
	// this workspace.
	provider, err := s.store.GetProvider(ctx, ws, in.ProviderID)
	if err != nil {
		return ModelDTO{}, err
	}
	settings, err := s.GetSettings(ctx, ws)
	if err != nil {
		return ModelDTO{}, err
	}
	dto := customModelDTO(row, provider.Kind)
	dto.Enabled = isEnabled(dto.ID, settings.EnabledModelIDs)
	return dto, nil
}

// DeleteModel removes a user-defined model. Zero rows is ErrNotFound.
func (s *Service) DeleteModel(ctx context.Context, ws, id uuid.UUID) error {
	rows, err := s.store.DeleteModel(ctx, ws, id)
	if err != nil {
		return err
	}
	if rows == 0 {
		return ErrNotFound
	}
	return nil
}

// ListModels returns the workspace's merged model list: models.dev native
// entries for native-kind doors plus user-defined models, each flagged
// enabled per the settings (empty enabled list = everything enabled).
func (s *Service) ListModels(ctx context.Context, ws uuid.UUID) ([]ModelDTO, error) {
	settings, err := s.GetSettings(ctx, ws)
	if err != nil {
		return nil, err
	}
	available, err := s.availableModels(ctx, ws)
	if err != nil {
		return nil, err
	}
	out := make([]ModelDTO, len(available))
	for i, m := range available {
		out[i] = ModelDTO{
			ID:                  m.ID,
			ProviderID:          m.ProviderID.String(),
			Kind:                m.Kind,
			Name:                m.Name,
			Label:               m.Label,
			ContextWindowTokens: m.ContextWindowTokens,
			MaxOutputTokens:     m.MaxOutputTokens,
			SupportsReasoning:   m.SupportsReasoning,
			Source:              m.Source,
			CustomModelID:       customModelID(m.CustomID),
			InputCostPerMTok:    m.InputCostPerMTok,
			OutputCostPerMTok:   m.OutputCostPerMTok,
			Enabled:             isEnabled(m.ID, settings.EnabledModelIDs),
		}
	}
	return out, nil
}

// customModelID renders a custom row id as a nullable wire field.
func customModelID(id uuid.UUID) *string {
	if id == uuid.Nil {
		return nil
	}
	s := id.String()
	return &s
}

// availableModels builds the workspace's merged model set. models.dev being
// unreachable with no cached snapshot (ai.ErrCatalogUnavailable) degrades to
// "no native entries" — discovery and manual models still work; a real cache
// backend error surfaces.
func (s *Service) availableModels(ctx context.Context, ws uuid.UUID) ([]ai.CatalogModel, error) {
	providers, err := s.store.ListProviders(ctx, ws)
	if err != nil {
		return nil, err
	}
	var out []ai.CatalogModel
	for _, p := range providers {
		key, ok := ai.NativeCatalogKey(p.Kind)
		if !ok {
			continue
		}
		native, err := s.catalog.NativeModels(ctx, key)
		if errors.Is(err, ai.ErrCatalogUnavailable) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, n := range native {
			in, outCost := n.InputCostPerMTok, n.OutputCostPerMTok
			out = append(out, ai.CatalogModel{
				ID:                  ai.ModelID(p.ID, n.Name),
				ProviderID:          p.ID,
				Kind:                p.Kind,
				Name:                n.Name,
				Label:               n.Label,
				ContextWindowTokens: n.ContextWindowTokens,
				MaxOutputTokens:     n.MaxOutputTokens,
				SupportsReasoning:   n.SupportsReasoning,
				Source:              ai.SourceCatalog,
				InputCostPerMTok:    &in,
				OutputCostPerMTok:   &outCost,
			})
		}
	}
	custom, err := s.store.ListModels(ctx, ws)
	if err != nil {
		return nil, err
	}
	for _, m := range custom {
		out = append(out, ai.CatalogModel{
			ID:                  ai.ModelID(m.ProviderID, m.Name),
			ProviderID:          m.ProviderID,
			Kind:                m.Kind,
			Name:                m.Name,
			Label:               m.Label,
			ContextWindowTokens: int(m.ContextWindowTokens),
			MaxOutputTokens:     int(m.MaxOutputTokens),
			SupportsReasoning:   m.SupportsReasoning,
			Source:              ai.SourceCustom,
			CustomID:            m.ID,
			InputCostPerMTok:    m.InputCostPerMtok,
			OutputCostPerMTok:   m.OutputCostPerMtok,
		})
	}
	return out, nil
}

func isEnabled(id string, enabledIDs []string) bool {
	if len(enabledIDs) == 0 {
		return true
	}
	for _, e := range enabledIDs {
		if e == id {
			return true
		}
	}
	return false
}

// ---- kind-specific validation ----------------------------------------------

// allowedConfigKeys is the closed per-kind config vocabulary. Unknown keys
// fail loud rather than being silently dropped.
var allowedConfigKeys = map[string]map[string]bool{
	ai.KindAnthropic:        {},
	ai.KindOpenAI:           {},
	ai.KindGoogle:           {},
	ai.KindOpenAICompatible: {"base_url": true},
	ai.KindAzureOpenAI:      {"endpoint": true, "api_version": true},
	ai.KindBedrock:          {"region": true},
	ai.KindVertexAnthropic:  {"project_id": true, "region": true},
	ai.KindVertexGoogle:     {"project_id": true, "region": true},
}

// validateConfig enforces the kind's config vocabulary and required fields,
// vetting user-supplied URL hosts (base_url, endpoint) against the SSRF
// policy.
func (s *Service) validateConfig(ctx context.Context, kind string, cfg map[string]string) error {
	allowed := allowedConfigKeys[kind]
	for k := range cfg {
		if !allowed[k] {
			return fmt.Errorf("%w: config key %q is not valid for kind %q", ErrValidation, k, kind)
		}
	}
	switch kind {
	case ai.KindOpenAICompatible:
		if cfg["base_url"] == "" {
			return fmt.Errorf("%w: config.base_url is required for kind %q", ErrValidation, kind)
		}
		return s.vetUserURL(ctx, "base_url", cfg["base_url"])
	case ai.KindAzureOpenAI:
		if cfg["endpoint"] == "" {
			return fmt.Errorf("%w: config.endpoint is required for kind %q", ErrValidation, kind)
		}
		if cfg["api_version"] == "" {
			return fmt.Errorf("%w: config.api_version is required for kind %q", ErrValidation, kind)
		}
		return s.vetUserURL(ctx, "endpoint", cfg["endpoint"])
	case ai.KindBedrock:
		if cfg["region"] == "" {
			return fmt.Errorf("%w: config.region is required for kind %q", ErrValidation, kind)
		}
	case ai.KindVertexAnthropic, ai.KindVertexGoogle:
		if cfg["project_id"] == "" || cfg["region"] == "" {
			return fmt.Errorf("%w: config.project_id and config.region are required for kind %q", ErrValidation, kind)
		}
	}
	return nil
}

// validateCredentials enforces the kind's credential shape: required fields
// present, foreign fields absent (a service-account blob on an api-key kind
// is a caller bug, not something to silently seal).
func validateCredentials(kind string, c ai.Credentials) error {
	requireAPIKey := func(required bool) error {
		if c.AccessKeyID != "" || c.SecretAccessKey != "" || c.ServiceAccountJSON != "" {
			return fmt.Errorf("%w: kind %q accepts only credentials.api_key", ErrValidation, kind)
		}
		if !required && c.APIKey == "" {
			return nil
		}
		if len(c.APIKey) < minKeyLen {
			return fmt.Errorf("%w: credentials.api_key must be at least %d characters", ErrValidation, minKeyLen)
		}
		return nil
	}
	switch kind {
	case ai.KindAnthropic, ai.KindOpenAI, ai.KindGoogle, ai.KindAzureOpenAI:
		return requireAPIKey(true)
	case ai.KindOpenAICompatible:
		// Keyless doors (a local Ollama) are legitimate.
		return requireAPIKey(false)
	case ai.KindBedrock:
		if c.APIKey != "" || c.ServiceAccountJSON != "" {
			return fmt.Errorf("%w: kind %q accepts only credentials.access_key_id + secret_access_key", ErrValidation, kind)
		}
		if len(c.AccessKeyID) < minKeyLen || c.SecretAccessKey == "" {
			return fmt.Errorf("%w: credentials.access_key_id and secret_access_key are required for kind %q", ErrValidation, kind)
		}
		return nil
	case ai.KindVertexAnthropic, ai.KindVertexGoogle:
		if c.APIKey != "" || c.AccessKeyID != "" || c.SecretAccessKey != "" {
			return fmt.Errorf("%w: kind %q accepts only credentials.service_account_json", ErrValidation, kind)
		}
		if _, err := serviceAccountEmail(c.ServiceAccountJSON); err != nil {
			return err
		}
		return nil
	default:
		return fmt.Errorf("%w: unknown kind %q", ErrValidation, kind)
	}
}

// serviceAccountEmail extracts client_email from a service-account JSON blob
// — the non-secret identifier used as the display prefix.
func serviceAccountEmail(saJSON string) (string, error) {
	if saJSON == "" {
		return "", fmt.Errorf("%w: credentials.service_account_json is required", ErrValidation)
	}
	var sa struct {
		ClientEmail string `json:"client_email"`
	}
	if err := json.Unmarshal([]byte(saJSON), &sa); err != nil || sa.ClientEmail == "" {
		return "", fmt.Errorf("%w: credentials.service_account_json must be a service-account key with client_email", ErrValidation)
	}
	return sa.ClientEmail, nil
}

// sealCredentials JSON-encodes and seals the credential object under the
// workspace DEK and derives the display prefix.
func (s *Service) sealCredentials(ctx context.Context, ws uuid.UUID, kind string, c ai.Credentials) (ciphertext, prefix string, err error) {
	blob, err := json.Marshal(c)
	if err != nil {
		return "", "", err
	}
	sealer, err := s.keyring.SealerFor(ctx, ws)
	if err != nil {
		return "", "", err
	}
	ciphertext, err = sealer.Seal(blob)
	if err != nil {
		return "", "", err
	}
	return ciphertext, credentialPrefix(kind, c), nil
}

// openCredentials unseals a door's credential blob for an outbound call. The
// plaintext exists only in the caller's frame and is never logged.
func (s *Service) openCredentials(ctx context.Context, ws uuid.UUID, ciphertext string) (ai.Credentials, error) {
	sealer, err := s.keyring.SealerFor(ctx, ws)
	if err != nil {
		return ai.Credentials{}, err
	}
	blob, err := sealer.Open(ciphertext)
	if err != nil {
		return ai.Credentials{}, err
	}
	var c ai.Credentials
	if err := json.Unmarshal(blob, &c); err != nil {
		return ai.Credentials{}, fmt.Errorf("aisettings: decode credential blob: %w", err)
	}
	return c, nil
}

// credentialPrefix derives the masked display identifier: the first 8 chars
// of the api key / access key id (only when long enough to stay a strict
// prefix), or the service account's non-secret client_email.
func credentialPrefix(kind string, c ai.Credentials) string {
	switch kind {
	case ai.KindBedrock:
		if len(c.AccessKeyID) >= minKeyLen {
			return c.AccessKeyID[:keyPrefixLen]
		}
	case ai.KindVertexAnthropic, ai.KindVertexGoogle:
		email, err := serviceAccountEmail(c.ServiceAccountJSON)
		if err == nil {
			return email
		}
	default:
		if len(c.APIKey) >= minKeyLen {
			return c.APIKey[:keyPrefixLen]
		}
	}
	return ""
}

// vetUserURL enforces the outbound-URL policy for user-supplied hosts
// (base_url, endpoint): absolute http(s), no embedded credentials, host
// resolvable and not link-local/metadata/multicast; a private or loopback
// host requires the operator opt-in; a PUBLIC host must be https.
func (s *Service) vetUserURL(ctx context.Context, field, raw string) error {
	u, err := url.Parse(raw)
	if err != nil || !u.IsAbs() || u.Hostname() == "" {
		return fmt.Errorf("%w: %s must be an absolute http(s) URL", ErrValidation, field)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("%w: %s scheme must be http or https", ErrValidation, field)
	}
	if u.User != nil {
		return fmt.Errorf("%w: %s must not contain credentials", ErrValidation, field)
	}
	private, err := s.classify(ctx, u.Hostname())
	if err != nil {
		return fmt.Errorf("%w: %s host not permitted: %w", ErrValidation, field, err)
	}
	if private && !s.allowPrivateBaseURL {
		return fmt.Errorf("%w: %s resolves to a private host; set INROAD_AI_ALLOW_PRIVATE_BASE_URL=true to allow", ErrValidation, field)
	}
	if !private && u.Scheme != "https" {
		return fmt.Errorf("%w: %s must use https for public hosts", ErrValidation, field)
	}
	return nil
}

// ---- mapping helpers ---------------------------------------------------------

// normalizeConfig drops empty values so "" and absent are one state (the
// uniqueness index coalesces them anyway).
func normalizeConfig(cfg map[string]string) map[string]string {
	out := make(map[string]string, len(cfg))
	for k, v := range cfg {
		if v != "" {
			out[k] = v
		}
	}
	return out
}

// configMap decodes the stored config JSONB; a corrupt blob yields an empty
// map rather than a 500 (the config is re-writable via PUT).
func configMap(raw []byte) map[string]string {
	out := map[string]string{}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &out)
	}
	return out
}

func providerDTO(id uuid.UUID, kind, displayName string, config []byte, keyPrefix string, createdAt, updatedAt pgtype.Timestamptz) ProviderDTO {
	return ProviderDTO{
		ID:          id.String(),
		Kind:        kind,
		DisplayName: displayName,
		Config:      configMap(config),
		Configured:  true,
		KeyPrefix:   keyPrefix,
		CreatedAt:   rfc3339(createdAt),
		UpdatedAt:   rfc3339(updatedAt),
	}
}

func customModelDTO(row gen.WorkspaceAiModel, kind string) ModelDTO {
	return ModelDTO{
		ID:                  ai.ModelID(row.ProviderID, row.Name),
		ProviderID:          row.ProviderID.String(),
		Kind:                kind,
		Name:                row.Name,
		Label:               row.Label,
		ContextWindowTokens: int(row.ContextWindowTokens),
		MaxOutputTokens:     int(row.MaxOutputTokens),
		SupportsReasoning:   row.SupportsReasoning,
		Source:              ai.SourceCustom,
		CustomModelID:       customModelID(row.ID),
		InputCostPerMTok:    row.InputCostPerMtok,
		OutputCostPerMTok:   row.OutputCostPerMtok,
	}
}

func settingsDTO(row gen.WorkspaceAiSetting) SettingsDTO {
	ids := row.EnabledModelIds
	if ids == nil {
		ids = []string{}
	}
	return SettingsDTO{
		DefaultSmartModel:      row.DefaultSmartModel,
		DefaultFastModel:       row.DefaultFastModel,
		EnabledModelIDs:        ids,
		AdditionalInstructions: row.AdditionalInstructions,
	}
}

// rfc3339 renders a timestamptz as an RFC3339 UTC string ("" when unset).
func rfc3339(t pgtype.Timestamptz) string {
	if !t.Valid {
		return ""
	}
	return t.Time.UTC().Format(time.RFC3339)
}
