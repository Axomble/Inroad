package twofa

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/inroad/inroad/internal/app/auth"
)

// Routes returns the 2FA surface, meant to be MOUNTED at "/2fa" under the
// identity /auth router (so the full paths are /api/v1/auth/2fa/...). The verify
// endpoint is PUBLIC — it runs after the password step but before a session
// exists, exactly like /auth/login — while enroll/confirm/disable/status require
// an authenticated session (verifier authenticates the access token for them).
//
// verifyThrottle (may be nil) is the pre-auth rate-limit middleware wrapping the
// public /verify endpoint; it is applied only there, never to the authed group.
func (h *Handler) Routes(verifier auth.Verifier, verifyThrottle func(http.Handler) http.Handler) http.Handler {
	r := chi.NewRouter()
	// Post-password, pre-session: the challenge token is the credential.
	if verifyThrottle != nil {
		r.With(verifyThrottle).Post("/verify", h.verify)
	} else {
		r.Post("/verify", h.verify)
	}
	r.Group(func(pr chi.Router) {
		pr.Use(auth.RequireAuth(verifier))
		pr.Get("/", h.status)               // GET  /auth/2fa
		pr.Post("/totp", h.enroll)          // POST /auth/2fa/totp
		pr.Post("/totp/confirm", h.confirm) // POST /auth/2fa/totp/confirm
		pr.Delete("/totp", h.disable)       // DELETE /auth/2fa/totp
	})
	return r
}
