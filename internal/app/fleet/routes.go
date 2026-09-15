package fleet

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/inroad/inroad/internal/app/auth"
)

// Routes returns this domain's HTTP surface, mounted under /api/v1/fleet:
//
//	GET /workers                        the fleet this workspace's mail egresses from
//	GET /mailboxes/{id}/decisions       why that mailbox is where it is
//	GET /jobs                           whether the periodic sweeps are running
//
// # ROLE-GATED, NOT SCOPE-GATED, AND THAT IS THE WHOLE SECURITY ARGUMENT
//
// The entire router is wrapped in auth.RequireRole("admin"), and cmd/inroad
// mounts it in the SESSION-ONLY group. Together those are structural, not
// advisory:
//
//   - The session-only group authenticates with the session verifier alone, so
//     an `inrd_` API key or an `inoa_` OAuth token presented here is rejected 401
//     before any handler runs.
//   - RequireRole ranks p.Role, and both machine principals carry an EMPTY role
//     by construction (apikey/verifier.go and oauthprovider/verifier.go each say
//     so, in a comment, for exactly this reason). A machine principal therefore
//     ranks 0 and can never satisfy an admin gate even if the mount moved.
//
// Belt and braces, and needed, because of what this surface returns.
//
// # WHY A WORKER ID IS EXPOSED AT ALL
//
// security.md invariant 24 says the `workers` registry and
// worker_provider_signals are "never returned on a tenant-facing API". This
// router narrows that: it returns both, to a workspace ADMIN over a session,
// for the workers that workspace already has mail pinned to. That is a
// deliberate amendment and the invariant has been updated to describe it rather
// than left to quietly contradict the code.
//
// The reason it cannot be avoided: a decision's reason prose NAMES the workers
// it chose between — fleetdecision.Chose renders "chose w-1 (score 0.82) over
// w-2 (score 0.31)" — and rendering that text unaltered is the entire point of
// the decision endpoint (the prose is constructed so it can never claim a
// comparison that was not made; reformatting it is how that guarantee gets
// lost). A response that stripped worker_id from its own fields while printing
// worker ids inside a sentence would be redaction theatre, and the fleet list
// would be unjoinable to the decision log that references it.
//
// What the earlier redaction was actually protecting against is a different
// threat, and it still holds. GET /dead-letters collapses a per-worker queue to
// the literal string "worker" (deadletter's workerQueueLabel) because that route
// is gated on campaigns:read, which IS OAuth-grantable — a delegated third-party
// client could otherwise enumerate fleet hostnames. Nothing delegated can reach
// THIS router at all, so the same identifier is withheld there and served here
// without contradiction: the gate, not the field, is what changed.
//
// # WHAT IS DELIBERATELY NOT HERE
//
//   - The fleet CENSUS. /workers is an inner join through this workspace's own
//     assignments, so a tenant learns about the workers carrying its mail and
//     not how large the deployment is. There is no "list all workers" route.
//   - scheduled_job_runs.error_message. See scheduledJobResponse — the column
//     may carry another tenant's data and the table has no workspace to scope it
//     by.
//   - Any write. There is no quarantine button, no reassign, no run-now. This
//     domain owns no table and mutates nothing; a placement decision made from a
//     browser would need its own authorization story and belongs with rotation,
//     not with the surface that reads it.
func (h *Handler) Routes() http.Handler {
	r := chi.NewRouter()
	r.Group(func(admin chi.Router) {
		admin.Use(auth.RequireRole("admin"))
		admin.Get("/workers", h.listWorkers)
		admin.Get("/mailboxes/{id}/decisions", h.listDecisions)
		admin.Get("/jobs", h.listScheduledJobs)
	})
	return r
}
