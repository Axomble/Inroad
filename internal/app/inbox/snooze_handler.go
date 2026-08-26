package inbox

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/app/auth"
	"github.com/inroad/inroad/internal/platform/httpx"
)

// snoozeRequest is the wire shape for PUT /inbox/threads/{id}/snooze.
//
// An RFC3339 timestamp rather than a duration or a preset name: the presets
// ("tomorrow morning", "next week") are a UI affordance whose meaning depends
// on the viewer's timezone and working hours, and resolving them server-side
// would need all of that context sent anyway. The client resolves its own
// preset to an instant; the server's job is to bound and store it.
type snoozeRequest struct {
	SnoozeUntil string `json:"snooze_until"`
}

// snoozeResponse is one thread's snooze state.
type snoozeResponse struct {
	ThreadID    string  `json:"thread_id"`
	SnoozeUntil string  `json:"snooze_until"`
	SnoozedBy   *string `json:"snoozed_by"`
	CreatedAt   string  `json:"created_at"`
}

func toSnoozeResponse(s Snooze) snoozeResponse {
	return snoozeResponse{
		ThreadID:    s.ThreadID.String(),
		SnoozeUntil: s.SnoozeUntil.UTC().Format(time.RFC3339),
		SnoozedBy:   uuidString(s.SnoozedBy),
		CreatedAt:   s.CreatedAt.UTC().Format(time.RFC3339),
	}
}

// snooze handles PUT /inbox/threads/{id}/snooze.
//
// PUT rather than POST because it is idempotent in the way that matters: the
// same request twice leaves the same single snooze, and re-snoozing replaces
// the moment rather than stacking. 200 with the stored snooze, so a client can
// render the exact instant the server accepted instead of assuming its own
// value round-tripped unchanged.
func (h *Handler) snooze(w http.ResponseWriter, r *http.Request) {
	wid, ok := auth.WorkspaceID(w, r)
	if !ok {
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid id")
		return
	}
	var req snoozeRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	until, err := time.Parse(time.RFC3339, req.SnoozeUntil)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "snooze_until must be an RFC3339 timestamp")
		return
	}

	in := UpsertSnoozeInput{
		WorkspaceID: wid,
		ThreadID:    id,
		SnoozeUntil: until,
		SnoozedBy:   callerUserID(r),
	}

	snooze, err := h.svc.Snooze(r.Context(), in)
	if err != nil {
		writeErr(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toSnoozeResponse(snooze))
}

// callerUserID is the authenticated caller's user id, or nil when there isn't
// one to record.
//
// Principal.UserID is a string, and it is legitimately absent or unparseable
// for a machine (api-key) principal, which has no human behind it. That is not
// an error: `snoozed_by` exists only to attribute the action in the UI, so an
// unattributable snooze is still a perfectly valid snooze. Never used for
// authorization — the workspace comes from auth.WorkspaceID.
func callerUserID(r *http.Request) *uuid.UUID {
	p, ok := auth.UserFromContext(r.Context())
	if !ok {
		return nil
	}
	id, err := uuid.Parse(p.UserID)
	if err != nil {
		return nil
	}
	return &id
}

// unsnooze handles DELETE /inbox/threads/{id}/snooze. 204: the caller knows
// the thread is back, and there is no remaining state to return.
func (h *Handler) unsnooze(w http.ResponseWriter, r *http.Request) {
	wid, ok := auth.WorkspaceID(w, r)
	if !ok {
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid id")
		return
	}
	if err := h.svc.Unsnooze(r.Context(), wid, id); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
