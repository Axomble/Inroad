package inbox

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/app/auth"
	"github.com/inroad/inroad/internal/platform/httpx"
)

// labelResponse is the wire shape of one Label.
type labelResponse struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Color     string `json:"color"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

func toLabelResponse(l Label) labelResponse {
	return labelResponse{
		ID:        l.ID.String(),
		Name:      l.Name,
		Color:     l.Color,
		CreatedAt: l.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt: l.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

// toLabelResponses builds the wire slice, always non-nil so an unlabelled
// thread marshals `[]` rather than `null` and a client can map unconditionally.
func toLabelResponses(labels []Label) []labelResponse {
	out := make([]labelResponse, 0, len(labels))
	for _, l := range labels {
		out = append(out, toLabelResponse(l))
	}
	return out
}

// labelListResponse is GET /inbox/labels. An object rather than a bare array,
// matching this API's other list responses, so counts can be added later
// without a breaking change.
type labelListResponse struct {
	Labels []labelResponse `json:"labels"`
}

// upsertLabelRequest is the body for creating or updating a label.
type upsertLabelRequest struct {
	Name string `json:"name"`
	// Color is optional on create (the server applies its default) and
	// required-in-practice on update, where an empty value would otherwise
	// silently reset it. Both paths run the same validation.
	Color string `json:"color"`
}

// decodeJSON reads a small JSON body strictly — unknown fields rejected, so a
// typo'd key is a 400 rather than a silently ignored one. Shared by the label
// handlers; the 64KiB cap is far above any legitimate label payload.
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid JSON body")
		return false
	}
	return true
}

// listLabels handles GET /inbox/labels.
func (h *Handler) listLabels(w http.ResponseWriter, r *http.Request) {
	wid, ok := auth.WorkspaceID(w, r)
	if !ok {
		return
	}
	labels, err := h.svc.ListLabels(r.Context(), wid)
	if err != nil {
		writeErr(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, labelListResponse{Labels: toLabelResponses(labels)})
}

// createLabel handles POST /inbox/labels.
//
// Search-or-create, not strict create: a name that already exists resolves to
// the EXISTING label with 200 rather than 409. That is what the picker needs —
// a member typing a name they have used before means "file it under that", not
// "fail". A caller wanting strict creation can list first and compare.
func (h *Handler) createLabel(w http.ResponseWriter, r *http.Request) {
	wid, ok := auth.WorkspaceID(w, r)
	if !ok {
		return
	}
	var req upsertLabelRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	label, err := h.svc.EnsureLabel(r.Context(), wid, req.Name, req.Color)
	if err != nil {
		writeErr(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toLabelResponse(label))
}

// updateLabel handles PUT /inbox/labels/{labelId}.
func (h *Handler) updateLabel(w http.ResponseWriter, r *http.Request) {
	wid, ok := auth.WorkspaceID(w, r)
	if !ok {
		return
	}
	labelID, ok := parseLabelID(w, r)
	if !ok {
		return
	}
	var req upsertLabelRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	label, err := h.svc.UpdateLabel(r.Context(), wid, labelID, req.Name, req.Color)
	if err != nil {
		writeErr(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toLabelResponse(label))
}

// deleteLabel handles DELETE /inbox/labels/{labelId}. Every thread carrying it
// is unfiled by the join's ON DELETE CASCADE — that is what deleting a label
// means, and the threads themselves are untouched.
func (h *Handler) deleteLabel(w http.ResponseWriter, r *http.Request) {
	wid, ok := auth.WorkspaceID(w, r)
	if !ok {
		return
	}
	labelID, ok := parseLabelID(w, r)
	if !ok {
		return
	}
	if err := h.svc.DeleteLabel(r.Context(), wid, labelID); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// assignLabel handles PUT /inbox/threads/{id}/labels/{labelId}.
//
// PUT and idempotent: applying an already-applied label is a 204, not a
// conflict. The composite primary key makes that true in the database rather
// than by a check here.
func (h *Handler) assignLabel(w http.ResponseWriter, r *http.Request) {
	h.mutateThreadLabel(w, r, h.svc.AssignLabel)
}

// unassignLabel handles DELETE /inbox/threads/{id}/labels/{labelId}. 404 when
// the label was not on the thread.
func (h *Handler) unassignLabel(w http.ResponseWriter, r *http.Request) {
	h.mutateThreadLabel(w, r, h.svc.UnassignLabel)
}

// mutateThreadLabel is the shared shape of assign/unassign: parse both ids,
// call, 204. Factored because the two differ ONLY in the service method, and
// duplicating the id-parsing would let their error handling drift.
func (h *Handler) mutateThreadLabel(
	w http.ResponseWriter,
	r *http.Request,
	action func(ctx context.Context, workspaceID, threadID, labelID uuid.UUID) error,
) {
	wid, ok := auth.WorkspaceID(w, r)
	if !ok {
		return
	}
	threadID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid id")
		return
	}
	labelID, ok := parseLabelID(w, r)
	if !ok {
		return
	}
	if err := action(r.Context(), wid, threadID, labelID); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// parseLabelID reads the {labelId} path param, writing a 400 and reporting
// false when it is not a UUID.
func parseLabelID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, "labelId"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid label id")
		return uuid.Nil, false
	}
	return id, true
}
