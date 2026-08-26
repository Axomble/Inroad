package inbox

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/inroad/inroad/internal/app/auth"
	"github.com/inroad/inroad/internal/platform/httpx"
)

// mailboxCountResponse is one entry of the overview's per-mailbox breakdown.
type mailboxCountResponse struct {
	MailboxID string `json:"mailbox_id"`
	Total     int64  `json:"total"`
	Unread    int64  `json:"unread"`
}

// replyClassCountResponse is one entry of the overview's per-reply-class
// breakdown. Key is the raw last_reply_class; the client resolves it against
// the reply-label taxonomy it already holds.
type replyClassCountResponse struct {
	Key    string `json:"key"`
	Total  int64  `json:"total"`
	Unread int64  `json:"unread"`
}

// overviewResponse is GET /inbox/overview.
type overviewResponse struct {
	Total         int64                     `json:"total"`
	Unread        int64                     `json:"unread"`
	Today         int64                     `json:"today"`
	ThisWeek      int64                     `json:"this_week"`
	AwaitingReply int64                     `json:"awaiting_reply"`
	ByMailbox     []mailboxCountResponse    `json:"by_mailbox"`
	ByReplyClass  []replyClassCountResponse `json:"by_reply_class"`
}

func toOverviewResponse(o Overview) overviewResponse {
	// Both slices are built with make(..., 0, n) so an empty breakdown
	// marshals as [] rather than null: a client mapping over the array should
	// not have to special-case a missing one.
	byMailbox := make([]mailboxCountResponse, 0, len(o.ByMailbox))
	for _, m := range o.ByMailbox {
		byMailbox = append(byMailbox, mailboxCountResponse{
			MailboxID: m.MailboxID.String(), Total: m.Total, Unread: m.Unread,
		})
	}
	byClass := make([]replyClassCountResponse, 0, len(o.ByReplyClass))
	for _, c := range o.ByReplyClass {
		// A direct conversion, which staticcheck rightly prefers over
		// re-listing identical fields: ReplyClassCount and its wire shape
		// happen to have the same layout, and a future divergence in either
		// breaks this line loudly rather than silently mismapping.
		byClass = append(byClass, replyClassCountResponse(c))
	}
	return overviewResponse{
		Total:         o.Total,
		Unread:        o.Unread,
		Today:         o.Today,
		ThisWeek:      o.ThisWeek,
		AwaitingReply: o.AwaitingReply,
		ByMailbox:     byMailbox,
		ByReplyClass:  byClass,
	}
}

// overview handles GET /inbox/overview.
func (h *Handler) overview(w http.ResponseWriter, r *http.Request) {
	wid, ok := auth.WorkspaceID(w, r)
	if !ok {
		return
	}
	window, err := parseOverviewWindow(r)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	out, err := h.svc.GetOverview(r.Context(), wid, window)
	if err != nil {
		writeErr(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toOverviewResponse(out))
}

// maxTimezoneOffsetMinutes bounds the ?tz_offset= a caller may claim. Real
// UTC offsets span -12:00..+14:00; 840 minutes (+14h) is the largest in use
// (Line Islands) and -720 the smallest. A value outside that is either a
// client bug or someone probing, and is rejected rather than used to shift
// the "today" window somewhere arbitrary.
const maxTimezoneOffsetMinutes = 840
const minTimezoneOffsetMinutes = -720

// parseOverviewWindow resolves the "today" and "this week" boundaries from the
// request's ?tz_offset= (minutes East of UTC, as JavaScript's
// -getTimezoneOffset() reports it).
//
// The offset comes from the client because only the client knows the viewer's
// zone — see OverviewWindow's doc. An absent offset means UTC, which is the
// honest default rather than the server's local zone: the server's zone is a
// deployment detail and carries no information about the viewer.
//
// Weeks start Monday, matching ISO-8601 and every other date surface in this
// product.
func parseOverviewWindow(r *http.Request) (OverviewWindow, error) {
	offsetMinutes, err := parseTimezoneOffset(r.URL.Query().Get("tz_offset"))
	if err != nil {
		return OverviewWindow{}, err
	}
	return overviewWindowAt(time.Now(), offsetMinutes), nil
}

// parseTimezoneOffset validates the ?tz_offset= param. "" means UTC.
func parseTimezoneOffset(raw string) (int, error) {
	if raw == "" {
		return 0, nil
	}
	parsed, err := strconv.Atoi(raw)
	if err != nil || parsed < minTimezoneOffsetMinutes || parsed > maxTimezoneOffsetMinutes {
		return 0, fmt.Errorf("tz_offset must be minutes East of UTC between %d and %d",
			minTimezoneOffsetMinutes, maxTimezoneOffsetMinutes)
	}
	return parsed, nil
}

// overviewWindowAt resolves the day and week boundaries containing `now`, in a
// zone `offsetMinutes` East of UTC.
//
// `now` is a parameter rather than read from the clock inside, so the boundary
// arithmetic has a seam a test can pin to a fixed instant — otherwise the only
// assertions available are weak ones ("it's a Monday") that several incorrect
// implementations also satisfy.
//
// time.FixedZone has no DST, which is correct for a caller-supplied offset:
// the client re-reads its own offset per request, so a DST shift is picked up
// on the next call rather than mis-modelled here.
func overviewWindowAt(now time.Time, offsetMinutes int) OverviewWindow {
	zone := time.FixedZone("", offsetMinutes*60)
	nowLocal := now.In(zone)
	todayStart := time.Date(nowLocal.Year(), nowLocal.Month(), nowLocal.Day(), 0, 0, 0, 0, zone)
	// Monday-based: Go's Weekday() puts Sunday at 0, so Sunday is 6 days into
	// the week, not 0 days.
	daysSinceMonday := (int(todayStart.Weekday()) + 6) % 7
	return OverviewWindow{TodayStart: todayStart, WeekStart: todayStart.AddDate(0, 0, -daysSinceMonday)}
}
