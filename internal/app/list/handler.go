package list

import (
	"encoding/json"
	"net/http"

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
