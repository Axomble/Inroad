package mailbox

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/inroad/inroad/internal/app/auth"
)

// Routes returns this domain's HTTP surface, mounted by the server under
// e.g. /api/v1/mailboxes. Every route requires an authenticated caller.
func (h *Handler) Routes() http.Handler {
	r := chi.NewRouter()
	r.Use(auth.RequireAuth(h.jwtSecret))

	r.Post("/", h.connect)
	r.Get("/", h.list)
	r.Get("/{id}", h.get)
	r.Post("/{id}/pause", h.pause)
	r.Post("/{id}/resume", h.resume)
	r.Delete("/{id}", h.delete)

	return r
}
