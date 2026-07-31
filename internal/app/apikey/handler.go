package apikey

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

// Handler exposes the workspace's api-key management surface (create/list/revoke).
// It is mounted session-authed: an api-key principal never reaches it, so a key
// cannot mint or revoke keys (no privilege escalation).
type Handler struct {
	svc *Service
}

// NewHandler builds a Handler over svc.
func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

type createRequest struct {
	Name            string     `json:"name" validate:"required"`
	Scopes          []string   `json:"scopes" validate:"required,min=1"`
	IPAllowlist     []string   `json:"ip_allowlist"`
	RateLimitPerMin *int       `json:"rate_limit_per_min"`
	ExpiresAt       *time.Time `json:"expires_at"`
}

// keyResponse is the non-secret view of a key. It carries no secret or hash field
// by construction — the source KeyView has none to render.
type keyResponse struct {
	ID              string   `json:"id"`
	Name            string   `json:"name"`
	Prefix          string   `json:"prefix"`
	Scopes          []string `json:"scopes"`
	IPAllowlist     []string `json:"ip_allowlist"`
	RateLimitPerMin *int32   `json:"rate_limit_per_min"`
	ExpiresAt       *string  `json:"expires_at"`
	RevokedAt       *string  `json:"revoked_at"`
	LastUsedAt      *string  `json:"last_used_at"`
	CreatedAt       string   `json:"created_at"`
}

// createResponse returns the full one-time token alongside the key metadata. The
// token is shown here exactly once and is never retrievable again.
type createResponse struct {
	Token  string      `json:"token"`
	APIKey keyResponse `json:"api_key"`
}

type listResponse struct {
	APIKeys []keyResponse `json:"api_keys"`
}

// create mints a new key for the caller's workspace and returns the full token
// once plus the key metadata.
func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	ws, ok := auth.WorkspaceID(w, r)
	if !ok {
		return
	}
	uid, ok := callerUserID(w, r)
	if !ok {
		return
	}
	var body createRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid json")
		return
	}
	if err := validate.Struct(body); err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	view, token, err := h.svc.Create(r.Context(), CreateInput{
		WorkspaceID:     ws,
		CreatedBy:       uid,
		Name:            body.Name,
		Scopes:          body.Scopes,
		IPAllowlist:     body.IPAllowlist,
		RateLimitPerMin: body.RateLimitPerMin,
		ExpiresAt:       body.ExpiresAt,
	})
	if err != nil {
		switch {
		case errors.Is(err, ErrInvalidScope), errors.Is(err, ErrInvalidIP),
			errors.Is(err, ErrInvalidExpiry), errors.Is(err, ErrInvalidRateLimit):
			httpx.Error(w, http.StatusBadRequest, err.Error())
		default:
			httpx.Error(w, http.StatusInternalServerError, "could not create api key")
		}
		return
	}
	httpx.JSON(w, http.StatusCreated, createResponse{Token: token, APIKey: toResponse(view)})
}

// list returns the caller's workspace keys, newest first, without any secret.
func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	ws, ok := auth.WorkspaceID(w, r)
	if !ok {
		return
	}
	views, err := h.svc.List(r.Context(), ws)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "could not list api keys")
		return
	}
	out := make([]keyResponse, 0, len(views))
	for _, v := range views {
		out = append(out, toResponse(v))
	}
	httpx.JSON(w, http.StatusOK, listResponse{APIKeys: out})
}

// revoke revokes a key owned by the caller's workspace. A foreign or unknown id
// returns 404 so a caller cannot probe or revoke another workspace's key.
func (h *Handler) revoke(w http.ResponseWriter, r *http.Request) {
	ws, ok := auth.WorkspaceID(w, r)
	if !ok {
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid id")
		return
	}
	if err := h.svc.Revoke(r.Context(), ws, id); err != nil {
		if errors.Is(err, ErrNotFound) {
			httpx.Error(w, http.StatusNotFound, "api key not found")
			return
		}
		httpx.Error(w, http.StatusInternalServerError, "could not revoke api key")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// callerUserID pulls and parses the authenticated caller's user id.
func callerUserID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	p, ok := auth.UserFromContext(r.Context())
	if !ok {
		httpx.Error(w, http.StatusUnauthorized, "unauthorized")
		return uuid.Nil, false
	}
	uid, err := uuid.Parse(p.UserID)
	if err != nil {
		httpx.Error(w, http.StatusUnauthorized, "invalid token")
		return uuid.Nil, false
	}
	return uid, true
}

func toResponse(v KeyView) keyResponse {
	return keyResponse{
		ID:              v.ID.String(),
		Name:            v.Name,
		Prefix:          v.Prefix,
		Scopes:          v.Scopes,
		IPAllowlist:     v.IPAllowlist,
		RateLimitPerMin: v.RateLimitPerMin,
		ExpiresAt:       rfc3339Ptr(v.ExpiresAt),
		RevokedAt:       rfc3339Ptr(v.RevokedAt),
		LastUsedAt:      rfc3339Ptr(v.LastUsedAt),
		CreatedAt:       v.CreatedAt.UTC().Format(time.RFC3339),
	}
}

// rfc3339Ptr renders an optional time as an RFC3339 (UTC) string pointer, nil for
// a nil time.
func rfc3339Ptr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := t.UTC().Format(time.RFC3339)
	return &s
}
