package warmup

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

// Register mounts the per-mailbox warmup routes onto an already-authenticated
// mailbox router (paths are relative to /mailboxes). Kept as Register rather than
// a standalone Routes() so /mailboxes/{id}/warmup lives under the same {id}
// mailbox scope and inherits the mailbox router's auth middleware — chi does not
// allow two routers mounted at the same prefix. Satisfies mailbox.SubRouter.
func (h *Handler) Register(r chi.Router) {
	r.Get("/{id}/warmup", h.detail)
	r.Put("/{id}/warmup", h.enable)
	r.Delete("/{id}/warmup", h.disable)
}

// Routes returns the workspace-level warmup surface (GET /overview), mounted by
// the server under /api/v1/warmup. Auth is enforced by the protected router
// group, not here.
func (h *Handler) Routes() http.Handler {
	r := chi.NewRouter()
	r.Get("/overview", h.overview)
	return r
}
