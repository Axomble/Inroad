package list

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/app/auth"
	"github.com/inroad/inroad/internal/platform/httpx"
)

// Routes returns this domain's HTTP surface, mounted by the server under
// e.g. /api/v1/lists. Every route requires an authenticated caller.
func (h *Handler) Routes() http.Handler {
	r := chi.NewRouter()
	r.Use(auth.RequireAuth(h.jwtSecret))
	r.Post("/", h.create)
	r.Get("/", h.list)
	return r
}

// workspaceID extracts and parses the workspace id from the JWT claims.
func workspaceID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	claims, ok := auth.UserFromContext(r.Context())
	if !ok {
		httpx.Error(w, http.StatusUnauthorized, "unauthorized")
		return uuid.Nil, false
	}
	id, err := uuid.Parse(claims.WorkspaceID)
	if err != nil {
		httpx.Error(w, http.StatusUnauthorized, "bad workspace")
		return uuid.Nil, false
	}
	return id, true
}
