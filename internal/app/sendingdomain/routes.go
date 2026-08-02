package sendingdomain

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/inroad/inroad/internal/app/auth"
)

// Routes returns this domain's HTTP surface, mounted by the server under
// /api/v1/sending-domains. Every route requires an authenticated caller; auth is
// enforced by the protected router group, not here.
//
// The scopes track the mailbox domain the data is derived from: reading the
// status needs mailboxes:read, and the recheck — which spends an outbound DNS
// lookup and writes the cached verdict — needs mailboxes:write.
func (h *Handler) Routes() http.Handler {
	r := chi.NewRouter()
	r.With(auth.RequireScope(auth.ScopeMailboxesRead)).Get("/", h.list)
	r.With(auth.RequireScope(auth.ScopeMailboxesWrite)).Post("/{domain}/check", h.check)
	return r
}
