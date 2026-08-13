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

// Routes returns the workspace-level warmup surface, mounted by the server under
// /api/v1/warmup. Auth is enforced by the protected router group, not here.
//
// The transition history hangs off this router rather than the mailbox one
// because the contract puts it at /warmup/mailboxes/{mailbox_id}/transitions:
// its subject is the warmup engine's decision record, and it stays readable for
// a mailbox that is not currently a participant.
func (h *Handler) Routes() http.Handler {
	r := chi.NewRouter()
	r.Get("/overview", h.overview)
	r.Get("/mailboxes/{mailbox_id}/transitions", h.transitions)
	return r
}
