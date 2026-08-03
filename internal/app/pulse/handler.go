package pulse

import (
	"net/http"

	"github.com/inroad/inroad/internal/app/auth"
	"github.com/inroad/inroad/internal/platform/httpx"
)

// Handler exposes the pulse read-model over HTTP. Authentication is applied
// by the protected router group (see cmd/inroad); the workspace is taken from
// the verified principal, never from the request.
type Handler struct {
	svc *Service
}

func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

// get handles GET /pulse — the whole aggregate payload in one response.
func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	wid, ok := auth.WorkspaceID(w, r)
	if !ok {
		return
	}
	p, err := h.svc.Get(r.Context(), wid)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "internal error")
		return
	}
	httpx.JSON(w, http.StatusOK, p)
}
