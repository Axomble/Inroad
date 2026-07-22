package list

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

// Routes returns this domain's HTTP surface, mounted by the server under
// e.g. /api/v1/lists. Every route requires an authenticated caller; auth is
// enforced by the protected router group, not here.
func (h *Handler) Routes() http.Handler {
	r := chi.NewRouter()
	r.Post("/", h.create)
	r.Get("/", h.list)
	return r
}
