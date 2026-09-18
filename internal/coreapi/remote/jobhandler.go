package remote

import (
	"errors"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/inroad/inroad/internal/coreapi"
)

// The control plane's side of the per-message job reads.
//
// Every one of these is the same four steps — decode under a cap with unknown
// fields refused, parse the ids, call the SAME in-process implementation the
// single-process topology calls, map the error — and they are written out one
// by one rather than generated from a table, because the ids differ per route
// and a table would have to carry them as an []string that nothing type-checks.
//
// WORKSPACE PINNING. The pin arrives on the wire because there is no session
// here to derive it from; the reader then applies the same workspace_id SQL
// filter the in-process path applies, including its belt-and-braces
// coreapi.ErrCrossTenant check. The wire ADDS a pin, it does not replace one.
// checkSuppression's doc has the full argument, and it applies unchanged to
// every route below.
//
// CREDENTIALS. None of these responses carries one. The in-process build this
// delegates to still opens the mailbox credential — it is one code path, and
// splitting it so this caller could skip the open would mean two job builds
// that can disagree — and the `json:"-"` tags on the coreapi job types are what
// keep the plaintext off the wire. That open costs an AES-GCM decrypt of an
// already-cached DEK, in the process that holds the key anyway. The worker gets
// its credential from the credential broker on the same listener.

func (h *handler) stepSendJob(w http.ResponseWriter, r *http.Request) {
	var in enrollmentRequest
	if !decode(w, r, &in) {
		return
	}
	ws, enrollment, ok := parsePair(w, in.WorkspaceID, "enrollment_id", in.EnrollmentID)
	if !ok {
		return
	}
	job, err := h.jobs.GetStepSendJob(r.Context(), enrollment.String(), ws.String())
	if err != nil {
		h.fail(w, failRead, "step send job", err, "workspace_id", ws, "enrollment_id", enrollment)
		return
	}
	respond(w, http.StatusOK, stepSendJobResponse{Job: job})
}

func (h *handler) inboxPollJob(w http.ResponseWriter, r *http.Request) {
	var in mailboxRequest
	if !decode(w, r, &in) {
		return
	}
	ws, mailbox, ok := parsePair(w, in.WorkspaceID, "mailbox_id", in.MailboxID)
	if !ok {
		return
	}
	job, err := h.jobs.GetInboxPollJob(r.Context(), mailbox.String(), ws.String())
	if err != nil {
		h.fail(w, failRead, "inbox poll job", err, "workspace_id", ws, "mailbox_id", mailbox)
		return
	}
	respond(w, http.StatusOK, inboxPollJobResponse{Job: job})
}

func (h *handler) warmupSendJob(w http.ResponseWriter, r *http.Request) {
	var in mailboxRequest
	if !decode(w, r, &in) {
		return
	}
	ws, mailbox, ok := parsePair(w, in.WorkspaceID, "mailbox_id", in.MailboxID)
	if !ok {
		return
	}
	job, err := h.jobs.GetWarmupSendJob(r.Context(), mailbox.String(), ws.String())
	if err != nil {
		h.fail(w, failRead, "warmup send job", err, "workspace_id", ws, "mailbox_id", mailbox)
		return
	}
	respond(w, http.StatusOK, warmupSendJobResponse{Job: job})
}

func (h *handler) warmupEngageJob(w http.ResponseWriter, r *http.Request) {
	var in receiptRequest
	if !decode(w, r, &in) {
		return
	}
	ws, receipt, ok := parsePair(w, in.WorkspaceID, "receipt_id", in.ReceiptID)
	if !ok {
		return
	}
	job, err := h.jobs.GetWarmupEngageJob(r.Context(), receipt.String(), ws.String())
	if err != nil {
		h.fail(w, failRead, "warmup engage job", err, "workspace_id", ws, "receipt_id", receipt)
		return
	}
	respond(w, http.StatusOK, warmupEngageJobResponse{Job: job})
}

func (h *handler) webhookDeliveryJob(w http.ResponseWriter, r *http.Request) {
	var in deliveryRequest
	if !decode(w, r, &in) {
		return
	}
	ws, delivery, ok := parsePair(w, in.WorkspaceID, "delivery_id", in.DeliveryID)
	if !ok {
		return
	}
	job, err := h.jobs.GetWebhookDeliveryJob(r.Context(), delivery.String(), ws.String())
	if err != nil {
		h.fail(w, failRead, "webhook delivery job", err, "workspace_id", ws, "delivery_id", delivery)
		return
	}
	respond(w, http.StatusOK, webhookDeliveryJobResponse{Job: job})
}

func (h *handler) testSendContent(w http.ResponseWriter, r *http.Request) {
	var in testSendContentRequest
	if !decode(w, r, &in) {
		return
	}
	ws, campaign, ok := parsePair(w, in.WorkspaceID, "campaign_id", in.CampaignID)
	if !ok {
		return
	}
	step, err := uuid.Parse(in.StepID)
	if err != nil {
		respond(w, http.StatusBadRequest, errorResponse{Error: "invalid step_id"})
		return
	}
	content, err := h.jobs.GetTestSendContent(r.Context(), ws.String(), campaign.String(), step.String())
	if err != nil {
		h.fail(w, failRead, "test send content", err, "workspace_id", ws, "campaign_id", campaign, "step_id", step)
		return
	}
	respond(w, http.StatusOK, testSendContentResponse{Content: content})
}

func (h *handler) senderTransport(w http.ResponseWriter, r *http.Request) {
	var in mailboxRequest
	if !decode(w, r, &in) {
		return
	}
	ws, mailbox, ok := parsePair(w, in.WorkspaceID, "mailbox_id", in.MailboxID)
	if !ok {
		return
	}
	transport, err := h.jobs.ResolveSenderTransport(r.Context(), ws.String(), mailbox.String())
	if err != nil {
		h.fail(w, failRead, "sender transport", err, "workspace_id", ws, "mailbox_id", mailbox)
		return
	}
	respond(w, http.StatusOK, senderTransportResponse{Transport: transport})
}

func (h *handler) sendByMessageID(w http.ResponseWriter, r *http.Request) {
	var in messageIDRequest
	if !decode(w, r, &in) {
		return
	}
	ws, err := uuid.Parse(in.WorkspaceID)
	if err != nil {
		respond(w, http.StatusBadRequest, errorResponse{Error: "invalid workspace_id"})
		return
	}
	// The Message-ID is NOT validated here, exactly as the address on the
	// suppression route is not: it is whatever the inbound message carried, the
	// in-process query would simply match nothing, and an extra check on one
	// transport is a behaviour difference between the two.
	send, err := h.jobs.FindSendByMessageID(r.Context(), ws.String(), in.MessageID)
	if err != nil {
		// The Message-ID is deliberately absent from the log arguments: it is
		// content from a tenant's inbound mail, and ErrNoMatch — by far the
		// most common outcome — does not reach the log at all (see fail).
		h.fail(w, failRead, "send by message id", err, "workspace_id", ws)
		return
	}
	respond(w, http.StatusOK, sendRefResponse{Send: send})
}

// parsePair parses the workspace id and one subject id, answering 400 on
// either. subjectField names the request field so an operator debugging a
// worker sees which id was wrong — it is a field NAME from this package's own
// wire contract, never a value.
func parsePair(w http.ResponseWriter, workspaceID, subjectField, subjectID string) (uuid.UUID, uuid.UUID, bool) {
	ws, err := uuid.Parse(workspaceID)
	if err != nil {
		respond(w, http.StatusBadRequest, errorResponse{Error: "invalid workspace_id"})
		return uuid.Nil, uuid.Nil, false
	}
	subject, err := uuid.Parse(subjectID)
	if err != nil {
		respond(w, http.StatusBadRequest, errorResponse{Error: "invalid " + subjectField})
		return uuid.Nil, uuid.Nil, false
	}
	return ws, subject, true
}

// failKind is what a failure was a failure OF. It exists so the read routes and
// the outcome-write routes share ONE sentinel mapping — the part that must not
// diverge — while still saying the right thing in a log line and a body.
type failKind struct{ logMsg, bodyPrefix string }

var (
	failRead  = failKind{logMsg: "coreapi remote: job read failed", bodyPrefix: "could not read "}
	failWrite = failKind{logMsg: "coreapi remote: outcome write failed", bodyPrefix: "could not record "}
)

// fail maps an implementation error to a status and a FIXED body, and logs the
// real one.
//
// FOUR sentinels are reported as themselves, because the execution plane branches
// on them and a generic failure would change what the worker does. None of the
// four is logged: every one is an ORDINARY answer rather than a fault, and
// logging them would emit a line per non-campaign email in the inbox and a line
// per undone reply.
//
// As 404 — the named row is gone or nothing matched:
//
//   - coreapi.ErrNoMatch is the ordinary answer for nearly every inbound
//     message the poller sees.
//   - pgx.ErrNoRows means the named row is gone (a delivery deleted after its
//     task was queued, a vanished receipt). The webhook worker drops the task
//     on it rather than retrying to exhaustion.
//
// As 409 — the row is THERE and its state forbids the transition (slice 3b; see
// wire.go for why these must not share 404):
//
//   - coreapi.ErrInboxPendingNotClaimable is the operator's undo, an already-sent
//     row, one not yet due, or another worker's live lease. worker/inbox stops on
//     it; anything else would retry an undone reply to exhaustion and write it
//     into task_dead_letters.
//   - coreapi.ErrInboxNoInbound is a thread with nothing to reply to. Permanent —
//     no retry can build that reply.
//
// Everything else is a 500 whose body is this package's own string. The
// implementation's error text is never relayed: it can carry a pg message or
// request detail, and relaying it would make this seam a probe oracle and put a
// database message in a fleet host's log.
//
// A 500 on an OUTCOME route is what the whole slice rests on: it fails the CALL,
// the worker returns the error, asynq retries the task, and the retry passes
// through the claim — which is the only thing that knows whether the write
// landed. So this never guesses, and it never answers 200 on a failure.
func (h *handler) fail(w http.ResponseWriter, k failKind, route string, err error, logArgs ...any) {
	switch {
	case errors.Is(err, coreapi.ErrNoMatch):
		respond(w, http.StatusNotFound, errorResponse{Error: "no matching send", Code: codeNoMatch})
	case errors.Is(err, pgx.ErrNoRows):
		respond(w, http.StatusNotFound, errorResponse{Error: "not found", Code: codeNotFound})
	case errors.Is(err, coreapi.ErrInboxPendingNotClaimable):
		respond(w, http.StatusConflict, errorResponse{Error: "not claimable", Code: codeNotClaimable})
	case errors.Is(err, coreapi.ErrInboxNoInbound):
		respond(w, http.StatusConflict, errorResponse{Error: "no inbound message to reply to", Code: codeNoInbound})
	default:
		h.logger.Error(k.logMsg, append(logArgs, "route", route, "err", err)...)
		respond(w, http.StatusInternalServerError, errorResponse{Error: k.bodyPrefix + route})
	}
}
