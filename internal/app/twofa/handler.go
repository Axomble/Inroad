package twofa

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/app/auth"
	"github.com/inroad/inroad/internal/platform/httpx"
	"github.com/inroad/inroad/internal/platform/validate"
)

// LoginCompleter issues a first-party session for an already-authenticated user
// and writes the standard login response (access token + refresh/CSRF cookies),
// exactly as a password login would. Implemented by the identity handler; kept as
// a narrow seam here so the twofa domain never imports identity (dependency
// inversion — the composition root wires the concrete implementation).
type LoginCompleter interface {
	CompleteLogin(w http.ResponseWriter, r *http.Request, userID uuid.UUID)
}

// SessionRevoker revokes a user's sessions other than the one they're acting from
// — the security-downgrade guard when 2FA is disabled. Implemented by
// identity.Service.
type SessionRevoker interface {
	RevokeOtherSessions(ctx context.Context, userID, keepSID uuid.UUID) ([]uuid.UUID, error)
}

// CacheBuster invalidates a revoked session's cached auth-state so it dies on the
// next request rather than after the verifier cache TTL. Satisfied by the
// identity SessionVerifier.
type CacheBuster interface {
	Bust(sid uuid.UUID)
}

// Handler exposes the twofa surface over HTTP: enroll/confirm/disable/status
// (authed) and the login-gate verify (post-password, pre-session).
type Handler struct {
	svc       *Service
	completer LoginCompleter
	sessions  SessionRevoker
	buster    CacheBuster
}

// NewHandler builds a Handler. completer issues the session on a successful 2FA
// verify; sessions + buster revoke and evict a user's OTHER sessions when they
// disable 2FA (any may be nil in unit tests that don't exercise those paths).
func NewHandler(svc *Service, completer LoginCompleter, sessions SessionRevoker, buster CacheBuster) *Handler {
	return &Handler{svc: svc, completer: completer, sessions: sessions, buster: buster}
}

type enrollResponse struct {
	Secret     string `json:"secret"`
	OtpauthURI string `json:"otpauth_uri"`
}

type confirmResponse struct {
	RecoveryCodes []string `json:"recovery_codes"`
}

type statusResponse struct {
	TOTPEnabled            bool `json:"totp_enabled"`
	RecoveryCodesRemaining int  `json:"recovery_codes_remaining"`
}

// userID pulls and parses the authenticated caller's user id from the principal.
func userID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
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

// enroll starts TOTP enrollment: returns the base32 secret + provisioning URI.
// Recovery codes are NOT returned here — only after confirm.
func (h *Handler) enroll(w http.ResponseWriter, r *http.Request) {
	uid, ok := userID(w, r)
	if !ok {
		return
	}
	res, err := h.svc.Enroll(r.Context(), uid)
	if err != nil {
		if errors.Is(err, ErrAlreadyEnrolled) {
			httpx.Error(w, http.StatusConflict, "two-factor is already enabled")
			return
		}
		httpx.Error(w, http.StatusInternalServerError, "could not start enrollment")
		return
	}
	httpx.JSON(w, http.StatusOK, enrollResponse{Secret: res.Secret, OtpauthURI: res.URI})
}

// confirm activates the pending factor against a presented code and returns the
// one-time recovery codes.
func (h *Handler) confirm(w http.ResponseWriter, r *http.Request) {
	uid, ok := userID(w, r)
	if !ok {
		return
	}
	var body struct {
		Code string `json:"code" validate:"required"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid json")
		return
	}
	if err := validate.Struct(body); err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	codes, err := h.svc.Confirm(r.Context(), uid, body.Code)
	if err != nil {
		switch {
		case errors.Is(err, ErrNotEnrolled):
			httpx.Error(w, http.StatusBadRequest, "enrollment not started")
		case errors.Is(err, ErrAlreadyEnrolled):
			httpx.Error(w, http.StatusConflict, "two-factor is already enabled")
		case errors.Is(err, ErrBadCode):
			httpx.Error(w, http.StatusBadRequest, "invalid code")
		default:
			httpx.Error(w, http.StatusInternalServerError, "could not confirm")
		}
		return
	}
	httpx.JSON(w, http.StatusOK, confirmResponse{RecoveryCodes: codes})
}

// disable turns off 2FA after proof of possession, then revokes the caller's
// OTHER sessions (disabling a second factor is a security downgrade) and busts
// their cached auth-state.
func (h *Handler) disable(w http.ResponseWriter, r *http.Request) {
	p, okP := auth.UserFromContext(r.Context())
	if !okP {
		httpx.Error(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	uid, err := uuid.Parse(p.UserID)
	if err != nil {
		httpx.Error(w, http.StatusUnauthorized, "invalid token")
		return
	}
	var body struct {
		Code string `json:"code" validate:"required"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid json")
		return
	}
	if err := validate.Struct(body); err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := h.svc.Disable(r.Context(), uid, body.Code); err != nil {
		switch {
		case errors.Is(err, ErrNotEnrolled):
			httpx.Error(w, http.StatusBadRequest, "two-factor is not enabled")
		case errors.Is(err, ErrBadCode):
			httpx.Error(w, http.StatusUnauthorized, "invalid code")
		default:
			httpx.Error(w, http.StatusInternalServerError, "could not disable")
		}
		return
	}
	h.revokeOtherSessions(r.Context(), uid, p.SessionID)
	w.WriteHeader(http.StatusNoContent)
}

// revokeOtherSessions revokes every session but the caller's current one and
// busts each from the verifier cache. Best-effort: the factor is already gone, so
// a revoke failure is logged (observable) rather than failing the whole request.
func (h *Handler) revokeOtherSessions(ctx context.Context, uid uuid.UUID, currentSID string) {
	if h.sessions == nil {
		return
	}
	keep, err := uuid.Parse(currentSID)
	if err != nil {
		slog.Error("twofa: disable could not parse current session id", "err", err)
		return
	}
	revoked, err := h.sessions.RevokeOtherSessions(ctx, uid, keep)
	if err != nil {
		slog.Error("twofa: disable failed to revoke other sessions", "err", err, "user_id", uid)
		return
	}
	if h.buster != nil {
		for _, sid := range revoked {
			h.buster.Bust(sid)
		}
	}
}

// status returns whether 2FA is enabled and how many recovery codes remain.
func (h *Handler) status(w http.ResponseWriter, r *http.Request) {
	uid, ok := userID(w, r)
	if !ok {
		return
	}
	res, err := h.svc.Status(r.Context(), uid)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "could not load status")
		return
	}
	httpx.JSON(w, http.StatusOK, statusResponse{
		TOTPEnabled:            res.Enabled,
		RecoveryCodesRemaining: res.RecoveryRemaining,
	})
}

// verify completes the login gate: it validates the challenge + code and, on
// success, issues the session exactly as a password login would (via the
// LoginCompleter). Every failure mode (dead challenge or wrong code) is a flat
// 401 — the client learns only "not authenticated", never whether the code was a
// TOTP or a recovery code, nor whether the challenge is still alive.
func (h *Handler) verify(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Challenge string `json:"challenge" validate:"required"`
		Code      string `json:"code" validate:"required"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid json")
		return
	}
	if err := validate.Struct(body); err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	uid, err := h.svc.VerifyChallenge(r.Context(), body.Challenge, body.Code)
	if err != nil {
		httpx.Error(w, http.StatusUnauthorized, "invalid or expired challenge")
		return
	}
	h.completer.CompleteLogin(w, r, uid)
}
