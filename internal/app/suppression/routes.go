package suppression

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

// Routes mounts the PUBLIC unsubscribe endpoint. Deliberately has NO auth
// middleware: recipients follow this link unauthenticated from an email.
func (h *Handler) Routes() http.Handler {
	r := chi.NewRouter()
	r.Get("/{token}", h.unsubscribe)
	return r
}
