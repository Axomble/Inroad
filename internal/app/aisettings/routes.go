package aisettings

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/inroad/inroad/internal/app/auth"
)

// Routes returns the /ai surface, mounted by the server under /api/v1/ai on
// the SESSION-ONLY tier (workspace administration, not part of the api-key
// contract). Authentication is enforced by the protected router group; every
// write — including discovery, which unseals a credential for an outbound
// dial — is additionally gated to workspace admins/owners, mirroring the
// invite-management routes.
func (h *Handler) Routes() http.Handler {
	admin := auth.RequireRole("admin")
	r := chi.NewRouter()
	r.Get("/settings", h.getSettings)
	r.With(admin).Put("/settings", h.updateSettings)
	r.Get("/providers", h.listProviders)
	r.With(admin).Post("/providers", h.createProvider)
	r.With(admin).Put("/providers/{id}", h.updateProvider)
	r.With(admin).Delete("/providers/{id}", h.deleteProvider)
	r.With(admin).Post("/providers/{id}/discover", h.discoverProvider)
	r.Get("/models", h.listModels)
	r.With(admin).Post("/models", h.createModel)
	r.With(admin).Delete("/models/{id}", h.deleteModel)
	return r
}
