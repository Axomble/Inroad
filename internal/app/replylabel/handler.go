package replylabel

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/app/auth"
	"github.com/inroad/inroad/internal/platform/db/gen"
	"github.com/inroad/inroad/internal/platform/httpx"
)

// Handler exposes the reply-label taxonomy over HTTP. Authentication is applied
// by the protected router group (see cmd/inroad), not here; the workspace comes
// from the authenticated principal, never from the request.
type Handler struct{ svc *Service }

func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

// Routes returns this domain's HTTP surface, mounted under
// /api/v1/reply-labels.
//
// It is gated on the CAMPAIGN scopes rather than the inbox ones. A label's role
// flags decide whether a reply stops an enrollment, suppresses a contact, or
// opens a deal — that is send-automation configuration, and inbox:write is
// deliberately narrow (the read/unread boolean) and OAuth-grantable, so it must
// not become a lever on what a reply does to a campaign.
func (h *Handler) Routes() http.Handler {
	r := chi.NewRouter()
	r.With(auth.RequireScope(auth.ScopeCampaignsRead)).Get("/", h.list)
	r.Group(func(write chi.Router) {
		write.Use(auth.RequireScope(auth.ScopeCampaignsWrite))
		write.Post("/", h.create)
		write.Put("/{id}", h.update)
		write.Put("/reorder", h.reorder)
		write.Delete("/{id}", h.delete)
	})
	return r
}

// labelRequest is the wire shape for create/update. `key` is deliberately
// absent: it is derived from the label on create and immutable thereafter,
// because historical sequence_enrollments.reply_class rows name it as free text.
type labelRequest struct {
	Label             string `json:"label"`
	Color             string `json:"color"`
	StopsEnrollment   bool   `json:"stops_enrollment"`
	IsAutomated       bool   `json:"is_automated"`
	SuppressesContact bool   `json:"suppresses_contact"`
	CapturesDeal      bool   `json:"captures_deal"`
	DefersEnrollment  bool   `json:"defers_enrollment"`
}

func (r labelRequest) input() Input { return Input(r) }

// reorderRequest carries the labels in their new order. Every label in the
// workspace must appear exactly once (enforced by the service), so a stale
// client cannot silently collapse two labels onto one position.
type reorderRequest struct {
	IDs []uuid.UUID `json:"ids"`
}

// labelResponse is the wire shape of a label. workspace_id is omitted — the
// caller is already scoped to it and echoing a tenant id back invites clients
// to start sending one.
type labelResponse struct {
	ID                string `json:"id"`
	Key               string `json:"key"`
	Label             string `json:"label"`
	Color             string `json:"color"`
	Position          int32  `json:"position"`
	IsBuiltin         bool   `json:"is_builtin"`
	StopsEnrollment   bool   `json:"stops_enrollment"`
	IsAutomated       bool   `json:"is_automated"`
	SuppressesContact bool   `json:"suppresses_contact"`
	CapturesDeal      bool   `json:"captures_deal"`
	DefersEnrollment  bool   `json:"defers_enrollment"`
	CreatedAt         string `json:"created_at"`
	UpdatedAt         string `json:"updated_at"`
}

func toResponse(l gen.ReplyLabel) labelResponse {
	return labelResponse{
		ID: l.ID.String(), Key: l.Key, Label: l.Label, Color: l.Color, Position: l.Position,
		IsBuiltin: l.IsBuiltin, StopsEnrollment: l.StopsEnrollment, IsAutomated: l.IsAutomated,
		SuppressesContact: l.SuppressesContact, CapturesDeal: l.CapturesDeal,
		DefersEnrollment: l.DefersEnrollment,
		CreatedAt:        l.CreatedAt.Time.UTC().Format(time.RFC3339),
		UpdatedAt:        l.UpdatedAt.Time.UTC().Format(time.RFC3339),
	}
}

func toResponses(labels []gen.ReplyLabel) []labelResponse {
	out := make([]labelResponse, len(labels))
	for i, l := range labels {
		out[i] = toResponse(l)
	}
	return out
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	ws, ok := auth.WorkspaceID(w, r)
	if !ok {
		return
	}
	labels, err := h.svc.List(r.Context(), ws)
	if err != nil {
		writeError(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"labels": toResponses(labels)})
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	ws, ok := auth.WorkspaceID(w, r)
	if !ok {
		return
	}
	var req labelRequest
	if !decode(w, r, &req) {
		return
	}
	label, err := h.svc.Create(r.Context(), ws, req.input())
	if err != nil {
		writeError(w, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, toResponse(label))
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
	var req labelRequest
	if !decode(w, r, &req) {
		return
	}
	label, err := h.svc.Update(r.Context(), ws, id, req.input())
	if err != nil {
		writeError(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toResponse(label))
}

func (h *Handler) reorder(w http.ResponseWriter, r *http.Request) {
	ws, ok := auth.WorkspaceID(w, r)
	if !ok {
		return
	}
	var req reorderRequest
	if !decode(w, r, &req) {
		return
	}
	labels, err := h.svc.Reorder(r.Context(), ws, req.IDs)
	if err != nil {
		writeError(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"labels": toResponses(labels)})
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

func pathID(r *http.Request) (uuid.UUID, error) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		return uuid.Nil, ErrNotFound
	}
	return id, nil
}

// decode reads exactly one JSON object, rejecting unknown fields so a typo'd
// role flag is a 400 rather than a silently-false automation switch.
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
		httpx.Error(w, http.StatusNotFound, "reply label not found")
	case errors.Is(err, ErrConflict):
		httpx.Error(w, http.StatusConflict, err.Error())
	case errors.Is(err, ErrValidation):
		httpx.Error(w, http.StatusUnprocessableEntity, err.Error())
	default:
		httpx.Error(w, http.StatusInternalServerError, "reply label request failed")
	}
}
