package tracking

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

// Routes mounts the PUBLIC tracking endpoints. Deliberately has NO auth
// middleware: a recipient's mail client follows these unauthenticated
// (mirrors internal/app/suppression's /u mount).
//
// GET /o/{token}.gif: records an 'open' event, always serves the pixel.
// GET /c/{token}:     records a 'click' event, 302s to the signed URL.
func (h *Handler) Routes() http.Handler {
	r := chi.NewRouter()
	r.Get("/o/{token}", h.openGIF)
	r.Get("/c/{token}", h.clickRedirect)
	return r
}
