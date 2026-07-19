package workspace

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

// Routes returns this domain's HTTP surface, mounted by the server under /api/v1.
func (h *Handler) Routes() http.Handler {
	r := chi.NewRouter()
	r.Post("/register", h.register)
	r.Post("/login", h.login)
	return r
}
