package list

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/inroad/inroad/internal/app/auth"
)

// Routes returns this domain's HTTP surface, mounted by the server under
// e.g. /api/v1/lists. Every route requires an authenticated caller; auth is
// enforced by the protected router group, not here. RequireScope attenuates
// machine (api-key) principals; a session principal implicitly holds every scope.
func (h *Handler) Routes() http.Handler {
	r := chi.NewRouter()
	r.With(auth.RequireScope(auth.ScopeListsWrite)).Post("/", h.create)
	r.With(auth.RequireScope(auth.ScopeListsWrite)).Patch("/{id}", h.rename)
	r.With(auth.RequireScope(auth.ScopeListsWrite)).Delete("/{id}", h.delete)
	r.With(auth.RequireScope(auth.ScopeListsRead)).Get("/", h.list)
	return r
}
