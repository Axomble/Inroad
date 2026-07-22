package campaign

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/inroad/inroad/internal/platform/db/gen"
)

// Routes returns this domain's HTTP surface. Every route requires an
// authenticated caller; auth is enforced by the protected router group.
func (h *Handler) Routes() http.Handler {
	r := chi.NewRouter()
	r.Post("/", h.create)
	r.Get("/", h.list)
	r.Get("/{id}", h.get)
	r.Post("/{id}/launch", h.launch)
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
