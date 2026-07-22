package campaign

import (
	"math"
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
	r.Put("/{id}/tracking", h.toggleTracking)
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
// plus its steps, send stats, enrollment counts by status, tracking flag, and
// engagement metrics.
type campaignDetailResponse struct {
	ID              string           `json:"id"`
	Name            string           `json:"name"`
	Subject         string           `json:"subject"`
	Status          string           `json:"status"`
	TrackingEnabled bool             `json:"tracking_enabled"`
	Stats           map[string]int64 `json:"stats,omitempty"`
	Enrollments     map[string]int64 `json:"enrollments,omitempty"`
	Steps           []stepView       `json:"steps"`
	Metrics         metricsResponse  `json:"metrics"`
}

// metricsResponse is the engagement rollup on campaignDetailResponse. Rates
// are fractions in 0..1 rounded to 4 decimal places (e.g. 0.4123 == 41.23%);
// the frontend formats them as percentages. opens_indicative/open_rate are
// proxy-filtered but remain approximate -- clicks are the reliable signal.
// NOTE for the frontend tooltip: open_rate/click_rate are per-send (a
// multi-step campaign sends multiple times per contact), while
// reply_rate/bounce_rate/unsub_rate are per-contact (an enrollment stops at
// most once) -- see Metrics in service.go for the full rationale.
type metricsResponse struct {
	Sent            int64   `json:"sent"`
	OpensIndicative int64   `json:"opens_indicative"`
	Clicks          int64   `json:"clicks"`
	Replies         int64   `json:"replies"`
	Bounces         int64   `json:"bounces"`
	Unsubscribes    int64   `json:"unsubscribes"`
	OpenRate        float64 `json:"open_rate"`
	ClickRate       float64 `json:"click_rate"`
	ReplyRate       float64 `json:"reply_rate"`
	BounceRate      float64 `json:"bounce_rate"`
	UnsubRate       float64 `json:"unsub_rate"`
}

// round4 rounds a 0..1 fraction to 4 decimal places for the response DTO.
func round4(f float64) float64 { return math.Round(f*10000) / 10000 }

func toMetricsResponse(m Metrics) metricsResponse {
	return metricsResponse{
		Sent: m.Sent, OpensIndicative: m.OpensIndicative, Clicks: m.Clicks,
		Replies: m.Replies, Bounces: m.Bounces, Unsubscribes: m.Unsubscribes,
		OpenRate: round4(m.OpenRate), ClickRate: round4(m.ClickRate), ReplyRate: round4(m.ReplyRate),
		BounceRate: round4(m.BounceRate), UnsubRate: round4(m.UnsubRate),
	}
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
		Status: d.Campaign.Status, TrackingEnabled: d.Campaign.TrackingEnabled,
		Stats: d.SendStats, Enrollments: d.Enrollments, Steps: steps, Metrics: toMetricsResponse(d.Metrics),
	}
}
