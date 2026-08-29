package campaign

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/app/auth"
	"github.com/inroad/inroad/internal/app/events"
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

// serveCampaignChild handles a read-only campaign sub-resource: pull the
// workspace from the JWT, parse the campaign id, load through fn, and map the
// outcome to 200/400/404/500. The schedule and senders GETs differ only in what
// they load and the failure message, so they share this instead of repeating the
// mapping — where they would drift apart.
func serveCampaignChild[T any](
	w http.ResponseWriter, r *http.Request, failure string,
	load func(ctx context.Context, ws, campaignID uuid.UUID) (T, error),
) {
	ws, ok := auth.WorkspaceID(w, r)
	if !ok {
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "bad id")
		return
	}
	got, err := load(r.Context(), ws, id)
	switch {
	case errors.Is(err, ErrNotFound):
		httpx.Error(w, http.StatusNotFound, "not found")
	case err != nil:
		httpx.Error(w, http.StatusInternalServerError, failure)
	default:
		httpx.JSON(w, http.StatusOK, got)
	}
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

// listEnrollments handles GET /campaigns/{id}/enrollments — per-contact reply
// status for the campaign, paginated by ?limit (default 100, max 500) and
// ?offset (default 0, clamped in the service). Workspace comes from the JWT; a
// cross-tenant id 404s before any enrollment read.
func (h *Handler) listEnrollments(w http.ResponseWriter, r *http.Request) {
	ws, ok := auth.WorkspaceID(w, r)
	if !ok {
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "bad id")
		return
	}
	rows, err := h.svc.ListEnrollments(r.Context(), ws, id,
		queryInt32(r.URL.Query().Get("limit")), queryInt32(r.URL.Query().Get("offset")))
	switch {
	case errors.Is(err, ErrNotFound):
		httpx.Error(w, http.StatusNotFound, "not found")
	case err != nil:
		httpx.Error(w, http.StatusInternalServerError, "could not list enrollments")
	default:
		out := make([]enrollmentResponse, 0, len(rows))
		for _, row := range rows {
			out = append(out, toEnrollmentResponse(row))
		}
		httpx.JSON(w, http.StatusOK, out)
	}
}

// queryInt32 parses a query-string integer, returning 0 for missing/invalid
// input so the service applies its default/clamp (limit→100, offset→0).
func queryInt32(v string) int32 {
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0
	}
	return int32(n)
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

// serveLifecycleAction handles a campaign lifecycle action that takes no
// request body and returns 204 on success: pull the workspace from the JWT,
// parse {id}, run fn, and map its outcome to 204/404/409/500. Pause, Resume
// and DeleteDraft share this whole shape and differ only in which transition
// is illegal (conflictErr/conflictMsg) and the fallback failure message.
func serveLifecycleAction(
	w http.ResponseWriter, r *http.Request, failure string,
	conflictErr error, conflictMsg string,
	fn func(ctx context.Context, ws, id uuid.UUID) error,
) {
	ws, ok := auth.WorkspaceID(w, r)
	if !ok {
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "bad id")
		return
	}
	switch err := fn(r.Context(), ws, id); {
	case errors.Is(err, ErrNotFound):
		httpx.Error(w, http.StatusNotFound, "not found")
	case errors.Is(err, conflictErr):
		httpx.Error(w, http.StatusConflict, conflictMsg)
	case err != nil:
		httpx.Error(w, http.StatusInternalServerError, failure)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

// pause handles POST /campaigns/{id}/pause.
func (h *Handler) pause(w http.ResponseWriter, r *http.Request) {
	serveLifecycleAction(w, r, "could not pause campaign",
		ErrNotPausable, "campaign is not running and cannot be paused", h.svc.Pause)
}

// resume handles POST /campaigns/{id}/resume.
func (h *Handler) resume(w http.ResponseWriter, r *http.Request) {
	serveLifecycleAction(w, r, "could not resume campaign",
		ErrNotResumable, "campaign is not paused and cannot be resumed", h.svc.Resume)
}

type renameRequest struct {
	Name string `json:"name" validate:"required,min=1,max=200"`
}

// rename handles PUT /campaigns/{id}.
func (h *Handler) rename(w http.ResponseWriter, r *http.Request) {
	ws, ok := auth.WorkspaceID(w, r)
	if !ok {
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "bad id")
		return
	}
	var req renameRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid json")
		return
	}
	if err := validate.Struct(req); err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	c, err := h.svc.Rename(r.Context(), ws, id, req.Name)
	switch {
	case errors.Is(err, ErrValidation):
		httpx.Error(w, http.StatusBadRequest, "invalid name")
	case errors.Is(err, ErrNotFound):
		httpx.Error(w, http.StatusNotFound, "not found")
	case err != nil:
		httpx.Error(w, http.StatusInternalServerError, "could not rename campaign")
	default:
		httpx.JSON(w, http.StatusOK, toResponse(c, nil))
	}
}

// deleteDraft handles DELETE /campaigns/{id}.
func (h *Handler) deleteDraft(w http.ResponseWriter, r *http.Request) {
	serveLifecycleAction(w, r, "could not delete campaign",
		ErrNotDraft, "campaign is not a draft", h.svc.DeleteDraft)
}

// preflight handles GET /campaigns/{id}/preflight.
func (h *Handler) preflight(w http.ResponseWriter, r *http.Request) {
	serveCampaignChild(w, r, "could not compute preflight",
		func(ctx context.Context, ws, id uuid.UUID) (preflightResponse, error) {
			rep, err := h.svc.Preflight(ctx, ws, id)
			if err != nil {
				return preflightResponse{}, err
			}
			return toPreflightResponse(rep), nil
		})
}

type testSendRequest struct {
	StepID string `json:"step_id" validate:"required,uuid"`
	To     string `json:"to" validate:"required,email"`
}

type testSendResponse struct {
	Queued bool `json:"queued"`
}

// testSend handles POST /campaigns/{id}/test-send: validates the request
// (campaign/step ownership, rate limit, an eligible sender exists) and
// enqueues a testsend:send task. Rendering and the actual send happen in the
// execution plane (internal/worker/testsend) -- nothing here decrypts a
// credential or dials a provider (docs/security.md invariant 1).
func (h *Handler) testSend(w http.ResponseWriter, r *http.Request) {
	ws, ok := auth.WorkspaceID(w, r)
	if !ok {
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "bad id")
		return
	}
	var req testSendRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid json")
		return
	}
	if err := validate.Struct(req); err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	stepID, err := uuid.Parse(req.StepID)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "bad step_id")
		return
	}
	switch err := h.svc.TestSend(r.Context(), ws, id, stepID, req.To); {
	case errors.Is(err, ErrNotFound):
		httpx.Error(w, http.StatusNotFound, "not found")
	case errors.Is(err, ErrStepNotFound):
		httpx.Error(w, http.StatusNotFound, "step not found")
	case errors.Is(err, ErrRecipientSuppressed):
		httpx.Error(w, http.StatusUnprocessableEntity, "recipient_suppressed")
	case errors.Is(err, ErrNoEligibleSender):
		httpx.Error(w, http.StatusUnprocessableEntity, "no enabled sender with an active mailbox")
	case errors.Is(err, ErrTestSendRateLimited):
		httpx.Error(w, http.StatusTooManyRequests, "too many test sends; try again shortly")
	case err != nil:
		httpx.Error(w, http.StatusInternalServerError, "could not send test email")
	default:
		httpx.JSON(w, http.StatusAccepted, testSendResponse{Queued: true})
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
	// Tag the request with who launched it, so the launching operator's own tab
	// drops the echo instead of re-rendering over its optimistic update. A
	// missing principal yields no actor, which is safe: the event then reads as
	// system-originated and every tab treats it as somebody else's.
	ctx := r.Context()
	if p, ok := auth.UserFromContext(ctx); ok {
		ctx = events.WithActor(ctx, p.UserID)
	}
	res, err := h.svc.Launch(ctx, ws, id, h.enq)
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
