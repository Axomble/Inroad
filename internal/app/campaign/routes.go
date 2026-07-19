package campaign

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/app/auth"
	"github.com/inroad/inroad/internal/platform/db/gen"
	"github.com/inroad/inroad/internal/platform/httpx"
)

func (h *Handler) Routes() http.Handler {
	r := chi.NewRouter()
	r.Use(auth.RequireAuth(h.jwtSecret))
	r.Post("/", h.create)
	r.Get("/", h.list)
	r.Get("/{id}", h.get)
	r.Post("/{id}/launch", h.launch) // implemented in Task 8
	return r
}

type campaignResponse struct {
	ID      string           `json:"id"`
	Name    string           `json:"name"`
	Subject string           `json:"subject"`
	Status  string           `json:"status"`
	Stats   map[string]int64 `json:"stats,omitempty"`
}

func toResponse(c gen.Campaign, stats map[string]int64) campaignResponse {
	return campaignResponse{ID: c.ID.String(), Name: c.Name, Subject: c.Subject, Status: c.Status, Stats: stats}
}

// workspaceID extracts and parses the workspace id from the JWT claims.
func wsID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
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
