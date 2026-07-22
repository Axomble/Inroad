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
	r.Post("/verify-email", h.verifyEmail)
	// forgot/reset are pre-authentication flows, unlike refresh/logout: a
	// logged-out caller has no csrf_token cookie, so the double-submit gate
	// would 403 the exact users who need these. The CSRF threat model doesn't
	// apply here either - forgot acts on an arbitrary body email with no
	// ambient cookie authority, and reset's out-of-band single-use token is
	// itself the credential and can't be CSRF-forged.
	r.Post("/password/forgot", h.forgotPassword)
	r.Post("/password/reset", h.resetPassword)
	r.Group(func(pr chi.Router) {
		pr.Use(auth.RequireAuth(secret))
		pr.Get("/me", h.me)
		pr.Post("/logout-all", h.logoutAll)
		pr.Post("/switch-workspace", h.switchWorkspace)
		pr.Post("/verify-email/resend", h.resendVerification)
	})
	return r
}
