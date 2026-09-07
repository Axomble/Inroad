package webhook

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/app/auth"
	"github.com/inroad/inroad/internal/platform/db/gen"
	"github.com/inroad/inroad/internal/platform/httpx"
)

// Handler exposes webhook-endpoint management over HTTP. Authentication is
// applied by the protected router group (see cmd/inroad — this is mounted in the
// sessionOnly group), not here; the workspace comes from the authenticated
// principal, never from the request.
type Handler struct{ svc *Service }

// NewHandler builds the HTTP surface over the service.
func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

// Routes returns this domain's HTTP surface, mounted under
// /api/v1/webhook-endpoints.
func (h *Handler) Routes() http.Handler {
	r := chi.NewRouter()
	r.Get("/", h.list)
	r.Post("/", h.create)
	r.Get("/{id}", h.get)
	r.Patch("/{id}", h.update)
	r.Delete("/{id}", h.delete)
	r.Post("/{id}/rotate-secret", h.rotateSecret)
	r.Post("/{id}/ping", h.ping)
	r.Get("/{id}/deliveries", h.listDeliveries)
	return r
}

// --- wire types ---

type createRequest struct {
	URL         string   `json:"url"`
	Description string   `json:"description"`
	EventTypes  []string `json:"event_types"`
}

// updateRequest is a PATCH: every field is a pointer so an absent key ("leave
// it") is distinguishable from a present zero value ("set it to this").
// event_types is a pointer-to-slice for the same reason — an absent key leaves
// the subscription list alone, whereas [] means "subscribe to every event".
type updateRequest struct {
	URL         *string   `json:"url"`
	Description *string   `json:"description"`
	EventTypes  *[]string `json:"event_types"`
	Active      *bool     `json:"active"`
}

// endpointResponse is the wire shape of an endpoint. workspace_id and
// secret_ciphertext are omitted by construction — the caller is already scoped
// to the workspace, and the sealed secret must never leave the server
// (docs/security.md invariant 2).
type endpointResponse struct {
	ID          string   `json:"id"`
	URL         string   `json:"url"`
	Description string   `json:"description"`
	EventTypes  []string `json:"event_types"`
	Active      bool     `json:"active"`
	CreatedAt   string   `json:"created_at"`
	UpdatedAt   string   `json:"updated_at"`
}

// endpointWithSecretResponse is endpointResponse plus the raw signing secret,
// returned exactly once (on create and on rotate-secret).
type endpointWithSecretResponse struct {
	endpointResponse
	Secret string `json:"secret"`
}

type deliveryResponse struct {
	ID             string  `json:"id"`
	EventType      string  `json:"event_type"`
	Status         string  `json:"status"`
	Attempts       int32   `json:"attempts"`
	LastError      string  `json:"last_error"`
	ResponseStatus *int32  `json:"response_status"`
	CreatedAt      string  `json:"created_at"`
	DeliveredAt    *string `json:"delivered_at"`
}

type deliveryListResponse struct {
	Items      []deliveryResponse `json:"items"`
	NextCursor *string            `json:"next_cursor"`
}

func toEndpointResponse(e gen.WebhookEndpoint) endpointResponse {
	types := e.EventTypes
	if types == nil {
		types = []string{}
	}
	return endpointResponse{
		ID:          e.ID.String(),
		URL:         e.Url,
		Description: e.Description,
		EventTypes:  types,
		Active:      e.Active,
		CreatedAt:   e.CreatedAt.Time.UTC().Format(time.RFC3339),
		UpdatedAt:   e.UpdatedAt.Time.UTC().Format(time.RFC3339),
	}
}

func toEndpointResponses(in []gen.WebhookEndpoint) []endpointResponse {
	out := make([]endpointResponse, len(in))
	for i, e := range in {
		out[i] = toEndpointResponse(e)
	}
	return out
}

func toDeliveryResponse(d gen.WebhookDelivery) deliveryResponse {
	out := deliveryResponse{
		ID:             d.ID.String(),
		EventType:      d.EventType,
		Status:         d.Status,
		Attempts:       d.Attempts,
		LastError:      d.LastError,
		ResponseStatus: d.ResponseStatus,
		CreatedAt:      d.CreatedAt.Time.UTC().Format(time.RFC3339),
	}
	if d.DeliveredAt.Valid {
		s := d.DeliveredAt.Time.UTC().Format(time.RFC3339)
		out.DeliveredAt = &s
	}
	return out
}

func toDeliveryResponses(in []gen.WebhookDelivery) []deliveryResponse {
	out := make([]deliveryResponse, 0, len(in))
	for _, d := range in {
		out = append(out, toDeliveryResponse(d))
	}
	return out
}

// --- handlers ---

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	ws, ok := auth.WorkspaceID(w, r)
	if !ok {
		return
	}
	endpoints, err := h.svc.List(r.Context(), ws)
	if err != nil {
		writeError(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": toEndpointResponses(endpoints)})
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	ws, ok := auth.WorkspaceID(w, r)
	if !ok {
		return
	}
	var req createRequest
	if !decode(w, r, &req) {
		return
	}
	ep, secret, err := h.svc.Create(r.Context(), ws, CreateInput(req))
	if err != nil {
		writeError(w, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, endpointWithSecretResponse{
		endpointResponse: toEndpointResponse(ep), Secret: secret,
	})
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	ws, ok := auth.WorkspaceID(w, r)
	if !ok {
		return
	}
	id, err := pathID(r)
	if err != nil {
		writeError(w, err)
		return
	}
	ep, err := h.svc.Get(r.Context(), ws, id)
	if err != nil {
		writeError(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toEndpointResponse(ep))
}

func (h *Handler) update(w http.ResponseWriter, r *http.Request) {
	ws, ok := auth.WorkspaceID(w, r)
	if !ok {
		return
	}
	id, err := pathID(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var req updateRequest
	if !decode(w, r, &req) {
		return
	}
	in := UpdateInput{URL: req.URL, Description: req.Description, Active: req.Active}
	if req.EventTypes != nil {
		// A present key: [] or null both mean "subscribe to everything". Hand the
		// service a non-nil slice so it treats this as a change, not "unchanged".
		if *req.EventTypes == nil {
			in.EventTypes = []string{}
		} else {
			in.EventTypes = *req.EventTypes
		}
	}
	ep, err := h.svc.Update(r.Context(), ws, id, in)
	if err != nil {
		writeError(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toEndpointResponse(ep))
}

func (h *Handler) delete(w http.ResponseWriter, r *http.Request) {
	ws, ok := auth.WorkspaceID(w, r)
	if !ok {
		return
	}
	id, err := pathID(r)
	if err != nil {
		writeError(w, err)
		return
	}
	if err := h.svc.Delete(r.Context(), ws, id); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) rotateSecret(w http.ResponseWriter, r *http.Request) {
	ws, ok := auth.WorkspaceID(w, r)
	if !ok {
		return
	}
	id, err := pathID(r)
	if err != nil {
		writeError(w, err)
		return
	}
	ep, secret, err := h.svc.RotateSecret(r.Context(), ws, id)
	if err != nil {
		writeError(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, endpointWithSecretResponse{
		endpointResponse: toEndpointResponse(ep), Secret: secret,
	})
}

func (h *Handler) ping(w http.ResponseWriter, r *http.Request) {
	ws, ok := auth.WorkspaceID(w, r)
	if !ok {
		return
	}
	id, err := pathID(r)
	if err != nil {
		writeError(w, err)
		return
	}
	del, err := h.svc.Ping(r.Context(), ws, id)
	if err != nil {
		writeError(w, err)
		return
	}
	httpx.JSON(w, http.StatusAccepted, toDeliveryResponse(del))
}

func (h *Handler) listDeliveries(w http.ResponseWriter, r *http.Request) {
	ws, ok := auth.WorkspaceID(w, r)
	if !ok {
		return
	}
	id, err := pathID(r)
	if err != nil {
		writeError(w, err)
		return
	}
	page, err := h.svc.ListDeliveries(r.Context(), ws, id, r.URL.Query().Get("cursor"), intQuery(r, "limit"))
	if err != nil {
		writeError(w, err)
		return
	}
	resp := deliveryListResponse{Items: toDeliveryResponses(page.Items)}
	if page.NextCursor != "" {
		resp.NextCursor = &page.NextCursor
	}
	httpx.JSON(w, http.StatusOK, resp)
}

// --- helpers ---

func pathID(r *http.Request) (uuid.UUID, error) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		return uuid.Nil, ErrNotFound
	}
	return id, nil
}

// intQuery reads an optional non-negative integer query parameter. An absent or
// unparseable value yields 0, which the service clamps to its default.
func intQuery(r *http.Request, key string) int32 {
	raw := r.URL.Query().Get(key)
	if raw == "" {
		return 0
	}
	n, err := strconv.ParseInt(raw, 10, 32)
	if err != nil || n < 0 {
		return 0
	}
	return int32(n)
}

// decode reads exactly one JSON object, rejecting unknown fields so a typo'd
// field is a 400 rather than a silently-ignored change.
func decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid JSON body")
		return false
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		httpx.Error(w, http.StatusBadRequest, "body must contain one JSON object")
		return false
	}
	return true
}

func writeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrNotFound):
		httpx.Error(w, http.StatusNotFound, "webhook endpoint not found")
	case errors.Is(err, ErrBadCursor):
		httpx.Error(w, http.StatusBadRequest, "page cursor is not valid for this list")
	case errors.Is(err, ErrValidation):
		httpx.Error(w, http.StatusUnprocessableEntity, err.Error())
	default:
		httpx.Error(w, http.StatusInternalServerError, "webhook request failed")
	}
}
