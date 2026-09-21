package remote

import (
	"net/http"
)

// The control plane's side of the WORKER INFRASTRUCTURE path (slice 4).
//
// Two of these four routes take no workspace, because their subjects have none:
// `workers` and `worker_provider_signals` are global infrastructure state with
// no tenant column (docs/security.md invariant 24). parsePair is therefore not
// used on them, and there is nothing to pin — which is stated here so a reader
// checking "is every route on this transport workspace-pinned" gets an answer
// rather than a suspicion.
//
// What bounds them instead is the same thing that bounds everything on this
// listener: the fleet bearer token, and an address the operator binds where
// only the fleet can reach it. A caller holding that token can already ask
// about any tenant's sends, so "which worker is alive" is not a widening.

// upsertWorkerHeartbeat refreshes one worker's row in the global registry.
//
// The worker id is NOT parsed as a uuid. It is a hostname, a public IP, or an
// operator's INROAD_WORKER_ID override (internal/platform/workerid), and the
// in-process path does not validate its shape either — refusing an unfamiliar
// one here would drop exactly the hosts whose identity came from the fallback.
func (h *handler) upsertWorkerHeartbeat(w http.ResponseWriter, r *http.Request) {
	var in workerHeartbeatRequest
	if !decode(w, r, &in) {
		return
	}
	if err := h.fleet.UpsertWorkerHeartbeat(r.Context(), in.WorkerID, in.EgressIP, in.IDFamily); err != nil {
		h.fail(w, failWrite, "worker heartbeat", err, "worker_id", in.WorkerID)
		return
	}
	respond(w, http.StatusOK, ackResponse{})
}

// recordWorkerProviderSignals persists one accumulation window of the verdicts
// mailbox providers returned to one worker's egress IP.
func (h *handler) recordWorkerProviderSignals(w http.ResponseWriter, r *http.Request) {
	var in providerSignalsRequest
	if !decode(w, r, &in) {
		return
	}
	if err := h.fleet.RecordWorkerProviderSignals(r.Context(), in.Signals); err != nil {
		h.fail(w, failWrite, "worker provider signals", err, "worker_id", in.Signals.WorkerID)
		return
	}
	respond(w, http.StatusOK, ackResponse{})
}

// assignMailboxWorker resolves the destination queue for one mailbox's outbound
// traffic.
//
// The answer is derived entirely from the persisted assignment and the live
// worker registry — the request names a mailbox and carries no field that could
// influence the placement (docs/security.md invariant 23). An EMPTY queue is a
// valid 200 meaning "the shared queue", never a 404.
func (h *handler) assignMailboxWorker(w http.ResponseWriter, r *http.Request) {
	var in mailboxRequest
	if !decode(w, r, &in) {
		return
	}
	ws, mailbox, ok := parsePair(w, in.WorkspaceID, "mailbox_id", in.MailboxID)
	if !ok {
		return
	}
	queue, err := h.fleet.AssignMailboxWorker(r.Context(), mailbox.String(), ws.String())
	if err != nil {
		// coreapi.ErrNoEligibleWorker reaches this arm as an ordinary 500 and
		// is NOT given a code, deliberately. The rule this package follows is
		// that a code names a sentinel a consumer BRANCHES on; the two callers
		// that branch on it are the inbox and warmup SWEEPS, which are
		// control-role and never use this transport. The send path returns the
		// error and lets asynq retry, which is exactly what it does in process.
		h.fail(w, failWrite, "mailbox worker assignment", err, "workspace_id", ws, "mailbox_id", mailbox)
		return
	}
	respond(w, http.StatusOK, assignMailboxResponse{Queue: queue})
}

// recordDeadLetter persists one retry-exhausted task.
//
// decodeJob, not decode: the captured payload is the failed task's body
// verbatim, and a step-send task's payload is small but a dead letter is
// precisely the thing whose size nobody controls. Refusing an oversized one
// would discard the failure hardest to diagnose.
//
// The workspace is not parsed. queue.DeadLetter carries whatever the failed
// task named, which is "" when the execution plane could not tell — and the
// in-process recorder already handles that. Refusing to record a dead letter
// because its workspace was unknown would throw away exactly the evidence an
// operator needs.
func (h *handler) recordDeadLetter(w http.ResponseWriter, r *http.Request) {
	var in deadLetterRequest
	if !decodeJob(w, r, &in) {
		return
	}
	if err := h.fleet.RecordDeadLetter(r.Context(), in.DeadLetter); err != nil {
		// Neither the payload nor the captured error text is logged: the payload
		// is the task's own body and the error came from a handler this route
		// does not own (the same reasoning docs/security.md invariant 70 gives
		// for withholding scheduled_job_runs.error_message).
		h.fail(w, failWrite, "dead letter", err, "task_type", in.DeadLetter.TaskType)
		return
	}
	respond(w, http.StatusOK, ackResponse{})
}
