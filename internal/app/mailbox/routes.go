package mailbox

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/inroad/inroad/internal/app/auth"
)

// Routes returns this domain's HTTP surface, mounted by the server under
// e.g. /api/v1/mailboxes. Every route requires an authenticated caller;
// auth is enforced by the protected router group, not here. connect
// additionally requires a verified email, checked via checker.
func (h *Handler) Routes(checker auth.VerifiedChecker) http.Handler {
	r := chi.NewRouter()

	r.With(auth.RequireVerified(checker)).Post("/", h.connect)
	r.Get("/", h.list)
	r.Get("/{id}", h.get)
	r.Post("/{id}/pause", h.pause)
	r.Post("/{id}/resume", h.resume)
	r.Delete("/{id}", h.delete)

	return r
}
