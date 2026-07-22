package contact

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

// Routes returns this domain's HTTP surface, mounted by the server at
// /api/v1/contacts. Every route requires an authenticated caller.
//
// POST /api/v1/contacts/import?list={id} (multipart "file")
// GET  /api/v1/contacts?list={id}
//
// Mounted alongside (not under) /api/v1/lists to avoid the chi mount-prefix
// overlap that would otherwise shadow a nested /lists/{id}/import route.
//
// Auth is enforced by the protected router group, not here.
func (h *Handler) Routes() http.Handler {
	r := chi.NewRouter()
	r.Post("/import", h.importCSV)
	r.Get("/", h.listContacts)
	return r
}
