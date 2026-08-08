package contact

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/inroad/inroad/internal/app/auth"
)

// Routes returns this domain's HTTP surface, mounted by the server at
// /api/v1/contacts. Every route requires an authenticated caller.
//
// POST /api/v1/contacts/import?list={id} (multipart "file")
// GET  /api/v1/contacts?list={id}
// GET  /api/v1/contacts/{id}
// GET  /api/v1/contacts/{id}/engagement
// PUT  /api/v1/contacts/{id}/company
//
// Mounted alongside (not under) /api/v1/lists to avoid the chi mount-prefix
// overlap that would otherwise shadow a nested /lists/{id}/import route.
//
// Auth is enforced by the protected router group, not here.
func (h *Handler) Routes() http.Handler {
	r := chi.NewRouter()
	// RequireScope attenuates machine (api-key) principals; a session principal
	// implicitly holds every scope.
	r.Group(func(write chi.Router) {
		write.Use(auth.RequireScope(auth.ScopeContactsWrite))
		write.Post("/import", h.importCSV)
		// Linking a contact to a company writes a contact column, so it takes the
		// contact-writing capability rather than crm:write. Unlike the alias
		// writes in the crm domain it does NOT also require crm:write: company_id
		// is CRM metadata with no send-path effect, so a contacts:write principal
		// tidying its own records is not escalating anything.
		write.Put("/{id}/company", h.putContactCompany)
	})
	r.Group(func(read chi.Router) {
		read.Use(auth.RequireScope(auth.ScopeContactsRead))
		read.Get("/", h.listContacts)
		read.Get("/{id}", h.getContact)
		read.Get("/{id}/engagement", h.getContactEngagement)
	})
	return r
}
