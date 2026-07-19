package campaign

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/platform/httpx"
	"github.com/inroad/inroad/internal/platform/validate"
)

type Handler struct {
	svc       *Service
	jwtSecret []byte
}

func NewHandler(svc *Service, jwtSecret []byte) *Handler { return &Handler{svc: svc, jwtSecret: jwtSecret} }

type createRequest struct {
	Name      string `json:"name" validate:"required,min=1,max=200"`
	MailboxID string `json:"mailbox_id" validate:"required,uuid"`
	ListID    string `json:"list_id" validate:"required,uuid"`
	Subject   string `json:"subject" validate:"required,min=1,max=500"`
	BodyText  string `json:"body_text"`
	BodyHTML  string `json:"body_html"`
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	ws, ok := wsID(w, r)
	if !ok {
		return
	}
	var req createRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid json")
		return
	}
	if err := validate.Struct(req); err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.BodyText == "" && req.BodyHTML == "" {
		httpx.Error(w, http.StatusBadRequest, "body_text or body_html required")
		return
	}
	mid, _ := uuid.Parse(req.MailboxID)
	lid, _ := uuid.Parse(req.ListID)
	c, err := h.svc.Create(r.Context(), ws, CreateInput{
		Name: req.Name, Subject: req.Subject, BodyText: req.BodyText, BodyHTML: req.BodyHTML,
		MailboxID: mid, ListID: lid,
	})
	switch {
	case errors.Is(err, ErrMailboxNotActive):
		httpx.Error(w, http.StatusUnprocessableEntity, "mailbox not found or not active")
	case errors.Is(err, ErrListMissing):
		httpx.Error(w, http.StatusNotFound, "list not found")
	case err != nil:
		httpx.Error(w, http.StatusInternalServerError, "could not create campaign")
	default:
		httpx.JSON(w, http.StatusOK, toResponse(c, nil))
	}
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	ws, ok := wsID(w, r)
	if !ok {
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "bad id")
		return
	}
	c, err := h.svc.Get(r.Context(), ws, id)
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "not found")
		return
	}
	stats, _ := h.svc.Stats(r.Context(), id)
	httpx.JSON(w, http.StatusOK, toResponse(c, stats))
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	ws, ok := wsID(w, r)
	if !ok {
		return
	}
	cs, err := h.svc.List(r.Context(), ws)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "could not list")
		return
	}
	out := make([]campaignResponse, 0, len(cs))
	for _, c := range cs {
		out = append(out, toResponse(c, nil))
	}
	httpx.JSON(w, http.StatusOK, out)
}

// launch is a temporary stub; the real implementation (enqueueing sends and
// transitioning the campaign to running) is added in Task 8.
func (h *Handler) launch(w http.ResponseWriter, r *http.Request) {
	httpx.Error(w, http.StatusNotImplemented, "not implemented")
}
