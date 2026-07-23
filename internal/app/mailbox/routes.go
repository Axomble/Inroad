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
	r.With(auth.RequireVerified(checker)).Post("/oauth/google/start", h.startGoogleOAuth)
	r.Get("/", h.list)
	r.Get("/{id}", h.get)
	r.Post("/{id}/pause", h.pause)
	r.Post("/{id}/resume", h.resume)
	r.Delete("/{id}", h.delete)

	return r
}

// CallbackRoutes returns the PUBLIC OAuth callback surface, mounted at /oauth
// (alongside /u and /t). It authenticates from the signed `state` parameter,
// not the JWT cookie -- Google redirects the browser here at the top level, so
// the cookie is unavailable -- which is why it lives outside the protected
// group.
func (h *Handler) CallbackRoutes() http.Handler {
	r := chi.NewRouter()
	r.Get("/google/callback", h.googleCallback)
	return r
}
