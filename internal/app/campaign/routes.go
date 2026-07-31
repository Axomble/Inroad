package campaign

import (
	"math"
	"net/http"
	"time"

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
	// RequireScope attenuates machine (api-key) principals to their granted scopes;
	// a session principal implicitly holds every scope, so these are transparent to
	// human callers. Launch requires the dedicated campaigns:send scope.
	read := auth.RequireScope(auth.ScopeCampaignsRead)
	write := auth.RequireScope(auth.ScopeCampaignsWrite)
	r.With(write).Post("/", h.create)
	r.With(read).Get("/", h.list)
	r.With(read).Get("/{id}", h.get)
	r.With(read).Get("/{id}/enrollments", h.listEnrollments)
	r.With(auth.RequireScope(auth.ScopeCampaignsSend), auth.RequireVerified(checker)).Post("/{id}/launch", h.launch)
	r.With(write).Put("/{id}/tracking", h.toggleTracking)
	r.With(read).Get("/{id}/schedule", h.getSchedule)
	r.With(write).Put("/{id}/schedule", h.putSchedule)
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

// enrollmentResponse is one row of GET /campaigns/{id}/enrollments: a contact's
// display email/name, the enrollment lifecycle status, and the classified reply
// (class/source/replied_at). The three reply fields are null until a reply is
// classified for that enrollment; replied_at is RFC3339 (UTC).
type enrollmentResponse struct {
	Email       string  `json:"email"`
	FirstName   string  `json:"first_name"`
	Status      string  `json:"status"`
	ReplyClass  *string `json:"reply_class"`
	ReplySource *string `json:"reply_source"`
	RepliedAt   *string `json:"replied_at"`
}

func toEnrollmentResponse(e gen.ListCampaignEnrollmentsRow) enrollmentResponse {
	var repliedAt *string
	if e.RepliedAt.Valid {
		s := e.RepliedAt.Time.UTC().Format(time.RFC3339)
		repliedAt = &s
	}
	return enrollmentResponse{
		Email: e.Email, FirstName: e.FirstName, Status: e.Status,
		ReplyClass: e.ReplyClass, ReplySource: e.ReplySource, RepliedAt: repliedAt,
	}
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
