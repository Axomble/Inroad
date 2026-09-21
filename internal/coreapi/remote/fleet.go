package remote

import (
	"context"

	"github.com/inroad/inroad/internal/coreapi"
)

// The WORKER INFRASTRUCTURE path, slice 4: the four calls whose subject is not
// a tenant row.
//
// A worker announces that it is alive, reports how mailbox providers treated
// its egress IP, asks where a mailbox's traffic should be routed, and records a
// task that exhausted its retries. Three of the four are about the FLEET; only
// the mailbox assignment is tenant data, and it is workspace-pinned like
// everything else on this transport.
//
// # Why two of these carry no workspace
//
// `workers` and `worker_provider_signals` hold no tenant column, deliberately
// (docs/security.md invariant 24): several workspaces' mailboxes share one
// worker, and how Google is treating one egress IP is a fact about an address,
// not about a customer. Adding a workspace field to make the four shapes look
// alike would be inventing a tenancy claim the data does not have. The dead
// letter carries the workspace the FAILED TASK named, which may legitimately be
// empty when the execution plane could not tell.
//
// # Idempotency
//
//   - UpsertWorkerHeartbeat — an upsert on the worker id, setting last_seen_at
//     absolutely. A lost response costs one tick of freshness; the next beat is
//     five minutes away and the assigner's live window is fifteen.
//   - RecordWorkerProviderSignals — the counts are DELTAS for a half-open
//     window, so a retried flush would double-count. It is not retried: the
//     flusher logs a failed flush and moves on, which loses a window of counts
//     and nothing else, exactly as it did in process. That is the accepted trade
//     the Flusher was built with (see internal/worker/fleetsignal), not a
//     property this transport weakens.
//   - AssignMailboxWorker — idempotent while the incumbent stays live: an
//     existing assignment to a live worker is returned unchanged, and the
//     placement's INSERT ... ON CONFLICT keeps a single race winner. A lost
//     response means the worker re-asks and is told the same queue.
//   - RecordDeadLetter — an append. A lost response can duplicate a
//     dead-letter row for one task; the ledger is an operator's record, nothing
//     branches on its cardinality, and the alternative (a synthetic
//     idempotency key on a path that only runs when something already failed)
//     would be machinery guarding a duplicate log line.

// UpsertWorkerHeartbeat refreshes this worker's row in the GLOBAL registry so
// the control-plane assigner can tell it is live.
//
// A failure is returned, and cmd/worker's beat LOGS it rather than exiting —
// the same posture as the in-process path, where a transient database blip must
// not take a worker down. What a sustained failure costs is routing: the worker
// ages out of the assigner's fifteen-minute live window and its mailboxes are
// placed elsewhere, which is the correct response to a worker the control plane
// cannot hear from.
func (c *Client) UpsertWorkerHeartbeat(ctx context.Context, workerID, egressIP, idFamily string) error {
	// No uuid parse: a worker id is not a uuid. It is a hostname, an IP, or an
	// operator's INROAD_WORKER_ID override (internal/platform/workerid), and
	// refusing an unfamiliar shape here would stop exactly the hosts whose
	// identity came from the fallback path.
	return c.post(ctx, c.outcomes, PathWorkerHeartbeat, workerHeartbeatRequest{
		WorkerID: workerID, EgressIP: egressIP, IDFamily: idFamily,
	}, &ackResponse{})
}

// RecordWorkerProviderSignals persists one accumulation window of provider
// verdicts.
//
// An EMPTY batch is a no-op on the control plane, not an error — a quiet window
// is the normal state of a healthy worker — but this side does not short-circuit
// it either, so the two transports agree about what an empty flush does rather
// than one of them deciding locally.
func (c *Client) RecordWorkerProviderSignals(ctx context.Context, in coreapi.WorkerProviderSignals) error {
	return c.post(ctx, c.outcomes, PathWorkerProviderSignals, providerSignalsRequest{Signals: in}, &ackResponse{})
}

// AssignMailboxWorker resolves the destination queue for a mailbox's outbound
// traffic, pinning it to one worker's egress IP.
//
// The answer is derived entirely SERVER-SIDE from the persisted assignment and
// the live worker registry (docs/security.md invariant 23) — the request names
// a mailbox and nothing else, so no field on this wire can influence which
// worker or which IP a tenant's mail egresses through. That property is
// unchanged by moving the call: it was never the caller's decision, and now the
// caller cannot even see the registry it is made from.
//
// An EMPTY queue is a valid answer meaning "the shared queue", so it is a
// response field rather than a 404 or a sentinel.
//
// FAIL CLOSED: "" alongside the error, and every caller returns the error
// rather than reading the queue — a guessed empty queue would silently
// un-pin a mailbox from the IP it has been building trust on.
func (c *Client) AssignMailboxWorker(ctx context.Context, mailboxID, workspaceID string) (string, error) {
	if err := parseIDs(workspaceID, mailboxID); err != nil {
		return "", err
	}
	var out assignMailboxResponse
	if err := c.post(ctx, c.outcomes, PathMailboxWorkerAssign, mailboxRequest{
		WorkspaceID: workspaceID, MailboxID: mailboxID,
	}, &out); err != nil {
		return "", err
	}
	return out.Queue, nil
}

// RecordDeadLetter persists one retry-exhausted task.
//
// The workspace id is NOT parsed here. queue.DeadLetter carries whatever the
// failed task's payload named, which is "" when the execution plane could not
// tell — and refusing to record a dead letter because its workspace was
// unknown would discard exactly the failures hardest to diagnose. The control
// plane's own handler decides what an unparseable workspace means, once, for
// both transports.
func (c *Client) RecordDeadLetter(ctx context.Context, in coreapi.DeadLetterInput) error {
	return c.post(ctx, c.outcomes, PathDeadLetterRecord, deadLetterRequest{DeadLetter: in}, &ackResponse{})
}

// Compile-time proof for the two capabilities cmd/worker feature-detects on the
// client it built (main.go's coreapi.ProviderSignalClient and
// coreapi.DeadLetterClient assertions). Both are comma-ok, so a drifted
// signature would not fail the build — it would log "coreapi has no
// dead-letter capability" on a fleet worker and silently stop capturing
// exhausted tasks.
var (
	_ coreapi.ProviderSignalClient = (*Client)(nil)
	_ coreapi.DeadLetterClient     = (*Client)(nil)
)
