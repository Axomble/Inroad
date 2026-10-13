package remote

import (
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/coreapi"
)

// The control plane's side of the INBOUND MAIL path (slice 4).
//
// Same four steps as every other route family here — decode under a cap with
// unknown fields refused, pin the workspace, call the SAME in-process
// implementation the single-process topology calls, map the error — written out
// one by one rather than tabled, because a table would carry the per-route ids
// and shapes as values nothing type-checks.
//
// WORKSPACE PINNING. Identical to the read and outcome routes: the pin arrives
// on the wire because there is no session here to derive it from, and the
// implementation then applies the same workspace_id SQL filter the in-process
// path applies. On the three input-carrying routes it is checked TWICE — once
// as the envelope's field, once against the workspace inside the input — and a
// disagreement is a 400 before anything runs. See checkSuppression's doc for
// the full argument about why a wire pin ADDS to the tenant filter.
//
// NOTHING IS LOGGED THAT A MESSAGE CARRIED. No body, no subject, no address, no
// Message-ID, no cursor value — the log arguments below are ids and nothing
// else. Two of these routes carry a tenant's own correspondence and the rest
// carry values chosen by whoever sent the mail; the ids identify the row an
// operator would look at anyway.

// setInboxCursorUID advances one mailbox's IMAP poll cursor.
func (h *handler) setInboxCursorUID(w http.ResponseWriter, r *http.Request) {
	var in inboxCursorUIDRequest
	if !decode(w, r, &in) {
		return
	}
	ws, mailbox, ok := parsePair(w, in.WorkspaceID, "mailbox_id", in.MailboxID)
	if !ok {
		return
	}
	if err := h.inbound.SetInboxCursor(r.Context(), mailbox.String(), ws.String(), in.LastSeenUID, in.UIDValidity); err != nil {
		h.fail(w, failWrite, "inbox cursor", err, "workspace_id", ws, "mailbox_id", mailbox)
		return
	}
	respond(w, http.StatusOK, ackResponse{})
}

// setInboxCursorString advances one mailbox's opaque provider cursor.
func (h *handler) setInboxCursorString(w http.ResponseWriter, r *http.Request) {
	var in inboxCursorStringRequest
	if !decode(w, r, &in) {
		return
	}
	ws, mailbox, ok := parsePair(w, in.WorkspaceID, "mailbox_id", in.MailboxID)
	if !ok {
		return
	}
	if err := h.inbound.SetInboxCursorString(r.Context(), mailbox.String(), ws.String(), in.Cursor); err != nil {
		// The cursor VALUE is absent from the log arguments. It is an opaque
		// provider URL (a Graph delta link) that can carry a tenant's mailbox
		// identity in its query string, and docs/security.md invariant 13 already
		// forbids logging one verbatim.
		h.fail(w, failWrite, "inbox cursor", err, "workspace_id", ws, "mailbox_id", mailbox)
		return
	}
	respond(w, http.StatusOK, ackResponse{})
}

// recordInboxPollFailure records one failed poll and schedules the next attempt.
//
// The ladder is validated HERE as well as at the client, because this is a
// process boundary: the client's check protects a developer from a mistake, and
// this one protects the database from an unbounded or degenerate array arriving
// from a host the control plane does not compile.
func (h *handler) recordInboxPollFailure(w http.ResponseWriter, r *http.Request) {
	var in inboxPollFailureRequest
	if !decode(w, r, &in) {
		return
	}
	ws, mailbox, ok := parsePair(w, in.WorkspaceID, "mailbox_id", in.MailboxID)
	if !ok {
		return
	}
	ladder := make([]time.Duration, len(in.BackoffSeconds))
	for i, s := range in.BackoffSeconds {
		ladder[i] = time.Duration(s * float64(time.Second))
	}
	if coreapi.ValidateBackoffLadder(ladder) != nil {
		// The reason is not echoed: it is derived from caller input, and this
		// route's replies stay as fixed as every other one here.
		respond(w, http.StatusBadRequest, errorResponse{Error: "invalid backoff_seconds"})
		return
	}
	out, err := h.inbound.RecordInboxPollFailure(r.Context(), mailbox.String(), ws.String(), ladder)
	if err != nil {
		h.fail(w, failWrite, "inbox poll failure", err, "workspace_id", ws, "mailbox_id", mailbox)
		return
	}
	respond(w, http.StatusOK, inboxPollFailureResponse{Failures: out.Failures, RetryAfter: out.RetryAfter})
}

// storeInboundMessage writes one matched inbound message onto its thread.
//
// decodeMessage, not decode: the body is a customer's reply, capped by the same
// ceiling the manual-reply record uses (see maxMessageRequestBytes). The 64 KiB
// ids-only cap would refuse a long reply, and refusing one means the poller
// returns before SetInboxCursor and the mailbox stops processing inbound mail
// entirely.
func (h *handler) storeInboundMessage(w http.ResponseWriter, r *http.Request) {
	var in inboxMessageRequest
	if !decodeMessage(w, r, &in) {
		return
	}
	ws, ok := pinnedWorkspace(w, in.WorkspaceID, in.Message.WorkspaceID)
	if !ok {
		return
	}
	if err := h.inbound.StoreInboundMessage(r.Context(), in.Message); err != nil {
		h.fail(w, failWrite, "inbound message", err, "workspace_id", ws, "mailbox_id", in.Message.MailboxID)
		return
	}
	respond(w, http.StatusOK, ackResponse{})
}

// captureCRMReply records one positive reply as a CRM activity.
func (h *handler) captureCRMReply(w http.ResponseWriter, r *http.Request) {
	var in crmReplyRequest
	if !decodeMessage(w, r, &in) {
		return
	}
	ws, ok := pinnedWorkspace(w, in.WorkspaceID, in.Reply.WorkspaceID)
	if !ok {
		return
	}
	if err := h.inbound.CaptureCRMReply(r.Context(), in.Reply); err != nil {
		h.fail(w, failWrite, "crm reply capture", err, "workspace_id", ws, "send_id", in.Reply.SendID)
		return
	}
	respond(w, http.StatusOK, ackResponse{})
}

// ingestComplaint records one complaint that arrived as mail.
//
// A permanently invalid input becomes a 422 carrying codeInvalidComplaint (see
// fail), which the worker reads back as coreapi.ErrInvalidComplaint and SKIPS.
// That mapping is the difference between one declined report and a mailbox
// whose cursor never moves again.
func (h *handler) ingestComplaint(w http.ResponseWriter, r *http.Request) {
	var in complaintRequest
	if !decode(w, r, &in) {
		return
	}
	ws, ok := pinnedWorkspace(w, in.WorkspaceID, in.Complaint.WorkspaceID)
	if !ok {
		return
	}
	if err := h.inbound.IngestComplaint(r.Context(), in.Complaint); err != nil {
		// The complained address is absent from the log arguments: it is a
		// tenant's contact. The send id identifies the row.
		h.fail(w, failWrite, "complaint ingest", err, "workspace_id", ws, "send_id", in.Complaint.SendID)
		return
	}
	respond(w, http.StatusOK, ackResponse{})
}

// resolveReplyLabel resolves one classified reply key to the workspace's label.
//
// found=false is a 200 with Found:false, never a 404. It is the ordinary answer
// for a deleted custom label whose key survives on historical rows, and the
// caller must fall back to its pre-taxonomy class switch rather than fail the
// poll — which a 404 with no recognised code would make it do.
func (h *handler) resolveReplyLabel(w http.ResponseWriter, r *http.Request) {
	var in replyLabelRequest
	if !decode(w, r, &in) {
		return
	}
	ws, err := uuid.Parse(in.WorkspaceID)
	if err != nil {
		respond(w, http.StatusBadRequest, errorResponse{Error: "invalid workspace_id"})
		return
	}
	label, found, err := h.inbound.ResolveReplyLabel(r.Context(), ws.String(), in.Key)
	if err != nil {
		h.fail(w, failRead, "reply label", err, "workspace_id", ws)
		return
	}
	respond(w, http.StatusOK, replyLabelResponse{Found: found, Label: label})
}

// recordWarmupReceipt records one received warmup message and answers with the
// deterministic engagement plan.
func (h *handler) recordWarmupReceipt(w http.ResponseWriter, r *http.Request) {
	var in warmupReceiptRequest
	if !decode(w, r, &in) {
		return
	}
	ws, ok := pinnedWorkspace(w, in.WorkspaceID, in.Receipt.WorkspaceID)
	if !ok {
		return
	}
	plan, err := h.inbound.RecordWarmupReceipt(r.Context(), in.Receipt)
	if err != nil {
		h.fail(w, failWrite, "warmup receipt", err, "workspace_id", ws, "warmup_send_id", in.Receipt.WarmupSendID)
		return
	}
	respond(w, http.StatusOK, warmupEngagePlanResponse{Plan: plan})
}

// warmupSendByMessageID resolves an inbound message back to a warmup send when
// the token header did not survive the provider. found=false is a 200, for the
// reason resolveReplyLabel's is: it is the answer for nearly every message.
func (h *handler) warmupSendByMessageID(w http.ResponseWriter, r *http.Request) {
	var in warmupSendLookupRequest
	if !decode(w, r, &in) {
		return
	}
	ws, mailbox, ok := parsePair(w, in.WorkspaceID, "to_mailbox_id", in.ToMailboxID)
	if !ok {
		return
	}
	// The Message-ID is not validated, exactly as on the campaign lookup: it is
	// whatever the inbound message carried, and the implementation's own
	// angle-bracket normalisation is what both transports share.
	send, found, err := h.inbound.FindWarmupSendByMessageID(r.Context(), ws.String(), mailbox.String(), in.MessageID)
	if err != nil {
		h.fail(w, failRead, "warmup send by message id", err, "workspace_id", ws, "mailbox_id", mailbox)
		return
	}
	respond(w, http.StatusOK, warmupSendLookupResponse{Found: found, Send: send})
}

// recordWarmupTokenFailure records a warmup token that was present but did not
// verify.
func (h *handler) recordWarmupTokenFailure(w http.ResponseWriter, r *http.Request) {
	var in warmupTokenFailureRequest
	if !decode(w, r, &in) {
		return
	}
	ws, mailbox, ok := parsePair(w, in.WorkspaceID, "recipient_mailbox", in.RecipientMailbox)
	if !ok {
		return
	}
	if err := h.inbound.RecordWarmupTokenFailure(r.Context(), ws.String(), mailbox.String(), in.Fingerprint, in.ReasonCode); err != nil {
		h.fail(w, failWrite, "warmup token failure", err, "workspace_id", ws, "mailbox_id", mailbox)
		return
	}
	respond(w, http.StatusOK, ackResponse{})
}

// recordWarmupHardBounce attributes one DSN to a warmup send, if it is one.
// matched=false is a 200: it is how the poller knows to try the campaign lookup.
func (h *handler) recordWarmupHardBounce(w http.ResponseWriter, r *http.Request) {
	var in warmupHardBounceRequest
	if !decode(w, r, &in) {
		return
	}
	ws, mailbox, ok := parsePair(w, in.WorkspaceID, "observer_mailbox", in.ObserverMailbox)
	if !ok {
		return
	}
	matched, err := h.inbound.RecordWarmupHardBounce(r.Context(), ws.String(), in.MessageID, mailbox.String())
	if err != nil {
		h.fail(w, failWrite, "warmup hard bounce", err, "workspace_id", ws, "mailbox_id", mailbox)
		return
	}
	respond(w, http.StatusOK, warmupHardBounceResponse{Matched: matched})
}
