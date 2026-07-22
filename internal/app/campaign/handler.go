package campaign

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/app/auth"
	"github.com/inroad/inroad/internal/platform/httpx"
	"github.com/inroad/inroad/internal/platform/validate"
)

// Handler exposes the campaign domain over HTTP. Authentication is applied by
// the protected router group (see cmd/inroad), not here.
type Handler struct {
	svc  *Service
	enq  Enqueuer
	subs []SubRouter
}

// NewHandler builds the campaign handler. Optional subs register additional
// routes under the campaign scope (e.g. sequence steps at /{id}/steps) so
// sub-resources share the {id} param and the protected group's auth without
// campaign importing their packages. Auth is applied by the protected router
// group (see cmd/inroad), so the handler no longer carries a jwtSecret.
func NewHandler(svc *Service, enq Enqueuer, subs ...SubRouter) *Handler {
	return &Handler{svc: svc, enq: enq, subs: subs}
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
	ws, ok := auth.WorkspaceID(w, r)
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
	ws, ok := auth.WorkspaceID(w, r)
	if !ok {
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "bad id")
		return
	}
	d, err := h.svc.Detail(r.Context(), ws, id)
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "not found")
		return
	}
	httpx.JSON(w, http.StatusOK, toDetailResponse(d))
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	ws, ok := auth.WorkspaceID(w, r)
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

type trackingRequest struct {
	Enabled bool `json:"enabled"`
}

// toggleTracking handles PUT /campaigns/{id}/tracking.
func (h *Handler) toggleTracking(w http.ResponseWriter, r *http.Request) {
	ws, ok := auth.WorkspaceID(w, r)
	if !ok {
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "bad id")
		return
	}
	var req trackingRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid json")
		return
	}
	switch err := h.svc.SetTracking(r.Context(), ws, id, req.Enabled); {
	case errors.Is(err, ErrNotFound):
		httpx.Error(w, http.StatusNotFound, "not found")
	case err != nil:
		httpx.Error(w, http.StatusInternalServerError, "could not update tracking")
	default:
		httpx.JSON(w, http.StatusOK, map[string]bool{"tracking_enabled": req.Enabled})
	}
}

// launch transitions a draft campaign to running: it materializes sends for
// every list member and enqueues a send:email task for each.
func (h *Handler) launch(w http.ResponseWriter, r *http.Request) {
	ws, ok := auth.WorkspaceID(w, r)
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
	case errors.Is(err, ErrNoSteps):
		httpx.Error(w, http.StatusUnprocessableEntity, "campaign has no sequence steps")
	case err != nil:
		httpx.Error(w, http.StatusInternalServerError, "could not launch")
	default:
		// "queued" preserves the existing client contract; the split-out fields
		// let callers spot partial-enqueue outcomes without breaking the shape.
		httpx.JSON(w, http.StatusOK, map[string]int{
			"queued":               res.EnqueuedCount,
			"total_enrolled":       res.TotalEnrolled,
			"failed_enqueue_count": res.FailedEnqueueCount,
		})
	}
}
