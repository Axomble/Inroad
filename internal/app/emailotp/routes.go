package emailotp

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

// Routes returns the email-OTP surface, meant to be MOUNTED at "/email-otp" under
// the identity /auth router (so the full paths are /api/v1/auth/email-otp/...).
// Both endpoints are PUBLIC — they run before any session exists, exactly like
// /auth/login.
//
// startMW guards /start (captcha + per-IP/per-email throttle to prevent
// mail-bombing); verifyMW guards /verify (per-IP/per-email throttle on
// code-guessing). Either slice may be empty (unit tests wire no middleware).
func (h *Handler) Routes(startMW, verifyMW []func(http.Handler) http.Handler) http.Handler {
	r := chi.NewRouter()
	r.With(startMW...).Post("/start", h.start)
	r.With(verifyMW...).Post("/verify", h.verify)
	return r
}
