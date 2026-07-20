package suppression

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

// Routes mounts the PUBLIC unsubscribe endpoint. Deliberately has NO auth
// middleware: recipients follow this link unauthenticated from an email.
//
// GET  /u/{token}: renders a confirmation page. NO state change (RFC 8058:
//                  email preview scanners auto-fire GETs and would otherwise
//                  unsubscribe every recipient the moment a message opens).
// POST /u/{token}: performs the suppression insert. Matches the
//                  List-Unsubscribe-Post: List-Unsubscribe=One-Click header
//                  the send path adds (see internal/platform/mail/sender.go).
func (h *Handler) Routes() http.Handler {
	r := chi.NewRouter()
	r.Get("/{token}", h.unsubscribeGET)
	r.Post("/{token}", h.unsubscribePOST)
	return r
}
