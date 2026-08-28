package deadletter

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/inroad/inroad/internal/app/auth"
)

// Routes returns this domain's HTTP surface, mounted under
// /api/v1/dead-letters:
//
//	GET    /                 list, optionally ?status=pending|replayed|discarded
//	GET    /{id}             one dead letter
//	POST   /{id}/replay      re-enqueue the captured payload, exactly once
//	POST   /{id}/discard     file as triaged without re-running
//
// SCOPES. The read side is gated on campaigns:read and the two writes on
// campaigns:send — deliberately the SEND scope, not campaigns:write.
//
// Replay's effect is to put a real task back on the queue, and the tasks that
// dominate this table are sends (sequence:advance, warmup:tick,
// inbox:pending_reply_send). Replaying one delivers mail. That is precisely the
// capability ScopeCampaignsSend exists to gate — the highest-abuse one in this
// system — so an integration holding only campaigns:write must not reach it.
//
// Discard shares the send scope rather than sitting on the read one: it is the
// decision NOT to deliver a queued piece of mail, and permanently closes the
// only path by which that send could still go out. Granting "can permanently
// drop this workspace's failed sends" to a weaker credential than "can send"
// would be the wrong way round.
//
// Neither write scope is OAuth-grantable (see auth.OAuthGrantableScopes), so a
// third-party client can never replay or discard; campaigns:read IS grantable,
// which means a delegated client can read this list — payloads and all.
//
// That is only acceptable while a payload is a POINTER. The list serves the
// captured bytes verbatim, so whatever a task carries, this scope reads. It was
// not always a pointer: inbox:reply_send carried the operator's free-text reply,
// and campaigns:read therefore reached correspondence that inbox:read is
// deliberately NOT granted (auth.OAuthGrantableScopes says why — reply bodies
// are a materially more sensitive category than the structured CRM data this set
// covers). The body now lives in an inbox_pending_replies row; capture of that
// task type is refused in the worker (internal/platform/queue), redacted and
// filed here if a stale worker sends one anyway (Service.Capture), stripped from
// the rows that already existed by migration 20260828133405, and removed from
// the response of any row that survived all three (redactLegacyReplyBody, which
// is what covers a deployment whose migration has not run).
//
// The rule that has to hold for this scope choice to stay honest: a task payload
// names WHAT failed — ids, plus at most contact-class data the campaign scopes
// already expose — and never the content of a message.
// internal/platform/queue's TestTaskPayloadsCarryNoContent is what enforces it.
func (h *Handler) Routes() http.Handler {
	r := chi.NewRouter()
	r.With(auth.RequireScope(auth.ScopeCampaignsRead)).Group(func(read chi.Router) {
		read.Get("/", h.list)
		read.Get("/{id}", h.get)
	})
	r.Group(func(write chi.Router) {
		write.Use(auth.RequireScope(auth.ScopeCampaignsSend))
		write.Post("/{id}/replay", h.replay)
		write.Post("/{id}/discard", h.discard)
	})
	return r
}
