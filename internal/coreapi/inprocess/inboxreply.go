package inprocess

import (
	"context"
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
