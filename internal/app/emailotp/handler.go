package emailotp

import (
	"encoding/json"
	"net/http"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/platform/httpx"
	"github.com/inroad/inroad/internal/platform/validate"
)

// FirstFactorCompleter completes a first-factor login for an already-authenticated
// user: it runs the SAME 2FA gate a password login does (a confirmed-2FA user gets
// a challenge and NO session) and otherwise issues a session, writing the standard
// login response. Implemented by the identity handler; kept as a narrow seam so the
// emailotp domain never imports identity (dependency inversion — the composition
// root wires the concrete implementation). Email possession is a first factor
// EQUIVALENT to a password, not a bypass of configured MFA — routing OTP verify
// through this shared method guarantees the 2FA gate is applied identically.
type FirstFactorCompleter interface {
	CompleteFirstFactor(w http.ResponseWriter, r *http.Request, userID uuid.UUID)
}

// Handler exposes the email-OTP surface over HTTP: request a code (start) and
// exchange it for a first-factor login (verify).
type Handler struct {
	svc       *Service
	completer FirstFactorCompleter
}

// NewHandler builds a Handler. completer issues the session (or interposes the 2FA
// gate) on a successful verify.
func NewHandler(svc *Service, completer FirstFactorCompleter) *Handler {
	return &Handler{svc: svc, completer: completer}
}

// startResponse is the generic body returned by start regardless of whether the
// email matched an account — the anti-enumeration guarantee is a constant
// response (and constant timing; the send runs off-path).
type startResponse struct {
	Status string `json:"status"`
}

// start requests a login code. It ALWAYS answers 200 with the same body whether or
// not the email exists, so it is never a user-enumeration oracle.
func (h *Handler) start(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email string `json:"email" validate:"required,email"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid json")
		return
	}
	if err := validate.Struct(body); err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	// Start returns nil on both the known and unknown path (any real work is logged
	// off-path), so the response is identical either way.
	_ = h.svc.Start(r.Context(), body.Email)
	httpx.JSON(w, http.StatusOK, startResponse{Status: "ok"})
}

// verify exchanges a code for a first-factor login. Every failure is a flat 401
// (no oracle distinguishing wrong-code from no-code from no-account); on success
// it hands off to the FirstFactorCompleter, which runs the 2FA gate exactly as a
// password login would — a user with confirmed 2FA still gets a challenge, never a
// session from email possession alone.
func (h *Handler) verify(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email string `json:"email" validate:"required,email"`
		Code  string `json:"code" validate:"required"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid json")
		return
	}
	if err := validate.Struct(body); err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	uid, err := h.svc.Verify(r.Context(), body.Email, body.Code)
	if err != nil {
		httpx.Error(w, http.StatusUnauthorized, "invalid or expired code")
		return
	}
	h.completer.CompleteFirstFactor(w, r, uid)
}
