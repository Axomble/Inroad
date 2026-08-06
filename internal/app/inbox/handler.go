package inbox

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/app/auth"
	"github.com/inroad/inroad/internal/platform/httpx"
)

// Handler exposes the inbox domain over HTTP. Authentication is applied by
// the protected router group (see cmd/inroad), not here; the workspace is
// always taken from the JWT/scope-checked principal, never from the request.
type Handler struct {
	svc *Service
}

// NewHandler builds the inbox handler.
func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

// writeErr maps domain errors to HTTP status codes.
func writeErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrNotFound):
		httpx.Error(w, http.StatusNotFound, err.Error())
	case errors.Is(err, ErrValidation):
		httpx.Error(w, http.StatusBadRequest, err.Error())
	default:
		httpx.Error(w, http.StatusInternalServerError, "internal error")
	}
}

// messageResponse is the wire shape of one Message.
type messageResponse struct {
	Direction  string `json:"direction"`
	MessageID  string `json:"message_id"`
	FromEmail  string `json:"from_email"`
	FromName   string `json:"from_name"`
	ToEmail    string `json:"to_email"`
	Subject    string `json:"subject"`
	BodyText   string `json:"body_text"`
	BodyHTML   string `json:"body_html"`
	ReplyClass string `json:"reply_class"`
	OccurredAt string `json:"occurred_at"`
}

func toMessageResponse(m Message) messageResponse {
	return messageResponse{
		Direction:  m.Direction,
		MessageID:  m.MessageID,
		FromEmail:  m.FromEmail,
		FromName:   m.FromName,
		ToEmail:    m.ToEmail,
		Subject:    m.Subject,
		BodyText:   m.BodyText,
		BodyHTML:   m.BodyHTML,
		ReplyClass: m.ReplyClass,
		OccurredAt: m.OccurredAt.UTC().Format(time.RFC3339),
	}
}

// threadSummaryResponse is the wire shape of one Thread — the ThreadSummary
// schema shared by the list and detail endpoints.
type threadSummaryResponse struct {
	ID               string  `json:"id"`
	MailboxID        string  `json:"mailbox_id"`
	CampaignID       *string `json:"campaign_id"`
	ContactID        *string `json:"contact_id"`
	ContactEmail     string  `json:"contact_email"`
	ContactFirstName string  `json:"contact_first_name"`
	ContactLastName  string  `json:"contact_last_name"`
	Subject          string  `json:"subject"`
	LastReplyClass   string  `json:"last_reply_class"`
	Unread           bool    `json:"unread"`
	LastMessageAt    string  `json:"last_message_at"`
}

func toThreadSummaryResponse(t Thread) threadSummaryResponse {
	return threadSummaryResponse{
		ID:               t.ID.String(),
		MailboxID:        t.MailboxID.String(),
		CampaignID:       uuidString(t.CampaignID),
		ContactID:        uuidString(t.ContactID),
		ContactEmail:     t.ContactEmail,
		ContactFirstName: t.ContactFirstName,
		ContactLastName:  t.ContactLastName,
		Subject:          t.Subject,
		LastReplyClass:   t.LastReplyClass,
		Unread:           t.Unread,
		LastMessageAt:    t.LastMessageAt.UTC().Format(time.RFC3339),
	}
}

func uuidString(id *uuid.UUID) *string {
	if id == nil {
		return nil
	}
	s := id.String()
	return &s
}

// threadDetailResponse is GET /inbox/threads/{id}: the ThreadSummary fields
// plus the full message history, oldest first.
type threadDetailResponse struct {
	threadSummaryResponse
	Messages []messageResponse `json:"messages"`
}

func toThreadDetailResponse(d ThreadDetail) threadDetailResponse {
	messages := make([]messageResponse, 0, len(d.Messages))
	for _, m := range d.Messages {
		messages = append(messages, toMessageResponse(m))
	}
	return threadDetailResponse{threadSummaryResponse: toThreadSummaryResponse(d.Thread), Messages: messages}
}

// threadPageResponse is GET /inbox/threads. There is no separate cursor
// field: the next page's before_last_message_at/before_id are the last
// item's own last_message_at/id, per the keyset the store queries on.
type threadPageResponse struct {
	Items []threadSummaryResponse `json:"items"`
}

// list handles GET /inbox/threads.
func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	wid, ok := auth.WorkspaceID(w, r)
	if !ok {
		return
	}
	filter, err := parseListFilter(r)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	page, err := h.svc.ListThreads(r.Context(), wid, filter)
	if err != nil {
		writeErr(w, err)
		return
	}
	out := threadPageResponse{Items: make([]threadSummaryResponse, 0, len(page.Items))}
	for _, t := range page.Items {
		out.Items = append(out.Items, toThreadSummaryResponse(t))
	}
	httpx.JSON(w, http.StatusOK, out)
}

// parseListFilter reads the ?mailbox_id=&reply_class=&before_last_message_at=
// &before_id=&limit= query controls. A present-but-malformed value is
// rejected (400) rather than silently ignored, which would return a
// misleadingly "unfiltered" page instead of surfacing the caller's typo.
func parseListFilter(r *http.Request) (ListFilter, error) {
	q := r.URL.Query()
	var filter ListFilter

	if raw := q.Get("mailbox_id"); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			return ListFilter{}, errors.New("mailbox_id must be a UUID")
		}
		filter.MailboxID = &id
	}
	if raw := q.Get("reply_class"); raw != "" {
		filter.ReplyClass = &raw
	}
	filter.Query = q.Get("q")
	beforeAt := q.Get("before_last_message_at")
	beforeID := q.Get("before_id")
	// Checked BEFORE trying to parse either field: a half-set pair must
	// always surface the SAME clear message Service.ListThreads uses for the
	// identical case, not whichever field happened to fail time.Parse/
	// uuid.Parse on its own empty string (e.g. "before_id must be a UUID"
	// when the caller's real mistake was omitting before_last_message_at).
	if (beforeAt == "") != (beforeID == "") {
		return ListFilter{}, errors.New(errHalfSetCursor)
	}
	if beforeAt != "" && beforeID != "" {
		at, err := time.Parse(time.RFC3339, beforeAt)
		if err != nil {
			return ListFilter{}, errors.New("before_last_message_at must be RFC3339")
		}
		id, err := uuid.Parse(beforeID)
		if err != nil {
			return ListFilter{}, errors.New("before_id must be a UUID")
		}
		filter.BeforeLastMessageAt = &at
		filter.BeforeID = &id
	}
	if raw := q.Get("limit"); raw != "" {
		limit, err := strconv.ParseInt(raw, 10, 32)
		if err != nil || limit < 1 {
			return ListFilter{}, errors.New("limit must be a positive integer")
		}
		filter.Limit = int32(limit)
	}
	return filter, nil
}

// get handles GET /inbox/threads/{id}.
func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	wid, ok := auth.WorkspaceID(w, r)
	if !ok {
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid id")
		return
	}
	detail, err := h.svc.GetThread(r.Context(), wid, id)
	if err != nil {
		writeErr(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toThreadDetailResponse(detail))
}

// setReadRequest is the wire shape for PUT /inbox/threads/{id}/read.
type setReadRequest struct {
	Unread bool `json:"unread"`
}

// setRead handles PUT /inbox/threads/{id}/read. The caller already knows the
// value it set (an optimistic UI update), so a successful toggle returns 204
// rather than re-fetching the thread just to echo it back.
func (h *Handler) setRead(w http.ResponseWriter, r *http.Request) {
	wid, ok := auth.WorkspaceID(w, r)
	if !ok {
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid id")
		return
	}
	var req setReadRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid json")
		return
	}
	if err := h.svc.SetUnread(r.Context(), wid, id, req.Unread); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
