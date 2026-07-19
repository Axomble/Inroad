package identity

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/inroad/inroad/internal/app/auth"
)

// PublicRoutes need no access token. refresh/logout self-authenticate via the
// refresh cookie + CSRF double-submit token.
func (h *Handler) PublicRoutes() http.Handler {
	r := chi.NewRouter()
	r.Post("/register", h.register)
	r.Post("/login", h.login)
	r.With(auth.RequireCSRF).Post("/refresh", h.refresh)
	r.With(auth.RequireCSRF).Post("/logout", h.logout)
	return r
}

// ProtectedRoutes require a valid access token (mounted under the protected group).
func (h *Handler) ProtectedRoutes() http.Handler {
	r := chi.NewRouter()
	r.Get("/me", h.me)
	r.Post("/logout-all", h.logoutAll)
	r.Post("/switch-workspace", h.switchWorkspace)
	return r
}
