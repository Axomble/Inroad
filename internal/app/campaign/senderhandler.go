package campaign

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/app/auth"
	"github.com/inroad/inroad/internal/platform/httpx"
)

// senderPoolResponse is the wire shape of a campaign's sender pool: the rotation
// mode plus one row per pool member.
type senderPoolResponse struct {
	RotationMode string           `json:"rotation_mode"`
	Senders      []senderResponse `json:"senders"`
}

// senderResponse is one pool member. email/provider/status and the rotation state
// (assigned_count/last_assigned_at) are read-only; only weight and enabled are
// editable. last_assigned_at is RFC3339 (UTC) or null when the mailbox has never
// been assigned a contact.
type senderResponse struct {
	MailboxID      string  `json:"mailbox_id"`
	Email          string  `json:"email"`
	Provider       string  `json:"provider"`
	Status         string  `json:"status"`
	Weight         int     `json:"weight"`
	Enabled        bool    `json:"enabled"`
	AssignedCount  int64   `json:"assigned_count"`
	LastAssignedAt *string `json:"last_assigned_at"`
}

// senderPoolRequest is the full-replace payload: mailboxes not listed leave the
// pool. weight and enabled are optional per member (1 and true).
type senderPoolRequest struct {
	RotationMode string          `json:"rotation_mode"`
	Senders      []senderRequest `json:"senders"`
}

type senderRequest struct {
	MailboxID string `json:"mailbox_id"`
	Weight    *int   `json:"weight"`
	Enabled   *bool  `json:"enabled"`
}

// toSenderInputs parses the request into the service's input, applying the
// per-member defaults. A malformed mailbox id is a bad request, not a 422: it
// cannot identify any mailbox to reject.
func (r senderPoolRequest) toSenderInputs() ([]SenderInput, error) {
	out := make([]SenderInput, len(r.Senders))
	for i, sender := range r.Senders {
		id, err := uuid.Parse(sender.MailboxID)
		if err != nil {
			return nil, err
		}
		in := SenderInput{MailboxID: id, Weight: defaultSenderWeight, Enabled: true}
		if sender.Weight != nil {
			in.Weight = *sender.Weight
		}
		if sender.Enabled != nil {
			in.Enabled = *sender.Enabled
		}
		out[i] = in
	}
	return out, nil
}

func newSenderPoolResponse(p SenderPool) senderPoolResponse {
	senders := make([]senderResponse, 0, len(p.Senders))
	for _, s := range p.Senders {
		var lastAssignedAt *string
		if s.LastAssignedAt != nil {
			at := s.LastAssignedAt.UTC().Format(time.RFC3339)
			lastAssignedAt = &at
		}
		senders = append(senders, senderResponse{
			MailboxID: s.MailboxID.String(), Email: s.Email, Provider: s.Provider, Status: s.Status,
			Weight: s.Weight, Enabled: s.Enabled,
			AssignedCount: s.AssignedCount, LastAssignedAt: lastAssignedAt,
		})
	}
	return senderPoolResponse{RotationMode: p.RotationMode, Senders: senders}
}

// getSenders handles GET /campaigns/{id}/senders.
func (h *Handler) getSenders(w http.ResponseWriter, r *http.Request) {
	serveCampaignChild(w, r, "could not load senders",
		func(ctx context.Context, ws, id uuid.UUID) (senderPoolResponse, error) {
			pool, err := h.svc.GetSenders(ctx, ws, id)
			if err != nil {
				return senderPoolResponse{}, err
			}
			return newSenderPoolResponse(pool), nil
		})
}

// putSenders handles PUT /campaigns/{id}/senders — a full replace of the pool and
// rotation mode. Editable while running: it affects future contact assignments
// only, never a thread already in flight.
func (h *Handler) putSenders(w http.ResponseWriter, r *http.Request) {
	ws, ok := auth.WorkspaceID(w, r)
	if !ok {
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "bad id")
		return
	}
	var req senderPoolRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid json")
		return
	}
	senders, err := req.toSenderInputs()
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid mailbox id")
		return
	}
	pool, err := h.svc.SetSenders(r.Context(), ws, id, req.RotationMode, senders)
	switch {
	case errors.Is(err, ErrNotFound):
		httpx.Error(w, http.StatusNotFound, "not found")
	case errors.Is(err, ErrRotationMode):
		httpx.Error(w, http.StatusUnprocessableEntity, "unknown rotation mode")
	case errors.Is(err, ErrEmptySenderPool):
		httpx.Error(w, http.StatusUnprocessableEntity, "at least one sender is required")
	case errors.Is(err, ErrDuplicateSender):
		httpx.Error(w, http.StatusUnprocessableEntity, "a mailbox is listed more than once")
	case errors.Is(err, ErrSenderWeight):
		httpx.Error(w, http.StatusUnprocessableEntity, "weight must be between 1 and 100")
	case errors.Is(err, ErrMailboxNotActive):
		httpx.Error(w, http.StatusUnprocessableEntity, "mailbox not found or not active")
	case err != nil:
		httpx.Error(w, http.StatusInternalServerError, "could not save senders")
	default:
		httpx.JSON(w, http.StatusOK, newSenderPoolResponse(pool))
	}
}
