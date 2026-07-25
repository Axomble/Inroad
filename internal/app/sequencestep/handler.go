package sequencestep

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/app/auth"
	"github.com/inroad/inroad/internal/platform/db/gen"
	"github.com/inroad/inroad/internal/platform/httpx"
	"github.com/inroad/inroad/internal/platform/validate"
)

// Handler exposes the step endpoints. It is mounted by the campaign handler so
// the {id} path param resolves to the campaign (see Routes wiring in cmd).
type Handler struct {
	svc       *Service
	jwtSecret []byte
}

func NewHandler(svc *Service, jwtSecret []byte) *Handler {
	return &Handler{svc: svc, jwtSecret: jwtSecret}
}

// stepRequest is the create/update body. Subject may be empty (a later step
// can reply in-thread with a "Re:" subject synthesized at send time); a step
// must carry a text or HTML body.
type stepRequest struct {
	DelaySeconds int32  `json:"delay_seconds" validate:"gte=0,lte=31536000"`
	Subject      string `json:"subject" validate:"max=500"`
	BodyText     string `json:"body_text"`
	BodyHTML     string `json:"body_html"`
}

func (r stepRequest) hasBody() bool { return r.BodyText != "" || r.BodyHTML != "" }

// Create handles POST /campaigns/{id}/steps.
func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	ws, ok := auth.WorkspaceID(w, r)
	if !ok {
		return
	}
	campaignID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "bad campaign id")
		return
	}
	req, ok := h.decode(w, r)
	if !ok {
		return
	}
	st, err := h.svc.Create(r.Context(), ws, campaignID, CreateInput{
		DelaySeconds: req.DelaySeconds, Subject: req.Subject, BodyText: req.BodyText, BodyHTML: req.BodyHTML,
	})
	h.writeStep(w, st, err)
}

// Update handles PUT /campaigns/{id}/steps/{stepId}.
func (h *Handler) Update(w http.ResponseWriter, r *http.Request) {
	ws, ok := auth.WorkspaceID(w, r)
	if !ok {
		return
	}
	campaignID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "bad campaign id")
		return
	}
	stepID, err := uuid.Parse(chi.URLParam(r, "stepId"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "bad step id")
		return
	}
	req, ok := h.decode(w, r)
	if !ok {
		return
	}
	st, err := h.svc.Update(r.Context(), ws, campaignID, UpdateInput{
		StepID: stepID, DelaySeconds: req.DelaySeconds, Subject: req.Subject, BodyText: req.BodyText, BodyHTML: req.BodyHTML,
	})
	h.writeStep(w, st, err)
}

// List handles GET /campaigns/{id}/steps.
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	ws, ok := auth.WorkspaceID(w, r)
	if !ok {
		return
	}
	campaignID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "bad campaign id")
		return
	}
	steps, err := h.svc.List(r.Context(), ws, campaignID)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "could not list steps")
		return
	}
	out := make([]stepResponse, 0, len(steps))
	for _, st := range steps {
		out = append(out, toResponse(st))
	}
	httpx.JSON(w, http.StatusOK, out)
}

// Delete handles DELETE /campaigns/{id}/steps/{stepId}.
func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	ws, ok := auth.WorkspaceID(w, r)
	if !ok {
		return
	}
	campaignID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "bad campaign id")
		return
	}
	stepID, err := uuid.Parse(chi.URLParam(r, "stepId"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "bad step id")
		return
	}
	err = h.svc.Delete(r.Context(), ws, campaignID, stepID)
	switch {
	case errors.Is(err, ErrCampaignNotFound):
		httpx.Error(w, http.StatusNotFound, "campaign not found")
	case errors.Is(err, ErrCampaignNotDraft):
		httpx.Error(w, http.StatusConflict, "steps can only be removed while the campaign is draft")
	case errors.Is(err, ErrNotFound):
		httpx.Error(w, http.StatusNotFound, "step not found")
	case err != nil:
		httpx.Error(w, http.StatusInternalServerError, "could not delete step")
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

// reorderRequest is the reorder body: the FULL ordered list of the campaign's
// step ids in the desired order. uuid.UUID unmarshals from the JSON string, so
// a malformed id fails decode → 400.
type reorderRequest struct {
	StepIDs []uuid.UUID `json:"step_ids"`
}

// Reorder handles POST /campaigns/{id}/steps/reorder.
func (h *Handler) Reorder(w http.ResponseWriter, r *http.Request) {
	ws, ok := auth.WorkspaceID(w, r)
	if !ok {
		return
	}
	campaignID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "bad campaign id")
		return
	}
	var req reorderRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid json")
		return
	}
	if len(req.StepIDs) == 0 {
		httpx.Error(w, http.StatusBadRequest, "step_ids required")
		return
	}
	steps, err := h.svc.Reorder(r.Context(), ws, campaignID, req.StepIDs)
	switch {
	case errors.Is(err, ErrCampaignNotFound):
		httpx.Error(w, http.StatusNotFound, "campaign not found")
	case errors.Is(err, ErrCampaignNotDraft):
		httpx.Error(w, http.StatusConflict, "steps can only be reordered while the campaign is draft")
	case errors.Is(err, ErrNotFound):
		httpx.Error(w, http.StatusNotFound, "step not found")
	case errors.Is(err, ErrInvalidOrder):
		httpx.Error(w, http.StatusBadRequest, err.Error())
	case err != nil:
		httpx.Error(w, http.StatusInternalServerError, "could not reorder steps")
	default:
		out := make([]stepResponse, 0, len(steps))
		for _, st := range steps {
			out = append(out, toResponse(st))
		}
		httpx.JSON(w, http.StatusOK, out)
	}
}

func (h *Handler) decode(w http.ResponseWriter, r *http.Request) (stepRequest, bool) {
	var req stepRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid json")
		return req, false
	}
	if err := validate.Struct(req); err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return req, false
	}
	if !req.hasBody() {
		httpx.Error(w, http.StatusBadRequest, "body_text or body_html required")
		return req, false
	}
	return req, true
}

func (h *Handler) writeStep(w http.ResponseWriter, st gen.SequenceStep, err error) {
	switch {
	case errors.Is(err, ErrCampaignNotFound):
		httpx.Error(w, http.StatusNotFound, "campaign not found")
	case errors.Is(err, ErrCampaignNotDraft):
		httpx.Error(w, http.StatusConflict, "steps can only be added while the campaign is draft")
	case errors.Is(err, ErrNotFound):
		httpx.Error(w, http.StatusNotFound, "step not found")
	case err != nil:
		httpx.Error(w, http.StatusInternalServerError, "could not save step")
	default:
		httpx.JSON(w, http.StatusOK, toResponse(st))
	}
}
