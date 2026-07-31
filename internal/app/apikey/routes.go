package apikey

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/inroad/inroad/internal/app/auth"
)

// Routes returns the api-key management surface, meant to be MOUNTED at
// "/api-keys" under the identity /auth router (so the full paths are
// /api/v1/auth/api-keys...). Management is SESSION-authed: verifier is the
// session verifier, NOT the api-key verifier — a key must not be able to mint or
// revoke keys. Every route is workspace-pinned in the handler from the JWT.
func (h *Handler) Routes(verifier auth.Verifier) http.Handler {
	r := chi.NewRouter()
	r.Group(func(pr chi.Router) {
		pr.Use(auth.RequireAuth(verifier))
		pr.Post("/", h.create)       // POST   /auth/api-keys
		pr.Get("/", h.list)          // GET    /auth/api-keys
		pr.Delete("/{id}", h.revoke) // DELETE /auth/api-keys/{id}
	})
	return r
}
