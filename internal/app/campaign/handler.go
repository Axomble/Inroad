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
	enq       Enqueuer
}

func NewHandler(svc *Service, jwtSecret []byte, enq Enqueuer) *Handler {
	return &Handler{svc: svc, jwtSecret: jwtSecret, enq: enq}
}

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
	stats, _ := h.svc.Stats(r.Context(), ws, id)
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

// launch transitions a draft campaign to running: it materializes sends for
// every list member and enqueues a send:email task for each.
func (h *Handler) launch(w http.ResponseWriter, r *http.Request) {
	ws, ok := wsID(w, r)
	if !ok {
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "bad id")
		return
	}
	res, err := h.svc.Launch(r.Context(), ws, id, h.enq)
	switch {
	case errors.Is(err, ErrNotFound):
		httpx.Error(w, http.StatusNotFound, "not found")
	case errors.Is(err, ErrAlreadyLaunched):
		httpx.Error(w, http.StatusConflict, "campaign already launched")
	case errors.Is(err, ErrEmptyList):
		httpx.Error(w, http.StatusUnprocessableEntity, "target list is empty")
	case err != nil:
		httpx.Error(w, http.StatusInternalServerError, "could not launch")
	default:
		// "queued" preserves the existing client contract; the split-out fields
		// let callers spot partial-enqueue outcomes without breaking the shape.
		httpx.JSON(w, http.StatusOK, map[string]int{
			"queued":               res.EnqueuedCount,
			"total_sends":          res.TotalSends,
			"failed_enqueue_count": res.FailedEnqueueCount,
		})
	}
}
