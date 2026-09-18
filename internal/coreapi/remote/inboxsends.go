package remote

import (
	"context"

	"github.com/inroad/inroad/internal/coreapi"
)

// The MANUAL MAIL protocol, slice 3b of the plane split: the reply and compose
// path for email a HUMAN wrote and pressed send on.
//
// # Why this slice is a slice
//
// Slice 3 moved the sequence and warmup claim/outcome writes and deliberately
// left this alone. ClaimPendingInboxReply is a claim-and-READ hybrid — it takes
// the lease and returns the BODY in one call — so moving its outcome half without
// its read half would have reproduced exactly the split slice 3 argued against
// for the step claim. It moves whole.
//
// # The bar is higher here
//
// A duplicate sequence step is one extra marketing touch. A duplicate manual
// reply is the operator's own words arriving twice in a customer's thread, in a
// conversation they are watching. Everything below is written to that bar, which
// in practice means the transport prefers DROPPING an ambiguous send over
// risking a second one — the same direction docs/security.md invariant 4a takes,
// applied harder.
//
// # What a lost response costs, method by method
//
// The third outcome a network has and a function call does not: the request
// arrived, the control plane COMMITTED, and the response was lost. Nothing here
// tries to tell that apart from "the call never happened". Audited against the
// SQL each method reaches (queries/inbox.sql), not assumed:
//
//   - ClaimPendingInboxReply / ClaimPendingInboxCompose — THE case this slice is
//     about, and the one worth reading twice. The claim commits ('scheduled' ->
//     'sending', claimed_at = now(), a 300s lease) and the response carrying the
//     LEASE AND THE BODY is lost together. The worker returns the error, asynq
//     retries the whole task, and the retry's claim is REFUSED: the row is
//     'sending' with a fresh claimed_at, so the guarded UPDATE matches nothing and
//     the control plane answers ErrInboxPendingNotClaimable. worker/inbox treats
//     that as terminal and returns nil, so THAT attempt sends nothing.
//
//     The reply is therefore DROPPED unless a later attempt lands after the lease
//     expires, at which point the row is claimable again and the body is still in
//     it — nothing was lost, only the worker's copy. That is the fail-safe
//     direction and it is deliberate: the alternative is teaching the control
//     plane to hand the body back to "the same worker", which needs an attempt id
//     on the wire and therefore a change to the coreapi signature, every fake and
//     both handlers — and any such mechanism weakens the mutual exclusion between
//     two DIFFERENT workers, which is the only thing standing between a lost
//     response and a duplicate reply. A drop the operator can see in their outbox
//     beats a double send they cannot take back.
//
//     This window is not new, only more reachable: an in-process worker that
//     claimed and then crashed leaves the identical row in the identical state,
//     and invariant 4a already says a crashed worker's row is reclaimed only once
//     its lease expires.
//
//   - MarkPendingInboxReplySent / MarkPendingInboxComposeSent — guarded on
//     status='sending' in SQL, so a repeat matches zero rows. A lost response
//     means the mail went out and the row stays 'sending': the handler is PAST
//     THE DIAL there, so it logs and returns nil rather than retrying, and no
//     second delivery can follow from this task. The row sits in the operator's
//     outbox showing 'sending' until its lease lets a future attempt reclaim it —
//     visible, and never a double send.
//
//   - ReleasePendingInboxReply / ReleasePendingInboxCompose — guarded on
//     'sending'. A repeat matches zero rows. A lost response costs the retry a
//     wait for the lease rather than an immediate reclaim.
//
//   - FailPendingInboxReply / FailPendingInboxCompose — guarded on status IN
//     ('scheduled','sending') and absolute. A repeat writes the same terminal
//     state.
//
//   - RecordInboxReply — the ONE method here that is not idempotent, and it is
//     left that way deliberately. It appends an inbox_messages row inside the
//     transaction that bumps the thread; a lost response would duplicate the row
//     if it were retried. It is not retried: both handlers call it PAST THE DIAL
//     and only log its failure, because returning an error there would re-run the
//     send. So the reachable outcomes are "the thread is missing one row" (the
//     call failed) and "the thread has it" — never two, unless a future caller
//     retries it, which is what this paragraph exists to warn against.
//
//   - ClaimInboxReply — the LEGACY drain's claim, an insert into idempotency_keys
//     keyed on (workspace, "inbox-reply:"+taskID). A lost response means the claim
//     is held and the worker does not know: the retry's own claim sees the row,
//     answers claimed=false, and the handler SKIPS. Drop, never double — the
//     posture that claim was built with.
//
//   - ReleaseInboxReply — a delete by key. A repeat deletes nothing.
//
//   - GetInboxReplyJob — a read. A lost response changes nothing.
//
// # Two sentinels cross as themselves
//
// coreapi.ErrInboxPendingNotClaimable and coreapi.ErrInboxNoInbound are both
// BRANCHED ON by internal/worker/inbox, and flattening either would change what a
// worker does with a human's mail: the first is the ordinary undo path (a
// returned error there retries an undone reply to exhaustion and writes it into
// task_dead_letters), the second is permanent (a retry can never build a reply to
// a thread with no inbound message). They cross as 409s carrying a fixed code —
// see wire.go for why 409 rather than 404.

// GetInboxReplyJob loads one thread's reply job: the sending mailbox, the
// subject, the recipient and the threading headers.
//
// No credential: the reply handlers resolve the mailbox transport separately
// through ResolveSenderTransport (slice 2), so nothing on this route needs the
// broker.
//
// FAIL CLOSED: any failure returns the zero job alongside the error, and the
// handler treats a non-nil error as "do not send".
func (c *Client) GetInboxReplyJob(ctx context.Context, threadID, workspaceID string) (coreapi.InboxReplyJob, error) {
	if err := parseIDs(workspaceID, threadID); err != nil {
		return coreapi.InboxReplyJob{}, err
	}
	var out inboxReplyJobResponse
	if err := c.post(ctx, c.jobs, PathInboxReplyJob, threadRequest{
		WorkspaceID: workspaceID, ThreadID: threadID,
	}, &out); err != nil {
		return coreapi.InboxReplyJob{}, err
	}
	return out.Job, nil
}

// RecordInboxReply writes one delivered reply onto its thread.
//
// This is the one route whose REQUEST carries a message body; see wire.go for
// why that is not a widening of the ids-in/values-out rule. The body is never
// logged, here or on the control plane.
func (c *Client) RecordInboxReply(ctx context.Context, in coreapi.RecordInboxReplyInput) error {
	if err := parseIDs(in.WorkspaceID, in.ThreadID); err != nil {
		return err
	}
	return c.post(ctx, c.outcomes, PathInboxReplyRecord, recordReplyRequest{
		WorkspaceID: in.WorkspaceID, Reply: in,
	}, &ackResponse{})
}

// ClaimInboxReply claims one LEGACY inbox:reply_send task for delivery.
//
// FAIL CLOSED, and note what it means for this one specifically: a failed call
// returns (false, err). false is "I do not hold the claim", which the caller
// reads as "skip", so a caller that ignored the error would fail safe rather than
// send. No caller does — worker/inbox returns the error and lets asynq retry —
// but the pairing is deliberate rather than accidental.
//
// What it must NEVER do is invent a true on a failed call: that would be a manual
// reply dialed on a claim nobody took.
func (c *Client) ClaimInboxReply(ctx context.Context, workspaceID, taskID string) (bool, error) {
	if err := parseIDs(workspaceID); err != nil {
		return false, err
	}
	// taskID is NOT parsed as a uuid — see replyTaskRequest for why.
	var out claimedResponse
	if err := c.post(ctx, c.outcomes, PathInboxReplyClaim, replyTaskRequest{
		WorkspaceID: workspaceID, TaskID: taskID,
	}, &out); err != nil {
		return false, err
	}
	return out.Claimed, nil
}

// ReleaseInboxReply releases a claim taken by ClaimInboxReply, after a TRANSIENT
// failure and before the handler returns for asynq to retry. Without it the
// retry's own claim would see its own abandoned claim as "already sent" and drop
// the reply forever.
func (c *Client) ReleaseInboxReply(ctx context.Context, workspaceID, taskID string) error {
	if err := parseIDs(workspaceID); err != nil {
		return err
	}
	return c.post(ctx, c.outcomes, PathInboxReplyRelease, replyTaskRequest{
		WorkspaceID: workspaceID, TaskID: taskID,
	}, &ackResponse{})
}

// ClaimPendingInboxReply claims one deferred reply and resolves what to send in
// the same call.
//
// The lease and the BODY cross together, which is what makes a lost response here
// different from every other claim on this seam — the worker loses both at once.
// This file's header states exactly what the retry then does and why that is the
// chosen answer.
//
// It uses the MESSAGE budget rather than the outcome budget: same timeouts (this
// is a contended write), a response ceiling sized for a reply body rather than
// for an enum.
func (c *Client) ClaimPendingInboxReply(ctx context.Context, workspaceID, pendingID string) (coreapi.PendingInboxReply, error) {
	if err := parseIDs(workspaceID, pendingID); err != nil {
		return coreapi.PendingInboxReply{}, err
	}
	var out pendingInboxReplyResponse
	if err := c.post(ctx, c.messages, PathInboxPendingReplyClaim, pendingRequest{
		WorkspaceID: workspaceID, PendingID: pendingID,
	}, &out); err != nil {
		return coreapi.PendingInboxReply{}, err
	}
	return out.Pending, nil
}

// MarkPendingInboxReplySent completes a claimed deferred reply. Guarded on
// 'sending' in SQL, so only the worker that claimed it can complete it.
func (c *Client) MarkPendingInboxReplySent(ctx context.Context, workspaceID, pendingID, messageID string) error {
	return c.postPendingSent(ctx, PathInboxPendingReplySent, workspaceID, pendingID, messageID)
}

// ReleasePendingInboxReply returns a claimed reply to 'scheduled' after a
// TRANSIENT failure, so the retry can claim it again rather than waiting out the
// full lease.
func (c *Client) ReleasePendingInboxReply(ctx context.Context, workspaceID, pendingID, reason string) error {
	return c.postPendingReason(ctx, PathInboxPendingReplyRelease, workspaceID, pendingID, reason)
}

// FailPendingInboxReply marks a claimed reply permanently failed. The row
// survives so the outbox can show what happened rather than the reply vanishing.
func (c *Client) FailPendingInboxReply(ctx context.Context, workspaceID, pendingID, reason string) error {
	return c.postPendingReason(ctx, PathInboxPendingReplyFail, workspaceID, pendingID, reason)
}

// ClaimPendingInboxCompose claims one deferred composed email. Same
// claim-and-read shape as ClaimPendingInboxReply, over its own table, and the
// same lost-response analysis applies unchanged.
func (c *Client) ClaimPendingInboxCompose(ctx context.Context, workspaceID, pendingID string) (coreapi.PendingInboxCompose, error) {
	if err := parseIDs(workspaceID, pendingID); err != nil {
		return coreapi.PendingInboxCompose{}, err
	}
	var out pendingInboxComposeResponse
	if err := c.post(ctx, c.messages, PathInboxPendingComposeClaim, pendingRequest{
		WorkspaceID: workspaceID, PendingID: pendingID,
	}, &out); err != nil {
		return coreapi.PendingInboxCompose{}, err
	}
	return out.Compose, nil
}

func (c *Client) MarkPendingInboxComposeSent(ctx context.Context, workspaceID, pendingID, messageID string) error {
	return c.postPendingSent(ctx, PathInboxPendingComposeSent, workspaceID, pendingID, messageID)
}

func (c *Client) ReleasePendingInboxCompose(ctx context.Context, workspaceID, pendingID, reason string) error {
	return c.postPendingReason(ctx, PathInboxPendingComposeRelease, workspaceID, pendingID, reason)
}

func (c *Client) FailPendingInboxCompose(ctx context.Context, workspaceID, pendingID, reason string) error {
	return c.postPendingReason(ctx, PathInboxPendingComposeFail, workspaceID, pendingID, reason)
}

// postPendingSent and postPendingReason are the shared halves of the six
// completion routes. The reply and the compose families differ only in which PATH
// the call goes to, so the request assembly and the id parsing live once — the
// same reason postReplyClass exists in outcomes.go.

func (c *Client) postPendingSent(ctx context.Context, path, workspaceID, pendingID, messageID string) error {
	if err := parseIDs(workspaceID, pendingID); err != nil {
		return err
	}
	return c.post(ctx, c.outcomes, path, pendingSentRequest{
		WorkspaceID: workspaceID, PendingID: pendingID, MessageID: messageID,
	}, &ackResponse{})
}

func (c *Client) postPendingReason(ctx context.Context, path, workspaceID, pendingID, reason string) error {
	if err := parseIDs(workspaceID, pendingID); err != nil {
		return err
	}
	return c.post(ctx, c.outcomes, path, pendingReasonRequest{
		WorkspaceID: workspaceID, PendingID: pendingID, Reason: reason,
	}, &ackResponse{})
}
