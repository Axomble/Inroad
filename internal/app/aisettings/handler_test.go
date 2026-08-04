package aisettings

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/app/auth"
	"github.com/inroad/inroad/internal/platform/ai"
)

const testSecret = "0123456789abcdef0123456789abcdef"

// authedRouter mounts the AI surface exactly as cmd/inroad does — Routes()
// under /ai behind auth.RequireAuth — so tests exercise real routing, JWT
// claim extraction, and the per-route role gates.
func authedRouter(h *Handler) http.Handler {
	r := chi.NewRouter()
	r.Use(auth.RequireAuth(auth.NewJWTVerifier([]byte(testSecret))))
	r.Mount("/ai", h.Routes())
	return r
}

func bearer(t *testing.T, ws uuid.UUID, role string) string {
	t.Helper()
	tok, err := auth.IssueToken([]byte(testSecret), auth.Claims{
		UserID: uuid.New().String(), WorkspaceID: ws.String(), Role: role, SessionID: uuid.New().String(),
	}, time.Hour)
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}
	return "Bearer " + tok
}

func do(t *testing.T, h http.Handler, method, target, authz, body string) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, target, http.NoBody)
	} else {
		r = httptest.NewRequest(method, target, strings.NewReader(body))
	}
	if authz != "" {
		r.Header.Set("Authorization", authz)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func newTestHandler(t *testing.T, o svcOpts) (*Handler, *fakeStore) {
	t.Helper()
	store := newFakeStore()
	return NewHandler(newTestService(t, store, o)), store
}

const anthropicBody = `{"kind":"anthropic","display_name":"prod key","credentials":{"api_key":"` + testKey + `"}}`

// createAnthropic POSTs an anthropic door and returns its DTO.
func createAnthropic(t *testing.T, router http.Handler, authz string) ProviderDTO {
	t.Helper()
	w := do(t, router, http.MethodPost, "/ai/providers", authz, anthropicBody)
	if w.Code != http.StatusCreated {
		t.Fatalf("POST provider: want 201, got %d: %s", w.Code, w.Body.String())
	}
	var dto ProviderDTO
	if err := json.Unmarshal(w.Body.Bytes(), &dto); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return dto
}

// TestUnauthenticatedIs401 proves every route fails closed without a token.
func TestUnauthenticatedIs401(t *testing.T) {
	h, _ := newTestHandler(t, svcOpts{})
	router := authedRouter(h)
	id := uuid.NewString()
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/ai/settings"},
		{http.MethodPut, "/ai/settings"},
		{http.MethodGet, "/ai/providers"},
		{http.MethodPost, "/ai/providers"},
		{http.MethodPut, "/ai/providers/" + id},
		{http.MethodDelete, "/ai/providers/" + id},
		{http.MethodPost, "/ai/providers/" + id + "/discover"},
		{http.MethodGet, "/ai/models"},
		{http.MethodPost, "/ai/models"},
		{http.MethodDelete, "/ai/models/" + id},
	} {
		if w := do(t, router, tc.method, tc.path, "", ""); w.Code != http.StatusUnauthorized {
			t.Errorf("%s %s without token: want 401, got %d", tc.method, tc.path, w.Code)
		}
	}
}

func TestMutationBodiesRejectUnknownFieldsAndTrailingJSON(t *testing.T) {
	h, _ := newTestHandler(t, svcOpts{})
	router := authedRouter(h)
	authz := bearer(t, uuid.New(), "owner")
	for name, body := range map[string]string{
		"unknown field": `{"kind":"anthropic","credentials":{"api_key":"` + testKey + `"},"typo":true}`,
		"trailing json": anthropicBody + ` {}`,
	} {
		t.Run(name, func(t *testing.T) {
			w := do(t, router, http.MethodPost, "/ai/providers", authz, body)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("want 400, got %d: %s", w.Code, w.Body.String())
			}
		})
	}
}

// TestWritesRequireAdminRole proves a member can read but every write —
// including discovery, which unseals a credential — is 403.
func TestWritesRequireAdminRole(t *testing.T) {
	h, _ := newTestHandler(t, svcOpts{})
	router := authedRouter(h)
	ws := uuid.New()
	member := bearer(t, ws, "member")
	id := uuid.NewString()

	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/ai/settings"},
		{http.MethodGet, "/ai/providers"},
		{http.MethodGet, "/ai/models"},
	} {
		if w := do(t, router, tc.method, tc.path, member, ""); w.Code != http.StatusOK {
			t.Errorf("member %s %s: want 200, got %d: %s", tc.method, tc.path, w.Code, w.Body.String())
		}
	}
	for _, tc := range []struct{ method, path, body string }{
		{http.MethodPut, "/ai/settings", `{}`},
		{http.MethodPost, "/ai/providers", anthropicBody},
		{http.MethodPut, "/ai/providers/" + id, `{}`},
		{http.MethodDelete, "/ai/providers/" + id, ""},
		{http.MethodPost, "/ai/providers/" + id + "/discover", ""},
		{http.MethodPost, "/ai/models", `{}`},
		{http.MethodDelete, "/ai/models/" + id, ""},
	} {
		if w := do(t, router, tc.method, tc.path, member, tc.body); w.Code != http.StatusForbidden {
			t.Errorf("member %s %s: want 403, got %d", tc.method, tc.path, w.Code)
		}
	}
}

// TestProviderLifecycleNeverEchoesSecrets proves the raw credential material
// appears NOWHERE in any response — create, list, update, models — only the
// 8-char prefix does; and the id-addressed lifecycle round-trips.
func TestProviderLifecycleNeverEchoesSecrets(t *testing.T) {
	h, _ := newTestHandler(t, svcOpts{})
	router := authedRouter(h)
	ws := uuid.New()
	admin := bearer(t, ws, "admin")

	created := createAnthropic(t, router, admin)
	if created.KeyPrefix != testKey[:8] || created.Kind != "anthropic" || !created.Configured {
		t.Fatalf("created DTO wrong: %+v", created)
	}

	list := do(t, router, http.MethodGet, "/ai/providers", admin, "")
	update := do(t, router, http.MethodPut, "/ai/providers/"+created.ID, admin,
		`{"display_name":"renamed","credentials":{"api_key":"sk-second-key-987654"}}`)
	models := do(t, router, http.MethodGet, "/ai/models", admin, "")
	for name, w := range map[string]*httptest.ResponseRecorder{"list": list, "update": update, "models": models} {
		if w.Code != http.StatusOK {
			t.Fatalf("%s: want 200, got %d: %s", name, w.Code, w.Body.String())
		}
		body := w.Body.String()
		for _, secret := range []string{testKey, "sk-second-key-987654"} {
			if strings.Contains(body, secret) {
				t.Fatalf("%s response leaks a raw key: %s", name, body)
			}
		}
		if strings.Contains(body, "ciphertext") || strings.Contains(body, "secret_access_key") || strings.Contains(body, "service_account_json") {
			t.Fatalf("%s response carries a secret field: %s", name, body)
		}
	}

	var updated ProviderDTO
	if err := json.Unmarshal(update.Body.Bytes(), &updated); err != nil {
		t.Fatalf("decode update: %v", err)
	}
	if updated.DisplayName != "renamed" || updated.KeyPrefix != "sk-secon" {
		t.Fatalf("update DTO wrong: %+v", updated)
	}

	if w := do(t, router, http.MethodDelete, "/ai/providers/"+created.ID, admin, ""); w.Code != http.StatusNoContent {
		t.Fatalf("DELETE provider: want 204, got %d", w.Code)
	}
	if w := do(t, router, http.MethodDelete, "/ai/providers/"+created.ID, admin, ""); w.Code != http.StatusNotFound {
		t.Fatalf("second DELETE: want 404, got %d", w.Code)
	}
}

// TestProviderStatusMapping proves the handler's status codes: 400 bad
// body/kind/id, 404 unknown id, 409 duplicate target.
func TestProviderStatusMapping(t *testing.T) {
	h, _ := newTestHandler(t, svcOpts{})
	router := authedRouter(h)
	ws := uuid.New()
	admin := bearer(t, ws, "admin")

	if w := do(t, router, http.MethodPost, "/ai/providers", admin, `not json`); w.Code != http.StatusBadRequest {
		t.Fatalf("bad json: want 400, got %d", w.Code)
	}
	if w := do(t, router, http.MethodPost, "/ai/providers", admin, `{"kind":"mistral","credentials":{"api_key":"`+testKey+`"}}`); w.Code != http.StatusBadRequest {
		t.Fatalf("unknown kind: want 400, got %d", w.Code)
	}
	if w := do(t, router, http.MethodPut, "/ai/providers/not-a-uuid", admin, `{}`); w.Code != http.StatusBadRequest {
		t.Fatalf("garbage id: want 400, got %d", w.Code)
	}
	if w := do(t, router, http.MethodPut, "/ai/providers/"+uuid.NewString(), admin, `{}`); w.Code != http.StatusNotFound {
		t.Fatalf("unknown id: want 404, got %d", w.Code)
	}
	createAnthropic(t, router, admin)
	if w := do(t, router, http.MethodPost, "/ai/providers", admin, anthropicBody); w.Code != http.StatusConflict {
		t.Fatalf("duplicate target: want 409, got %d: %s", w.Code, w.Body.String())
	}
}

// TestDiscoverEndpoint proves the discover route: 200 with candidates on
// success, 502 on an upstream failure, and workspace pinning (a foreign
// door's id is 404).
func TestDiscoverEndpoint(t *testing.T) {
	disc := &fakeDiscoverer{result: ai.DiscoveryResult{Supported: true, Models: []ai.DiscoveredModel{{Name: "or/llama"}}}}
	h, _ := newTestHandler(t, svcOpts{discoverer: disc})
	router := authedRouter(h)
	ws := uuid.New()
	admin := bearer(t, ws, "admin")
	created := createAnthropic(t, router, admin)

	w := do(t, router, http.MethodPost, "/ai/providers/"+created.ID+"/discover", admin, "")
	if w.Code != http.StatusOK {
		t.Fatalf("discover: want 200, got %d: %s", w.Code, w.Body.String())
	}
	var dto DiscoveryDTO
	if err := json.Unmarshal(w.Body.Bytes(), &dto); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !dto.Supported || len(dto.Models) != 1 || dto.Models[0].Name != "or/llama" {
		t.Fatalf("discover payload wrong: %+v", dto)
	}

	// Another workspace cannot discover through this door.
	if w := do(t, router, http.MethodPost, "/ai/providers/"+created.ID+"/discover", bearer(t, uuid.New(), "admin"), ""); w.Code != http.StatusNotFound {
		t.Fatalf("foreign workspace discover: want 404, got %d", w.Code)
	}

	disc.err = ai.ErrNoModel // any upstream error
	if w := do(t, router, http.MethodPost, "/ai/providers/"+created.ID+"/discover", admin, ""); w.Code != http.StatusBadGateway {
		t.Fatalf("upstream failure: want 502, got %d", w.Code)
	}
}

// TestModelEndpointsRoundTrip proves POST /ai/models 201 + contract fields,
// the merged GET, and DELETE by custom_model_id.
func TestModelEndpointsRoundTrip(t *testing.T) {
	h, _ := newTestHandler(t, svcOpts{})
	router := authedRouter(h)
	ws := uuid.New()
	admin := bearer(t, ws, "admin")
	created := createAnthropic(t, router, admin)

	w := do(t, router, http.MethodPost, "/ai/models", admin,
		`{"provider_id":"`+created.ID+`","name":"my-tuned-model","label":"My Tuned Model","context_window_tokens":32768,"max_output_tokens":4096,"supports_reasoning":true,"input_cost_per_mtok":1.5}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("POST model: want 201, got %d: %s", w.Code, w.Body.String())
	}
	var model ModelDTO
	if err := json.Unmarshal(w.Body.Bytes(), &model); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if model.ID != created.ID+"/my-tuned-model" || model.Source != "custom" || model.CustomModelID == nil ||
		*model.InputCostPerMTok != 1.5 || model.OutputCostPerMTok != nil {
		t.Fatalf("model DTO wrong: %+v", model)
	}

	list := do(t, router, http.MethodGet, "/ai/models", admin, "")
	var resp map[string][]map[string]any
	if err := json.Unmarshal(list.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	models := resp["models"]
	if len(models) == 0 {
		t.Fatalf("models must not be empty")
	}
	for _, k := range []string{"id", "provider_id", "kind", "name", "label", "context_window_tokens",
		"max_output_tokens", "supports_reasoning", "source", "custom_model_id",
		"input_cost_per_mtok", "output_cost_per_mtok", "enabled"} {
		if _, ok := models[0][k]; !ok {
			t.Fatalf("contract missing key %q: %+v", k, models[0])
		}
	}

	if w := do(t, router, http.MethodDelete, "/ai/models/"+*model.CustomModelID, admin, ""); w.Code != http.StatusNoContent {
		t.Fatalf("DELETE model: want 204, got %d", w.Code)
	}
	if w := do(t, router, http.MethodDelete, "/ai/models/"+*model.CustomModelID, admin, ""); w.Code != http.StatusNotFound {
		t.Fatalf("second DELETE model: want 404, got %d", w.Code)
	}
}

// TestSettingsRoundTripUsesWorkspaceFromToken proves a PUT writes under the
// token's workspace and the update is visible only there.
func TestSettingsRoundTripUsesWorkspaceFromToken(t *testing.T) {
	h, store := newTestHandler(t, svcOpts{})
	router := authedRouter(h)
	wsA, wsB := uuid.New(), uuid.New()
	adminA := bearer(t, wsA, "owner")
	created := createAnthropic(t, router, adminA)
	sonnetID := created.ID + "/claude-sonnet-5"

	w := do(t, router, http.MethodPut, "/ai/settings", adminA,
		`{"default_smart_model":"`+sonnetID+`","additional_instructions":"keep it short"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("PUT settings: want 200, got %d: %s", w.Code, w.Body.String())
	}
	var got SettingsDTO
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.DefaultSmartModel != sonnetID || got.AdditionalInstructions != "keep it short" {
		t.Fatalf("PUT response wrong: %+v", got)
	}
	if _, ok := store.settings[wsA]; !ok {
		t.Fatalf("settings not written under token workspace")
	}

	w = do(t, router, http.MethodGet, "/ai/settings", bearer(t, wsB, "member"), "")
	var other SettingsDTO
	if err := json.Unmarshal(w.Body.Bytes(), &other); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if other.DefaultSmartModel != "default-smart-model" {
		t.Fatalf("workspace B must be untouched: %+v", other)
	}
}
