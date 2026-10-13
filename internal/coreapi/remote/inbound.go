package remote

import (
	"context"
	"time"

	"github.com/inroad/inroad/internal/coreapi"
)

// The INBOUND MAIL path, slice 4 of the plane split: everything an inbox poll
// learns from a message it has just read, plus the cursor that says it is done
// with it.
//
// # Why this is the slice that removes the pool
//
// Slices 1–3b moved the suppression check, the job reads, the claim/outcome
// writes and the manual-mail protocol. All of it was plumbing: cmd/worker still
// called db.ConnectSized, so a role=send host still held a pgxpool and could
// read every workspace's contacts, message bodies and reply text. The ten
// methods here are what was LEFT — and a poller that can fetch its job and
// record its outcomes but cannot advance its own cursor is a poller that
// re-reads the same window forever, so the pool could not go until they moved.
//
// # The idempotency audit, method by method
//
// These are writes, so slice 3's rule applies unchanged: a lost response is
// indistinguishable from a call that never happened, every method fails the
// CALL, and the retry must be safe. Audited against the SQL rather than assumed:
//
//   - SetInboxCursor / SetInboxCursorString — set the cursor ABSOLUTELY,
//     workspace-pinned. A repeat writes the same values. A LOST response means
//     the poller re-polls the window it already processed, which is the same
//     window an in-process worker re-polls after crashing between the last
//     message and the cursor write; every per-message effect downstream of it is
//     itself idempotent (the suppression insert is ON CONFLICT DO NOTHING, the
//     receipt is unique on (send, recipient), the reply match is keyed on the
//     send row).
//   - StoreInboundMessage — the thread upsert plus the message insert commit in
//     one transaction inside app/inbox, and the message is keyed on its RFC 5322
//     Message-ID, so a re-poll of the same message writes nothing new.
//   - CaptureCRMReply — the capture is keyed on the send, and a capture the
//     workspace has disabled is a nil (not an error) on both transports.
//   - IngestComplaint — idempotent on (workspace_id, provider_event_id), which
//     the poller namespaces as "arf:<send id>". A redelivered report writes
//     nothing and therefore CAUSES nothing: no second suppression, no second
//     breaker evaluation (docs/security.md invariant 41).
//   - RecordWarmupReceipt — idempotent on (warmup_send_id, recipient_mailbox).
//     A duplicate returns the stored deterministic plan rather than a second
//     volume bump, which is exactly what makes a re-poll harmless.
//   - RecordWarmupTokenFailure / RecordWarmupHardBounce — evidence rows; the
//     bounce is matched by message id against our own warmup sends and the
//     token failure is a counter on a (mailbox, reason) pair. Both are
//     append-or-merge, neither gates anything on an exact count.
//   - ResolveReplyLabel / FindWarmupSendByMessageID — READS. No idempotency
//     question; both fail closed with their zero value.
//
// # What crosses, and the one thing that does not
//
// Eight of the ten carry ids and closed-vocabulary tokens. Two carry a tenant's
// own inbound correspondence — StoreInboundMessage (the message) and
// CaptureCRMReply (its subject and the sender's name) — and they are the SECOND
// place on this wire where that happens, after slice 3b's inbox-reply/record.
// It is unavoidable and it is stated rather than glossed: the poller holds the
// mailbox connection now, so the control plane can only write the thread's
// history from what the poller read. The rule that follows is the same one
// slice 3b set — no route logs a body, a subject, a recipient or an address, on
// either side — and inboundhandler.go's log arguments are ids only.
//
// No credential crosses here in either direction. Nothing in this file names a
// mailbox secret; the poller is already holding the one it dialed with, brokered
// through internal/platform/credbroker.

// SetInboxCursor persists the IMAP poll cursor after a poll pass.
//
// FAIL CLOSED, and note which direction "closed" is here: a failure returns an
// error, the poll handler returns it, and asynq retries the whole poll. The
// window is then re-read, not skipped — losing a cursor write costs duplicate
// WORK, never a missed reply, which is the right way round for a method whose
// job is to say "I have seen these messages".
func (c *Client) SetInboxCursor(ctx context.Context, mailboxID, workspaceID string, lastSeenUID, uidValidity uint32) error {
	if err := parseIDs(workspaceID, mailboxID); err != nil {
		return err
	}
	return c.post(ctx, c.outcomes, PathInboxCursorUID, inboxCursorUIDRequest{
		WorkspaceID: workspaceID, MailboxID: mailboxID,
		LastSeenUID: lastSeenUID, UIDValidity: uidValidity,
	}, &ackResponse{})
}

// SetInboxCursorString persists an opaque provider cursor (a Gmail historyId, a
// Graph delta link) after a poll pass. Same fail-closed direction as the UID
// cursor above.
func (c *Client) SetInboxCursorString(ctx context.Context, mailboxID, workspaceID, cursor string) error {
	if err := parseIDs(workspaceID, mailboxID); err != nil {
		return err
	}
	// cursor travels UNVALIDATED. It is an opaque provider value the control
	// plane stores and hands back; the host-pinning that makes re-dialing a
	// Graph delta link safe (docs/security.md invariant 13) happens where it is
	// USED, in internal/platform/mail, on both transports alike.
	return c.post(ctx, c.outcomes, PathInboxCursorString, inboxCursorStringRequest{
		WorkspaceID: workspaceID, MailboxID: mailboxID, Cursor: cursor,
	}, &ackResponse{})
}

// RecordInboxPollFailure records one failed poll and returns the schedule the
// control plane computed: the new consecutive-failure count and the earliest
// time the poll fan-out will consider this mailbox again.
//
// FAIL OPEN, which is the opposite direction from the cursor writes above, and
// deliberately so. If this call fails the mailbox simply keeps its old
// eligibility and is polled again on the next sweep — today's behaviour. The
// caller therefore treats an error here as something to log, never as a reason
// to fail the poll task: a backoff that could turn a control-plane hiccup into a
// mailbox that stops being polled would be the deactivation this whole change
// exists to avoid.
//
// The ladder is validated before it leaves, so a programming error surfaces at
// the caller rather than as a 400 from the control plane (which validates it
// again — see the handler).
func (c *Client) RecordInboxPollFailure(ctx context.Context, mailboxID, workspaceID string, ladder []time.Duration) (coreapi.InboxPollBackoff, error) {
	if err := coreapi.ValidateBackoffLadder(ladder); err != nil {
		return coreapi.InboxPollBackoff{}, err
	}
	if err := parseIDs(workspaceID, mailboxID); err != nil {
		return coreapi.InboxPollBackoff{}, err
	}
	seconds := make([]float64, len(ladder))
	for i, d := range ladder {
		seconds[i] = d.Seconds()
	}
	var out inboxPollFailureResponse
	if err := c.post(ctx, c.outcomes, PathInboxPollFailure, inboxPollFailureRequest{
		WorkspaceID: workspaceID, MailboxID: mailboxID, BackoffSeconds: seconds,
	}, &out); err != nil {
		return coreapi.InboxPollBackoff{}, err
	}
	return coreapi.InboxPollBackoff{Failures: out.Failures, RetryAfter: out.RetryAfter}, nil
}

// StoreInboundMessage writes one matched inbound message onto its thread.
//
// The job travels WHOLE (coreapi.InboxMessageInput, not a subset) for the reason
// every other input-carrying request on this transport does: a hand-written
// mirror that forgot CampaignID would not fail to compile, it would file a
// customer's reply under no campaign.
func (c *Client) StoreInboundMessage(ctx context.Context, in coreapi.InboxMessageInput) error {
	if err := parseIDs(in.WorkspaceID, in.MailboxID); err != nil {
		return err
	}
	return c.post(ctx, c.messages, PathInboxMessageStore, inboxMessageRequest{
		WorkspaceID: in.WorkspaceID, Message: in,
	}, &ackResponse{})
}

// CaptureCRMReply records one positive reply as a CRM activity.
//
// A workspace with CRM capture disabled is a nil return, not an error, in
// process — and that decision stays on the control plane's side of this wire so
// the two transports cannot disagree about what "disabled" does.
func (c *Client) CaptureCRMReply(ctx context.Context, in coreapi.CRMReplyInput) error {
	if err := parseIDs(in.WorkspaceID, in.EnrollmentID, in.SendID); err != nil {
		return err
	}
	return c.post(ctx, c.messages, PathCRMReplyCapture, crmReplyRequest{
		WorkspaceID: in.WorkspaceID, Reply: in,
	}, &ackResponse{})
}

// IngestComplaint records one complaint that arrived AS MAIL.
//
// coreapi.ErrInvalidComplaint crosses AS ITSELF, on 422, and that is the
// load-bearing part of this method. The poller skips a permanently-invalid
// report and retries everything else — and it returns BEFORE SetInboxCursor
// either way, so flattening the sentinel into a plain error would wedge the
// mailbox's cursor and stop every inbound signal for it (campaign replies and
// bounces included) on the strength of one unauthenticated inbound message. See
// unprocessable() for why an unrecognised 422 stays a plain error instead.
func (c *Client) IngestComplaint(ctx context.Context, in coreapi.ComplaintInput) error {
	if err := parseIDs(in.WorkspaceID, in.SendID); err != nil {
		return err
	}
	return c.post(ctx, c.outcomes, PathComplaintIngest, complaintRequest{
		WorkspaceID: in.WorkspaceID, Complaint: in,
	}, &ackResponse{})
}

// ResolveReplyLabel resolves a classified reply key to the workspace's label.
//
// ok=false is NOT an error and does not cross as a 404: it is the ordinary
// answer for a deleted custom label whose key survives on historical rows, and
// the caller must fall back to its pre-taxonomy class switch rather than fail
// the poll. See replyLabelResponse.
//
// FAIL CLOSED on a real failure: the zero label and false alongside the error,
// never a label the control plane did not send.
func (c *Client) ResolveReplyLabel(ctx context.Context, workspaceID, key string) (coreapi.ReplyLabel, bool, error) {
	if err := parseIDs(workspaceID); err != nil {
		return coreapi.ReplyLabel{}, false, err
	}
	var out replyLabelResponse
	if err := c.post(ctx, c.check, PathReplyLabelResolve, replyLabelRequest{
		WorkspaceID: workspaceID, Key: key,
	}, &out); err != nil {
		return coreapi.ReplyLabel{}, false, err
	}
	if !out.Found {
		return coreapi.ReplyLabel{}, false, nil
	}
	return out.Label, true, nil
}

// RecordWarmupReceipt records one received warmup message and returns the
// deterministic engagement plan.
//
// FAIL CLOSED: a zero plan alongside the error. The zero plan does nothing —
// no rescue, no mark-read, no reply — so a failed receipt costs a warmup
// engagement, never an action on a mailbox the control plane did not authorise.
func (c *Client) RecordWarmupReceipt(ctx context.Context, in coreapi.WarmupReceiptInput) (coreapi.WarmupEngagePlan, error) {
	if err := parseIDs(in.WorkspaceID, in.WarmupSendID, in.RecipientMailbox); err != nil {
		return coreapi.WarmupEngagePlan{}, err
	}
	var out warmupEngagePlanResponse
	if err := c.post(ctx, c.outcomes, PathWarmupReceipt, warmupReceiptRequest{
		WorkspaceID: in.WorkspaceID, Receipt: in,
	}, &out); err != nil {
		return coreapi.WarmupEngagePlan{}, err
	}
	return out.Plan, nil
}

// FindWarmupSendByMessageID resolves an inbound message back to a warmup send
// when the X-Inroad-Warmup token did not survive the provider (Microsoft strips
// unknown custom headers).
//
// ok=false is the ORDINARY answer for nearly every message the poller sees, so
// it is a response field rather than a 404 — mapping it to pgx.ErrNoRows would
// turn the common case into a failed poll.
func (c *Client) FindWarmupSendByMessageID(ctx context.Context, workspaceID, toMailboxID, messageID string) (coreapi.WarmupSendRef, bool, error) {
	if err := parseIDs(workspaceID, toMailboxID); err != nil {
		return coreapi.WarmupSendRef{}, false, err
	}
	// messageID is passed through UNVALIDATED: it comes off unauthenticated
	// inbound mail, and whatever string the worker would have handed the local
	// query the control plane hands to the same one. The angle-bracket
	// normalisation both sides rely on lives in the implementation, not here.
	var out warmupSendLookupResponse
	if err := c.post(ctx, c.check, PathWarmupSendByMessageID, warmupSendLookupRequest{
		WorkspaceID: workspaceID, ToMailboxID: toMailboxID, MessageID: messageID,
	}, &out); err != nil {
		return coreapi.WarmupSendRef{}, false, err
	}
	if !out.Found {
		return coreapi.WarmupSendRef{}, false, nil
	}
	return out.Send, true, nil
}

// RecordWarmupTokenFailure records that a warmup token was present but did not
// verify — evidence that a participant's mail is being rewritten in transit.
func (c *Client) RecordWarmupTokenFailure(ctx context.Context, workspaceID, recipientMailbox, fingerprint, reasonCode string) error {
	if err := parseIDs(workspaceID, recipientMailbox); err != nil {
		return err
	}
	return c.post(ctx, c.outcomes, PathWarmupTokenFailure, warmupTokenFailureRequest{
		WorkspaceID: workspaceID, RecipientMailbox: recipientMailbox,
		Fingerprint: fingerprint, ReasonCode: reasonCode,
	}, &ackResponse{})
}

// RecordWarmupHardBounce attributes one DSN to a warmup send, if it is one.
//
// matched=false is how the poller knows to try the CAMPAIGN lookup next, so it
// is a response field rather than a 404 — and FAIL CLOSED here returns false
// alongside the error rather than true, because a wrongly-claimed match would
// swallow a real campaign bounce and leave a dead address in the list.
func (c *Client) RecordWarmupHardBounce(ctx context.Context, workspaceID, messageID, observerMailbox string) (bool, error) {
	if err := parseIDs(workspaceID, observerMailbox); err != nil {
		return false, err
	}
	var out warmupHardBounceResponse
	if err := c.post(ctx, c.outcomes, PathWarmupHardBounce, warmupHardBounceRequest{
		WorkspaceID: workspaceID, MessageID: messageID, ObserverMailbox: observerMailbox,
	}, &out); err != nil {
		return false, err
	}
	return out.Matched, nil
}

// Compile-time proof that this transport satisfies every OPTIONAL execution-plane
// capability the inbox poller feature-detects. Each of these is consumed through
// a comma-ok type assertion (internal/worker/inbox), so a missing or drifted
// signature would not fail the build — it would silently degrade a fleet worker
// to "no CRM capture", "no inbound storage", "no complaint ingest" or "no label
// taxonomy" while every test that uses the in-process client kept passing.
//
// That is not hypothetical: these are exactly the six assertions that decide
// whether a poll writes anything at all beyond its cursor.
var (
	_ coreapi.CRMCaptureClient              = (*Client)(nil)
	_ coreapi.InboxCaptureClient            = (*Client)(nil)
	_ coreapi.WarmupEvidenceClient          = (*Client)(nil)
	_ coreapi.DeliverabilityComplaintClient = (*Client)(nil)
	_ coreapi.WarmupSendLookupClient        = (*Client)(nil)
	_ coreapi.ReplyLabelClient              = (*Client)(nil)
)
