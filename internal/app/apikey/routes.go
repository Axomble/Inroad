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
// revoke keys. It is ADMIN-gated (matching invite management): a workspace member
// cannot mint, list, or revoke workspace-wide keys (revoke is workspace-pinned,
// not creator-pinned, so a member could otherwise revoke an admin's key). Every
// route is workspace-pinned in the handler from the JWT.
func (h *Handler) Routes(verifier auth.Verifier) http.Handler {
	r := chi.NewRouter()
	r.Group(func(pr chi.Router) {
		pr.Use(auth.RequireAuth(verifier))
		pr.Use(auth.RequireRole("admin"))
		pr.Post("/", h.create)       // POST   /auth/api-keys
		pr.Get("/", h.list)          // GET    /auth/api-keys
		pr.Delete("/{id}", h.revoke) // DELETE /auth/api-keys/{id}
	})
	return r
}
