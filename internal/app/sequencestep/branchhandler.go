package sequencestep

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/app/auth"
	"github.com/inroad/inroad/internal/platform/db/gen"
	"github.com/inroad/inroad/internal/platform/httpx"
	"github.com/inroad/inroad/internal/platform/seqgraph"
)

// branchRequest is the PUT body (StepBranchRequest in api/openapi.yaml). A null
// or absent exit ends the path.
type branchRequest struct {
	Condition     string     `json:"condition"`
	WithinDays    *int32     `json:"within_days"`
	ReplyLabelKey *string    `json:"reply_label_key"`
	YesStepID     *uuid.UUID `json:"yes_step_id"`
	NoStepID      *uuid.UUID `json:"no_step_id"`
}

// branchResponse is the wire shape of one router (StepBranch).
type branchResponse struct {
	StepID        string  `json:"step_id"`
	Condition     string  `json:"condition"`
	WithinDays    *int32  `json:"within_days"`
	ReplyLabelKey *string `json:"reply_label_key"`
	YesStepID     *string `json:"yes_step_id"`
	NoStepID      *string `json:"no_step_id"`
	UpdatedAt     string  `json:"updated_at"`
}

// graphNodeResponse is one step as a graph node (CampaignGraphNode).
type graphNodeResponse struct {
	StepID    string `json:"step_id"`
	StepOrder int32  `json:"step_order"`
	// DefaultNextStepID is where the step goes when it has NO branch: the next
	// step by step_order, or null for the last step. Served rather than left for
	// the client to derive so the canvas draws the same fall-through edge the
	// send path follows.
	DefaultNextStepID *string         `json:"default_next_step_id"`
	Branch            *branchResponse `json:"branch"`
}

// graphResponse is GET /campaigns/{id}/graph (CampaignGraph).
type graphResponse struct {
	CampaignID  string              `json:"campaign_id"`
	EntryStepID *string             `json:"entry_step_id"`
	Nodes       []graphNodeResponse `json:"nodes"`
}

// graphErrorResponse is every graph validation failure (BranchValidationError):
// the usual human message plus a stable code, and — for a loop — the steps on
// it, so the canvas can highlight the offending edges.
type graphErrorResponse struct {
	Error   string   `json:"error"`
	Code    string   `json:"code"`
	StepIDs []string `json:"step_ids,omitempty"`
}

// Graph handles GET /campaigns/{id}/graph.
func (h *Handler) Graph(w http.ResponseWriter, r *http.Request) {
	ws, ok := auth.WorkspaceID(w, r)
	if !ok {
		return
	}
	campaignID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "bad campaign id")
		return
	}
	g, err := h.svc.Graph(r.Context(), ws, campaignID)
	if err != nil {
		writeGraphError(w, err, "could not load the sequence graph")
		return
	}
	httpx.JSON(w, http.StatusOK, toGraphResponse(campaignID, g))
}

// SetBranch handles PUT /campaigns/{id}/steps/{stepId}/branch.
func (h *Handler) SetBranch(w http.ResponseWriter, r *http.Request) {
	ws, campaignID, stepID, ok := campaignAndStepIDs(w, r)
	if !ok {
		return
	}
	var req branchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid json")
		return
	}
	// An empty label is "no label", not a label whose key is "".
	if req.ReplyLabelKey != nil && *req.ReplyLabelKey == "" {
		req.ReplyLabelKey = nil
	}
	b, err := h.svc.SetBranch(r.Context(), ws, campaignID, BranchInput{
		StepID: stepID, Condition: req.Condition, WithinDays: req.WithinDays,
		ReplyLabelKey: req.ReplyLabelKey, YesStepID: req.YesStepID, NoStepID: req.NoStepID,
	})
	if err != nil {
		writeGraphError(w, err, "could not save branch")
		return
	}
	httpx.JSON(w, http.StatusOK, toBranchResponse(b))
}

// DeleteBranch handles DELETE /campaigns/{id}/steps/{stepId}/branch.
func (h *Handler) DeleteBranch(w http.ResponseWriter, r *http.Request) {
	ws, campaignID, stepID, ok := campaignAndStepIDs(w, r)
	if !ok {
		return
	}
	if err := h.svc.DeleteBranch(r.Context(), ws, campaignID, stepID); err != nil {
		writeGraphError(w, err, "could not remove branch")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// campaignAndStepIDs reads the pinned workspace and the two path ids, writing
// the error response itself when any is missing or malformed.
func campaignAndStepIDs(w http.ResponseWriter, r *http.Request) (ws, campaignID, stepID uuid.UUID, ok bool) {
	ws, ok = auth.WorkspaceID(w, r)
	if !ok {
		return uuid.Nil, uuid.Nil, uuid.Nil, false
	}
	campaignID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "bad campaign id")
		return uuid.Nil, uuid.Nil, uuid.Nil, false
	}
	stepID, err = uuid.Parse(chi.URLParam(r, "stepId"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "bad step id")
		return uuid.Nil, uuid.Nil, uuid.Nil, false
	}
	return ws, campaignID, stepID, true
}

// writeGraphError maps every error a graph read or write can return.
//
// A malformed branch (seqgraph shape codes) is 400, like any other invalid
// body. A well-formed edit the GRAPH refuses — an exit to a step outside the
// campaign, or a loop — is 422: the request was understood and is the wrong
// thing to commit. Both carry `code`.
func writeGraphError(w http.ResponseWriter, err error, fallback string) {
	var cycle *seqgraph.CycleError
	switch code := seqgraph.CodeOf(err); {
	case errors.Is(err, ErrCampaignNotFound):
		httpx.Error(w, http.StatusNotFound, "campaign not found")
	case errors.Is(err, ErrNotFound), errors.Is(err, ErrBranchConflict):
		httpx.Error(w, http.StatusNotFound, "step not found")
	case errors.Is(err, ErrCampaignNotDraft):
		httpx.Error(w, http.StatusConflict, "steps can only be changed structurally while the campaign is draft")
	case errors.Is(err, ErrTargetGone):
		httpx.JSON(w, http.StatusUnprocessableEntity, graphErrorResponse{Error: err.Error(), Code: seqgraph.CodeUnknownTarget})
	case errors.As(err, &cycle):
		httpx.JSON(w, http.StatusUnprocessableEntity, graphErrorResponse{
			Error: err.Error(), Code: code, StepIDs: uuidStrings(cycle.StepIDs),
		})
	case code == seqgraph.CodeUnknownTarget || code == seqgraph.CodeUnknownStep:
		httpx.JSON(w, http.StatusUnprocessableEntity, graphErrorResponse{Error: err.Error(), Code: code})
	case code != "":
		httpx.JSON(w, http.StatusBadRequest, graphErrorResponse{Error: err.Error(), Code: code})
	default:
		httpx.Error(w, http.StatusInternalServerError, fallback)
	}
}

func toGraphResponse(campaignID uuid.UUID, g Graph) graphResponse {
	model := BuildGraph(g.Steps, g.Branches)
	byStep := make(map[uuid.UUID]gen.SequenceStepBranch, len(g.Branches))
	for _, b := range g.Branches {
		byStep[b.StepID] = b
	}
	out := graphResponse{CampaignID: campaignID.String(), Nodes: make([]graphNodeResponse, 0, len(g.Steps))}
	if entry, ok := model.Entry(); ok {
		out.EntryStepID = uuidString(entry.ID)
	}
	for _, st := range g.Steps {
		node := graphNodeResponse{StepID: st.ID.String(), StepOrder: st.StepOrder}
		if next, ok := model.DefaultNext(st.ID); ok {
			node.DefaultNextStepID = uuidString(next.ID)
		}
		if b, ok := byStep[st.ID]; ok {
			resp := toBranchResponse(b)
			node.Branch = &resp
		}
		out.Nodes = append(out.Nodes, node)
	}
	return out
}

func toBranchResponse(b gen.SequenceStepBranch) branchResponse {
	out := branchResponse{
		StepID: b.StepID.String(), Condition: b.Condition,
		WithinDays: b.WithinDays, ReplyLabelKey: b.ReplyLabelKey,
		UpdatedAt: b.UpdatedAt.Time.UTC().Format(time.RFC3339),
	}
	if b.YesStepID.Valid {
		out.YesStepID = uuidString(b.YesStepID.Bytes)
	}
	if b.NoStepID.Valid {
		out.NoStepID = uuidString(b.NoStepID.Bytes)
	}
	return out
}

func uuidString(id uuid.UUID) *string {
	s := id.String()
	return &s
}

func uuidStrings(ids []uuid.UUID) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = id.String()
	}
	return out
}
