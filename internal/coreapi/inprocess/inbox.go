package inprocess

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/app/inbox"
	"github.com/inroad/inroad/internal/coreapi"
)

// StoreInboundMessage satisfies coreapi.InboxCaptureClient. It parses the
// caller's string/pointer-string ids once at this boundary (coreapi's
// cross-process-friendly contract is all strings) rather than pushing
// uuid.Parse into internal/app/inbox, then delegates to
// inbox.Service.RecordReply — the ONE call that upserts the thread and
// inserts the message atomically; see that method's doc for why this never
// splits into two calls.
//
// in.Subject/in.BodyText/in.BodyHTML are free-text business correspondence:
// like any other sensitive content, they are never logged here (or by
// RecordReply/its store) — only ids ever reach a log line on this path.
func (c client) StoreInboundMessage(ctx context.Context, in coreapi.InboxMessageInput) error {
	workspaceID, err := uuid.Parse(in.WorkspaceID)
	if err != nil {
		return err
	}
	mailboxID, err := uuid.Parse(in.MailboxID)
	if err != nil {
		return err
	}
	campaignID, err := optionalUUID(in.CampaignID)
	if err != nil {
		return err
	}
	contactID, err := optionalUUID(in.ContactID)
	if err != nil {
		return err
	}
	thread, err := c.inbox.RecordReply(ctx, inbox.RecordReplyInput{
		WorkspaceID: workspaceID, MailboxID: mailboxID, CampaignID: campaignID, ContactID: contactID,
		RootMessageID: in.RootMessageID, Subject: in.Subject, LastReplyClass: in.ReplyClass,
		Message: inbox.InsertMessageInput{
			Direction: "inbound", MessageID: in.MessageID, FromEmail: in.FromEmail,
			FromName: in.FromName, ToEmail: in.ToEmail, Subject: in.Subject,
			BodyText: in.BodyText, BodyHTML: in.BodyHTML, ReplyClass: in.ReplyClass,
			OccurredAt: in.OccurredAt,
		},
	})
	if err != nil {
		return err
	}

	c.publishInboxMessageCreated(ctx, in.WorkspaceID, thread, in.OccurredAt)
	return nil
}

// publishInboxMessageCreated tells the workspace's open tabs that a reply
// landed. Emitted from here because RecordReply above is the one call that
// upserts the thread and inserts the message in a single transaction, so this is
// the first point where the event is certainly true.
//
// It returns NOTHING, deliberately. The message is already committed; a browser
// that could not be notified is a missed optimisation (the next poll or refetch
// finds it), not a reason to fail a poller task that would then retry and
// re-deliver mail. Errors are the caller's to log, and this path has no logger,
// so a publish failure is swallowed HERE rather than propagated into a retry —
// which is the one place in this codebase where swallowing is the correct
// choice, and why it is spelled out.
//
// The payload is ids only. The client refetches the thread through the normal
// authorized endpoint, so a socket event cannot become a way around the
// permission checks the REST surface applies — and it carries no sender address,
// subject or body, none of which a workspace-wide broadcast should include.
func (c client) publishInboxMessageCreated(ctx context.Context, workspaceID string, thread inbox.Thread, occurredAt time.Time) {
	if c.realtime == nil {
		return
	}
	data := map[string]any{
		"thread_id":  thread.ID.String(),
		"mailbox_id": thread.MailboxID.String(),
		"unread":     thread.Unread,
	}
	// Present only when the reply matched a campaign/contact, so a client can
	// scope an update without a refetch. Nil is normal (an unmatched inbound).
	if thread.CampaignID != nil {
		data["campaign_id"] = thread.CampaignID.String()
	}
	if thread.ContactID != nil {
		data["contact_id"] = thread.ContactID.String()
	}
	// No ActorID: nobody clicked to make an inbound reply arrive. The client's
	// self-echo guard correctly treats an actorless event as "not mine".
	_ = c.PublishRealtime(ctx, coreapi.RealtimeEventInput{
		WorkspaceID: workspaceID,
		Type:        "inbox.message.created",
		SubjectKind: "thread",
		SubjectID:   thread.ID.String(),
		OccurredAt:  occurredAt,
		Data:        data,
	})
}

// optionalUUID parses a nilable id string at the coreapi boundary: nil (no
// campaign/contact matched) stays nil; a non-nil value must parse as a UUID.
func optionalUUID(id *string) (*uuid.UUID, error) {
	if id == nil {
		return nil, nil
	}
	parsed, err := uuid.Parse(*id)
	if err != nil {
		return nil, err
	}
	return &parsed, nil
}

var _ coreapi.InboxCaptureClient = client{}
