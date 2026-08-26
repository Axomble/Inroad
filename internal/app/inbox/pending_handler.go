package inbox

import (
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/app/auth"
	"github.com/inroad/inroad/internal/platform/httpx"
)

// pendingReplyResponse is the wire shape of one queued reply.
type pendingReplyResponse struct {
	ID       string `json:"id"`
	ThreadID string `json:"thread_id"`
	Status   string `json:"status"`
	// SendAfter is what the client's undo countdown runs against.
	SendAfter string  `json:"send_after"`
	SentAt    *string `json:"sent_at"`
	BodyText  string  `json:"body_text"`
	LastError string  `json:"last_error"`
	// Cancellable saves the client from reimplementing the server's status rule
	// — and from offering an Undo button that is guaranteed to fail.
	Cancellable   bool   `json:"cancellable"`
	ThreadSubject string `json:"thread_subject"`
	ContactEmail  string `json:"contact_email"`
	CreatedAt     string `json:"created_at"`
}

func toPendingReplyResponse(p PendingReply) pendingReplyResponse {
	out := pendingReplyResponse{
		ID:            p.ID.String(),
		ThreadID:      p.ThreadID.String(),
		Status:        p.Status,
		SendAfter:     p.SendAfter.UTC().Format(time.RFC3339),
		BodyText:      p.BodyText,
		LastError:     p.LastError,
		Cancellable:   p.Cancellable(),
		ThreadSubject: p.ThreadSubject,
		ContactEmail:  p.ContactEmail,
		CreatedAt:     p.CreatedAt.UTC().Format(time.RFC3339),
	}
	if p.SentAt != nil {
		sent := p.SentAt.UTC().Format(time.RFC3339)
		out.SentAt = &sent
	}
	return out
}

// pendingReplyListResponse is GET /inbox/outbox.
type pendingReplyListResponse struct {
	Items []pendingReplyResponse `json:"items"`
}

// scheduleReplyRequest is the body for POST /inbox/threads/{id}/schedule-reply.
type scheduleReplyRequest struct {
	BodyText string `json:"body_text"`
	// SendAt is an optional RFC3339 instant. Omitted means "after the
	// workspace's undo window" — the ordinary Send. An explicit value is a
	// deliberate schedule and is bounded to 30 days.
	SendAt string `json:"send_at"`
}

// scheduleReply handles POST /inbox/threads/{id}/schedule-reply.
//
// A separate route from POST .../reply rather than a flag on it, because the two
// have genuinely different contracts: the immediate path is fire-and-forget
// (202, no body), while this one must return the pending reply's id — without it
// the client has no handle to undo with, which is the entire feature.
func (h *Handler) scheduleReply(w http.ResponseWriter, r *http.Request) {
	wid, ok := auth.WorkspaceID(w, r)
	if !ok {
		return
	}
	threadID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid id")
		return
	}
	var req scheduleReplyRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	var sendAt *time.Time
	if req.SendAt != "" {
		at, err := time.Parse(time.RFC3339, req.SendAt)
		if err != nil {
			httpx.Error(w, http.StatusBadRequest, "send_at must be an RFC3339 timestamp")
			return
		}
		sendAt = &at
	}

	pending, err := h.svc.ScheduleReply(r.Context(), wid, threadID, req.BodyText, sendAt, callerUserID(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	// 201: a new resource exists at /inbox/outbox, and the operator can act on
	// it. Deliberately NOT the immediate path's bodyless 202 — the id is the
	// point.
	httpx.JSON(w, http.StatusCreated, toPendingReplyResponse(pending))
}

// cancelPendingReply handles DELETE /inbox/outbox/{pendingId} — the undo.
func (h *Handler) cancelPendingReply(w http.ResponseWriter, r *http.Request) {
	wid, ok := auth.WorkspaceID(w, r)
	if !ok {
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "pendingId"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid id")
		return
	}
	if err := h.svc.CancelPendingReply(r.Context(), wid, id); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// listPendingReplies handles GET /inbox/outbox.
func (h *Handler) listPendingReplies(w http.ResponseWriter, r *http.Request) {
	wid, ok := auth.WorkspaceID(w, r)
	if !ok {
		return
	}
	var limit int32
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 32)
		if err != nil || parsed < 1 {
			httpx.Error(w, http.StatusBadRequest, "limit must be a positive integer")
			return
		}
		limit = int32(parsed)
	}

	items, err := h.svc.ListPendingReplies(r.Context(), wid, limit)
	if err != nil {
		writeErr(w, err)
		return
	}
	out := pendingReplyListResponse{Items: make([]pendingReplyResponse, 0, len(items))}
	for _, p := range items {
		out.Items = append(out.Items, toPendingReplyResponse(p))
	}
	httpx.JSON(w, http.StatusOK, out)
}

// inboxSettingsResponse is GET/PUT /inbox/settings.
type inboxSettingsResponse struct {
	UndoSendSeconds int `json:"undo_send_seconds"`
}

// getInboxSettings handles GET /inbox/settings.
func (h *Handler) getInboxSettings(w http.ResponseWriter, r *http.Request) {
	wid, ok := auth.WorkspaceID(w, r)
	if !ok {
		return
	}
	window, err := h.svc.UndoWindow(r.Context(), wid)
	if err != nil {
		writeErr(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, inboxSettingsResponse{UndoSendSeconds: int(window.Seconds())})
}

// updateInboxSettings handles PUT /inbox/settings.
func (h *Handler) updateInboxSettings(w http.ResponseWriter, r *http.Request) {
	wid, ok := auth.WorkspaceID(w, r)
	if !ok {
		return
	}
	var req inboxSettingsResponse
	if !decodeJSON(w, r, &req) {
		return
	}
	if err := h.svc.SetUndoWindow(r.Context(), wid, int32(req.UndoSendSeconds)); err != nil {
		writeErr(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, req)
}
