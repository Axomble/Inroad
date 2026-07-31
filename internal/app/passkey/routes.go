package passkey

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/inroad/inroad/internal/app/auth"
)

// Routes returns the passkey surface, meant to be MOUNTED at "/passkeys" under the
// identity /auth router (so the full paths are /api/v1/auth/passkeys/...). The login
// begin/finish endpoints are PUBLIC — they run before any session exists, exactly
// like /auth/login: the discoverable assertion is the credential. Register
// begin/finish and list/delete require an authenticated session (verifier
// authenticates the access token for them).
//
// loginThrottle (may be nil) is the pre-auth rate-limit middleware wrapping the
// public /login/finish endpoint (the assertion-verifying, credential-guessing
// surface); it is applied only there, never to the authed group.
func (h *Handler) Routes(verifier auth.Verifier, loginThrottle func(http.Handler) http.Handler) http.Handler {
	r := chi.NewRouter()
	// Public discoverable login: the signed assertion is the credential.
	r.Post("/login/begin", h.loginBegin)
	if loginThrottle != nil {
		r.With(loginThrottle).Post("/login/finish", h.loginFinish)
	} else {
		r.Post("/login/finish", h.loginFinish)
	}
	r.Group(func(pr chi.Router) {
		pr.Use(auth.RequireAuth(verifier))
		pr.Get("/", h.list)                           // GET    /auth/passkeys
		pr.Post("/register/begin", h.registerBegin)   // POST   /auth/passkeys/register/begin
		pr.Post("/register/finish", h.registerFinish) // POST   /auth/passkeys/register/finish
		pr.Delete("/{id}", h.deleteCredential)        // DELETE /auth/passkeys/{id}
	})
	return r
}
