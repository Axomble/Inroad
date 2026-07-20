package suppression

import (
	"context"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/platform/httpx"
	"github.com/inroad/inroad/internal/platform/unsub"
)

// Adder is the subset of the store the handler needs. Defined at the
// consumer so unit tests can inject a fake without a database.
type Adder interface {
	Add(ctx context.Context, workspaceID uuid.UUID, email, reason string) error
}

// Handler serves the public, stateless unsubscribe endpoint.
type Handler struct {
	secret []byte
	store  Adder
}

// NewHandler builds a Handler that verifies tokens with secret and records
// suppressions via store.
func NewHandler(secret []byte, store Adder) *Handler { return &Handler{secret: secret, store: store} }

// unsubscribePOST is the RFC 8058 one-click endpoint: the token is verified
// and the suppression row is inserted here. Email preview scanners (Gmail
// Postmaster tools, corporate MTAs, security appliances) auto-follow
// links with GET; keeping the state change on POST prevents those scanners
// from silently unsubscribing every recipient the moment a message is opened.
func (h *Handler) unsubscribePOST(w http.ResponseWriter, r *http.Request) {
	wsID, email, ok := parseAndDecodeToken(h.secret, chi.URLParam(r, "token"))
	if !ok {
		httpx.Error(w, http.StatusBadRequest, "invalid unsubscribe link")
		return
	}
	_ = h.store.Add(r.Context(), wsID, email, "unsubscribe") // idempotent; ignore dup
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("<html><body><p>You have been unsubscribed. You will no longer receive emails.</p></body></html>"))
}

// unsubscribeGET renders a confirmation page whose form POSTs back to the
// same URL. It performs NO state change: the token is still verified so a
// bad link 400s instead of showing a form that leads nowhere, but the
// suppression row only lands after the user (or their MUA's one-click
// header) submits the POST.
func (h *Handler) unsubscribeGET(w http.ResponseWriter, r *http.Request) {
	token := chi.URLParam(r, "token")
	if _, _, ok := parseAndDecodeToken(h.secret, token); !ok {
		httpx.Error(w, http.StatusBadRequest, "invalid unsubscribe link")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	// Minimal, dependency-free page. The form posts back to the same path
	// (relative URL) so the browser preserves the token. No JS, no CSS —
	// email clients that render the page get a working button.
	_, _ = w.Write([]byte(`<!doctype html><html><body>
<h1>Unsubscribe</h1>
<p>Click confirm to unsubscribe from future emails.</p>
<form method="post" action="">
  <button type="submit">Confirm unsubscribe</button>
</form>
</body></html>`))
}

// parseAndDecodeToken is a small helper so the two HTTP handlers share the
// same "invalid" verdict for malformed tokens and unparseable workspace ids.
func parseAndDecodeToken(secret []byte, token string) (uuid.UUID, string, bool) {
	ws, email, ok := unsub.ParseToken(secret, token)
	if !ok {
		return uuid.Nil, "", false
	}
	wsID, err := uuid.Parse(ws)
	if err != nil {
		return uuid.Nil, "", false
	}
	return wsID, email, true
}
