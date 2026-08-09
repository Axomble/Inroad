package campaign

import (
	"encoding/csv"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/app/auth"
	"github.com/inroad/inroad/internal/platform/httpx"
)

// resultRowResponse is one arm's wire shape (CampaignResultRow in
// api/openapi.yaml).
type resultRowResponse struct {
	VariantID  *string `json:"variant_id"`
	Label      string  `json:"label"`
	IsBase     bool    `json:"is_base"`
	Weight     int32   `json:"weight"`
	Sent       int64   `json:"sent"`
	Opens      int64   `json:"opens"`
	Clicks     int64   `json:"clicks"`
	Replies    int64   `json:"replies"`
	Bounces    int64   `json:"bounces"`
	Unsubs     int64   `json:"unsubscribes"`
	OpenRate   float64 `json:"open_rate"`
	ClickRate  float64 `json:"click_rate"`
	ReplyRate  float64 `json:"reply_rate"`
	BounceRate float64 `json:"bounce_rate"`
	UnsubRate  float64 `json:"unsub_rate"`
}

type stepResultsResponse struct {
	StepOrder  int32               `json:"step_order"`
	Subject    string              `json:"subject"`
	Rows       []resultRowResponse `json:"rows"`
	Winner     *string             `json:"winner"`
	WinnerNote string              `json:"winner_note"`
}

type resultsResponse struct {
	CampaignID string                `json:"campaign_id"`
	Steps      []stepResultsResponse `json:"steps"`
}

// Results serves GET /campaigns/{id}/results.
func (h *Handler) Results(w http.ResponseWriter, r *http.Request) {
	ws, id, ok := campaignScope(w, r)
	if !ok {
		return
	}
	results, err := h.svc.Results(r.Context(), ws, id)
	if err != nil {
		writeResultsError(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, resultsPayload(results))
}

// ResultsCSV serves GET /campaigns/{id}/results.csv.
//
// A separate endpoint rather than a `format=csv` query parameter, because the
// two differ in more than encoding: this one is a flat table with one row per
// (step, variant), where the JSON is nested by step and carries the winner
// reading. Flattening is what a spreadsheet wants; the nesting is what the UI
// wants, and pretending one is a serialisation of the other would force one of
// them into the wrong shape.
//
// The winner is deliberately NOT a column. It is a judgement with a stated rule
// and a "too close to call" state, and a bare TRUE/FALSE in a spreadsheet cell
// loses the part that matters.
func (h *Handler) ResultsCSV(w http.ResponseWriter, r *http.Request) {
	ws, id, ok := campaignScope(w, r)
	if !ok {
		return
	}
	results, err := h.svc.Results(r.Context(), ws, id)
	if err != nil {
		writeResultsError(w, err)
		return
	}

	// Headers before the first write: once the CSV writer flushes, the status is
	// already sent and an error can no longer be reported as one.
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", "campaign-"+id.String()+"-results.csv"))

	cw := csv.NewWriter(w)
	// Rates are written as raw ratios, not percentages, because a spreadsheet
	// formats them and a pre-multiplied "4.2" is ambiguous between 4.2% and 420%.
	header := []string{
		"step_order", "subject", "variant", "is_base", "weight",
		"sent", "opens", "clicks", "replies", "bounces", "unsubscribes",
		"open_rate", "click_rate", "reply_rate", "bounce_rate", "unsub_rate",
	}
	if err := cw.Write(header); err != nil {
		return // the connection is gone; nothing useful to report
	}
	for _, step := range results.Steps {
		for _, row := range step.Rows {
			record := []string{
				strconv.FormatInt(int64(step.StepOrder), 10), step.Subject, row.Label,
				strconv.FormatBool(row.IsBase), strconv.FormatInt(int64(row.Weight), 10),
				strconv.FormatInt(row.Sent, 10), strconv.FormatInt(row.Opens, 10),
				strconv.FormatInt(row.Clicks, 10), strconv.FormatInt(row.Replies, 10),
				strconv.FormatInt(row.Bounces, 10), strconv.FormatInt(row.Unsubs, 10),
				formatRate(row.OpenRate), formatRate(row.ClickRate), formatRate(row.ReplyRate),
				formatRate(row.BounceRate), formatRate(row.UnsubRate),
			}
			if err := cw.Write(record); err != nil {
				return
			}
		}
	}
	cw.Flush()
}

// formatRate renders a ratio with enough precision to distinguish the reply
// rates this report exists to compare — 1.2% and 1.5% differ in the fourth
// decimal place.
func formatRate(rate float64) string {
	return strconv.FormatFloat(rate, 'f', 4, 64)
}

// campaignScope resolves the caller's workspace from the verified token and the
// campaign id from the path.
//
// serveCampaignChild is the usual helper for these routes, but it always writes
// a JSON body and maps every non-ErrNotFound error to 500. Neither fits here:
// the CSV response is not JSON, and ErrResultsUnavailable is a 503 about the
// server rather than a 500 about the request.
func campaignScope(w http.ResponseWriter, r *http.Request) (uuid.UUID, uuid.UUID, bool) {
	ws, ok := auth.WorkspaceID(w, r)
	if !ok {
		return uuid.Nil, uuid.Nil, false
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "bad id")
		return uuid.Nil, uuid.Nil, false
	}
	return ws, id, true
}

func writeResultsError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrNotFound):
		httpx.Error(w, http.StatusNotFound, "campaign not found")
	case errors.Is(err, ErrResultsUnavailable):
		// 503, not 500: nothing is broken about the request or the campaign, the
		// reporting dependency simply is not available on this deployment.
		httpx.Error(w, http.StatusServiceUnavailable, "campaign results are not available on this server")
	default:
		httpx.Error(w, http.StatusInternalServerError, "could not load campaign results")
	}
}

func resultsPayload(results CampaignResults) resultsResponse {
	out := resultsResponse{
		CampaignID: results.CampaignID.String(),
		Steps:      make([]stepResultsResponse, 0, len(results.Steps)),
	}
	for _, step := range results.Steps {
		rows := make([]resultRowResponse, 0, len(step.Rows))
		for _, r := range step.Rows {
			row := resultRowResponse{
				Label: r.Label, IsBase: r.IsBase, Weight: r.Weight,
				Sent: r.Sent, Opens: r.Opens, Clicks: r.Clicks,
				Replies: r.Replies, Bounces: r.Bounces, Unsubs: r.Unsubs,
				OpenRate: r.OpenRate, ClickRate: r.ClickRate, ReplyRate: r.ReplyRate,
				BounceRate: r.BounceRate, UnsubRate: r.UnsubRate,
			}
			if !r.IsBase {
				id := r.VariantID.String()
				row.VariantID = &id
			}
			rows = append(rows, row)
		}
		payload := stepResultsResponse{
			StepOrder: step.StepOrder, Subject: step.Subject, Rows: rows,
			WinnerNote: step.WinnerNote,
		}
		// null rather than "" for "no winner": an empty string would render as a
		// named-but-blank arm, and winner_note carries the reason.
		if step.Winner != "" {
			winner := step.Winner
			payload.Winner = &winner
		}
		out.Steps = append(out.Steps, payload)
	}
	return out
}
