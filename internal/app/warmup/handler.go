package warmup

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/app/auth"
	"github.com/inroad/inroad/internal/platform/httpx"
)

// Handler exposes the warmup domain over HTTP. Authentication is applied by the
// protected router group (see cmd/inroad), not here; every workspace is taken
// from the JWT claims, never from the request body or path.
type Handler struct {
	svc *Service
}

func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

// warmupSettingsRequest is the wire shape for PUT /mailboxes/{id}/warmup. Every
// field is a pointer so an omitted key is distinguishable from a zero value: it
// maps 1:1 onto WarmupSettings, whose merge logic keeps the current-or-default
// value for a nil field.
type warmupSettingsRequest struct {
	StartVolume   *int32   `json:"start_volume"`
	MaxVolume     *int32   `json:"max_volume"`
	RampIncrement *int32   `json:"ramp_increment"`
	ReplyRate     *float32 `json:"reply_rate"`
}

// toSettings converts the wire request to the service settings type. The two
// structs share an identical field set (only JSON tags differ), so a direct
// struct conversion keeps the mapping honest with no field-by-field drift.
func (req warmupSettingsRequest) toSettings() WarmupSettings {
	return WarmupSettings(req)
}

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

// enable handles PUT /mailboxes/{id}/warmup — enable warmup or update ramp
// settings. The body is optional; an empty body enables with defaults.
func (h *Handler) enable(w http.ResponseWriter, r *http.Request) {
	wid, ok := auth.WorkspaceID(w, r)
	if !ok {
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid id")
		return
	}
	var req warmupSettingsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		httpx.Error(w, http.StatusBadRequest, "invalid json")
		return
	}
	p, err := h.svc.EnableWarmup(r.Context(), wid, id, req.toSettings())
	if err != nil {
		writeErr(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, p)
}

// disable handles DELETE /mailboxes/{id}/warmup — idempotent, always 204.
func (h *Handler) disable(w http.ResponseWriter, r *http.Request) {
	wid, ok := auth.WorkspaceID(w, r)
	if !ok {
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid id")
		return
	}
	if err := h.svc.DisableWarmup(r.Context(), wid, id); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// detail handles GET /mailboxes/{id}/warmup — participant + 30-day series.
func (h *Handler) detail(w http.ResponseWriter, r *http.Request) {
	wid, ok := auth.WorkspaceID(w, r)
	if !ok {
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid id")
		return
	}
	d, err := h.svc.GetWarmupDetail(r.Context(), wid, id)
	if err != nil {
		writeErr(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, d)
}

// transitions handles GET /warmup/mailboxes/{mailbox_id}/transitions — the
// append-only decision record for one mailbox, newest first.
func (h *Handler) transitions(w http.ResponseWriter, r *http.Request) {
	wid, ok := auth.WorkspaceID(w, r)
	if !ok {
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "mailbox_id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid mailbox id")
		return
	}
	limit, ok := queryLimit(w, r)
	if !ok {
		return
	}
	page, err := h.svc.ListTransitions(r.Context(), wid, id, limit)
	if err != nil {
		writeErr(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, page)
}

// queryLimit reads the optional ?limit. An absent value is 0, which the service
// resolves to the contract's default; a value that is not a positive integer is
// a caller error rather than something to silently reinterpret. The service
// clamps the upper bound, so the cap has one home.
func queryLimit(w http.ResponseWriter, r *http.Request) (int32, bool) {
	raw := r.URL.Query().Get("limit")
	if raw == "" {
		return 0, true
	}
	n, err := strconv.ParseInt(raw, 10, 32)
	if err != nil || n < 1 {
		httpx.Error(w, http.StatusBadRequest, "limit must be a positive integer")
		return 0, false
	}
	return int32(n), true
}

// overview handles GET /warmup/overview — workspace pool summary.
func (h *Handler) overview(w http.ResponseWriter, r *http.Request) {
	wid, ok := auth.WorkspaceID(w, r)
	if !ok {
		return
	}
	ov, err := h.svc.GetOverview(r.Context(), wid)
	if err != nil {
		writeErr(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, ov)
}
