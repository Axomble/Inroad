package identity

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/inroad/inroad/internal/app/auth"
)

// RouteDeps carries the collaborators identity.Routes mounts and the pre-auth
// guards it applies. All fields are optional (a nil handler is not mounted; a nil
// middleware is skipped), so unit tests can wire a bare login surface.
//
// TwoFA, Passkeys, APIKeys, and EmailOTP are the sibling domain routers, mounted
// at "/2fa", "/passkeys", "/api-keys", and "/email-otp" so their paths land under
// /api/v1/auth/... . They are mounted here rather than at the top router because
// chi cannot mount a deeper prefix alongside the /api/v1/auth mount that already
// owns this subtree. They are passed as bare http.Handlers so identity never
// imports those packages (the app-packages-don't-import-each-other invariant); the
// composition root supplies the concrete routers. api-key management is
// session-authed (its own router wraps RequireAuth with the session verifier), so a
// key can never mint or revoke keys.
//
// Captcha (register + login), LoginThrottle (login), and ForgotThrottle
// (password/forgot) are the pre-authentication guards. The sibling routers apply
// their own throttles to their public verify endpoints (the composition root wires
// those in when building each router).
type RouteDeps struct {
	Verifier       auth.Verifier
	TwoFA          http.Handler
	Passkeys       http.Handler
	APIKeys        http.Handler
	EmailOTP       http.Handler
	Captcha        func(http.Handler) http.Handler
	LoginThrottle  func(http.Handler) http.Handler
	ForgotThrottle func(http.Handler) http.Handler
}

// mw drops nil middlewares so chi's With never invokes a nil func. It returns the
// non-nil middlewares in order.
func mw(ms ...func(http.Handler) http.Handler) []func(http.Handler) http.Handler {
	out := make([]func(http.Handler) http.Handler, 0, len(ms))
	for _, m := range ms {
		if m != nil {
			out = append(out, m)
		}
	}
	return out
}

// Routes mounts the full identity surface: public register/login, CSRF-guarded
// refresh/logout, and an access-token-protected group for session
// introspection/management and workspace switching. d.Verifier authenticates the
// access token for the protected group; the register/login/forgot pre-auth guards
// and sibling routers come from d.
func (h *Handler) Routes(d RouteDeps) http.Handler {
	r := chi.NewRouter()
	r.With(mw(d.Captcha)...).Post("/register", h.register)
	r.With(mw(d.Captcha, d.LoginThrottle)...).Post("/login", h.login)
	if d.TwoFA != nil {
		r.Mount("/2fa", d.TwoFA)
	}
	if d.Passkeys != nil {
		r.Mount("/passkeys", d.Passkeys)
	}
	if d.APIKeys != nil {
		r.Mount("/api-keys", d.APIKeys)
	}
	if d.EmailOTP != nil {
		r.Mount("/email-otp", d.EmailOTP)
	}
	r.With(auth.RequireCSRF).Post("/refresh", h.refresh)
	r.With(auth.RequireCSRF).Post("/logout", h.logout)
	r.Post("/verify-email", h.verifyEmail)
	// forgot/reset are pre-authentication flows, unlike refresh/logout: a
	// logged-out caller has no csrf_token cookie, so the double-submit gate
	// would 403 the exact users who need these. The CSRF threat model doesn't
	// apply here either - forgot acts on an arbitrary body email with no
	// ambient cookie authority, and reset's out-of-band single-use token is
	// itself the credential and can't be CSRF-forged. forgot is throttled
	// per-IP/per-email (it triggers a reset email) to prevent mail-bombing.
	r.With(mw(d.ForgotThrottle)...).Post("/password/forgot", h.forgotPassword)
	r.Post("/password/reset", h.resetPassword)
	// invites/accept is public for the same reason forgot/reset are: the
	// out-of-band invite token is itself the credential, and the caller
	// accepting it usually isn't logged in yet (no csrf_token cookie to
	// double-submit).
	r.Post("/invites/accept", h.acceptInvite)
	r.Group(func(pr chi.Router) {
		pr.Use(auth.RequireAuth(d.Verifier))
		pr.Get("/me", h.me)
		pr.Post("/logout-all", h.logoutAll)
		pr.Post("/switch-workspace", h.switchWorkspace)
		pr.Post("/verify-email/resend", h.resendVerification)
		// Session management (this user's own sessions only; user-pinned).
		pr.Get("/sessions", h.listSessions)
		pr.Post("/sessions/revoke-others", h.revokeOtherSessions)
		pr.Delete("/sessions/{id}", h.revokeSession)
	})
	return r
}

// InviteRoutes returns the workspace-scoped invite-management surface
// (create/list/revoke), meant to be mounted at "/api/v1/workspaces" under the
// protected router group (main.go) so auth.RequireAuth already runs before
// any request reaches here. Every route additionally requires the caller be
// an admin of the workspace named in the path - see pathWorkspaceID for why
// the path segment is checked against the JWT's workspace claim rather than
// trusted outright.
func (h *Handler) InviteRoutes() http.Handler {
	r := chi.NewRouter()
	r.Route("/{id}/invites", func(wr chi.Router) {
		wr.Use(auth.RequireRole("admin"))
		wr.Post("/", h.createInvite)
		wr.Get("/", h.listInvites)
		wr.Delete("/{inviteId}", h.revokeInvite)
	})
	return r
}
