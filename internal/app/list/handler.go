package list

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/app/auth"
	"github.com/inroad/inroad/internal/platform/httpx"
	"github.com/inroad/inroad/internal/platform/validate"
)

// Handler exposes the list domain over HTTP. Authentication is applied by the
// protected router group (see cmd/inroad), not here.
type Handler struct {
	svc *Service
}

func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

type createRequest struct {
	Name string `json:"name" validate:"required,min=1,max=200"`
}
type listResponse struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	ws, ok := auth.WorkspaceID(w, r)
	if !ok {
		return
	}
	var req createRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid json")
		return
	}
	if err := validate.Struct(req); err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	l, err := h.svc.Create(r.Context(), ws, req.Name)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "could not create list")
		return
	}
	httpx.JSON(w, http.StatusOK, listResponse{ID: l.ID.String(), Name: l.Name})
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	ws, ok := auth.WorkspaceID(w, r)
	if !ok {
		return
	}
	ls, err := h.svc.List(r.Context(), ws)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "could not list")
		return
	}
	out := make([]listResponse, 0, len(ls))
	for _, l := range ls {
		out = append(out, listResponse{ID: l.ID.String(), Name: l.Name})
	}
	httpx.JSON(w, http.StatusOK, out)
}

type renameRequest struct {
	Name string `json:"name" validate:"required,min=1,max=200"`
}

// rename handles PATCH /lists/{id}.
func (h *Handler) rename(w http.ResponseWriter, r *http.Request) {
	ws, ok := auth.WorkspaceID(w, r)
	if !ok {
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "bad id")
		return
	}
	var req renameRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid json")
		return
	}
	l, err := h.svc.Rename(r.Context(), ws, id, req.Name)
	switch {
	case errors.Is(err, ErrValidation):
		httpx.Error(w, http.StatusBadRequest, "invalid name")
	case errors.Is(err, ErrNotFound):
		httpx.Error(w, http.StatusNotFound, "not found")
	case err != nil:
		httpx.Error(w, http.StatusInternalServerError, "could not rename list")
	default:
		httpx.JSON(w, http.StatusOK, listResponse{ID: l.ID.String(), Name: l.Name})
	}
}

// delete handles DELETE /lists/{id}.
func (h *Handler) delete(w http.ResponseWriter, r *http.Request) {
	ws, ok := auth.WorkspaceID(w, r)
	if !ok {
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "bad id")
		return
	}
	switch err := h.svc.Delete(r.Context(), ws, id); {
	case errors.Is(err, ErrNotFound):
		httpx.Error(w, http.StatusNotFound, "not found")
	case errors.Is(err, ErrInUse):
		httpx.Error(w, http.StatusConflict, "list is used by a campaign")
	case err != nil:
		httpx.Error(w, http.StatusInternalServerError, "could not delete list")
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}
