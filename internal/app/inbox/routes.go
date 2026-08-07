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
func (h *Handler) Routes() http.Handler {
	r := chi.NewRouter()
	read := auth.RequireScope(auth.ScopeInboxRead)
	write := auth.RequireScope(auth.ScopeInboxWrite)
	send := auth.RequireScope(auth.ScopeInboxSend)
	r.With(read).Get("/threads", h.list)
	r.With(read).Get("/threads/{id}", h.get)
	r.With(send).Post("/threads/{id}/reply", h.reply)
	r.With(write).Put("/threads/{id}/read", h.setRead)
	return r
}
