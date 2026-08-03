package deliverability

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/inroad/inroad/internal/app/auth"
)

// Routes returns the workspace-level surface, mounted by the server under
// /api/v1/deliverability. Every route requires an authenticated caller; auth is
// enforced by the protected router group, not here.
//
// Scopes: the rollup reads campaign sending data, so it takes campaigns:read.
// The ingest is a MACHINE endpoint on the api-key-accepting data plane and takes
// its own deliverability:write scope rather than campaigns:write — an external
// bounce pipeline needs to report events, and nothing else, so handing it the
// capability to mutate campaigns would be authority it has no use for.
func (h *Handler) Routes() http.Handler {
	r := chi.NewRouter()
	r.With(auth.RequireScope(auth.ScopeCampaignsRead)).Get("/", h.report)
	r.With(auth.RequireScope(auth.ScopeDeliverabilityWrite)).Post("/events", h.ingest)
	return r
}

// Register mounts the per-campaign routes onto the campaign router, so
// /campaigns/{id}/deliverability and /campaigns/{id}/guardrails live under the
// campaigns prefix and inherit its auth — chi disallows two routers mounted at
// the same prefix. Satisfies campaign.SubRouter (structurally; this package does
// not import app/campaign).
func (h *Handler) Register(r chi.Router) {
	r.With(auth.RequireScope(auth.ScopeCampaignsRead)).Get("/{id}/deliverability", h.campaignReport)
	r.With(auth.RequireScope(auth.ScopeCampaignsWrite)).Put("/{id}/guardrails", h.putGuardrails)
}
