package passkey

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/app/auth"
	"github.com/inroad/inroad/internal/platform/httpx"
	"github.com/inroad/inroad/internal/platform/validate"
)

// LoginCompleter issues a first-party session for an already-authenticated user and
// writes the standard login response (access token + refresh/CSRF cookies), exactly
// as a password login would. Implemented by the identity handler; kept as a narrow
// seam here so the passkey domain never imports identity (dependency inversion — the
// composition root wires the concrete implementation). A passkey login is
// phishing-resistant strong auth, so it completes the login directly and skips the
// TOTP gate a password login would hit.
type LoginCompleter interface {
	CompleteLogin(w http.ResponseWriter, r *http.Request, userID uuid.UUID)
}

// Handler exposes the passkey surface over HTTP: register begin/finish and
// list/delete (authed), and the discoverable login begin/finish (public).
type Handler struct {
	svc       *Service
	completer LoginCompleter
}

// NewHandler builds a Handler. completer mints the session on a successful login
// finish.
func NewHandler(svc *Service, completer LoginCompleter) *Handler {
	return &Handler{svc: svc, completer: completer}
}

// beginResponse carries the opaque ceremony session id plus the WebAuthn options
// (a pass-through of the library-defined PublicKeyCredentialCreation/RequestOptions)
// the browser hands to navigator.credentials.create/get.
type beginResponse struct {
	SessionID string `json:"session_id"`
	PublicKey any    `json:"publicKey"`
}

type finishRequest struct {
	SessionID  string          `json:"session_id" validate:"required"`
	Credential json.RawMessage `json:"credential" validate:"required"`
	// Label is optional and only meaningful for registration.
	Label string `json:"label"`
}

type credentialResponse struct {
	ID         string     `json:"id"`
	Label      string     `json:"label"`
	Transports []string   `json:"transports"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
}

type listResponse struct {
	Passkeys []credentialResponse `json:"passkeys"`
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

// registerBegin starts adding a passkey to the caller's account.
func (h *Handler) registerBegin(w http.ResponseWriter, r *http.Request) {
	uid, ok := userID(w, r)
	if !ok {
		return
	}
	opts, err := h.svc.BeginRegistration(r.Context(), uid)
	if err != nil {
		h.writeConfigOr(w, err, "could not start registration")
		return
	}
	httpx.JSON(w, http.StatusOK, beginResponse{SessionID: opts.SessionID, PublicKey: opts.PublicKey})
}

// registerFinish verifies the attestation and stores the new credential.
func (h *Handler) registerFinish(w http.ResponseWriter, r *http.Request) {
	uid, ok := userID(w, r)
	if !ok {
		return
	}
	body, ok := decodeFinish(w, r)
	if !ok {
		return
	}
	err := h.svc.FinishRegistration(r.Context(), uid, body.SessionID, body.Credential, body.Label)
	if err != nil {
		switch {
		case errors.Is(err, ErrNotConfigured):
			httpx.Error(w, http.StatusNotImplemented, "passkeys are not configured")
		case errors.Is(err, ErrChallengeInvalid):
			httpx.Error(w, http.StatusBadRequest, "invalid or expired challenge")
		case errors.Is(err, ErrCredentialExists):
			httpx.Error(w, http.StatusConflict, "passkey already registered")
		case errors.Is(err, ErrRegistration):
			httpx.Error(w, http.StatusBadRequest, "registration failed")
		default:
			httpx.Error(w, http.StatusInternalServerError, "could not complete registration")
		}
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// loginBegin starts a discoverable (usernameless) login. Public: no session exists
// yet — the assertion is the credential.
func (h *Handler) loginBegin(w http.ResponseWriter, r *http.Request) {
	opts, err := h.svc.BeginLogin(r.Context())
	if err != nil {
		h.writeConfigOr(w, err, "could not start login")
		return
	}
	httpx.JSON(w, http.StatusOK, beginResponse{SessionID: opts.SessionID, PublicKey: opts.PublicKey})
}

// loginFinish verifies the assertion and, on success, mints a session via the
// LoginCompleter — identical to a password login's session, but skipping the TOTP
// gate because a user-verified passkey is strong auth. Every failure is a flat 401.
func (h *Handler) loginFinish(w http.ResponseWriter, r *http.Request) {
	body, ok := decodeFinish(w, r)
	if !ok {
		return
	}
	uid, err := h.svc.FinishLogin(r.Context(), body.SessionID, body.Credential)
	if err != nil {
		if errors.Is(err, ErrNotConfigured) {
			httpx.Error(w, http.StatusNotImplemented, "passkeys are not configured")
			return
		}
		// A clone signal is worth an operator log (no secrets), but the client learns
		// only "not authenticated" — never whether the credential exists or why.
		if errors.Is(err, ErrCloneDetected) {
			slog.Warn("passkey: login rejected on signature-counter regression (possible clone)")
		}
		httpx.Error(w, http.StatusUnauthorized, "authentication failed")
		return
	}
	h.completer.CompleteLogin(w, r, uid)
}

// list returns the caller's registered passkeys (no key material).
func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	uid, ok := userID(w, r)
	if !ok {
		return
	}
	creds, err := h.svc.List(r.Context(), uid)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "could not list passkeys")
		return
	}
	out := listResponse{Passkeys: make([]credentialResponse, len(creds))}
	for i, c := range creds {
		// CredentialInfo and credentialResponse share an identical field layout
		// (the latter only adds JSON tags), so a direct conversion keeps the DTO
		// mapping DRY and fails to compile if the two ever diverge.
		out.Passkeys[i] = credentialResponse(c)
	}
	httpx.JSON(w, http.StatusOK, out)
}

// deleteCredential removes one of the caller's passkeys (own-only; 404 on foreign).
func (h *Handler) deleteCredential(w http.ResponseWriter, r *http.Request) {
	uid, ok := userID(w, r)
	if !ok {
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid id")
		return
	}
	if err := h.svc.Delete(r.Context(), uid, id); err != nil {
		if errors.Is(err, ErrNotFound) {
			httpx.Error(w, http.StatusNotFound, "passkey not found")
			return
		}
		httpx.Error(w, http.StatusInternalServerError, "could not delete passkey")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// decodeFinish parses and validates a finish body (session id + raw credential).
func decodeFinish(w http.ResponseWriter, r *http.Request) (finishRequest, bool) {
	var body finishRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid json")
		return finishRequest{}, false
	}
	if err := validate.Struct(body); err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return finishRequest{}, false
	}
	return body, true
}

// writeConfigOr maps ErrNotConfigured to 501 (feature off) and anything else to a
// 500 with the given message.
func (h *Handler) writeConfigOr(w http.ResponseWriter, err error, msg string) {
	if errors.Is(err, ErrNotConfigured) {
		httpx.Error(w, http.StatusNotImplemented, "passkeys are not configured")
		return
	}
	httpx.Error(w, http.StatusInternalServerError, msg)
}
