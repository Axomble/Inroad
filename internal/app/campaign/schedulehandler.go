package campaign

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/app/auth"
	"github.com/inroad/inroad/internal/platform/cadence"
	"github.com/inroad/inroad/internal/platform/httpx"
)

// scheduleResponse is the wire shape of a campaign's sending plan: the zone plus
// the week's open intervals, grouped per weekday so the UI renders a row per day
// without regrouping a flat list, and the campaign-wide daily limit (null = no
// limit).
type scheduleResponse struct {
	Timezone   string        `json:"timezone"`
	Days       []scheduleDay `json:"days"`
	DailyLimit *int          `json:"daily_limit"`
	Preview    []string      `json:"preview"`
}

type scheduleDay struct {
	Weekday   int                `json:"weekday"`
	Intervals []scheduleInterval `json:"intervals"`
}

type scheduleInterval struct {
	StartMinute int `json:"start_minute"`
	EndMinute   int `json:"end_minute"`
}

// scheduleRequest is the full-replace payload. Days not present are closed, so
// omitting a weekday is how the client turns sending off for it; an omitted or null
// daily_limit is how it clears the campaign-wide limit.
type scheduleRequest struct {
	Timezone   string        `json:"timezone"`
	Days       []scheduleDay `json:"days"`
	DailyLimit *int          `json:"daily_limit"`
}

func (r scheduleRequest) toPlan() Plan {
	windows := make([]SendWindow, 0, len(r.Days))
	for _, d := range r.Days {
		for _, iv := range d.Intervals {
			windows = append(windows, SendWindow{
				Weekday: d.Weekday, StartMinute: iv.StartMinute, EndMinute: iv.EndMinute,
			})
		}
	}
	return Plan{Schedule: Schedule{Timezone: r.Timezone, Windows: windows}, DailyLimit: r.DailyLimit}
}

// newScheduleResponse groups the flat window list by weekday and renders a short
// preview of upcoming send instants, so the operator can see the cadence the
// schedule produces rather than having to trust it.
func newScheduleResponse(p Plan) scheduleResponse {
	byDay := map[int][]scheduleInterval{}
	for _, w := range p.Windows {
		byDay[w.Weekday] = append(byDay[w.Weekday], scheduleInterval{
			StartMinute: w.StartMinute, EndMinute: w.EndMinute,
		})
	}
	out := scheduleResponse{
		Timezone: p.Timezone, Days: make([]scheduleDay, 0, len(byDay)),
		DailyLimit: p.DailyLimit, Preview: schedulePreview(p.Schedule),
	}
	for weekday := range 7 {
		if intervals, ok := byDay[weekday]; ok {
			out.Days = append(out.Days, scheduleDay{Weekday: weekday, Intervals: intervals})
		}
	}
	return out
}

// previewCount is how many upcoming instants the preview renders — enough to show
// that sends are spread and off the clock grid, few enough to read at a glance.
const previewCount = 5

// schedulePreview renders the next few send instants this schedule would produce,
// formatted in the schedule's own zone. Returns nil for a schedule that doesn't
// compile; the caller only reaches this with a valid one.
func schedulePreview(s Schedule) []string {
	win, err := s.Compile()
	if err != nil {
		return nil
	}
	start, err := win.Next(time.Now(), "preview")
	if err != nil {
		return nil
	}
	offsets := cadence.Offsets(win.OpenDuration(start), previewCount, "preview")
	out := make([]string, 0, previewCount)
	for i, off := range offsets {
		at, err := win.NextAfterOffset(start, off, "preview-"+strconv.Itoa(i))
		if err != nil {
			return out
		}
		out = append(out, at.In(win.Loc).Format("Mon 15:04:05"))
	}
	return out
}

// getSchedule handles GET /campaigns/{id}/schedule.
func (h *Handler) getSchedule(w http.ResponseWriter, r *http.Request) {
	serveCampaignChild(w, r, "could not load schedule",
		func(ctx context.Context, ws, id uuid.UUID) (scheduleResponse, error) {
			plan, err := h.svc.GetSchedule(ctx, ws, id)
			if err != nil {
				return scheduleResponse{}, err
			}
			return newScheduleResponse(plan), nil
		})
}

// putSchedule handles PUT /campaigns/{id}/schedule — a full replace of the
// timezone, every window and the campaign-wide daily limit. Editable while
// running: it only affects sends scheduled after the change.
func (h *Handler) putSchedule(w http.ResponseWriter, r *http.Request) {
	ws, ok := auth.WorkspaceID(w, r)
	if !ok {
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "bad id")
		return
	}
	var req scheduleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid json")
		return
	}
	plan, err := h.svc.SetSchedule(r.Context(), ws, id, req.toPlan())
	switch {
	case errors.Is(err, ErrNotFound):
		httpx.Error(w, http.StatusNotFound, "not found")
	case errors.Is(err, ErrDailyLimit):
		httpx.Error(w, http.StatusUnprocessableEntity, "daily limit must be between 1 and 1000000")
	case errors.Is(err, cadence.ErrUnknownTimezone):
		httpx.Error(w, http.StatusUnprocessableEntity, "unknown timezone")
	case errors.Is(err, cadence.ErrEmptySchedule):
		httpx.Error(w, http.StatusUnprocessableEntity, "schedule must leave at least one interval open")
	case errors.Is(err, cadence.ErrBadSchedule):
		httpx.Error(w, http.StatusUnprocessableEntity, "invalid or overlapping send window")
	case err != nil:
		httpx.Error(w, http.StatusInternalServerError, "could not save schedule")
	default:
		httpx.JSON(w, http.StatusOK, newScheduleResponse(plan))
	}
}
