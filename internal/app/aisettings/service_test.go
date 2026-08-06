package aisettings

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/inroad/inroad/internal/platform/ai"
	"github.com/inroad/inroad/internal/platform/crypto"
	"github.com/inroad/inroad/internal/platform/db/gen"
)

// fakeStore is an in-memory Store enforcing the same uniqueness the real
// schema does: one door per (workspace, kind, base_url/endpoint/project) and
// one model name per door.
type fakeStore struct {
	settings  map[uuid.UUID]gen.WorkspaceAiSetting
	providers map[uuid.UUID]gen.WorkspaceAiProvider
	models    map[uuid.UUID]gen.WorkspaceAiModel
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		settings:  make(map[uuid.UUID]gen.WorkspaceAiSetting),
		providers: make(map[uuid.UUID]gen.WorkspaceAiProvider),
		models:    make(map[uuid.UUID]gen.WorkspaceAiModel),
	}
}

func (f *fakeStore) GetSettings(_ context.Context, ws uuid.UUID) (gen.WorkspaceAiSetting, error) {
	s, ok := f.settings[ws]
	if !ok {
		return gen.WorkspaceAiSetting{}, pgx.ErrNoRows
	}
	return s, nil
}

func (f *fakeStore) UpsertSettings(_ context.Context, arg gen.UpsertAISettingsParams) (gen.WorkspaceAiSetting, error) {
	row := gen.WorkspaceAiSetting{
		WorkspaceID:            arg.WorkspaceID,
		DefaultSmartModel:      arg.DefaultSmartModel,
		DefaultFastModel:       arg.DefaultFastModel,
		EnabledModelIds:        arg.EnabledModelIds,
		AdditionalInstructions: arg.AdditionalInstructions,
	}
	f.settings[arg.WorkspaceID] = row
	return row, nil
}

// targetKey mirrors uq_workspace_ai_providers_target.
func targetKey(ws uuid.UUID, kind string, config []byte) string {
	cfg := map[string]string{}
	_ = json.Unmarshal(config, &cfg)
	return ws.String() + "|" + kind + "|" + cfg["base_url"] + "|" + cfg["endpoint"] + "|" + cfg["project_id"]
}

func (f *fakeStore) InsertProvider(_ context.Context, arg gen.InsertAIProviderParams) (gen.InsertAIProviderRow, error) {
	key := targetKey(arg.WorkspaceID, arg.Kind, arg.Config)
	for _, p := range f.providers {
		if targetKey(p.WorkspaceID, p.Kind, p.Config) == key {
			return gen.InsertAIProviderRow{}, ErrDuplicateTarget
		}
	}
	row := gen.WorkspaceAiProvider{
		ID: uuid.New(), WorkspaceID: arg.WorkspaceID, Kind: arg.Kind, Config: arg.Config,
		SecretCiphertext: arg.SecretCiphertext, KeyPrefix: arg.KeyPrefix, DisplayName: arg.DisplayName,
	}
	f.providers[row.ID] = row
	return gen.InsertAIProviderRow{
		ID: row.ID, WorkspaceID: row.WorkspaceID, Kind: row.Kind, Config: row.Config,
		KeyPrefix: row.KeyPrefix, DisplayName: row.DisplayName,
	}, nil
}

func (f *fakeStore) GetProvider(_ context.Context, ws, id uuid.UUID) (gen.WorkspaceAiProvider, error) {
	p, ok := f.providers[id]
	if !ok || p.WorkspaceID != ws {
		return gen.WorkspaceAiProvider{}, pgx.ErrNoRows
	}
	return p, nil
}

func (f *fakeStore) ListProviders(_ context.Context, ws uuid.UUID) ([]gen.ListAIProvidersRow, error) {
	var out []gen.ListAIProvidersRow
	for _, p := range f.providers {
		if p.WorkspaceID == ws {
			out = append(out, gen.ListAIProvidersRow{
				ID: p.ID, WorkspaceID: p.WorkspaceID, Kind: p.Kind, Config: p.Config,
				KeyPrefix: p.KeyPrefix, DisplayName: p.DisplayName,
			})
		}
	}
	return out, nil
}

func (f *fakeStore) UpdateProvider(_ context.Context, arg gen.UpdateAIProviderParams) (gen.UpdateAIProviderRow, error) {
	p, ok := f.providers[arg.ID]
	if !ok || p.WorkspaceID != arg.WorkspaceID {
		return gen.UpdateAIProviderRow{}, pgx.ErrNoRows
	}
	key := targetKey(arg.WorkspaceID, p.Kind, arg.Config)
	for _, other := range f.providers {
		if other.ID != p.ID && targetKey(other.WorkspaceID, other.Kind, other.Config) == key {
			return gen.UpdateAIProviderRow{}, ErrDuplicateTarget
		}
	}
	p.DisplayName, p.Config = arg.DisplayName, arg.Config
	p.SecretCiphertext, p.KeyPrefix = arg.SecretCiphertext, arg.KeyPrefix
	f.providers[p.ID] = p
	return gen.UpdateAIProviderRow{
		ID: p.ID, WorkspaceID: p.WorkspaceID, Kind: p.Kind, Config: p.Config,
		KeyPrefix: p.KeyPrefix, DisplayName: p.DisplayName,
	}, nil
}

func (f *fakeStore) DeleteProvider(_ context.Context, ws, id uuid.UUID) (int64, error) {
	p, ok := f.providers[id]
	if !ok || p.WorkspaceID != ws {
		return 0, nil
	}
	delete(f.providers, id)
	for mid, m := range f.models { // FK cascade
		if m.ProviderID == id {
			delete(f.models, mid)
		}
	}
	return 1, nil
}

func (f *fakeStore) InsertModel(_ context.Context, arg gen.InsertAIModelParams) (gen.WorkspaceAiModel, error) {
	p, ok := f.providers[arg.ProviderID]
	if !ok || p.WorkspaceID != arg.WorkspaceID {
		return gen.WorkspaceAiModel{}, ErrProviderNotInWorkspace
	}
	for _, m := range f.models {
		if m.ProviderID == arg.ProviderID && m.Name == arg.Name {
			return gen.WorkspaceAiModel{}, ErrDuplicateModel
		}
	}
	row := gen.WorkspaceAiModel{
		ID: uuid.New(), WorkspaceID: arg.WorkspaceID, ProviderID: arg.ProviderID,
		Name: arg.Name, Label: arg.Label,
		ContextWindowTokens: arg.ContextWindowTokens, MaxOutputTokens: arg.MaxOutputTokens,
		SupportsReasoning: arg.SupportsReasoning,
		InputCostPerMtok:  arg.InputCostPerMtok, OutputCostPerMtok: arg.OutputCostPerMtok,
	}
	f.models[row.ID] = row
	return row, nil
}

func (f *fakeStore) ListModels(_ context.Context, ws uuid.UUID) ([]gen.ListAIModelsRow, error) {
	var out []gen.ListAIModelsRow
	for _, m := range f.models {
		if m.WorkspaceID != ws {
			continue
		}
		out = append(out, gen.ListAIModelsRow{
			ID: m.ID, WorkspaceID: m.WorkspaceID, ProviderID: m.ProviderID,
			Name: m.Name, Label: m.Label,
			ContextWindowTokens: m.ContextWindowTokens, MaxOutputTokens: m.MaxOutputTokens,
			SupportsReasoning: m.SupportsReasoning,
			InputCostPerMtok:  m.InputCostPerMtok, OutputCostPerMtok: m.OutputCostPerMtok,
			Kind: f.providers[m.ProviderID].Kind,
		})
	}
	return out, nil
}

func (f *fakeStore) DeleteModel(_ context.Context, ws, id uuid.UUID) (int64, error) {
	m, ok := f.models[id]
	if !ok || m.WorkspaceID != ws {
		return 0, nil
	}
	delete(f.models, id)
	return 1, nil
}

// fakeCatalog serves canned models.dev entries per provider key.
type fakeCatalog struct {
	byKey map[string][]ai.NativeModel
	err   error
}

func (f *fakeCatalog) NativeModels(_ context.Context, key string) ([]ai.NativeModel, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.byKey[key], nil
}

// fakeDiscoverer records the request it was handed and returns a canned
// result.
type fakeDiscoverer struct {
	got    ai.DiscoverRequest
	result ai.DiscoveryResult
	err    error
}

func (f *fakeDiscoverer) Discover(_ context.Context, req ai.DiscoverRequest) (ai.DiscoveryResult, error) {
	f.got = req
	return f.result, f.err
}

// testKeyring builds a real Keyring over an in-memory DEK store so the sealed
// ciphertext path is exercised for real (no crypto mocking).
type memDEKStore struct{ rows map[uuid.UUID][]byte }

func (m *memDEKStore) GetWrappedDEK(_ context.Context, ws uuid.UUID) ([]byte, string, error) {
	w, ok := m.rows[ws]
	if !ok {
		return nil, "", fmt.Errorf("no row: %w", crypto.ErrDEKNotFound)
	}
	return w, "local", nil
}

func (m *memDEKStore) PutWrappedDEK(_ context.Context, ws uuid.UUID, wrapped []byte, _ string) error {
	if _, ok := m.rows[ws]; ok {
		return errors.New("dek exists")
	}
	m.rows[ws] = wrapped
	return nil
}

func testKeyring(t *testing.T) *crypto.Keyring {
	t.Helper()
	provider, err := crypto.NewLocalKeyProvider(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatalf("NewLocalKeyProvider: %v", err)
	}
	return crypto.NewKeyring(provider, &memDEKStore{rows: make(map[uuid.UUID][]byte)}, nil)
}

func publicHost(context.Context, string) (bool, error)  { return false, nil }
func privateHost(context.Context, string) (bool, error) { return true, nil }
func hostileHost(context.Context, string) (bool, error) {
	return false, errors.New("host not permitted")
}

// nativeSonnetAndGPT is the canned models.dev slice most tests use.
func nativeSonnetAndGPT() *fakeCatalog {
	return &fakeCatalog{byKey: map[string][]ai.NativeModel{
		"anthropic": {{Name: "claude-sonnet-5", Label: "Claude Sonnet 5", ContextWindowTokens: 1000000, MaxOutputTokens: 128000, SupportsReasoning: true, InputCostPerMTok: 3, OutputCostPerMTok: 15}},
		"openai":    {{Name: "gpt-5.2", Label: "GPT-5.2", ContextWindowTokens: 400000, MaxOutputTokens: 128000, SupportsReasoning: true, InputCostPerMTok: 1.25, OutputCostPerMTok: 10}},
	}}
}

type svcOpts struct {
	catalog    NativeCatalog
	discoverer Discoverer
	classify   HostClassifier
	allowPriv  bool
}

func newTestService(t *testing.T, store Store, o svcOpts) *Service {
	t.Helper()
	if o.catalog == nil {
		o.catalog = nativeSonnetAndGPT()
	}
	if o.discoverer == nil {
		o.discoverer = &fakeDiscoverer{}
	}
	if o.classify == nil {
		o.classify = publicHost
	}
	return NewService(ServiceDeps{
		Store: store, Keyring: testKeyring(t), Catalog: o.catalog, Discoverer: o.discoverer,
		ClassifyHost: o.classify, AllowPrivateBaseURL: o.allowPriv,
	})
}

const testKey = "sk-test-1234567890abcdef"

func mustCreateProvider(t *testing.T, svc *Service, ws uuid.UUID, in ProviderCreateInput) ProviderDTO {
	t.Helper()
	dto, err := svc.CreateProvider(context.Background(), ws, in)
	if err != nil {
		t.Fatalf("CreateProvider(%s): %v", in.Kind, err)
	}
	return dto
}

// ---- settings ---------------------------------------------------------------

// TestGetSettingsDefaultsWhenNoRow proves a workspace with no settings row
// reads the sentinel defaults, not an error.
func TestGetSettingsDefaultsWhenNoRow(t *testing.T) {
	svc := newTestService(t, newFakeStore(), svcOpts{})
	got, err := svc.GetSettings(context.Background(), uuid.New())
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	if got.DefaultSmartModel != ai.SentinelSmartModel || got.DefaultFastModel != ai.SentinelFastModel ||
		len(got.EnabledModelIDs) != 0 || got.AdditionalInstructions != "" {
		t.Fatalf("defaults wrong: %+v", got)
	}
}

// TestUpdateSettingsValidatesAgainstAvailableSet proves model references must
// be ids from the workspace's merged model set (or the field's own sentinel).
func TestUpdateSettingsValidatesAgainstAvailableSet(t *testing.T) {
	store := newFakeStore()
	svc := newTestService(t, store, svcOpts{})
	ws := uuid.New()
	p := mustCreateProvider(t, svc, ws, ProviderCreateInput{Kind: ai.KindAnthropic, Credentials: ai.Credentials{APIKey: testKey}})
	sonnetID := p.ID + "/claude-sonnet-5"

	// A valid available id passes and persists.
	got, err := svc.UpdateSettings(context.Background(), ws, SettingsUpdate{DefaultSmartModel: &sonnetID})
	if err != nil || got.DefaultSmartModel != sonnetID {
		t.Fatalf("valid id must pass: %+v, %v", got, err)
	}

	// An id from a door the workspace doesn't have is rejected.
	foreign := uuid.NewString() + "/claude-sonnet-5"
	if _, err := svc.UpdateSettings(context.Background(), ws, SettingsUpdate{DefaultSmartModel: &foreign}); !errors.Is(err, ErrValidation) {
		t.Fatalf("unavailable id must be ErrValidation, got %v", err)
	}
	// The OTHER sentinel is not a valid value for this field.
	wrongSentinel := ai.SentinelFastModel
	if _, err := svc.UpdateSettings(context.Background(), ws, SettingsUpdate{DefaultSmartModel: &wrongSentinel}); !errors.Is(err, ErrValidation) {
		t.Fatalf("cross-sentinel must be ErrValidation, got %v", err)
	}
	badList := []string{sonnetID, "nope"}
	if _, err := svc.UpdateSettings(context.Background(), ws, SettingsUpdate{EnabledModelIDs: &badList}); !errors.Is(err, ErrValidation) {
		t.Fatalf("unavailable enabled id must be ErrValidation, got %v", err)
	}
}

func TestUpdateSettingsRejectsDisabledDefaultsAndDuplicates(t *testing.T) {
	store := newFakeStore()
	svc := newTestService(t, store, svcOpts{})
	ws := uuid.New()
	provider := mustCreateProvider(t, svc, ws, ProviderCreateInput{
		Kind: ai.KindAnthropic, Credentials: ai.Credentials{APIKey: testKey},
	})
	modelID := provider.ID + "/claude-sonnet-5"
	duplicate := []string{modelID, modelID}
	if _, err := svc.UpdateSettings(context.Background(), ws, SettingsUpdate{EnabledModelIDs: &duplicate}); !errors.Is(err, ErrValidation) {
		t.Fatalf("duplicate enabled ids must be rejected, got %v", err)
	}

	empty := []string{}
	if _, err := svc.UpdateSettings(context.Background(), ws, SettingsUpdate{
		DefaultSmartModel: &modelID, EnabledModelIDs: &empty,
	}); err != nil {
		t.Fatalf("empty enabled list means all models and must accept an explicit default: %v", err)
	}

	other := mustCreateProvider(t, svc, ws, ProviderCreateInput{
		Kind: ai.KindOpenAI, Credentials: ai.Credentials{APIKey: testKey},
	})
	onlyGPT := []string{other.ID + "/gpt-5.2"}
	if _, err := svc.UpdateSettings(context.Background(), ws, SettingsUpdate{EnabledModelIDs: &onlyGPT}); !errors.Is(err, ErrValidation) {
		t.Fatalf("an explicit default excluded by enabled_model_ids must be rejected, got %v", err)
	}
}

// TestUpdateSettingsMergesOmittedFields proves an omitted field keeps its
// current value (pointer-merge semantics).
func TestUpdateSettingsMergesOmittedFields(t *testing.T) {
	store := newFakeStore()
	svc := newTestService(t, store, svcOpts{})
	ws := uuid.New()
	p := mustCreateProvider(t, svc, ws, ProviderCreateInput{Kind: ai.KindAnthropic, Credentials: ai.Credentials{APIKey: testKey}})
	sonnetID := p.ID + "/claude-sonnet-5"

	instructions := "always be concise"
	if _, err := svc.UpdateSettings(context.Background(), ws, SettingsUpdate{
		DefaultSmartModel: &sonnetID, AdditionalInstructions: &instructions,
	}); err != nil {
		t.Fatalf("first update: %v", err)
	}
	ids := []string{sonnetID}
	got, err := svc.UpdateSettings(context.Background(), ws, SettingsUpdate{EnabledModelIDs: &ids})
	if err != nil {
		t.Fatalf("second update: %v", err)
	}
	if got.DefaultSmartModel != sonnetID || got.AdditionalInstructions != instructions {
		t.Fatalf("omitted fields must keep current values: %+v", got)
	}
	if got.DefaultFastModel != ai.SentinelFastModel {
		t.Fatalf("untouched fast default must stay sentinel: %+v", got)
	}
}

// ---- providers ----------------------------------------------------------------

// TestCreateProviderSealsWholeCredentialObject proves the persisted secret is
// the sealed JSON credential OBJECT (round-trips through the workspace
// sealer), the display prefix is a strict prefix, and the DTO carries no
// secret material.
func TestCreateProviderSealsWholeCredentialObject(t *testing.T) {
	store := newFakeStore()
	svc := newTestService(t, store, svcOpts{})
	ws := uuid.New()

	dto := mustCreateProvider(t, svc, ws, ProviderCreateInput{
		Kind: ai.KindAnthropic, DisplayName: "prod",
		Credentials: ai.Credentials{APIKey: testKey},
	})
	var persisted gen.WorkspaceAiProvider
	for _, p := range store.providers {
		persisted = p
	}
	if persisted.SecretCiphertext == "" || strings.Contains(persisted.SecretCiphertext, testKey) {
		t.Fatalf("secret must be sealed, got %q", persisted.SecretCiphertext)
	}
	sealer, err := svc.keyring.SealerFor(context.Background(), ws)
	if err != nil {
		t.Fatalf("SealerFor: %v", err)
	}
	blob, err := sealer.Open(persisted.SecretCiphertext)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	var creds ai.Credentials
	if err := json.Unmarshal(blob, &creds); err != nil || creds.APIKey != testKey {
		t.Fatalf("blob must decode to the credential object: %q, %v", blob, err)
	}
	if persisted.KeyPrefix != testKey[:8] || dto.KeyPrefix != testKey[:8] || !dto.Configured {
		t.Fatalf("prefix wrong: %q / %+v", persisted.KeyPrefix, dto)
	}
}

// TestCreateProviderKindValidation walks the kind matrix: required
// credentials/config per kind, foreign fields rejected, unknown kinds 400.
func TestCreateProviderKindValidation(t *testing.T) {
	svc := newTestService(t, newFakeStore(), svcOpts{})
	ws := uuid.New()
	ctx := context.Background()
	saJSON := `{"type":"service_account","client_email":"agent@proj.iam.gserviceaccount.com","private_key":"pk"}`

	cases := []struct {
		name    string
		in      ProviderCreateInput
		wantErr error
	}{
		{"unknown kind", ProviderCreateInput{Kind: "mistral", Credentials: ai.Credentials{APIKey: testKey}}, ErrValidation},
		{"anthropic missing key", ProviderCreateInput{Kind: ai.KindAnthropic}, ErrValidation},
		{"anthropic short key", ProviderCreateInput{Kind: ai.KindAnthropic, Credentials: ai.Credentials{APIKey: "short"}}, ErrValidation},
		{"anthropic rejects config", ProviderCreateInput{Kind: ai.KindAnthropic, Credentials: ai.Credentials{APIKey: testKey}, Config: map[string]string{"base_url": "https://x.example"}}, ErrValidation},
		{"anthropic rejects foreign creds", ProviderCreateInput{Kind: ai.KindAnthropic, Credentials: ai.Credentials{APIKey: testKey, ServiceAccountJSON: saJSON}}, ErrValidation},
		{"compatible requires base_url", ProviderCreateInput{Kind: ai.KindOpenAICompatible, Credentials: ai.Credentials{APIKey: testKey}}, ErrValidation},
		{"azure requires endpoint+version", ProviderCreateInput{Kind: ai.KindAzureOpenAI, Credentials: ai.Credentials{APIKey: testKey}, Config: map[string]string{"endpoint": "https://r.openai.azure.com"}}, ErrValidation},
		{"bedrock requires both keys", ProviderCreateInput{Kind: ai.KindBedrock, Credentials: ai.Credentials{AccessKeyID: "AKIAEXAMPLE12345"}, Config: map[string]string{"region": "us-east-1"}}, ErrValidation},
		{"bedrock requires region", ProviderCreateInput{Kind: ai.KindBedrock, Credentials: ai.Credentials{AccessKeyID: "AKIAEXAMPLE12345", SecretAccessKey: "secret"}}, ErrValidation},
		{"vertex requires sa json", ProviderCreateInput{Kind: ai.KindVertexAnthropic, Config: map[string]string{"project_id": "p", "region": "us-east5"}}, ErrValidation},
		{"vertex sa must have client_email", ProviderCreateInput{Kind: ai.KindVertexGoogle, Credentials: ai.Credentials{ServiceAccountJSON: `{"type":"service_account"}`}, Config: map[string]string{"project_id": "p", "region": "r"}}, ErrValidation},
	}
	for _, tc := range cases {
		if _, err := svc.CreateProvider(ctx, ws, tc.in); !errors.Is(err, tc.wantErr) {
			t.Errorf("%s: want %v, got %v", tc.name, tc.wantErr, err)
		}
	}

	// The happy path for each multi-cloud kind.
	mustCreateProvider(t, svc, ws, ProviderCreateInput{Kind: ai.KindBedrock,
		Credentials: ai.Credentials{AccessKeyID: "AKIAEXAMPLE12345", SecretAccessKey: "aws-secret"},
		Config:      map[string]string{"region": "us-east-1"}})
	vertex := mustCreateProvider(t, svc, ws, ProviderCreateInput{Kind: ai.KindVertexAnthropic,
		Credentials: ai.Credentials{ServiceAccountJSON: saJSON},
		Config:      map[string]string{"project_id": "proj", "region": "us-east5"}})
	if vertex.KeyPrefix != "agent@proj.iam.gserviceaccount.com" {
		t.Fatalf("vertex prefix must be the client_email: %q", vertex.KeyPrefix)
	}
	azure := mustCreateProvider(t, svc, ws, ProviderCreateInput{Kind: ai.KindAzureOpenAI,
		Credentials: ai.Credentials{APIKey: "azure-key-12345678"},
		Config:      map[string]string{"endpoint": "https://r.openai.azure.com", "api_version": "2024-10-21"}})
	if azure.Config["endpoint"] != "https://r.openai.azure.com" {
		t.Fatalf("config must round-trip: %+v", azure.Config)
	}
	// Keyless gateway (local Ollama posture) is legitimate.
	svcPriv := newTestService(t, newFakeStore(), svcOpts{classify: privateHost, allowPriv: true})
	ollama := mustCreateProvider(t, svcPriv, ws, ProviderCreateInput{Kind: ai.KindOpenAICompatible,
		Config: map[string]string{"base_url": "http://localhost:11434/v1"}})
	if ollama.KeyPrefix != "" {
		t.Fatalf("keyless door must have empty prefix: %+v", ollama)
	}
}

// TestCreateProviderURLPolicy covers the SSRF/scheme matrix for user-supplied
// base_url/endpoint hosts.
func TestCreateProviderURLPolicy(t *testing.T) {
	ws := uuid.New()
	ctx := context.Background()
	gateway := func(url string) ProviderCreateInput {
		return ProviderCreateInput{Kind: ai.KindOpenAICompatible,
			Credentials: ai.Credentials{APIKey: testKey}, Config: map[string]string{"base_url": url}}
	}

	svc := newTestService(t, newFakeStore(), svcOpts{classify: publicHost})
	if _, err := svc.CreateProvider(ctx, ws, gateway("https://llm.example.com/v1")); err != nil {
		t.Fatalf("public https must pass: %v", err)
	}
	if _, err := svc.CreateProvider(ctx, ws, gateway("http://llm2.example.com/v1")); !errors.Is(err, ErrValidation) {
		t.Fatalf("public http must be ErrValidation, got %v", err)
	}

	svc = newTestService(t, newFakeStore(), svcOpts{classify: privateHost})
	if _, err := svc.CreateProvider(ctx, ws, gateway("http://localhost:11434/v1")); !errors.Is(err, ErrValidation) {
		t.Fatalf("private host without flag must be ErrValidation, got %v", err)
	}
	svc = newTestService(t, newFakeStore(), svcOpts{classify: privateHost, allowPriv: true})
	if _, err := svc.CreateProvider(ctx, ws, gateway("http://localhost:11434/v1")); err != nil {
		t.Fatalf("private host with flag must pass: %v", err)
	}

	svc = newTestService(t, newFakeStore(), svcOpts{classify: hostileHost, allowPriv: true})
	if _, err := svc.CreateProvider(ctx, ws, gateway("https://169.254.169.254/v1")); !errors.Is(err, ErrValidation) {
		t.Fatalf("hostile host must be ErrValidation even with flag, got %v", err)
	}

	svc = newTestService(t, newFakeStore(), svcOpts{allowPriv: true})
	for _, bad := range []string{"not-a-url", "ftp://x.example", "https://user:pw@x.example", "https://"} {
		if _, err := svc.CreateProvider(ctx, ws, gateway(bad)); !errors.Is(err, ErrValidation) {
			t.Fatalf("base_url %q must be ErrValidation, got %v", bad, err)
		}
	}
}

// TestCreateProviderDuplicateTargetIs409 proves the one-door-per-target rule:
// same (kind, base_url) conflicts, a different base_url is a second door.
func TestCreateProviderDuplicateTargetIs409(t *testing.T) {
	svc := newTestService(t, newFakeStore(), svcOpts{})
	ws := uuid.New()
	ctx := context.Background()
	gw := func(url string) ProviderCreateInput {
		return ProviderCreateInput{Kind: ai.KindOpenAICompatible,
			Credentials: ai.Credentials{APIKey: testKey}, Config: map[string]string{"base_url": url}}
	}
	mustCreateProvider(t, svc, ws, gw("https://openrouter.ai/api/v1"))
	if _, err := svc.CreateProvider(ctx, ws, gw("https://openrouter.ai/api/v1")); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("same target must be ErrDuplicate, got %v", err)
	}
	mustCreateProvider(t, svc, ws, gw("https://gateway.two.example/v1"))
	if _, err := svc.CreateProvider(ctx, ws, ProviderCreateInput{Kind: ai.KindAnthropic, Credentials: ai.Credentials{APIKey: testKey}}); err != nil {
		t.Fatalf("different kind must not conflict: %v", err)
	}
}

// TestUpdateProviderKeepsCredentialsWhenAbsent proves the update contract:
// credentials nil keeps the sealed blob; supplying them replaces blob and
// prefix; config/display update independently.
func TestUpdateProviderKeepsCredentialsWhenAbsent(t *testing.T) {
	store := newFakeStore()
	svc := newTestService(t, store, svcOpts{})
	ws := uuid.New()
	created := mustCreateProvider(t, svc, ws, ProviderCreateInput{
		Kind: ai.KindOpenAICompatible, DisplayName: "router",
		Credentials: ai.Credentials{APIKey: testKey},
		Config:      map[string]string{"base_url": "https://openrouter.ai/api/v1"},
	})
	id := uuid.MustParse(created.ID)
	var before gen.WorkspaceAiProvider
	for _, p := range store.providers {
		before = p
	}

	name := "renamed"
	updated, err := svc.UpdateProvider(context.Background(), ws, id, ProviderUpdateInput{DisplayName: &name})
	if err != nil {
		t.Fatalf("UpdateProvider: %v", err)
	}
	if updated.DisplayName != "renamed" || updated.KeyPrefix != testKey[:8] {
		t.Fatalf("update wrong: %+v", updated)
	}
	var after gen.WorkspaceAiProvider
	for _, p := range store.providers {
		after = p
	}
	if after.SecretCiphertext != before.SecretCiphertext {
		t.Fatalf("absent credentials must keep the sealed blob")
	}
	if after.Config == nil || !strings.Contains(string(after.Config), "openrouter.ai") {
		t.Fatalf("absent config must keep current config: %s", after.Config)
	}

	// Supplying credentials replaces blob + prefix.
	newCreds := ai.Credentials{APIKey: "sk-new-key-9876543210"}
	updated, err = svc.UpdateProvider(context.Background(), ws, id, ProviderUpdateInput{Credentials: &newCreds})
	if err != nil {
		t.Fatalf("credential update: %v", err)
	}
	if updated.KeyPrefix != "sk-new-k" {
		t.Fatalf("prefix must reflect the new key: %+v", updated)
	}
	for _, p := range store.providers {
		if p.SecretCiphertext == before.SecretCiphertext {
			t.Fatalf("blob must be replaced")
		}
	}

	// Unknown / foreign ids 404.
	if _, err := svc.UpdateProvider(context.Background(), uuid.New(), id, ProviderUpdateInput{}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign workspace must be ErrNotFound, got %v", err)
	}
}

func TestUpdateProviderDuplicateTargetDoesNotPartiallyReplaceCredentials(t *testing.T) {
	store := newFakeStore()
	svc := newTestService(t, store, svcOpts{})
	ws := uuid.New()
	first := mustCreateProvider(t, svc, ws, ProviderCreateInput{
		Kind: ai.KindOpenAICompatible, Credentials: ai.Credentials{APIKey: testKey},
		Config: map[string]string{"base_url": "https://first.example/v1"},
	})
	mustCreateProvider(t, svc, ws, ProviderCreateInput{
		Kind: ai.KindOpenAICompatible, Credentials: ai.Credentials{APIKey: testKey},
		Config: map[string]string{"base_url": "https://second.example/v1"},
	})
	id := uuid.MustParse(first.ID)
	before := store.providers[id]
	newCredentials := ai.Credentials{APIKey: "sk-replacement-123456789"}
	_, err := svc.UpdateProvider(context.Background(), ws, id, ProviderUpdateInput{
		Credentials: &newCredentials,
		Config:      map[string]string{"base_url": "https://second.example/v1"},
	})
	if !errors.Is(err, ErrDuplicate) {
		t.Fatalf("duplicate target must be ErrDuplicate, got %v", err)
	}
	after := store.providers[id]
	if after.SecretCiphertext != before.SecretCiphertext || after.KeyPrefix != before.KeyPrefix {
		t.Fatal("failed config update must not partially replace credentials")
	}
}

// TestDeleteProvider proves id-addressed delete: 404 on unknown/foreign, and
// custom models cascade with the door.
func TestDeleteProvider(t *testing.T) {
	store := newFakeStore()
	svc := newTestService(t, store, svcOpts{})
	ws := uuid.New()
	ctx := context.Background()
	created := mustCreateProvider(t, svc, ws, ProviderCreateInput{
		Kind: ai.KindOpenAICompatible, Credentials: ai.Credentials{APIKey: testKey},
		Config: map[string]string{"base_url": "https://gw.example/v1"},
	})
	id := uuid.MustParse(created.ID)
	if _, err := svc.CreateModel(ctx, ws, ModelCreateInput{
		ProviderID: id, Name: "llama-3.3-70b", Label: "Llama 3.3 70B",
		ContextWindowTokens: 131072, MaxOutputTokens: 8192,
	}); err != nil {
		t.Fatalf("CreateModel: %v", err)
	}

	if err := svc.DeleteProvider(ctx, ws, uuid.New()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown id must be ErrNotFound, got %v", err)
	}
	if err := svc.DeleteProvider(ctx, ws, id); err != nil {
		t.Fatalf("DeleteProvider: %v", err)
	}
	if len(store.models) != 0 {
		t.Fatalf("models must cascade with the door: %+v", store.models)
	}
}

// ---- discovery -----------------------------------------------------------------

// TestDiscoverUnsealsAndForwards proves discovery hands the discoverer the
// door's kind, config, and UNSEALED credentials — and maps failures to
// ErrDiscoveryFailed rather than a bare 500.
func TestDiscoverUnsealsAndForwards(t *testing.T) {
	store := newFakeStore()
	disc := &fakeDiscoverer{result: ai.DiscoveryResult{Supported: true, Models: []ai.DiscoveredModel{{Name: "m1"}}}}
	svc := newTestService(t, store, svcOpts{discoverer: disc})
	ws := uuid.New()
	created := mustCreateProvider(t, svc, ws, ProviderCreateInput{
		Kind: ai.KindOpenAICompatible, Credentials: ai.Credentials{APIKey: testKey},
		Config: map[string]string{"base_url": "https://gw.example/v1"},
	})
	id := uuid.MustParse(created.ID)

	dto, err := svc.Discover(context.Background(), ws, id)
	if err != nil || !dto.Supported || len(dto.Models) != 1 {
		t.Fatalf("Discover: %+v, %v", dto, err)
	}
	if disc.got.Kind != ai.KindOpenAICompatible || disc.got.Config["base_url"] != "https://gw.example/v1" {
		t.Fatalf("request wrong: %+v", disc.got)
	}
	if disc.got.Credentials.APIKey != testKey {
		t.Fatalf("credentials must be unsealed for the dial: %+v", disc.got.Credentials)
	}

	if _, err := svc.Discover(context.Background(), ws, uuid.New()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown door must be ErrNotFound, got %v", err)
	}
	disc.err = errors.New("dial refused")
	if _, err := svc.Discover(context.Background(), ws, id); !errors.Is(err, ErrDiscoveryFailed) {
		t.Fatalf("upstream failure must be ErrDiscoveryFailed, got %v", err)
	}
}

// ---- models ---------------------------------------------------------------------

// TestCreateModelValidationAndCollisions proves shape validation, the
// 404-on-foreign-door path, custom-vs-custom 409, and custom-vs-catalog 409
// (two entries must never answer to one composite id).
func TestCreateModelValidationAndCollisions(t *testing.T) {
	store := newFakeStore()
	svc := newTestService(t, store, svcOpts{})
	ws := uuid.New()
	ctx := context.Background()
	anthropic := mustCreateProvider(t, svc, ws, ProviderCreateInput{Kind: ai.KindAnthropic, Credentials: ai.Credentials{APIKey: testKey}})
	anthropicID := uuid.MustParse(anthropic.ID)

	valid := ModelCreateInput{ProviderID: anthropicID, Name: "m", Label: "M", ContextWindowTokens: 1000, MaxOutputTokens: 100}
	for name, mutate := range map[string]func(*ModelCreateInput){
		"missing name":  func(m *ModelCreateInput) { m.Name = "" },
		"missing label": func(m *ModelCreateInput) { m.Label = "" },
		"zero context":  func(m *ModelCreateInput) { m.ContextWindowTokens = 0 },
		"zero output":   func(m *ModelCreateInput) { m.MaxOutputTokens = 0 },
	} {
		in := valid
		mutate(&in)
		if _, err := svc.CreateModel(ctx, ws, in); !errors.Is(err, ErrValidation) {
			t.Errorf("%s: want ErrValidation, got %v", name, err)
		}
	}

	// A foreign door is 404.
	if _, err := svc.CreateModel(ctx, ws, ModelCreateInput{ProviderID: uuid.New(), Name: "m", Label: "M", ContextWindowTokens: 1, MaxOutputTokens: 1}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign provider must be ErrNotFound, got %v", err)
	}
	// Colliding with a NATIVE catalog entry of the same door is 409.
	if _, err := svc.CreateModel(ctx, ws, ModelCreateInput{ProviderID: anthropicID, Name: "claude-sonnet-5", Label: "dupe", ContextWindowTokens: 1, MaxOutputTokens: 1}); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("catalog collision must be ErrDuplicate, got %v", err)
	}
	// Custom-vs-custom duplicate is 409.
	dto, err := svc.CreateModel(ctx, ws, valid)
	if err != nil {
		t.Fatalf("CreateModel: %v", err)
	}
	if dto.ID != anthropic.ID+"/m" || dto.Source != ai.SourceCustom || dto.CustomModelID == nil {
		t.Fatalf("created model wrong: %+v", dto)
	}
	if _, err := svc.CreateModel(ctx, ws, valid); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("custom duplicate must be ErrDuplicate, got %v", err)
	}

	// DeleteModel by row id; unknown → 404.
	if err := svc.DeleteModel(ctx, ws, uuid.MustParse(*dto.CustomModelID)); err != nil {
		t.Fatalf("DeleteModel: %v", err)
	}
	if err := svc.DeleteModel(ctx, ws, uuid.New()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown model must be ErrNotFound, got %v", err)
	}
}

// TestListModelsMergesCatalogAndCustom proves the merged list: native entries
// for native-kind doors, custom entries for any door, stable composite ids,
// enabled semantics (empty list = all enabled), and graceful degradation when
// models.dev is unavailable.
func TestListModelsMergesCatalogAndCustom(t *testing.T) {
	store := newFakeStore()
	svc := newTestService(t, store, svcOpts{})
	ws := uuid.New()
	ctx := context.Background()

	anthropic := mustCreateProvider(t, svc, ws, ProviderCreateInput{Kind: ai.KindAnthropic, Credentials: ai.Credentials{APIKey: testKey}})
	gateway := mustCreateProvider(t, svc, ws, ProviderCreateInput{
		Kind: ai.KindOpenAICompatible, Credentials: ai.Credentials{APIKey: testKey},
		Config: map[string]string{"base_url": "https://gw.example/v1"},
	})
	gatewayID := uuid.MustParse(gateway.ID)
	if _, err := svc.CreateModel(ctx, ws, ModelCreateInput{
		ProviderID: gatewayID, Name: "llama-3.3-70b", Label: "Llama 3.3 70B",
		ContextWindowTokens: 131072, MaxOutputTokens: 8192,
	}); err != nil {
		t.Fatalf("CreateModel: %v", err)
	}

	models, err := svc.ListModels(ctx, ws)
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	byID := map[string]ModelDTO{}
	for _, m := range models {
		byID[m.ID] = m
	}
	native := byID[anthropic.ID+"/claude-sonnet-5"]
	if native.Source != ai.SourceCatalog || !native.Enabled || native.Kind != ai.KindAnthropic || native.CustomModelID != nil {
		t.Fatalf("native entry wrong: %+v", native)
	}
	if native.InputCostPerMTok == nil || *native.InputCostPerMTok != 3 {
		t.Fatalf("native cost wrong: %+v", native)
	}
	custom := byID[gateway.ID+"/llama-3.3-70b"]
	if custom.Source != ai.SourceCustom || !custom.Enabled || custom.CustomModelID == nil {
		t.Fatalf("custom entry wrong: %+v", custom)
	}

	// A non-empty enabled list narrows.
	ids := []string{custom.ID}
	if _, err := svc.UpdateSettings(ctx, ws, SettingsUpdate{EnabledModelIDs: &ids}); err != nil {
		t.Fatalf("UpdateSettings: %v", err)
	}
	models, _ = svc.ListModels(ctx, ws)
	for _, m := range models {
		if m.Enabled != (m.ID == custom.ID) {
			t.Fatalf("enabled narrowing wrong for %s: %+v", m.ID, m)
		}
	}

	// models.dev unavailable → native lists degrade to empty, customs remain.
	svcDown := NewService(ServiceDeps{
		Store: store, Keyring: testKeyring(t),
		Catalog:    &fakeCatalog{err: fmt.Errorf("wrap: %w", ai.ErrCatalogUnavailable)},
		Discoverer: &fakeDiscoverer{}, ClassifyHost: publicHost,
	})
	models, err = svcDown.ListModels(ctx, ws)
	if err != nil {
		t.Fatalf("ListModels with catalog down: %v", err)
	}
	if len(models) != 1 || models[0].Source != ai.SourceCustom {
		t.Fatalf("catalog-down must serve customs only: %+v", models)
	}

	// A REAL catalog/cache backend error surfaces, never silently empty.
	svcBroken := NewService(ServiceDeps{
		Store: store, Keyring: testKeyring(t),
		Catalog:    &fakeCatalog{err: errors.New("db down")},
		Discoverer: &fakeDiscoverer{}, ClassifyHost: publicHost,
	})
	if _, err := svcBroken.ListModels(ctx, ws); err == nil {
		t.Fatalf("real backend error must surface")
	}
}
