package deliverability

import (
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/app/auth"
	"github.com/inroad/inroad/internal/platform/deliverability"
	"github.com/inroad/inroad/internal/platform/httpx"
)

// Handler exposes the deliverability surface over HTTP. Authentication is applied
// by the protected router group (see cmd/inroad), not here.
type Handler struct {
	svc *Service
}

func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

// --- wire shapes (components/schemas/Deliverability*, frozen before implementation) ---

// scoreComponentResponse is ScoreComponent. Rate is a pointer so an unmeasured
// signal serializes as null: the UI renders "not measured", and it must never be
// handed a 0 it could render as a clean 0%.
type scoreComponentResponse struct {
	Key      string   `json:"key"`
	Label    string   `json:"label"`
	Penalty  int      `json:"penalty"`
	Rate     *float64 `json:"rate"`
	Measured bool     `json:"measured"`
	Detail   string   `json:"detail"`
}

type scoreResponse struct {
	Value      int                      `json:"value"`
	Confidence string                   `json:"confidence"`
	Delivered  int                      `json:"delivered"`
	Components []scoreComponentResponse `json:"components"`
}

// deliverabilityPointResponse is DeliverabilityPoint. Complained and SpamPlaced
// are pointers for the same reason component rates are — an unmeasured day is
// null, not zero.
type deliverabilityPointResponse struct {
	Date       string `json:"date"`
	Delivered  int    `json:"delivered"`
	Bounced    int    `json:"bounced"`
	Complained *int   `json:"complained"`
	SpamPlaced *int   `json:"spam_placed"`
}

type atRiskItemResponse struct {
	Label  string `json:"label"`
	Reason string `json:"reason"`
}

type reportResponse struct {
	Score           scoreResponse                 `json:"score"`
	Series          []deliverabilityPointResponse `json:"series"`
	AtRiskMailboxes []atRiskItemResponse          `json:"at_risk_mailboxes"`
	AtRiskDomains   []atRiskItemResponse          `json:"at_risk_domains"`
}

type pauseEventResponse struct {
	Reason    string  `json:"reason"`
	Metric    string  `json:"metric"`
	Value     float64 `json:"value"`
	Threshold float64 `json:"threshold"`
	Delivered int     `json:"delivered"`
	CreatedAt string  `json:"created_at"`
}

type guardrailsResponse struct {
	AutoPauseEnabled  bool    `json:"auto_pause_enabled"`
	BouncePausePct    float64 `json:"bounce_pause_pct"`
	ComplaintPausePct float64 `json:"complaint_pause_pct"`
}

// campaignReportResponse is CampaignDeliverability. It has NO series field: the
// frozen schema has none, so emitting one would be payload the generated client
// cannot reach.
type campaignReportResponse struct {
	Score       scoreResponse        `json:"score"`
	Guardrails  guardrailsResponse   `json:"guardrails"`
	PauseEvents []pauseEventResponse `json:"pause_events"`
	Verdict     string               `json:"verdict"`
}

// guardrailsRequest is the PUT body. The three fields are pointers so a partial
// body is a caller error rather than a silent reset: omitting auto_pause_enabled
// would otherwise decode as false and turn the safeguard off by accident.
type guardrailsRequest struct {
	AutoPauseEnabled  *bool    `json:"auto_pause_enabled"`
	BouncePausePct    *float64 `json:"bounce_pause_pct"`
	ComplaintPausePct *float64 `json:"complaint_pause_pct"`
}

// eventRequest is the ingest body (DeliverabilityEvent).
type eventRequest struct {
	Kind            string  `json:"kind"`
	Email           string  `json:"email"`
	ProviderEventID string  `json:"provider_event_id"`
	SendID          *string `json:"send_id"`
}

// round2 rounds a percentage to two decimals for the wire. The stored value keeps
// full precision; this is so the UI is not handed 8.333333333333334 to format.
func round2(f float64) float64 { return math.Round(f*100) / 100 }

func toScoreResponse(s deliverability.Score) scoreResponse {
	comps := make([]scoreComponentResponse, 0, len(s.Components))
	for _, c := range s.Components {
		out := scoreComponentResponse{
			Key: c.Key, Label: c.Label, Penalty: c.Penalty,
			Measured: c.Measured, Detail: c.Detail,
		}
		if c.Rate != nil {
			r := round2(*c.Rate)
			out.Rate = &r
		}
		comps = append(comps, out)
	}
	return scoreResponse{
		Value: s.Value, Confidence: string(s.Confidence),
		Delivered: s.Delivered, Components: comps,
	}
}

// toSeriesResponse maps the per-day series, nulling the counts whose signal was
// not measured. The flags come from the score, so the two halves of the response
// agree by construction.
func toSeriesResponse(points []Point) []deliverabilityPointResponse {
	out := make([]deliverabilityPointResponse, 0, len(points))
	for _, p := range points {
		item := deliverabilityPointResponse{
			Date: p.Day.UTC().Format(time.DateOnly), Delivered: p.Delivered, Bounced: p.Bounced,
		}
		if p.ComplaintMeasured {
			n := p.Complained
			item.Complained = &n
		}
		if p.PlacementMeasured {
			n := p.SpamPlaced
			item.SpamPlaced = &n
		}
		out = append(out, item)
	}
	return out
}

// toRiskResponse maps the at-risk lists. The fields are assigned by name rather
// than converted wholesale even though the two structs currently match: the wire
// shape is frozen, and a conversion would silently follow a reordering of Risk's
// two same-typed fields.
func toRiskResponse(risks []Risk) []atRiskItemResponse {
	out := make([]atRiskItemResponse, len(risks))
	for i, r := range risks {
		out[i].Label = r.Label
		out[i].Reason = r.Reason
	}
	return out
}

func toGuardrailsResponse(g Guardrails) guardrailsResponse {
	return guardrailsResponse{
		AutoPauseEnabled:  g.AutoPauseEnabled,
		BouncePausePct:    round2(g.BouncePausePct),
		ComplaintPausePct: round2(g.ComplaintPausePct),
	}
}

func toPauseEventsResponse(events []PauseEvent) []pauseEventResponse {
	out := make([]pauseEventResponse, 0, len(events))
	for _, e := range events {
		out = append(out, pauseEventResponse{
			Reason: e.Reason, Metric: e.Metric,
			Value: round2(e.Value), Threshold: round2(e.Threshold),
			Delivered: e.Delivered, CreatedAt: e.CreatedAt.UTC().Format(time.RFC3339),
		})
	}
	return out
}

// writeErr maps the domain's two sentinel errors onto their status codes.
// Anything else is a 500 with no detail — an internal failure is not the caller's
// information.
func writeErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrNotFound):
		httpx.Error(w, http.StatusNotFound, "campaign not found")
	case errors.Is(err, ErrInvalid):
		httpx.Error(w, http.StatusUnprocessableEntity, err.Error())
	default:
		httpx.Error(w, http.StatusInternalServerError, "internal error")
	}
}

// campaignID parses the {id} path parameter. It is caller-controlled, so it is
// only ever used alongside the workspace from the JWT — every store method pins
// both, so a foreign id matches zero rows and becomes a 404.
func campaignID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid campaign id")
		return uuid.Nil, false
	}
	return id, true
}

func (h *Handler) report(w http.ResponseWriter, r *http.Request) {
	wid, ok := auth.WorkspaceID(w, r)
	if !ok {
		return
	}
	rep, err := h.svc.Report(r.Context(), wid)
	if err != nil {
		writeErr(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, reportResponse{
		Score:           toScoreResponse(rep.Score),
		Series:          toSeriesResponse(rep.Series),
		AtRiskMailboxes: toRiskResponse(rep.AtRiskMailboxes),
		AtRiskDomains:   toRiskResponse(rep.AtRiskDomains),
	})
}

func (h *Handler) campaignReport(w http.ResponseWriter, r *http.Request) {
	wid, ok := auth.WorkspaceID(w, r)
	if !ok {
		return
	}
	id, ok := campaignID(w, r)
	if !ok {
		return
	}
	rep, err := h.svc.CampaignReport(r.Context(), wid, id)
	if err != nil {
		writeErr(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, campaignReportResponse{
		Score:       toScoreResponse(rep.Score),
		Guardrails:  toGuardrailsResponse(rep.Guardrails),
		PauseEvents: toPauseEventsResponse(rep.PauseEvents),
		Verdict:     rep.Verdict,
	})
}

// putGuardrails replaces all three settings. Every field is required: a PUT that
// silently defaulted a missing auto_pause_enabled to false would disable the
// safeguard on a request that meant only to change a threshold.
func (h *Handler) putGuardrails(w http.ResponseWriter, r *http.Request) {
	wid, ok := auth.WorkspaceID(w, r)
	if !ok {
		return
	}
	id, ok := campaignID(w, r)
	if !ok {
		return
	}
	var req guardrailsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.AutoPauseEnabled == nil || req.BouncePausePct == nil || req.ComplaintPausePct == nil {
		httpx.Error(w, http.StatusUnprocessableEntity,
			"auto_pause_enabled, bounce_pause_pct and complaint_pause_pct are all required")
		return
	}
	saved, err := h.svc.SetGuardrails(r.Context(), wid, id, Guardrails{
		AutoPauseEnabled:  *req.AutoPauseEnabled,
		BouncePausePct:    *req.BouncePausePct,
		ComplaintPausePct: *req.ComplaintPausePct,
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toGuardrailsResponse(saved))
}

// ingest is the MACHINE endpoint: an external pipeline (an SES SNS subscriber, a
// provider webhook) reporting a complaint or bounce.
//
// 202 on accept, 200 on a duplicate replay. The distinction is deliberate and
// useful to the caller: both mean "we have it", and neither is an error, but a
// pipeline that sees 200s knows it is redelivering. The workspace comes from the
// authenticated principal, never from the body — an event can only ever be
// recorded against the tenant whose credential presented it.
func (h *Handler) ingest(w http.ResponseWriter, r *http.Request) {
	wid, ok := auth.WorkspaceID(w, r)
	if !ok {
		return
	}
	var req eventRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	in := EventInput{Kind: req.Kind, Email: req.Email, ProviderEventID: req.ProviderEventID}
	if req.SendID != nil && *req.SendID != "" {
		id, err := uuid.Parse(*req.SendID)
		if err != nil {
			httpx.Error(w, http.StatusUnprocessableEntity, "send_id is not a uuid")
			return
		}
		in.SendID = &id
	}
	res, err := h.svc.Ingest(r.Context(), wid, in)
	if err != nil {
		writeErr(w, err)
		return
	}
	if res.Duplicate {
		httpx.JSON(w, http.StatusOK, map[string]string{"status": "duplicate"})
		return
	}
	httpx.JSON(w, http.StatusAccepted, map[string]string{"status": "accepted"})
}
