package pulse

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

// Routes returns this domain's HTTP surface, mounted by the server under
// /api/v1/pulse (session-only group — the pulse is console chrome, not part
// of the api-key contract). Auth is enforced by the protected router group,
// not here.
func (h *Handler) Routes() http.Handler {
	r := chi.NewRouter()
	r.Get("/", h.get)
	return r
}
