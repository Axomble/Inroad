package suppression

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/platform/httpx"
)

// Handler serves the public, stateless unsubscribe endpoint.
type Handler struct {
	secret []byte
	store  *Store
}

// NewHandler builds a Handler that verifies tokens with secret and records
// suppressions via store.
func NewHandler(secret []byte, store *Store) *Handler { return &Handler{secret: secret, store: store} }

func (h *Handler) unsubscribe(w http.ResponseWriter, r *http.Request) {
	ws, email, ok := ParseToken(h.secret, chi.URLParam(r, "token"))
	if !ok {
		httpx.Error(w, http.StatusBadRequest, "invalid unsubscribe link")
		return
	}
	wsID, err := uuid.Parse(ws)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid unsubscribe link")
		return
	}
	_ = h.store.Add(r.Context(), wsID, email, "unsubscribe") // idempotent; ignore dup
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("<html><body><p>You have been unsubscribed. You will no longer receive emails.</p></body></html>"))
}
