package aisettings

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/app/auth"
	"github.com/inroad/inroad/internal/platform/ai"
	"github.com/inroad/inroad/internal/platform/httpx"
)

// Handler exposes the AI-settings domain over HTTP. Authentication is applied
// by the protected router group (see cmd/inroad); role gating for writes is
// applied per-route in Routes(). The workspace is always taken from the JWT
// claims, never from the request body or path.
type Handler struct {
	svc *Service
}

func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

// decodeJSON enforces one object and rejects unknown fields. Silently
// accepting typos in credential/config requests can make callers believe an
// endpoint or secret was updated when it was ignored.
func decodeJSON(r *http.Request, dst any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	var trailing struct{}
	if err := dec.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple json values")
		}
		return err
	}
	return nil
}

// settingsRequest is the wire shape for PUT /ai/settings. Every field is a
// pointer so an omitted key keeps the current (or default) value; it maps 1:1
// onto SettingsUpdate.
type settingsRequest struct {
	DefaultSmartModel      *string   `json:"default_smart_model"`
	DefaultFastModel       *string   `json:"default_fast_model"`
	EnabledModelIDs        *[]string `json:"enabled_model_ids"`
	AdditionalInstructions *string   `json:"additional_instructions"`
}

// credentialsRequest is the kind-variant secret object. It lives only for the
// duration of the request; the service seals it whole and never echoes it.
type credentialsRequest struct {
	APIKey             string `json:"api_key"`
	AccessKeyID        string `json:"access_key_id"`
	SecretAccessKey    string `json:"secret_access_key"`
	ServiceAccountJSON string `json:"service_account_json"`
}

func (c credentialsRequest) toCredentials() ai.Credentials {
	// Identical field sets (only JSON tags differ); direct conversion keeps
	// the mapping honest.
	return ai.Credentials(c)
}

// providerCreateRequest is the wire shape for POST /ai/providers.
type providerCreateRequest struct {
	Kind        string             `json:"kind"`
	DisplayName string             `json:"display_name"`
	Credentials credentialsRequest `json:"credentials"`
	Config      map[string]string  `json:"config"`
}

// providerUpdateRequest is the wire shape for PUT /ai/providers/{id}.
// credentials absent = keep the sealed blob; config absent = keep;
// display_name absent = keep. kind is immutable and not accepted.
type providerUpdateRequest struct {
	DisplayName *string             `json:"display_name"`
	Credentials *credentialsRequest `json:"credentials"`
	Config      map[string]string   `json:"config"`
}

// modelCreateRequest is the wire shape for POST /ai/models.
type modelCreateRequest struct {
	ProviderID          string   `json:"provider_id"`
	Name                string   `json:"name"`
	Label               string   `json:"label"`
	ContextWindowTokens int      `json:"context_window_tokens"`
	MaxOutputTokens     int      `json:"max_output_tokens"`
	SupportsReasoning   bool     `json:"supports_reasoning"`
	InputCostPerMTok    *float64 `json:"input_cost_per_mtok"`
	OutputCostPerMTok   *float64 `json:"output_cost_per_mtok"`
}

// writeErr maps domain errors to HTTP status codes.
func writeErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrNotFound):
		httpx.Error(w, http.StatusNotFound, err.Error())
	case errors.Is(err, ErrDuplicate):
		httpx.Error(w, http.StatusConflict, err.Error())
	case errors.Is(err, ErrValidation):
		httpx.Error(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, ErrDiscoveryFailed):
		httpx.Error(w, http.StatusBadGateway, err.Error())
	default:
		httpx.Error(w, http.StatusInternalServerError, "internal error")
	}
}

// pathUUID parses the {id} URL parameter, writing a 400 on garbage.
func pathUUID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid id")
		return uuid.Nil, false
	}
	return id, true
}

// getSettings handles GET /ai/settings.
func (h *Handler) getSettings(w http.ResponseWriter, r *http.Request) {
	wid, ok := auth.WorkspaceID(w, r)
	if !ok {
		return
	}
	dto, err := h.svc.GetSettings(r.Context(), wid)
	if err != nil {
		writeErr(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, dto)
}

// updateSettings handles PUT /ai/settings.
func (h *Handler) updateSettings(w http.ResponseWriter, r *http.Request) {
	wid, ok := auth.WorkspaceID(w, r)
	if !ok {
		return
	}
	var req settingsRequest
	if err := decodeJSON(r, &req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid json")
		return
	}
	dto, err := h.svc.UpdateSettings(r.Context(), wid, SettingsUpdate(req))
	if err != nil {
		writeErr(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, dto)
}

// listProviders handles GET /ai/providers.
func (h *Handler) listProviders(w http.ResponseWriter, r *http.Request) {
	wid, ok := auth.WorkspaceID(w, r)
	if !ok {
		return
	}
	providers, err := h.svc.ListProviders(r.Context(), wid)
	if err != nil {
		writeErr(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string][]ProviderDTO{"providers": providers})
}

// createProvider handles POST /ai/providers.
func (h *Handler) createProvider(w http.ResponseWriter, r *http.Request) {
	wid, ok := auth.WorkspaceID(w, r)
	if !ok {
		return
	}
	var req providerCreateRequest
	if err := decodeJSON(r, &req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid json")
		return
	}
	dto, err := h.svc.CreateProvider(r.Context(), wid, ProviderCreateInput{
		Kind:        req.Kind,
		DisplayName: req.DisplayName,
		Credentials: req.Credentials.toCredentials(),
		Config:      req.Config,
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, dto)
}

// updateProvider handles PUT /ai/providers/{id}.
func (h *Handler) updateProvider(w http.ResponseWriter, r *http.Request) {
	wid, ok := auth.WorkspaceID(w, r)
	if !ok {
		return
	}
	id, ok := pathUUID(w, r)
	if !ok {
		return
	}
	var req providerUpdateRequest
	if err := decodeJSON(r, &req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid json")
		return
	}
	in := ProviderUpdateInput{DisplayName: req.DisplayName, Config: req.Config}
	if req.Credentials != nil {
		creds := req.Credentials.toCredentials()
		in.Credentials = &creds
	}
	dto, err := h.svc.UpdateProvider(r.Context(), wid, id, in)
	if err != nil {
		writeErr(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, dto)
}

// deleteProvider handles DELETE /ai/providers/{id}.
func (h *Handler) deleteProvider(w http.ResponseWriter, r *http.Request) {
	wid, ok := auth.WorkspaceID(w, r)
	if !ok {
		return
	}
	id, ok := pathUUID(w, r)
	if !ok {
		return
	}
	if err := h.svc.DeleteProvider(r.Context(), wid, id); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// discoverProvider handles POST /ai/providers/{id}/discover.
func (h *Handler) discoverProvider(w http.ResponseWriter, r *http.Request) {
	wid, ok := auth.WorkspaceID(w, r)
	if !ok {
		return
	}
	id, ok := pathUUID(w, r)
	if !ok {
		return
	}
	dto, err := h.svc.Discover(r.Context(), wid, id)
	if err != nil {
		writeErr(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, dto)
}

// listModels handles GET /ai/models.
func (h *Handler) listModels(w http.ResponseWriter, r *http.Request) {
	wid, ok := auth.WorkspaceID(w, r)
	if !ok {
		return
	}
	models, err := h.svc.ListModels(r.Context(), wid)
	if err != nil {
		writeErr(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string][]ModelDTO{"models": models})
}

// createModel handles POST /ai/models.
func (h *Handler) createModel(w http.ResponseWriter, r *http.Request) {
	wid, ok := auth.WorkspaceID(w, r)
	if !ok {
		return
	}
	var req modelCreateRequest
	if err := decodeJSON(r, &req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid json")
		return
	}
	providerID, err := uuid.Parse(req.ProviderID)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid provider_id")
		return
	}
	dto, err := h.svc.CreateModel(r.Context(), wid, ModelCreateInput{
		ProviderID:          providerID,
		Name:                req.Name,
		Label:               req.Label,
		ContextWindowTokens: req.ContextWindowTokens,
		MaxOutputTokens:     req.MaxOutputTokens,
		SupportsReasoning:   req.SupportsReasoning,
		InputCostPerMTok:    req.InputCostPerMTok,
		OutputCostPerMTok:   req.OutputCostPerMTok,
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, dto)
}

// deleteModel handles DELETE /ai/models/{id}.
func (h *Handler) deleteModel(w http.ResponseWriter, r *http.Request) {
	wid, ok := auth.WorkspaceID(w, r)
	if !ok {
		return
	}
	id, ok := pathUUID(w, r)
	if !ok {
		return
	}
	if err := h.svc.DeleteModel(r.Context(), wid, id); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
