package inbox

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/inroad/inroad/internal/app/auth"
)

// Routes returns this domain's HTTP surface, mounted by the server under
// e.g. /api/v1/inbox. Every route requires an authenticated caller; auth is
// enforced by the protected router group (see cmd/inroad), not here.
//
// RequireScope attenuates machine (api-key) principals to their granted
// scopes; a session principal implicitly holds every scope, so these are
// transparent to human callers. Reading threads needs inbox:read, marking
// one read/unread inbox:write, and sending a reply inbox:send — its own
// scope (not folded into inbox:write) because sending mail is a materially
// more dangerous capability than a boolean toggle; see ScopeInboxSend's doc.
//
// draft-reply is gated on inbox:send too, NOT inbox:read. It reads no more
// than the thread endpoint already exposes, but it spends the workspace's AI
// budget on every call and is only useful as a step toward replying, so it
// belongs behind the same non-OAuth-grantable authority as the send itself: a
// read-only third-party integration must not be able to burn tokens.
//
// draftThrottle rate-limits that spend (per-IP and per-workspace). It is
// nil-safe so a test can mount the router without a Redis-backed limiter;
// cmd/inroad always passes one.
func (h *Handler) Routes(draftThrottle func(http.Handler) http.Handler) http.Handler {
	r := chi.NewRouter()
	read := auth.RequireScope(auth.ScopeInboxRead)
	write := auth.RequireScope(auth.ScopeInboxWrite)
	send := auth.RequireScope(auth.ScopeInboxSend)
	r.With(read).Get("/overview", h.overview)
	r.With(read).Get("/threads", h.list)
	r.With(read).Get("/threads/{id}", h.get)
	r.With(send).Post("/threads/{id}/reply", h.reply)
	draft := []func(http.Handler) http.Handler{send}
	if draftThrottle != nil {
		draft = append(draft, draftThrottle)
	}
	r.With(draft...).Post("/threads/{id}/draft-reply", h.draftReply)
	r.With(write).Put("/threads/{id}/read", h.setRead)
	return r
}
