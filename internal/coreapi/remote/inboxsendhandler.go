package remote

import (
	"context"
	"net/http"

	"github.com/google/uuid"
)

// The control plane's side of the MANUAL MAIL protocol.
//
// Every one of these is the same four steps the job and outcome routes are —
// decode under a cap with unknown fields refused, pin the workspace, call the
// SAME in-process implementation the single-process topology calls, map the error
// — written out one by one for the same reason: a table would carry the per-route
// ids and shapes as values nothing type-checks.
//
// WORKSPACE PINNING is identical to the other two families: the pin arrives on
// the wire because there is no session here to derive it from, and the
// implementation then applies the same workspace_id SQL filter the in-process
// path applies. On the record route it is checked TWICE — once as the envelope's
// own field, once against the workspace inside the reply — and a disagreement is
// a 400 before anything runs. See checkSuppression's doc for the full argument.
//
// CORRESPONDENCE IS NEVER LOGGED. Every log argument below is an id. A reply's
// body, its subject and its recipient are the operator's own mail, and the whole
// reason the body lives on a row rather than in a task payload is that a payload
// ends up in task_dead_letters and is served under campaigns:read
// (docs/security.md invariant 64). Putting one in the control plane's log would
// re-open that in a different sink.
//
// NOTHING IS GUESSED. A failed implementation call is a 5xx or a coded 409 with a
// fixed body. It is never a 200 with a zero value, and a claim is never answered
// "you hold it" on a path that did not take one — the worker's retry has to reach
// the real claim for the protocol to mean anything.

// inboxReplyJob loads one thread's reply job (legacy drain).
func (h *handler) inboxReplyJob(w http.ResponseWriter, r *http.Request) {
	var in threadRequest
	if !decode(w, r, &in) {
		return
	}
	ws, thread, ok := parsePair(w, in.WorkspaceID, "thread_id", in.ThreadID)
	if !ok {
		return
	}
	job, err := h.inboxSends.GetInboxReplyJob(r.Context(), thread.String(), ws.String())
	if err != nil {
		h.fail(w, failRead, "inbox reply job", err, "workspace_id", ws, "thread_id", thread)
		return
	}
	respond(w, http.StatusOK, inboxReplyJobResponse{Job: job})
}

// recordInboxReply writes one delivered reply onto its thread.
//
// This is the one route on this transport whose REQUEST carries a message body,
// so it decodes under the message cap rather than the ids-only one.
func (h *handler) recordInboxReply(w http.ResponseWriter, r *http.Request) {
	var in recordReplyRequest
	if !decodeMessage(w, r, &in) {
		return
	}
	ws, ok := pinnedWorkspace(w, in.WorkspaceID, in.Reply.WorkspaceID)
	if !ok {
		return
	}
	if err := h.inboxSends.RecordInboxReply(r.Context(), in.Reply); err != nil {
		// Ids only: the Message-ID, the subject, the recipient and the body are
		// all the tenant's own mail, and the thread id identifies the row an
		// operator would look at anyway.
		h.fail(w, failWrite, "inbox reply record", err, "workspace_id", ws, "thread_id", in.Reply.ThreadID)
		return
	}
	respond(w, http.StatusOK, ackResponse{})
}

// claimInboxReply claims one legacy inbox:reply_send task.
func (h *handler) claimInboxReply(w http.ResponseWriter, r *http.Request) {
	var in replyTaskRequest
	if !decode(w, r, &in) {
		return
	}
	ws, ok := parseWorkspace(w, in.WorkspaceID)
	if !ok {
		return
	}
	// TaskID is NOT validated: it is an asynq task id rather than a row id, and
	// refusing an unfamiliar shape would strand exactly the in-flight tasks this
	// drain exists to finish. The in-process path hands it straight to the claim
	// too, so validating on one transport only would be a behaviour difference.
	claimed, err := h.inboxSends.ClaimInboxReply(r.Context(), ws.String(), in.TaskID)
	if err != nil {
		h.fail(w, failWrite, "inbox reply claim", err, "workspace_id", ws)
		return
	}
	respond(w, http.StatusOK, claimedResponse{Claimed: claimed})
}

// releaseInboxReply releases a claim taken by claimInboxReply.
func (h *handler) releaseInboxReply(w http.ResponseWriter, r *http.Request) {
	var in replyTaskRequest
	if !decode(w, r, &in) {
		return
	}
	ws, ok := parseWorkspace(w, in.WorkspaceID)
	if !ok {
		return
	}
	if err := h.inboxSends.ReleaseInboxReply(r.Context(), ws.String(), in.TaskID); err != nil {
		h.fail(w, failWrite, "inbox reply release", err, "workspace_id", ws)
		return
	}
	respond(w, http.StatusOK, ackResponse{})
}

// claimPendingInboxReply claims one deferred reply and answers with the body.
func (h *handler) claimPendingInboxReply(w http.ResponseWriter, r *http.Request) {
	ws, pending, ok := decodePending(w, r)
	if !ok {
		return
	}
	got, err := h.inboxSends.ClaimPendingInboxReply(r.Context(), ws.String(), pending.String())
	if err != nil {
		h.fail(w, failWrite, "pending reply claim", err, "workspace_id", ws, "pending_id", pending)
		return
	}
	respond(w, http.StatusOK, pendingInboxReplyResponse{Pending: got})
}

// markPendingInboxReplySent completes a claimed deferred reply.
func (h *handler) markPendingInboxReplySent(w http.ResponseWriter, r *http.Request) {
	var in pendingSentRequest
	if !decode(w, r, &in) {
		return
	}
	ws, pending, ok := parsePair(w, in.WorkspaceID, "pending_id", in.PendingID)
	if !ok {
		return
	}
	if err := h.inboxSends.MarkPendingInboxReplySent(r.Context(), ws.String(), pending.String(), in.MessageID); err != nil {
		h.fail(w, failWrite, "pending reply completion", err, "workspace_id", ws, "pending_id", pending)
		return
	}
	respond(w, http.StatusOK, ackResponse{})
}

// releasePendingInboxReply returns a claimed reply to 'scheduled'.
func (h *handler) releasePendingInboxReply(w http.ResponseWriter, r *http.Request) {
	h.pendingReason(w, r, "pending reply release", h.inboxSends.ReleasePendingInboxReply)
}

// failPendingInboxReply marks a claimed reply permanently failed.
func (h *handler) failPendingInboxReply(w http.ResponseWriter, r *http.Request) {
	h.pendingReason(w, r, "pending reply failure", h.inboxSends.FailPendingInboxReply)
}

// claimPendingInboxCompose claims one deferred composed email.
func (h *handler) claimPendingInboxCompose(w http.ResponseWriter, r *http.Request) {
	ws, pending, ok := decodePending(w, r)
	if !ok {
		return
	}
	got, err := h.inboxSends.ClaimPendingInboxCompose(r.Context(), ws.String(), pending.String())
	if err != nil {
		h.fail(w, failWrite, "pending compose claim", err, "workspace_id", ws, "pending_id", pending)
		return
	}
	respond(w, http.StatusOK, pendingInboxComposeResponse{Compose: got})
}

func (h *handler) markPendingInboxComposeSent(w http.ResponseWriter, r *http.Request) {
	var in pendingSentRequest
	if !decode(w, r, &in) {
		return
	}
	ws, pending, ok := parsePair(w, in.WorkspaceID, "pending_id", in.PendingID)
	if !ok {
		return
	}
	if err := h.inboxSends.MarkPendingInboxComposeSent(r.Context(), ws.String(), pending.String(), in.MessageID); err != nil {
		h.fail(w, failWrite, "pending compose completion", err, "workspace_id", ws, "pending_id", pending)
		return
	}
	respond(w, http.StatusOK, ackResponse{})
}

func (h *handler) releasePendingInboxCompose(w http.ResponseWriter, r *http.Request) {
	h.pendingReason(w, r, "pending compose release", h.inboxSends.ReleasePendingInboxCompose)
}

func (h *handler) failPendingInboxCompose(w http.ResponseWriter, r *http.Request) {
	h.pendingReason(w, r, "pending compose failure", h.inboxSends.FailPendingInboxCompose)
}

// decodePending reads and pins the ids-only request the two claims share.
func decodePending(w http.ResponseWriter, r *http.Request) (ws, pending uuid.UUID, ok bool) {
	var in pendingRequest
	if !decode(w, r, &in) {
		return uuid.Nil, uuid.Nil, false
	}
	return parsePair(w, in.WorkspaceID, "pending_id", in.PendingID)
}

// pendingReason is the shared half of the four release/fail routes, which take
// the same request and differ only in which transition they ask for — a
// difference decided by the PATH, so the request assembly lives once.
//
// The reason is NOT validated here. It is a stable client-safe token chosen by
// internal/worker/inbox, and the reason it is safe is enforced where it is
// chosen: worker/inbox.releaseAndReturn refuses to put a provider's error text
// there because the row's last_error is served to any inbox:read caller. A second
// vocabulary maintained on this side would be one more thing to forget when a
// reason is added.
func (h *handler) pendingReason(w http.ResponseWriter, r *http.Request, route string,
	call func(ctx context.Context, workspaceID, pendingID, reason string) error,
) {
	var in pendingReasonRequest
	if !decode(w, r, &in) {
		return
	}
	ws, pending, ok := parsePair(w, in.WorkspaceID, "pending_id", in.PendingID)
	if !ok {
		return
	}
	if err := call(r.Context(), ws.String(), pending.String(), in.Reason); err != nil {
		// The reason IS logged: it is one of worker/inbox's own closed set of
		// operator-facing tokens, not tenant content, and "which failure was being
		// recorded" is the first thing an operator needs.
		h.fail(w, failWrite, route, err, "workspace_id", ws, "pending_id", pending, "reason", in.Reason)
		return
	}
	respond(w, http.StatusOK, ackResponse{})
}
