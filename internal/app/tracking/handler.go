package tracking

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/inroad/inroad/internal/platform/httpx"
)

// pixelGIF is the smallest valid transparent GIF (1x1, single transparent
// pixel) — a fixed constant so openGIF never touches the filesystem or
// re-encodes anything per request.
var pixelGIF = []byte{
	0x47, 0x49, 0x46, 0x38, 0x39, 0x61, // "GIF89a"
	0x01, 0x00, 0x01, 0x00, // width=1, height=1
	0x80, 0x00, 0x00, // packed flags (global color table, size 2), bg index, aspect ratio
	0x00, 0x00, 0x00, 0xff, 0xff, 0xff, // global color table: black, white
	0x21, 0xf9, 0x04, 0x01, 0x00, 0x00, 0x00, 0x00, // graphic control extension (transparent index 0)
	0x2c, 0x00, 0x00, 0x00, 0x00, 0x01, 0x00, 0x01, 0x00, 0x00, // image descriptor
	0x02, 0x02, 0x44, 0x01, 0x00, // image data (LZW)
	0x3b, // trailer
}

// Handler serves the public, stateless open/click tracking endpoints.
type Handler struct{ svc *Service }

// NewHandler builds a Handler around svc.
func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

// openGIF always returns the 1x1 pixel with Cache-Control: no-store,
// whether or not the token is valid or the send exists -- see
// Service.RecordOpen. Never fails the pixel: a mail client that renders it
// must always get back a 200 image, or the blank box some clients would
// otherwise show could tip off a recipient that tracking is broken (or,
// worse, invite probing to see which tokens fail).
func (h *Handler) openGIF(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimSuffix(chi.URLParam(r, "token"), ".gif")
	h.svc.RecordOpen(r.Context(), token, r.UserAgent())

	w.Header().Set("Content-Type", "image/gif")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(pixelGIF)
}

// clickRedirect 302s to the click token's signed URL. A malformed/tampered
// token, an unsafe redirect scheme, or an unknown send all yield 404 with
// no redirect and no recorded event -- see Service.RecordClick. Unlike the
// pixel, 404 here is fine: there's no recipient-visible cost to a missing
// image, and a 404 gives no more of an oracle than any other dead link.
func (h *Handler) clickRedirect(w http.ResponseWriter, r *http.Request) {
	token := chi.URLParam(r, "token")
	dest, ok := h.svc.RecordClick(r.Context(), token, r.UserAgent())
	if !ok {
		httpx.Error(w, http.StatusNotFound, "not found")
		return
	}
	http.Redirect(w, r, dest, http.StatusFound)
}
