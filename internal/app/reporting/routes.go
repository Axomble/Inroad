package reporting

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/inroad/inroad/internal/app/auth"
)

// Routes returns this domain's HTTP surface, mounted by the server under
// /api/v1/reports. Auth is enforced by the protected router group, not here.
//
// Gated on campaigns:read rather than being session-only: unlike the pulse
// (console chrome), a campaign report is exactly the kind of thing an external
// dashboard has a legitimate reason to pull, and it exposes no data a caller
// holding campaigns:read couldn't already assemble from the per-campaign
// endpoints — this one just doesn't make them do it N times.
func (h *Handler) Routes() http.Handler {
	r := chi.NewRouter()
	r.With(auth.RequireScope(auth.ScopeCampaignsRead)).Get("/campaigns", h.campaigns)
	return r
}
