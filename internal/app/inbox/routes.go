package inbox

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/inroad/inroad/internal/app/auth"
)

// Routes returns this domain's HTTP surface, mounted by the server under
// e.g. /api/v1/inbox. Every route requires an authenticated caller; auth is
// enforced by the protected router group (see cmd/inroad), not here.
//
// RequireScope attenuates machine (api-key) principals to their granted
// scopes; a session principal implicitly holds every scope, so these are
// transparent to human callers. Reading threads needs inbox:read, marking
// one read/unread inbox:write, and sending a reply inbox:send — its own
// scope (not folded into inbox:write) because sending mail is a materially
// more dangerous capability than a boolean toggle; see ScopeInboxSend's doc.
//
// draft-reply is gated on inbox:send too, NOT inbox:read. It reads no more
// than the thread endpoint already exposes, but it spends the workspace's AI
// budget on every call and is only useful as a step toward replying, so it
// belongs behind the same non-OAuth-grantable authority as the send itself: a
// read-only third-party integration must not be able to burn tokens.
//
// draftThrottle rate-limits that spend (per-IP and per-workspace). It is
// nil-safe so a test can mount the router without a Redis-backed limiter;
// cmd/inroad always passes one.
func (h *Handler) Routes(draftThrottle func(http.Handler) http.Handler) http.Handler {
	r := chi.NewRouter()
	read := auth.RequireScope(auth.ScopeInboxRead)
	write := auth.RequireScope(auth.ScopeInboxWrite)
	send := auth.RequireScope(auth.ScopeInboxSend)
	r.With(read).Get("/overview", h.overview)
	r.With(read).Get("/threads", h.list)
	r.With(read).Get("/threads/{id}", h.get)
	r.With(send).Post("/threads/{id}/reply", h.reply)
	draft := []func(http.Handler) http.Handler{send}
	if draftThrottle != nil {
		draft = append(draft, draftThrottle)
	}
	r.With(draft...).Post("/threads/{id}/draft-reply", h.draftReply)
	r.With(write).Put("/threads/{id}/read", h.setRead)
	// Snoozing is inbox:write, not inbox:send: it changes triage state and
	// sends nothing, exactly like marking a thread read.
	r.With(write).Put("/threads/{id}/snooze", h.snooze)
	r.With(write).Delete("/threads/{id}/snooze", h.unsnooze)
	// Operator-assigned labels. Reading the taxonomy is inbox:read; creating,
	// editing and applying are inbox:write — filing is triage, and none of it
	// sends mail or spends AI budget.
	r.With(read).Get("/labels", h.listLabels)
	r.With(write).Post("/labels", h.createLabel)
	r.With(write).Put("/labels/{labelId}", h.updateLabel)
	r.With(write).Delete("/labels/{labelId}", h.deleteLabel)
	r.With(write).Put("/threads/{id}/labels/{labelId}", h.assignLabel)
	r.With(write).Delete("/threads/{id}/labels/{labelId}", h.unassignLabel)
	// Deferred + undoable replies. Scheduling needs inbox:send — it is a send,
	// merely a later one. CANCELLING needs only inbox:write: stopping mail from
	// going out is a safety action, and a read-only-ish integration that can
	// prevent a send but never cause one is the right side of that asymmetry.
	r.With(send).Post("/threads/{id}/schedule-reply", h.scheduleReply)
	r.With(read).Get("/outbox", h.listPendingReplies)
	r.With(write).Delete("/outbox/{pendingId}", h.cancelPendingReply)
	r.With(read).Get("/settings", h.getInboxSettings)
	r.With(write).Put("/settings", h.updateInboxSettings)
	return r
}
