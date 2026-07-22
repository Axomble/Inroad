package campaign

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/inroad/inroad/internal/app/auth"
	"github.com/inroad/inroad/internal/platform/db/gen"
)

// SubRouter registers additional routes onto the campaign router. Sub-resources
// (sequence steps) implement it so they live under /campaigns/{id} and inherit
// the auth middleware — chi disallows two routers mounted at the same prefix.
type SubRouter interface{ Register(r chi.Router) }

// Routes returns this domain's HTTP surface. Every route requires an
// authenticated caller; auth is enforced by the protected router group (see
// cmd/inroad). launch additionally requires a verified email (checker); and
// sub-resources (sequence steps) registered here inherit the group's auth by
// being mounted under /campaigns.
func (h *Handler) Routes(checker auth.VerifiedChecker) http.Handler {
	r := chi.NewRouter()
	r.Post("/", h.create)
	r.Get("/", h.list)
	r.Get("/{id}", h.get)
	r.With(auth.RequireVerified(checker)).Post("/{id}/launch", h.launch)
	for _, s := range h.subs {
		s.Register(r)
	}
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

// stepView is a step in the campaign detail response.
type stepView struct {
	ID           string `json:"id"`
	StepOrder    int32  `json:"step_order"`
	DelaySeconds int32  `json:"delay_seconds"`
	Subject      string `json:"subject"`
	BodyText     string `json:"body_text"`
	BodyHTML     string `json:"body_html"`
}

// campaignDetailResponse is the GET /campaigns/{id} payload: campaign summary
// plus its steps, send stats, and enrollment counts by status.
type campaignDetailResponse struct {
	ID          string           `json:"id"`
	Name        string           `json:"name"`
	Subject     string           `json:"subject"`
	Status      string           `json:"status"`
	Stats       map[string]int64 `json:"stats,omitempty"`
	Enrollments map[string]int64 `json:"enrollments,omitempty"`
	Steps       []stepView       `json:"steps"`
}

func toDetailResponse(d CampaignDetail) campaignDetailResponse {
	steps := make([]stepView, 0, len(d.Steps))
	for _, s := range d.Steps {
		steps = append(steps, stepView{
			ID: s.ID.String(), StepOrder: s.StepOrder, DelaySeconds: s.DelaySeconds,
			Subject: s.Subject, BodyText: s.BodyText, BodyHTML: s.BodyHtml,
		})
	}
	return campaignDetailResponse{
		ID: d.Campaign.ID.String(), Name: d.Campaign.Name, Subject: d.Campaign.Subject,
		Status: d.Campaign.Status, Stats: d.SendStats, Enrollments: d.Enrollments, Steps: steps,
	}
}
