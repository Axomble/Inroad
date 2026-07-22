package identity

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/app/auth"
	"github.com/inroad/inroad/internal/platform/httpx"
	"github.com/inroad/inroad/internal/platform/validate"
)

type inviteDTO struct {
	ID        string `json:"id"`
	Email     string `json:"email"`
	Role      string `json:"role"`
	Status    string `json:"status"`
	ExpiresAt string `json:"expires_at"`
	CreatedAt string `json:"created_at"`
}

func toInviteDTO(i Invite) inviteDTO {
	return inviteDTO{
		ID: i.ID.String(), Email: i.Email, Role: i.Role, Status: i.Status,
		ExpiresAt: i.ExpiresAt.Format(time.RFC3339), CreatedAt: i.CreatedAt.Format(time.RFC3339),
	}
}

// pathWorkspaceID resolves the workspace the caller is acting on. It reads
// the active workspace from the JWT (auth.WorkspaceID) - never from the path
// - and only checks the {id} path segment for a match, so the value ever used
// to scope a query is always the token's, per the tenant-isolation invariant.
// A mismatch (an admin in workspace A trying to hit workspace B's invites via
// the URL) is rejected as 403, since RequireRole alone only ranks the
// caller's role in their own active workspace - it says nothing about a
// different workspace named in the path.
func pathWorkspaceID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	wsID, ok := auth.WorkspaceID(w, r)
	if !ok {
		return uuid.Nil, false
	}
	pathID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil || pathID != wsID {
		httpx.Error(w, http.StatusForbidden, "workspace mismatch")
		return uuid.Nil, false
	}
	return wsID, true
}

// createInvite invites email to join the caller's workspace at role.
// RequireRole(admin) gates this route (see InviteRoutes).
func (h *Handler) createInvite(w http.ResponseWriter, r *http.Request) {
	wsID, ok := pathWorkspaceID(w, r)
	if !ok {
		return
	}
	claims, _ := auth.UserFromContext(r.Context())
	invitedBy, err := uuid.Parse(claims.UserID)
	if err != nil {
		httpx.Error(w, http.StatusUnauthorized, "invalid token")
		return
	}
	var body struct {
		Email string `json:"email" validate:"required,email"`
		Role  string `json:"role" validate:"required,oneof=admin member"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid json")
		return
	}
	if err := validate.Struct(body); err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	inv, err := h.svc.CreateInvite(r.Context(), wsID, invitedBy, body.Email, body.Role)
	if err != nil {
		if errors.Is(err, ErrInviteExists) {
			httpx.Error(w, http.StatusConflict, "an invite is already pending for this email")
			return
		}
		httpx.Error(w, http.StatusInternalServerError, "could not create invite")
		return
	}
	httpx.JSON(w, http.StatusCreated, toInviteDTO(inv))
}

// listInvites returns every pending invite for the caller's workspace.
// RequireRole(admin) gates this route (see InviteRoutes).
func (h *Handler) listInvites(w http.ResponseWriter, r *http.Request) {
	wsID, ok := pathWorkspaceID(w, r)
	if !ok {
		return
	}
	invites, err := h.svc.ListInvites(r.Context(), wsID)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "could not load invites")
		return
	}
	dtos := make([]inviteDTO, len(invites))
	for i, inv := range invites {
		dtos[i] = toInviteDTO(inv)
	}
	httpx.JSON(w, http.StatusOK, dtos)
}

// revokeInvite revokes a pending invite belonging to the caller's workspace.
// RequireRole(admin) gates this route (see InviteRoutes).
func (h *Handler) revokeInvite(w http.ResponseWriter, r *http.Request) {
	wsID, ok := pathWorkspaceID(w, r)
	if !ok {
		return
	}
	inviteID, err := uuid.Parse(chi.URLParam(r, "inviteId"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid invite id")
		return
	}
	if err := h.svc.RevokeInvite(r.Context(), wsID, inviteID); err != nil {
		httpx.Error(w, http.StatusInternalServerError, "could not revoke invite")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// acceptInvite consumes an invite token, resolving-or-creating the invited
// user and issuing a session. Public: like verifyEmail/resetPassword, the
// out-of-band token is itself the credential, so no bearer auth or CSRF
// applies here (see routes.go).
func (h *Handler) acceptInvite(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Token    string  `json:"token" validate:"required"`
		Password *string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid json")
		return
	}
	if err := validate.Struct(body); err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	if body.Password != nil && len(*body.Password) < 8 {
		httpx.Error(w, http.StatusBadRequest, "password must be 8+ characters")
		return
	}
	ua, ip := h.clientMeta(r)
	sess, err := h.svc.AcceptInvite(r.Context(), body.Token, body.Password, ua, ip)
	if err != nil {
		switch {
		case errors.Is(err, ErrTokenInvalid):
			httpx.Error(w, http.StatusNotFound, "invalid or expired invite")
		case errors.Is(err, ErrPasswordRequired):
			httpx.Error(w, http.StatusUnprocessableEntity, "password required to create an account")
		default:
			httpx.Error(w, http.StatusInternalServerError, "could not accept invite")
		}
		return
	}
	h.issueSession(w, sess)
}
