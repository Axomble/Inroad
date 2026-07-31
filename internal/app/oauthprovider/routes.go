package oauthprovider

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/inroad/inroad/internal/app/auth"
)

// Routes returns the OAuth 2.1 authorization-server surface, meant to be MOUNTED at
// the top level under "/oauth2" (distinct from the mailbox-connect "/oauth" mount, so
// there is no chi mount collision).
//
// Auth posture per endpoint:
//   - POST /register     — SESSION-authed + ADMIN (RFC 7591 dynamic registration,
//     restricted to a workspace admin) and rate-limited (registerThrottle, kept as
//     defense-in-depth). The registering admin's user + workspace are recorded so the
//     client is workspace-owned and revocable; scopes are capped to the grantable
//     allowlist and redirect URIs are validated.
//   - GET  /authorize    — PUBLIC top-level navigation; the resource owner is
//     resolved via the ResourceOwner seam inside the handler (unauth -> login
//     redirect), NOT by RequireAuth (which would 401 instead of redirecting).
//   - GET  /consent/{id} — SESSION-authed (SPA fetch with its access token).
//   - POST /consent      — SESSION-authed + CSRF (double-submit).
//   - GET  /clients, DELETE /clients/{id} — SESSION-authed + ADMIN, workspace-pinned.
//
// verifier is the session verifier; registerThrottle may be nil (unit tests wire no
// throttle).
func (h *Handler) Routes(verifier auth.Verifier, registerThrottle func(http.Handler) http.Handler) http.Handler {
	r := chi.NewRouter()

	// The authorization endpoint (public top-level navigation; self-guards via the seam).
	r.Get("/authorize", h.authorize)

	// Admin-authed, rate-limited dynamic client registration. The throttle is OUTERMOST
	// (defense-in-depth: it sheds an over-cap flood before the auth check), then session
	// + admin. The handler records the admin's workspace + user from the session.
	r.Group(func(pr chi.Router) {
		pr.Use(nonNil(registerThrottle)...)
		pr.Use(auth.RequireAuth(verifier))
		pr.Use(auth.RequireRole("admin"))
		pr.Post("/register", h.register)
	})

	// Consent data + decision: the logged-in resource owner only.
	r.Group(func(pr chi.Router) {
		pr.Use(auth.RequireAuth(verifier))
		pr.Get("/consent/{consent_id}", h.consentData)
		pr.With(auth.RequireCSRF).Post("/consent", h.decideConsent)
	})

	// Admin client management, workspace-pinned.
	r.Group(func(pr chi.Router) {
		pr.Use(auth.RequireAuth(verifier))
		pr.Use(auth.RequireRole("admin"))
		pr.Get("/clients", h.listClients)
		pr.Delete("/clients/{client_id}", h.revokeClient)
	})

	return r
}

// nonNil drops a nil middleware so chi's With never invokes a nil func (unit tests
// pass no throttle).
func nonNil(ms ...func(http.Handler) http.Handler) []func(http.Handler) http.Handler {
	out := make([]func(http.Handler) http.Handler, 0, len(ms))
	for _, m := range ms {
		if m != nil {
			out = append(out, m)
		}
	}
	return out
}
