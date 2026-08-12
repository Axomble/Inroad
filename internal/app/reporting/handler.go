package reporting

import (
	"net/http"

	"github.com/inroad/inroad/internal/app/auth"
	"github.com/inroad/inroad/internal/platform/httpx"
)

// Handler exposes the reporting read-model over HTTP. Authentication is applied
// by the protected router group (see cmd/inroad); the workspace is taken from
// the verified principal, never from the request.
type Handler struct {
	svc *Service
}

func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

// campaigns handles GET /reports/campaigns.
func (h *Handler) campaigns(w http.ResponseWriter, r *http.Request) {
	wid, ok := auth.WorkspaceID(w, r)
	if !ok {
		return
	}
	report, err := h.svc.CampaignPerformance(r.Context(), wid)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "internal error")
		return
	}
	httpx.JSON(w, http.StatusOK, report)
}
