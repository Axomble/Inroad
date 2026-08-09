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
//
// Custom field DEFINITIONS are mounted separately (see FieldRoutes): they are a
// workspace-level setting, not a sub-resource of any one contact, and putting
// them under /contacts/fields would sit ambiguously beside /contacts/{id}.
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
		write.Put("/{id}/fields", h.putContactFields)
	})
	r.Group(func(read chi.Router) {
		read.Use(auth.RequireScope(auth.ScopeContactsRead))
		read.Get("/", h.listContacts)
		read.Get("/{id}", h.getContact)
		read.Get("/{id}/engagement", h.getContactEngagement)
		read.Get("/{id}/fields", h.getContactFields)
	})
	return r
}

// FieldRoutes returns the custom field DEFINITION surface, mounted at
// /api/v1/custom-fields.
//
// GET    /api/v1/custom-fields
// POST   /api/v1/custom-fields
// PATCH  /api/v1/custom-fields/{fieldID}
// DELETE /api/v1/custom-fields/{fieldID}
//
// Definitions take the same contacts:read / contacts:write scopes as the values
// they describe. Defining a field is a workspace-level setting, but it is a
// setting ABOUT contacts and grants no capability a contacts:write principal
// lacks — it cannot read a credential, reach another domain, or change what
// sends. Giving it a scope of its own would mean every existing integration
// silently lost the ability to import its own columns.
func (h *Handler) FieldRoutes() http.Handler {
	r := chi.NewRouter()
	r.Group(func(write chi.Router) {
		write.Use(auth.RequireScope(auth.ScopeContactsWrite))
		write.Post("/", h.createFieldDef)
		write.Patch("/{fieldID}", h.updateFieldDef)
		write.Delete("/{fieldID}", h.archiveFieldDef)
	})
	r.Group(func(read chi.Router) {
		read.Use(auth.RequireScope(auth.ScopeContactsRead))
		read.Get("/", h.listFieldDefs)
	})
	return r
}
