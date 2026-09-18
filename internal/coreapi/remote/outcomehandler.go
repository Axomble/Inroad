package remote

import (
	"net/http"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/coreapi"
)

// The control plane's side of the claim and outcome path.
//
// Every one of these is the same four steps the job routes are — decode under a
// cap with unknown fields refused, pin the workspace, call the SAME in-process
// implementation the single-process topology calls, map the error — and they
// are written out one by one for the same reason: a table would have to carry
// the per-route ids and shapes as values nothing type-checks.
//
// WORKSPACE PINNING. Identical to the read routes: the pin arrives on the wire
// because there is no session here to derive it from, and the implementation
// then applies the same workspace_id SQL filter the in-process path applies. On
// the job-carrying routes the pin is checked TWICE — once as the envelope's own
// field, once against the workspace inside the job — and a disagreement is a 400
// before anything runs. See checkSuppression's doc for the full argument about
// why a wire pin ADDS to the tenant filter rather than replacing it.
//
// NOTHING IS GUESSED. A failed implementation call is a 500 with a fixed body.
// It is never a 200 with a zero value, and a claim is never answered "won" on a
// path that did not take one — the worker's retry has to reach the real claim
// for the protocol to mean anything.

// claimStepSend claims one step-send and answers the four-state protocol.
func (h *handler) claimStepSend(w http.ResponseWriter, r *http.Request) {
	var in stepJobRequest
	if !decodeJob(w, r, &in) {
		return
	}
	ws, ok := pinnedWorkspace(w, in.WorkspaceID, in.Job.WorkspaceID)
	if !ok {
		return
	}
	outcome, err := h.outcomes.ClaimStepSend(r.Context(), in.Job)
	if err != nil {
		h.fail(w, failWrite, "step send claim", err, "workspace_id", ws)
		return
	}
	h.respondClaim(w, "step send claim", ws, outcome)
}

// markStepDelivered records a delivery in its own committed statement.
func (h *handler) markStepDelivered(w http.ResponseWriter, r *http.Request) {
	var in stepDeliveredRequest
	if !decodeJob(w, r, &in) {
		return
	}
	ws, ok := pinnedWorkspace(w, in.WorkspaceID, in.Job.WorkspaceID)
	if !ok {
		return
	}
	if err := h.outcomes.MarkStepDelivered(r.Context(), in.Job, in.MessageID); err != nil {
		// The Message-ID is absent from the log arguments: it is a header off a
		// tenant's own outbound mail, and the send id below identifies the row
		// an operator would look at anyway.
		h.fail(w, failWrite, "step send delivery", err, "workspace_id", ws, "send_id", in.Job.SendID)
		return
	}
	respond(w, http.StatusOK, ackResponse{})
}

// advanceStepCursor advances the enrollment cursor. Also the recover-forward
// call, which is why it is a separate route from the delivery above: merging
// them would delete the window the recovery exists to survive.
func (h *handler) advanceStepCursor(w http.ResponseWriter, r *http.Request) {
	var in stepJobRequest
	if !decodeJob(w, r, &in) {
		return
	}
	ws, ok := pinnedWorkspace(w, in.WorkspaceID, in.Job.WorkspaceID)
	if !ok {
		return
	}
	adv, err := h.outcomes.AdvanceStepCursor(r.Context(), in.Job)
	if err != nil {
		h.fail(w, failWrite, "step cursor advance", err, "workspace_id", ws, "enrollment_id", in.Job.EnrollmentID)
		return
	}
	respond(w, http.StatusOK, advanceResponse{Advance: adv})
}

// releaseStepSend expires a claim's lease after a retryable failure.
func (h *handler) releaseStepSend(w http.ResponseWriter, r *http.Request) {
	var in stepJobRequest
	if !decodeJob(w, r, &in) {
		return
	}
	ws, ok := pinnedWorkspace(w, in.WorkspaceID, in.Job.WorkspaceID)
	if !ok {
		return
	}
	if err := h.outcomes.ReleaseStepSend(r.Context(), in.Job); err != nil {
		h.fail(w, failWrite, "step send release", err, "workspace_id", ws, "send_id", in.Job.SendID)
		return
	}
	respond(w, http.StatusOK, ackResponse{})
}

// finalizeStepSend finalizes a step to a non-'sent' terminal state and advances
// the cursor in one transaction.
func (h *handler) finalizeStepSend(w http.ResponseWriter, r *http.Request) {
	var in stepFinalizeRequest
	if !decodeJob(w, r, &in) {
		return
	}
	ws, ok := pinnedWorkspace(w, in.WorkspaceID, in.Job.WorkspaceID)
	if !ok {
		return
	}
	adv, err := h.outcomes.FinalizeStepSend(r.Context(), in.Job, in.Result)
	if err != nil {
		// Result.Err is the provider's rejection text and is NOT logged here: it
		// is stored on the send row for the operator, and echoing it into the
		// control plane's log would put a recipient address (bounce text often
		// quotes one) in a second place.
		h.fail(w, failWrite, "step send finalize", err, "workspace_id", ws, "send_id", in.Job.SendID)
		return
	}
	respond(w, http.StatusOK, advanceResponse{Advance: adv})
}

// markStepStopped halts one enrollment.
func (h *handler) markStepStopped(w http.ResponseWriter, r *http.Request) {
	var in enrollmentStopRequest
	if !decode(w, r, &in) {
		return
	}
	ws, enrollment, ok := parsePair(w, in.WorkspaceID, "enrollment_id", in.EnrollmentID)
	if !ok {
		return
	}
	if err := h.outcomes.MarkStepStopped(r.Context(), enrollment.String(), ws.String(), in.Reason); err != nil {
		// The reason IS logged: it is a value from the enrollment state
		// machine's own closed vocabulary, not tenant content, and "which stop
		// was refused" is the first thing an operator needs.
		h.fail(w, failWrite, "enrollment stop", err, "workspace_id", ws, "enrollment_id", enrollment, "reason", in.Reason)
		return
	}
	respond(w, http.StatusOK, ackResponse{})
}

// deferEnrollment pushes an active enrollment's next_due_at out.
func (h *handler) deferEnrollment(w http.ResponseWriter, r *http.Request) {
	var in enrollmentDeferRequest
	if !decode(w, r, &in) {
		return
	}
	ws, enrollment, ok := parsePair(w, in.WorkspaceID, "enrollment_id", in.EnrollmentID)
	if !ok {
		return
	}
	if err := h.outcomes.DeferEnrollment(r.Context(), enrollment.String(), ws.String(), in.Until); err != nil {
		h.fail(w, failWrite, "enrollment defer", err, "workspace_id", ws, "enrollment_id", enrollment)
		return
	}
	respond(w, http.StatusOK, ackResponse{})
}

// incrementCapDeferrals bumps the cap-deferral counter and returns the new value.
func (h *handler) incrementCapDeferrals(w http.ResponseWriter, r *http.Request) {
	var in enrollmentRequest
	if !decode(w, r, &in) {
		return
	}
	ws, enrollment, ok := parsePair(w, in.WorkspaceID, "enrollment_id", in.EnrollmentID)
	if !ok {
		return
	}
	n, err := h.outcomes.IncrementEnrollmentCapDeferrals(r.Context(), enrollment.String(), ws.String())
	if err != nil {
		h.fail(w, failWrite, "enrollment cap deferral", err, "workspace_id", ws, "enrollment_id", enrollment)
		return
	}
	respond(w, http.StatusOK, capDeferralResponse{Deferrals: n})
}

// claimWarmupSend claims one warmup send.
func (h *handler) claimWarmupSend(w http.ResponseWriter, r *http.Request) {
	var in warmupJobRequest
	if !decodeJob(w, r, &in) {
		return
	}
	ws, ok := pinnedWorkspace(w, in.WorkspaceID, in.Job.WorkspaceID)
	if !ok {
		return
	}
	outcome, err := h.outcomes.ClaimWarmupSend(r.Context(), in.Job)
	if err != nil {
		h.fail(w, failWrite, "warmup send claim", err, "workspace_id", ws)
		return
	}
	h.respondClaim(w, "warmup send claim", ws, outcome)
}

// markWarmupSent finalizes a claimed warmup send to 'sent'.
func (h *handler) markWarmupSent(w http.ResponseWriter, r *http.Request) {
	var in warmupSentRequest
	if !decodeJob(w, r, &in) {
		return
	}
	ws, ok := pinnedWorkspace(w, in.WorkspaceID, in.Job.WorkspaceID)
	if !ok {
		return
	}
	if err := h.outcomes.MarkWarmupSent(r.Context(), in.Job, in.MessageID); err != nil {
		h.fail(w, failWrite, "warmup send finalize", err, "workspace_id", ws, "send_id", in.Job.SendID)
		return
	}
	respond(w, http.StatusOK, ackResponse{})
}

// releaseWarmupSend returns a claimed row to 'queued'.
func (h *handler) releaseWarmupSend(w http.ResponseWriter, r *http.Request) {
	var in warmupJobRequest
	if !decodeJob(w, r, &in) {
		return
	}
	ws, ok := pinnedWorkspace(w, in.WorkspaceID, in.Job.WorkspaceID)
	if !ok {
		return
	}
	if err := h.outcomes.ReleaseWarmupSend(r.Context(), in.Job); err != nil {
		h.fail(w, failWrite, "warmup send release", err, "workspace_id", ws, "send_id", in.Job.SendID)
		return
	}
	respond(w, http.StatusOK, ackResponse{})
}

// failWarmupSend finalizes a claimed row to 'failed'.
func (h *handler) failWarmupSend(w http.ResponseWriter, r *http.Request) {
	var in warmupFailRequest
	if !decodeJob(w, r, &in) {
		return
	}
	ws, ok := pinnedWorkspace(w, in.WorkspaceID, in.Job.WorkspaceID)
	if !ok {
		return
	}
	if err := h.outcomes.FailWarmupSend(r.Context(), in.Job, in.Error); err != nil {
		h.fail(w, failWrite, "warmup send fail", err, "workspace_id", ws, "send_id", in.Job.SendID)
		return
	}
	respond(w, http.StatusOK, ackResponse{})
}

// markWarmupEngaged flips one receipt's engaged guard.
func (h *handler) markWarmupEngaged(w http.ResponseWriter, r *http.Request) {
	var in warmupEngagedRequest
	if !decode(w, r, &in) {
		return
	}
	ws, receipt, ok := parsePair(w, in.WorkspaceID, "receipt_id", in.ReceiptID)
	if !ok {
		return
	}
	if err := h.outcomes.MarkWarmupEngaged(r.Context(), receipt.String(), ws.String(), in.Replied); err != nil {
		h.fail(w, failWrite, "warmup engagement", err, "workspace_id", ws, "receipt_id", receipt)
		return
	}
	respond(w, http.StatusOK, ackResponse{})
}

// markReplied stops the enrollment on an inbound reply and tags it.
func (h *handler) markReplied(w http.ResponseWriter, r *http.Request) {
	h.replyClass(w, r, "reply", func(in replyClassRequest, ws uuid.UUID) error {
		return h.outcomes.MarkReplied(r.Context(), in.EnrollmentID, ws.String(), in.Class, in.Source, in.Confidence)
	})
}

// recordReplyClass tags the enrollment WITHOUT stopping it.
func (h *handler) recordReplyClass(w http.ResponseWriter, r *http.Request) {
	h.replyClass(w, r, "reply class", func(in replyClassRequest, ws uuid.UUID) error {
		return h.outcomes.RecordReplyClass(r.Context(), in.EnrollmentID, ws.String(), in.Class, in.Source, in.Confidence)
	})
}

// replyClass is the shared half of the two routes above, which take the same
// request and differ only in whether the enrollment stops. The enrollment id is
// passed through VERBATIM — including the empty string, which means the matched
// send had no enrollment and makes both methods a no-op in process.
//
// The class/source/confidence are NOT validated here. The classifier's
// vocabulary is enforced by the reply_class CHECK constraint in the database,
// which both transports reach, and a second vocabulary maintained on this side
// would be one more thing to forget when a label is added.
func (h *handler) replyClass(w http.ResponseWriter, r *http.Request, route string,
	call func(replyClassRequest, uuid.UUID) error,
) {
	var in replyClassRequest
	if !decode(w, r, &in) {
		return
	}
	ws, ok := parseWorkspace(w, in.WorkspaceID)
	if !ok {
		return
	}
	if !optionalEnrollment(w, in.EnrollmentID) {
		return
	}
	if err := call(in, ws); err != nil {
		// The class IS logged (a closed vocabulary, not content); nothing else
		// from the reply is.
		h.fail(w, failWrite, route, err, "workspace_id", ws, "enrollment_id", in.EnrollmentID, "class", in.Class)
		return
	}
	respond(w, http.StatusOK, ackResponse{})
}

// markUnsubscribed suppresses the address and stops the enrollment if there is
// one. The suppression runs even with no enrollment — compliance.
func (h *handler) markUnsubscribed(w http.ResponseWriter, r *http.Request) {
	var in unsubscribeRequest
	if !decode(w, r, &in) {
		return
	}
	ws, ok := parseWorkspace(w, in.WorkspaceID)
	if !ok {
		return
	}
	if !optionalEnrollment(w, in.EnrollmentID) {
		return
	}
	if err := h.outcomes.MarkUnsubscribed(r.Context(), in.EnrollmentID, ws.String(), in.Email); err != nil {
		// The ADDRESS is not logged: it is a tenant's contact, exactly as on the
		// suppression check route.
		h.fail(w, failWrite, "unsubscribe", err, "workspace_id", ws, "enrollment_id", in.EnrollmentID)
		return
	}
	respond(w, http.StatusOK, ackResponse{})
}

// markBounced records a hard bounce.
func (h *handler) markBounced(w http.ResponseWriter, r *http.Request) {
	var in bounceRequest
	if !decode(w, r, &in) {
		return
	}
	ws, ok := parseWorkspace(w, in.WorkspaceID)
	if !ok {
		return
	}
	if !optionalEnrollment(w, in.EnrollmentID) {
		return
	}
	if err := h.outcomes.MarkBounced(r.Context(), in.EnrollmentID, ws.String(), in.Email, in.Hard); err != nil {
		h.fail(w, failWrite, "bounce", err, "workspace_id", ws, "enrollment_id", in.EnrollmentID)
		return
	}
	respond(w, http.StatusOK, ackResponse{})
}

// markWebhookDelivered finalizes one delivery to 'delivered'.
func (h *handler) markWebhookDelivered(w http.ResponseWriter, r *http.Request) {
	var in webhookDeliveredRequest
	if !decode(w, r, &in) {
		return
	}
	ws, delivery, ok := parsePair(w, in.WorkspaceID, "delivery_id", in.DeliveryID)
	if !ok {
		return
	}
	if err := h.outcomes.MarkWebhookDelivered(r.Context(), delivery.String(), ws.String(), in.Attempts, in.ResponseStatus); err != nil {
		h.fail(w, failWrite, "webhook delivery", err, "workspace_id", ws, "delivery_id", delivery)
		return
	}
	respond(w, http.StatusOK, ackResponse{})
}

// markWebhookRetrying records a failed attempt that still has retries left.
func (h *handler) markWebhookRetrying(w http.ResponseWriter, r *http.Request) {
	var in webhookRetryingRequest
	if !decode(w, r, &in) {
		return
	}
	ws, delivery, ok := parsePair(w, in.WorkspaceID, "delivery_id", in.DeliveryID)
	if !ok {
		return
	}
	// LastError is the RECEIVER's error text, from a host a tenant chose. It is
	// stored on the row for that tenant to read and is not logged here.
	if err := h.outcomes.MarkWebhookRetrying(r.Context(), delivery.String(), ws.String(),
		in.Attempts, in.LastError, in.ResponseStatus, in.NextAttemptAt); err != nil {
		h.fail(w, failWrite, "webhook retry", err, "workspace_id", ws, "delivery_id", delivery)
		return
	}
	respond(w, http.StatusOK, ackResponse{})
}

// markWebhookFailed finalizes one delivery to 'failed'.
func (h *handler) markWebhookFailed(w http.ResponseWriter, r *http.Request) {
	var in webhookFailedRequest
	if !decode(w, r, &in) {
		return
	}
	ws, delivery, ok := parsePair(w, in.WorkspaceID, "delivery_id", in.DeliveryID)
	if !ok {
		return
	}
	if err := h.outcomes.MarkWebhookFailed(r.Context(), delivery.String(), ws.String(),
		in.Attempts, in.LastError, in.ResponseStatus); err != nil {
		h.fail(w, failWrite, "webhook failure", err, "workspace_id", ws, "delivery_id", delivery)
		return
	}
	respond(w, http.StatusOK, ackResponse{})
}

// respondClaim encodes the four-state protocol, refusing rather than guessing.
//
// An outcome the wire vocabulary does not carry is a 500, not a best-effort
// string: it can only mean coreapi.ClaimOutcome grew a state this transport was
// not taught, and shipping the nearest neighbour would be the transport deciding
// a send question on its own.
func (h *handler) respondClaim(w http.ResponseWriter, route string, ws uuid.UUID, outcome coreapi.ClaimOutcome) {
	encoded, err := encodeClaimOutcome(outcome)
	if err != nil {
		h.fail(w, failWrite, route, err, "workspace_id", ws)
		return
	}
	respond(w, http.StatusOK, claimOutcomeResponse{Outcome: encoded})
}

// pinnedWorkspace parses the envelope's workspace and checks the job inside
// agrees with it.
//
// The comparison is on the PARSED uuids, not the strings: uuid.Parse accepts
// braced and urn: forms, so two spellings of the same workspace are the same
// tenant and must not be refused as a mismatch.
func pinnedWorkspace(w http.ResponseWriter, envelope, job string) (uuid.UUID, bool) {
	ws, ok := parseWorkspace(w, envelope)
	if !ok {
		return uuid.Nil, false
	}
	inner, err := uuid.Parse(job)
	if err != nil {
		respond(w, http.StatusBadRequest, errorResponse{Error: "invalid job workspace_id"})
		return uuid.Nil, false
	}
	if inner != ws {
		// Not a tenancy breach in itself — the implementation pins on the job's
		// own workspace either way — but it is a request no correct worker
		// assembles, so it is refused rather than silently resolved in favour of
		// one of the two.
		respond(w, http.StatusBadRequest, errorResponse{Error: "workspace_id does not match the job"})
		return uuid.Nil, false
	}
	return ws, true
}

func parseWorkspace(w http.ResponseWriter, workspaceID string) (uuid.UUID, bool) {
	ws, err := uuid.Parse(workspaceID)
	if err != nil {
		respond(w, http.StatusBadRequest, errorResponse{Error: "invalid workspace_id"})
		return uuid.Nil, false
	}
	return ws, true
}

// optionalEnrollment accepts the empty string — the three reply outcomes take an
// enrollment id that is legitimately absent when the matched send had none — and
// rejects any other non-uuid.
//
// Refusing the empty string here would be worse than pedantic: the poller cannot
// distinguish a 400 from a real failure, so it would return before
// SetInboxCursor and the mailbox would stop processing ALL inbound mail.
func optionalEnrollment(w http.ResponseWriter, enrollmentID string) bool {
	if enrollmentID == "" {
		return true
	}
	if _, err := uuid.Parse(enrollmentID); err != nil {
		respond(w, http.StatusBadRequest, errorResponse{Error: "invalid enrollment_id"})
		return false
	}
	return true
}
