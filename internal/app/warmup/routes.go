package warmup

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/inroad/inroad/internal/app/auth"
)

// Register mounts the per-mailbox warmup routes onto an already-authenticated
// mailbox router (paths are relative to /mailboxes). Kept as Register rather than
// a standalone Routes() so /mailboxes/{id}/warmup lives under the same {id}
// mailbox scope and inherits the mailbox router's auth middleware — chi does not
// allow two routers mounted at the same prefix. Satisfies mailbox.SubRouter.
func (h *Handler) Register(r chi.Router) {
	// Warm-up is a mailbox capability, so it shares the mailbox scopes: reading a
	// mailbox's warm-up state needs mailboxes:read, toggling it mailboxes:write.
	// Transparent to session principals (they hold every scope).
	r.With(auth.RequireScope(auth.ScopeMailboxesRead)).Get("/{id}/warmup", h.detail)
	r.With(auth.RequireScope(auth.ScopeMailboxesWrite)).Put("/{id}/warmup", h.enable)
	r.With(auth.RequireScope(auth.ScopeMailboxesWrite)).Delete("/{id}/warmup", h.disable)
}

// Routes returns the workspace-level warmup surface (GET /overview), mounted by
// the server under /api/v1/warmup. Auth is enforced by the protected router
// group, not here.
func (h *Handler) Routes() http.Handler {
	r := chi.NewRouter()
	r.Get("/overview", h.overview)
	return r
}
