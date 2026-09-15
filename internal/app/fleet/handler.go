package fleet

import (
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/app/auth"
	"github.com/inroad/inroad/internal/platform/db/gen"
	"github.com/inroad/inroad/internal/platform/httpx"
)

// Handler exposes the fleet read surface over HTTP. Authentication is applied by
// the protected router group (cmd/inroad's session-only group) and the role gate
// by Routes; the workspace always comes from the authenticated principal, never
// from the request.
type Handler struct{ svc *Service }

// NewHandler builds the HTTP surface over the service.
func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

// providerSignalsResponse is one (provider, operation) leg's verdict rollup.
//
// EVERY COUNT IS RAW. No rate, percentage or health score is computed here: a
// derived number and the counts it came from are one fact with two
// representations, and serving both invites them to disagree after a filter or a
// rounding change. The client renders the percentage beside the pair it divided.
//
// attempts is the TOTAL across every classified verdict, including ones with no
// column of their own ('other'), so the five named buckets can never silently
// fail to account for the whole.
type providerSignalsResponse struct {
	Provider  string `json:"provider"`
	Operation string `json:"operation"`
	Attempts  int64  `json:"attempts"`
	Successes int64  `json:"successes"`
	// AuthFailures is its own field rather than a member of a "soft failures"
	// bucket because it is the provider-side signal that predicts an egress IP
	// being challenged or throttled — the reason worker_provider_signals is
	// collected at all. Burying it in a total would defeat the point of the
	// table.
	AuthFailures int64 `json:"auth_failures"`
	// Throttled pairs with AuthFailures: rate_limited + throttled, the provider
	// slowing this IP down rather than refusing it.
	Throttled int64 `json:"throttled"`
	// Blocked is the hard per-IP pair, blocked + unreachable — the provider will
	// not talk to this address at all.
	Blocked int64 `json:"blocked"`
	// Rejected is deliberately reported apart from every per-IP number above. A
	// permanent non-security 5xx is about the RECIPIENT (dead address, oversized
	// message), not about the worker, and reading it as evidence against an IP
	// is the specific mistake internal/platform/providersignal's doc warns
	// against. It is here so attempts reconcile.
	Rejected int64 `json:"rejected"`
}

// workerResponse is one worker as an operator sees it.
//
// ON worker_id AND egress_ip. Both are fleet infrastructure and both are
// returned here, which is a narrowing of security.md invariant 24's "never
// returned on a tenant-facing API" and is why this whole router is
// admin-session-only (see Routes for the full argument). The short version: a
// worker id is unavoidable, because the decision log's reason prose NAMES the
// workers it chose between ("chose w-1 (score 0.82) over w-2 (score 0.31)") and
// rendering that text is the entire point of the decision endpoint. A surface
// that withheld the id while printing it inside a sentence would be redaction
// theatre.
//
// No credential, no ciphertext and no tenant row appears here by construction:
// every field is either a count this workspace owns or a column of the `workers`
// registry, which holds none of those things.
type workerResponse struct {
	WorkerID string `json:"worker_id"`
	EgressIP string `json:"egress_ip"`
	// IDFamily is how the worker's id was derived — "ipv4" | "ipv6" |
	// "hostname" | "override" (internal/platform/workerid). It qualifies
	// worker_id rather than decorating it: a hostname-derived id changes when
	// the container is replaced, so a decision log naming one is a weaker record
	// than one naming an IP-derived id, and an operator reading that log needs
	// to know which they are looking at.
	IDFamily string `json:"id_family"`
	// LastSeenAt is the raw heartbeat, served next to Live rather than replaced
	// by it. The boolean is what an operator scans; the timestamp is what they
	// need the moment the boolean surprises them.
	LastSeenAt string `json:"last_seen_at"`
	Live       bool   `json:"live"`
	// MailboxCount is THIS WORKSPACE's footprint on the worker, never the
	// fleet-wide occupancy the placement scorer reads. How many of another
	// tenant's mailboxes share the IP is not a question this caller is entitled
	// to an answer to.
	MailboxCount int64 `json:"mailbox_count"`
	// DegradedMailboxCount is how many of those were placed in the 'degraded'
	// risk band. Paired with MailboxCount for the same reason attempts pairs
	// with successes: three degraded of four is a different worker from three of
	// three hundred.
	DegradedMailboxCount int64 `json:"degraded_mailbox_count"`
	// FirstAssignedAt is the oldest of this workspace's pins to the worker — how
	// long its mail has been egressing from that IP, which is the thing a
	// provider's trust accrues against.
	FirstAssignedAt string                    `json:"first_assigned_at"`
	Signals         []providerSignalsResponse `json:"signals"`
}

type workerListResponse struct {
	Workers []workerResponse `json:"workers"`
	// WindowHours is the window the signals were actually rolled up over, which
	// is not always the one asked for — it is clamped. Echoing the effective
	// value means a client labelling its column never has to guess, and never
	// labels a 24-hour rollup as the 9000 hours someone typed.
	WindowHours int64 `json:"window_hours"`
}

// decisionResponse is one entry from the placement log.
//
// mailbox_id and workspace_id are both omitted: the mailbox is named in the
// request path and the workspace is the caller's own, so echoing either adds
// nothing and invites a client to start SENDING one (the rule deadletter's
// response applies for the same reason).
type decisionResponse struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
	// WorkerID is null when the decision named no destination — a refusal has
	// none, and fleetdecision's own doc explains that saying so is more honest
	// than naming the worker that was rejected.
	WorkerID *string `json:"worker_id"`
	// Reason is the prose EXACTLY as recorded. It is not parsed, split or
	// re-rendered into fields here, and must not be downstream either: these
	// strings are built only through fleetdecision's constructors precisely so
	// that a forced decision never prints a score comparison nobody computed.
	// Imposing a structure on them at the wire would reintroduce the claim the
	// type system was shaped to prevent.
	Reason      string `json:"reason"`
	TriggeredBy string `json:"triggered_by"`
	CreatedAt   string `json:"created_at"`
}

type decisionListResponse struct {
	Decisions []decisionResponse `json:"decisions"`
}

// scheduledJobResponse is one periodic sweep's health.
//
// THERE IS NO error_message FIELD, AND ITS ABSENCE IS THE DESIGN. The stored
// column holds err.Error() from six handlers the ledger does not own; the
// table's own migration says in as many words that it is NOT guaranteed to be
// free of tenant content, and scheduled_job_runs carries no workspace_id to
// scope a read by. Serving that text on a workspace-scoped endpoint would let
// one tenant's admin read an error produced while sweeping another tenant's
// mailbox — a cross-tenant leak arriving through a diagnostics field.
//
// What the operator needs from this screen is fully answered without it: whether
// the sweep ran, whether it failed, when it last failed and how often. The text
// stays in the deployment's logs, where whoever runs the deployment already has
// it.
type scheduledJobResponse struct {
	JobName        string `json:"job_name"`
	LastStartedAt  string `json:"last_started_at"`
	LastFinishedAt string `json:"last_finished_at"`
	LastDurationMs int64  `json:"last_duration_ms"`
	// LastOutcome is "ok" or "error" (the column's CHECK), naming the most
	// recent run whether or not it falls inside the window below.
	LastOutcome string `json:"last_outcome"`
	// RunsInWindow and FailuresInWindow are paired for the same reason attempts
	// pairs with successes: two failures means something different out of two
	// runs than out of two hundred.
	RunsInWindow     int64 `json:"runs_in_window"`
	FailuresInWindow int64 `json:"failures_in_window"`
	// LastFailureAt is null when nothing failed inside the window. It is the
	// closest this response comes to an error report, and it is deliberately a
	// timestamp rather than a message — see the type doc.
	LastFailureAt *string `json:"last_failure_at"`
}

type scheduledJobListResponse struct {
	Jobs        []scheduledJobResponse `json:"jobs"`
	WindowHours int64                  `json:"window_hours"`
}

func (h *Handler) listWorkers(w http.ResponseWriter, r *http.Request) {
	ws, ok := auth.WorkspaceID(w, r)
	if !ok {
		return
	}
	window := clampWindow(hoursQuery(r, "window_hours"))
	rows, err := h.svc.Workers(r.Context(), ws, window)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "fleet worker request failed")
		return
	}
	httpx.JSON(w, http.StatusOK, workerListResponse{
		Workers:     toWorkerResponses(rows),
		WindowHours: int64(window / time.Hour),
	})
}

func (h *Handler) listDecisions(w http.ResponseWriter, r *http.Request) {
	ws, ok := auth.WorkspaceID(w, r)
	if !ok {
		return
	}
	mailboxID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		// 400, not the 404 the dead-letter path uses for the same mistake. That
		// endpoint hides a malformed id behind not-found so a caller cannot
		// distinguish it from someone else's row; this one has no not-found
		// state to hide behind — an unknown mailbox legitimately returns an
		// empty list — so there is nothing a 404 would conceal and "that is not
		// a UUID" is simply true.
		httpx.Error(w, http.StatusBadRequest, "mailbox id is not a UUID")
		return
	}
	rows, err := h.svc.DecisionsForMailbox(r.Context(), ws, mailboxID, intQuery(r, "limit"))
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "fleet decision request failed")
		return
	}
	httpx.JSON(w, http.StatusOK, decisionListResponse{Decisions: toDecisionResponses(rows)})
}

func (h *Handler) listScheduledJobs(w http.ResponseWriter, r *http.Request) {
	// The workspace is resolved and then not passed anywhere, which looks
	// redundant and is not: it is the 401 for a request whose principal carries
	// no usable workspace, applied uniformly across this router so that one
	// endpoint cannot end up with a weaker precondition than its neighbours
	// merely because its query happens not to need the id.
	if _, ok := auth.WorkspaceID(w, r); !ok {
		return
	}
	window := clampWindow(hoursQuery(r, "window_hours"))
	rows, err := h.svc.ScheduledJobs(r.Context(), window)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "scheduled job request failed")
		return
	}
	httpx.JSON(w, http.StatusOK, scheduledJobListResponse{
		Jobs:        toScheduledJobResponses(rows),
		WindowHours: int64(window / time.Hour),
	})
}

// toWorkerResponses always returns a non-nil slice so an empty fleet serialises
// as [] rather than null — a client mapping over the result must not have to
// guard. The same rule applies to the nested signal slices.
func toWorkerResponses(rows []WorkerHealth) []workerResponse {
	out := make([]workerResponse, 0, len(rows))
	for _, row := range rows {
		out = append(out, workerResponse{
			WorkerID:             row.WorkerID,
			EgressIP:             row.EgressIp,
			IDFamily:             row.IDFamily,
			LastSeenAt:           rfc3339(row.LastSeenAt.Time),
			Live:                 row.Live,
			MailboxCount:         row.WorkspaceMailboxes,
			DegradedMailboxCount: row.DegradedMailboxes,
			FirstAssignedAt:      rfc3339(row.FirstAssignedAt.Time),
			Signals:              toSignalResponses(row.Signals),
		})
	}
	return out
}

func toSignalResponses(rows []gen.RollupWorkspaceFleetProviderSignalsRow) []providerSignalsResponse {
	out := make([]providerSignalsResponse, 0, len(rows))
	for _, row := range rows {
		out = append(out, providerSignalsResponse{
			Provider:     row.Provider,
			Operation:    row.Operation,
			Attempts:     row.Attempts,
			Successes:    row.Successes,
			AuthFailures: row.AuthFailures,
			Throttled:    row.Throttled,
			Blocked:      row.Blocked,
			Rejected:     row.Rejected,
		})
	}
	return out
}

func toDecisionResponses(rows []gen.FleetDecision) []decisionResponse {
	out := make([]decisionResponse, 0, len(rows))
	for _, row := range rows {
		out = append(out, decisionResponse{
			ID:          row.ID.String(),
			Kind:        row.Kind,
			WorkerID:    row.WorkerID,
			Reason:      row.Reason,
			TriggeredBy: row.TriggeredBy,
			CreatedAt:   rfc3339(row.CreatedAt.Time),
		})
	}
	return out
}

func toScheduledJobResponses(rows []gen.ListScheduledJobHealthRow) []scheduledJobResponse {
	out := make([]scheduledJobResponse, 0, len(rows))
	for _, row := range rows {
		job := scheduledJobResponse{
			JobName:          row.JobName,
			LastStartedAt:    rfc3339(row.LastStartedAt.Time),
			LastFinishedAt:   rfc3339(row.LastFinishedAt.Time),
			LastDurationMs:   row.LastDurationMs,
			LastOutcome:      row.LastOutcome,
			RunsInWindow:     row.RunsInWindow,
			FailuresInWindow: row.FailuresInWindow,
		}
		if row.LastFailureAt.Valid {
			failed := rfc3339(row.LastFailureAt.Time)
			job.LastFailureAt = &failed
		}
		out = append(out, job)
	}
	return out
}

func rfc3339(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// hoursQuery reads an optional window in whole hours. An absent, unparseable or
// non-positive value yields 0, which clampWindow then reads as "use the
// default" — the same forgiving rule the dead-letter list applies to its page
// size, and for the same reason: a window is a convenience, so a typo returns
// the default view rather than an error page.
func hoursQuery(r *http.Request, key string) time.Duration {
	n := intQuery(r, key)
	if n <= 0 {
		return 0
	}
	return time.Duration(n) * time.Hour
}

// intQuery reads an optional non-negative int32 query parameter, yielding 0 for
// anything absent, unparseable or negative.
func intQuery(r *http.Request, key string) int32 {
	raw := r.URL.Query().Get(key)
	if raw == "" {
		return 0
	}
	n, err := strconv.ParseInt(raw, 10, 32)
	if err != nil || n < 0 {
		return 0
	}
	return int32(n)
}
