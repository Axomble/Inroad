package identity

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/inroad/inroad/internal/app/auth"
)

// Routes mounts the full identity surface: public register/login, CSRF-guarded
// refresh/logout, and an access-token-protected group for session
// introspection and workspace switching. secret verifies the access token for
// the protected group.
func (h *Handler) Routes(secret []byte) http.Handler {
	r := chi.NewRouter()
	r.Post("/register", h.register)
	r.Post("/login", h.login)
	r.With(auth.RequireCSRF).Post("/refresh", h.refresh)
	r.With(auth.RequireCSRF).Post("/logout", h.logout)
	r.Group(func(pr chi.Router) {
		pr.Use(auth.RequireAuth(secret))
		pr.Get("/me", h.me)
		pr.Post("/logout-all", h.logoutAll)
		pr.Post("/switch-workspace", h.switchWorkspace)
	})
	return r
}
