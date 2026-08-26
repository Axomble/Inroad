package inprocess

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/app/inbox"
	"github.com/inroad/inroad/internal/coreapi"
)

// GetInboxReplyJob loads everything internal/worker/inbox's reply-send
// handler needs to build and send one manual reply, workspace-pinned. It
// reads through the SAME inbox.Service.GetThread the control-plane HTTP
// handler reads from (composed once as c.inbox), rather than re-deriving the
// inbound/outbound merge here.
func (c client) GetInboxReplyJob(ctx context.Context, threadID, workspaceID string) (coreapi.InboxReplyJob, error) {
	ws, err := uuid.Parse(workspaceID)
	if err != nil {
		return coreapi.InboxReplyJob{}, err
	}
	tid, err := uuid.Parse(threadID)
	if err != nil {
		return coreapi.InboxReplyJob{}, err
	}
	detail, err := c.inbox.GetThread(ctx, ws, tid)
	if err != nil {
		return coreapi.InboxReplyJob{}, err
	}
	return inboxReplyJobFrom(detail)
}

// inboxReplyJobFrom derives the reply job from a thread's full (merged
// inbound+outbound, chronologically ordered) message history. References is
// the message-id chain up to and including the latest inbound message —
// deliberately NOT the whole thread, so a later outbound message (a
// follow-up step the sequence sent after this reply arrived) never appears
// AFTER the anchor this reply threads on.
func inboxReplyJobFrom(detail inbox.ThreadDetail) (coreapi.InboxReplyJob, error) {
	latestIdx := -1
	for i := len(detail.Messages) - 1; i >= 0; i-- {
		if detail.Messages[i].Direction == "inbound" {
			latestIdx = i
			break
		}
	}
	if latestIdx == -1 {
		return coreapi.InboxReplyJob{}, coreapi.ErrInboxNoInbound
	}
	latest := detail.Messages[latestIdx]

	refs := make([]string, 0, latestIdx+1)
	for _, m := range detail.Messages[:latestIdx+1] {
		if m.MessageID != "" {
			refs = append(refs, m.MessageID)
		}
	}

	return coreapi.InboxReplyJob{
		MailboxID:  detail.Thread.MailboxID.String(),
		Subject:    detail.Thread.Subject,
		ToEmail:    latest.FromEmail,
		InReplyTo:  latest.MessageID,
		References: strings.Join(refs, " "),
	}, nil
}

// RecordInboxReply persists one delivered manual reply: the outbound message
// row plus the thread's last_message_at bump, in ONE transaction (see
// inbox.Service.RecordOutboundReply). Consumed through the narrow
// worker/inbox.ReplyCore interface, satisfied by type assertion — the same
// pattern as StoreInboundMessage/coreapi.InboxCaptureClient.
func (c client) RecordInboxReply(ctx context.Context, in coreapi.RecordInboxReplyInput) error {
	ws, err := uuid.Parse(in.WorkspaceID)
	if err != nil {
		return err
	}
	tid, err := uuid.Parse(in.ThreadID)
	if err != nil {
		return err
	}
	return c.inbox.RecordOutboundReply(ctx, ws, tid, inbox.InsertMessageInput{
		Direction: "outbound",
		MessageID: in.MessageID,
		FromEmail: in.FromEmail,
		FromName:  in.FromName,
		ToEmail:   in.ToEmail,
		Subject:   in.Subject,
		BodyText:  in.BodyText,
		// ReplyClass left "" — a manual reply is not itself a classified
		// inbound reply, matching this domain's "absent is empty string"
		// convention.
		OccurredAt: time.Now().UTC(),
	})
}

// inboxReplyClaimKeyPrefix namespaces the claim inside idempotency_keys —
// that table's primary key is only (workspace_id, key), with no domain
// discriminator, so a namespaced key is what keeps a reply's claim from ever
// colliding with an HTTP Idempotency-Key a client happened to choose the
// identical raw value for.
const inboxReplyClaimKeyPrefix = "inbox-reply:"

// ClaimInboxReply attempts to claim taskID for delivery — claim-before-send
// for a manual reply, the SAME correctness problem ClaimStepSend/
// ClaimWarmupSend solve for a sequence/warmup send, solved here by reusing
// the EXISTING generic Idempotency-Key replay cache (migration 000045)
// rather than inventing a THIRD claim table: taskID is stable across every
// retry/redelivery of one enqueued inbox:reply_send task (see
// queue.InboxReplySendPayload.TaskID's doc), so claiming (workspace_id,
// "inbox-reply:"+taskID) once and skipping every later attempt at the SAME
// task is exactly the claim semantics this needs. claimed=false means
// another attempt at this exact task already reached the dial (a prior
// worker crashed AFTER sending but before this claim would have been
// released) — the caller must skip, not send again: "never double,
// occasionally drop a rare ambiguous send" (docs/security.md invariant 4a's
// accepted posture, reused here for the identical reason). The request_hash
// column is meaningless for this reuse (there is no request to replay); it
// is populated with taskID itself only to satisfy the column's NOT NULL.
// Claim rows age out via the SAME 24h maintenance sweep as HTTP idempotency
// rows — no dedicated retention job.
func (c client) ClaimInboxReply(ctx context.Context, workspaceID, taskID string) (bool, error) {
	return c.replyClaims.TryInsert(ctx, workspaceID, inboxReplyClaimKeyPrefix+taskID, []byte(taskID))
}

// ReleaseInboxReply releases a claim taken by ClaimInboxReply, called ONLY
// after a TRANSIENT send failure (the handler is about to return err for
// asynq to retry): without releasing first, the retry's own ClaimInboxReply
// call would see its own abandoned claim as "already sent" and skip
// forever, permanently dropping a reply that never actually went out.
func (c client) ReleaseInboxReply(ctx context.Context, workspaceID, taskID string) error {
	return c.replyClaims.Delete(ctx, workspaceID, inboxReplyClaimKeyPrefix+taskID)
}

// --- Deferred (undoable) manual replies ---

// ClaimPendingInboxReply claims a deferred reply for delivery, resolving the
// body and threading headers in the same step.
//
// Unlike ClaimInboxReply, this needs no idempotency-table claim: the
// inbox_pending_replies ROW is the claim. Its status column carries the
// 'scheduled' -> 'sending' transition under a WHERE guard, so exactly one
// worker can move it, and the same row also expresses the state that table
// cannot — 'cancelled'. That is the whole reason deferred sends have a row at
// all (see migration 000066).
//
// Returns coreapi.ErrInboxPendingNotClaimable when the transition matched no
// row: cancelled, already sent, not yet due, or held by a live lease. All four
// mean "stop and do not retry", so they deliberately share one error.
func (c client) ClaimPendingInboxReply(ctx context.Context, workspaceID, pendingID string) (coreapi.PendingInboxReply, error) {
	ws, err := uuid.Parse(workspaceID)
	if err != nil {
		return coreapi.PendingInboxReply{}, fmt.Errorf("parse workspace id: %w", err)
	}
	id, err := uuid.Parse(pendingID)
	if err != nil {
		return coreapi.PendingInboxReply{}, fmt.Errorf("parse pending id: %w", err)
	}

	// The claim comes FIRST, before reading anything: a read-then-claim
	// ordering would let two workers both read a claimable row and race.
	if err := c.inbox.ClaimPendingReply(ctx, ws, id); err != nil {
		return coreapi.PendingInboxReply{}, err
	}

	pending, err := c.inbox.GetPendingReply(ctx, ws, id)
	if err != nil {
		return coreapi.PendingInboxReply{}, fmt.Errorf("load pending reply: %w", err)
	}
	detail, err := c.inbox.GetThread(ctx, ws, pending.ThreadID)
	if err != nil {
		return coreapi.PendingInboxReply{}, fmt.Errorf("load thread: %w", err)
	}
	job, err := inboxReplyJobFrom(detail)
	if err != nil {
		return coreapi.PendingInboxReply{}, err
	}
	return coreapi.PendingInboxReply{
		ThreadID: pending.ThreadID.String(),
		BodyText: pending.BodyText,
		Job:      job,
	}, nil
}

// MarkPendingInboxReplySent completes a claimed deferred reply. Guarded on
// 'sending' in SQL, so only the worker that claimed it can complete it.
func (c client) MarkPendingInboxReplySent(ctx context.Context, workspaceID, pendingID, messageID string) error {
	ws, id, err := parsePendingIDs(workspaceID, pendingID)
	if err != nil {
		return err
	}
	return c.inbox.MarkPendingReplySent(ctx, ws, id, messageID)
}

// ReleasePendingInboxReply returns a claimed reply to 'scheduled' after a
// TRANSIENT failure, so asynq's retry can claim it again. Without this the
// retry would find its own abandoned 'sending' row and have to wait out the
// full lease before trying — turning a momentary SMTP blip into a five-minute
// delay.
func (c client) ReleasePendingInboxReply(ctx context.Context, workspaceID, pendingID, reason string) error {
	ws, id, err := parsePendingIDs(workspaceID, pendingID)
	if err != nil {
		return err
	}
	return c.inbox.ReleasePendingReply(ctx, ws, id, reason)
}

// FailPendingInboxReply marks a claimed reply permanently failed. The row
// survives so the outbox can show what happened rather than the reply simply
// vanishing.
func (c client) FailPendingInboxReply(ctx context.Context, workspaceID, pendingID, reason string) error {
	ws, id, err := parsePendingIDs(workspaceID, pendingID)
	if err != nil {
		return err
	}
	return c.inbox.FailPendingReply(ctx, ws, id, reason)
}

func parsePendingIDs(workspaceID, pendingID string) (ws, id uuid.UUID, err error) {
	ws, err = uuid.Parse(workspaceID)
	if err != nil {
		return uuid.Nil, uuid.Nil, fmt.Errorf("parse workspace id: %w", err)
	}
	id, err = uuid.Parse(pendingID)
	if err != nil {
		return uuid.Nil, uuid.Nil, fmt.Errorf("parse pending id: %w", err)
	}
	return ws, id, nil
}

// --- Deferred composed emails ---

// ClaimPendingInboxCompose claims a composed email for delivery. Same
// claim-then-read ordering as ClaimPendingInboxReply: claiming first is what
// stops two workers both deciding a row is theirs.
func (c client) ClaimPendingInboxCompose(ctx context.Context, workspaceID, pendingID string) (coreapi.PendingInboxCompose, error) {
	ws, id, err := parsePendingIDs(workspaceID, pendingID)
	if err != nil {
		return coreapi.PendingInboxCompose{}, err
	}
	if err := c.inbox.ClaimPendingCompose(ctx, ws, id); err != nil {
		return coreapi.PendingInboxCompose{}, err
	}
	row, err := c.inbox.GetPendingCompose(ctx, ws, id)
	if err != nil {
		return coreapi.PendingInboxCompose{}, fmt.Errorf("load pending compose: %w", err)
	}
	return coreapi.PendingInboxCompose{
		MailboxID: row.MailboxID.String(),
		ToEmails:  row.ToEmails,
		CcEmails:  row.CcEmails,
		BccEmails: row.BccEmails,
		Subject:   row.Subject,
		BodyText:  row.BodyText,
	}, nil
}

func (c client) MarkPendingInboxComposeSent(ctx context.Context, workspaceID, pendingID, messageID string) error {
	ws, id, err := parsePendingIDs(workspaceID, pendingID)
	if err != nil {
		return err
	}
	return c.inbox.MarkPendingComposeSent(ctx, ws, id, messageID)
}

func (c client) ReleasePendingInboxCompose(ctx context.Context, workspaceID, pendingID, reason string) error {
	ws, id, err := parsePendingIDs(workspaceID, pendingID)
	if err != nil {
		return err
	}
	return c.inbox.ReleasePendingCompose(ctx, ws, id, reason)
}

func (c client) FailPendingInboxCompose(ctx context.Context, workspaceID, pendingID, reason string) error {
	ws, id, err := parsePendingIDs(workspaceID, pendingID)
	if err != nil {
		return err
	}
	return c.inbox.FailPendingCompose(ctx, ws, id, reason)
}
