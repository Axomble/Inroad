package contact

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/inroad/inroad/internal/app/auth"
)

// Routes returns this domain's HTTP surface, mounted by the server at
// /api/v1/contacts. Every route requires an authenticated caller.
//
// POST   /api/v1/contacts/import?list={id} (multipart "file")
// GET    /api/v1/contacts?list={id}
// GET    /api/v1/contacts/{id}
// GET    /api/v1/contacts/{id}/engagement
// PUT    /api/v1/contacts/{id}/company
// GET    /api/v1/contacts/{id}/fields
// PUT    /api/v1/contacts/{id}/fields
// GET    /api/v1/contacts/fields
// POST   /api/v1/contacts/fields
// PATCH  /api/v1/contacts/fields/{fieldID}
// DELETE /api/v1/contacts/fields/{fieldID}
//
// The literal /fields segment is registered alongside /{id}: chi matches static
// path segments ahead of wildcards, so "fields" can never be read as a contact
// id — and it is not a uuid anyway, which is the second line of defence.
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
		// Custom fields are contact data, so both the definitions and the
		// per-contact values take contacts:write. Defining a field is a
		// workspace-level setting, but it is a setting ABOUT contacts and
		// grants no capability a contacts:write principal lacks — it cannot
		// read a credential, reach another domain, or affect what sends.
		write.Post("/fields", h.createFieldDef)
		write.Patch("/fields/{fieldID}", h.updateFieldDef)
		write.Delete("/fields/{fieldID}", h.archiveFieldDef)
		write.Put("/{id}/fields", h.putContactFields)
	})
	r.Group(func(read chi.Router) {
		read.Use(auth.RequireScope(auth.ScopeContactsRead))
		read.Get("/", h.listContacts)
		read.Get("/fields", h.listFieldDefs)
		read.Get("/{id}", h.getContact)
		read.Get("/{id}/engagement", h.getContactEngagement)
		read.Get("/{id}/fields", h.getContactFields)
	})
	return r
}
