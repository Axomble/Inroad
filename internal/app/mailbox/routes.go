package mailbox

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/inroad/inroad/internal/app/auth"
)

// SubRouter registers additional routes onto the mailbox router. Sub-resources
// (warmup) implement it so they live under /mailboxes/{id} and inherit the auth
// middleware — chi disallows two routers mounted at the same prefix.
type SubRouter interface{ Register(r chi.Router) }

// Routes returns this domain's HTTP surface, mounted by the server under
// e.g. /api/v1/mailboxes. Every route requires an authenticated caller;
// auth is enforced by the protected router group, not here. connect
// additionally requires a verified email, checked via checker. Sub-resources
// (warmup) registered here inherit the group's auth by being mounted under
// /mailboxes.
func (h *Handler) Routes(checker auth.VerifiedChecker) http.Handler {
	r := chi.NewRouter()

	// RequireScope attenuates machine (api-key) principals to their granted scopes;
	// a session principal implicitly holds every scope, so these are transparent to
	// human callers. Read routes need mailboxes:read, mutating routes mailboxes:write.
	write := auth.RequireScope(auth.ScopeMailboxesWrite)
	read := auth.RequireScope(auth.ScopeMailboxesRead)
	r.With(write, auth.RequireVerified(checker)).Post("/", h.connect)
	r.With(write, auth.RequireVerified(checker)).Post("/oauth/google/start", h.startGoogleOAuth)
	r.With(write, auth.RequireVerified(checker)).Post("/oauth/microsoft/start", h.startMicrosoftOAuth)
	r.With(read).Get("/", h.list)
	r.With(read).Get("/{id}", h.get)
	r.With(write).Post("/{id}/pause", h.pause)
	r.With(write).Post("/{id}/resume", h.resume)
	r.With(write).Delete("/{id}", h.delete)
	for _, s := range h.subs {
		s.Register(r)
	}

	return r
}

// CallbackRoutes returns the PUBLIC OAuth callback surface, mounted at /oauth
// (alongside /u and /t). It authenticates from the signed `state` parameter,
// not the JWT cookie -- Google and Microsoft redirect the browser here at the
// top level, so the cookie is unavailable -- which is why it lives outside the
// protected group.
func (h *Handler) CallbackRoutes() http.Handler {
	r := chi.NewRouter()
	r.Get("/google/callback", h.googleCallback)
	r.Get("/microsoft/callback", h.microsoftCallback)
	return r
}
